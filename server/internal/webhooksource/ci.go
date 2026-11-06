package webhooksource

// CI source normalization (design §5.1–§5.3).
//
// Five provider shapes are reduced to the same intake.Trigger: the vendor-neutral
// generic body, GitLab pipeline/build events, Jenkins (generic body), Buildkite
// build.finished and CircleCI workflow-completed. Statuses outside the four
// normalized CI kinds are dropped (ok=false) instead of becoming tasks, so a
// running/skipped pipeline never wakes an agent.
//
// The payload is data: nothing here grants authority. Every trigger is
// ReceiveAll (an unlisted actor is only an inferred candidate) and Explicit=false
// — a machine event can never act as a user request.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

// CIMaxLogExcerpt is the hard cap on inlined log text. The full log stays behind
// SourceURL; an oversized excerpt would only bloat the event row.
const CIMaxLogExcerpt = 16 * 1024

// CI provider names accepted by the CI source.
const (
	CIProviderGeneric   = "generic"
	CIProviderGitLab    = "gitlab"
	CIProviderJenkins   = "jenkins"
	CIProviderBuildkite = "buildkite"
	CIProviderCircleCI  = "circleci"
)

// CI authentication modes. Task 3's inbound handler uses the preset to decide
// which header to read and how to validate the presented credential.
const (
	CIAuthToken  = "token"       // constant-time compare with the binding secret
	CIAuthHMAC   = "hmac_sha256" // hex HMAC-SHA256 over the request body
	CIAuthBearer = "bearer"      // "Authorization: Bearer <secret>"
)

// CIAuthMethod is one accepted credential carrier for a provider.
type CIAuthMethod struct {
	Mode   string // CIAuthToken | CIAuthHMAC | CIAuthBearer
	Header string // canonical header name
}

// CIProviderPreset is everything the inbound path needs to know about a CI
// vendor: how it authenticates and which events a new binding subscribes to by
// default.
type CIProviderPreset struct {
	Provider      string
	DisplayName   string
	Auth          []CIAuthMethod
	DefaultEvents []string
}

var ciProviderPresets = []CIProviderPreset{
	{
		Provider:    CIProviderGeneric,
		DisplayName: "Generic webhook",
		Auth: []CIAuthMethod{
			{Mode: CIAuthHMAC, Header: "X-Niuniu-Signature-256"},
			{Mode: CIAuthBearer, Header: "Authorization"},
		},
		DefaultEvents: []string{intake.EventKindCIFailed},
	},
	{
		Provider:      CIProviderGitLab,
		DisplayName:   "GitLab",
		Auth:          []CIAuthMethod{{Mode: CIAuthToken, Header: "X-Gitlab-Token"}},
		DefaultEvents: []string{intake.EventKindCIFailed},
	},
	{
		Provider:    CIProviderJenkins,
		DisplayName: "Jenkins",
		Auth: []CIAuthMethod{
			{Mode: CIAuthToken, Header: "X-Niuniu-Token"},
			{Mode: CIAuthBearer, Header: "Authorization"},
		},
		DefaultEvents: []string{intake.EventKindCIFailed},
	},
	{
		Provider:      CIProviderBuildkite,
		DisplayName:   "Buildkite",
		Auth:          []CIAuthMethod{{Mode: CIAuthToken, Header: "X-Buildkite-Token"}},
		DefaultEvents: []string{intake.EventKindCIFailed},
	},
	{
		Provider:      CIProviderCircleCI,
		DisplayName:   "CircleCI",
		Auth:          []CIAuthMethod{{Mode: CIAuthHMAC, Header: "Circleci-Signature"}},
		DefaultEvents: []string{intake.EventKindCIFailed},
	},
}

// CIProviderPresets returns every supported preset. The slice is a copy; the
// caller must not depend on mutating it to change behavior.
func CIProviderPresets() []CIProviderPreset {
	return append([]CIProviderPreset(nil), ciProviderPresets...)
}

// CIProviderPresetFor resolves a provider name. Case and surrounding space are
// ignored; an empty name means the generic contract.
func CIProviderPresetFor(provider string) (CIProviderPreset, bool) {
	name := strings.ToLower(strings.TrimSpace(provider))
	if name == "" {
		name = CIProviderGeneric
	}
	for _, p := range ciProviderPresets {
		if p.Provider == name {
			return p, true
		}
	}
	return CIProviderPreset{}, false
}

// Credential returns the first credential presented in h together with the auth
// mode it belongs to. It does not validate the value — the caller compares or
// verifies it in constant time.
func (p CIProviderPreset) Credential(h http.Header) (mode, value string, ok bool) {
	if h == nil {
		return "", "", false
	}
	for _, m := range p.Auth {
		raw := strings.TrimSpace(h.Get(m.Header))
		if m.Mode == CIAuthBearer {
			const prefix = "bearer "
			if len(raw) >= len(prefix) && strings.EqualFold(raw[:len(prefix)], prefix) {
				if v := strings.TrimSpace(raw[len(prefix):]); v != "" {
					return m.Mode, v, true
				}
			}
			continue
		}
		if raw != "" {
			return m.Mode, raw, true
		}
	}
	return "", "", false
}

// CISignatureHex normalizes an HMAC header value: providers commonly prefix the
// hex digest with "sha256=" and may send it in mixed case.
func CISignatureHex(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '='); i >= 0 {
		s = s[i+1:]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// ciEvent is the provider-neutral intermediate every parser fills in.
type ciEvent struct {
	ExplicitID     string
	Status         string
	PreviousStatus string
	EventHint      string

	Repository  string
	Branch      string
	Commit      string
	PullRequest int64
	ThreadID    string

	PipelineID   string
	PipelineName string
	PipelineURL  string
	JobID        string
	JobName      string
	JobURL       string

	Summary    string
	LogExcerpt string
	Actor      intake.ActorRef
	URL        string
	ReceivedAt time.Time
}

// NormalizeCI maps one provider payload onto a Trigger. ok=false means the event
// matched no normalized CI kind (running, skipped, on_hold, ...) and must be
// ignored without error.
func NormalizeCI(projectID int64, bindingName, provider string, raw []byte) (intake.Trigger, bool, error) {
	name := strings.ToLower(strings.TrimSpace(provider))
	if name == "" {
		name = CIProviderGeneric
	}

	var (
		ev  ciEvent
		err error
	)
	switch name {
	case CIProviderGeneric, CIProviderJenkins:
		ev, err = parseGenericCI(raw)
	case CIProviderGitLab:
		ev, err = parseGitLabCI(raw)
	case CIProviderBuildkite:
		ev, err = parseBuildkiteCI(raw)
	case CIProviderCircleCI:
		ev, err = parseCircleCICI(raw)
	default:
		return intake.Trigger{}, false, fmt.Errorf("webhooksource: unknown ci provider %q", provider)
	}
	if err != nil {
		return intake.Trigger{}, false, err
	}

	kind, ok := ciEventKind(ev.Status, ev.PreviousStatus, ev.EventHint)
	if !ok {
		return intake.Trigger{}, false, nil
	}

	ev.LogExcerpt = truncateUTF8(strings.TrimSpace(ev.LogExcerpt), CIMaxLogExcerpt)
	return intake.Trigger{
		ProjectID:  projectID,
		SourceKind: intake.SourceKindCI,
		SourceRef:  bindingName,
		EventID:    ciEventID(ev, name, raw),
		EventKind:  kind,
		Thread:     intake.ThreadRef{ThreadExtID: ciThreadID(ev)},
		Actor:      ciActor(ev, name),
		Text:       ciText(ev, kind),
		Raw:        append(json.RawMessage(nil), raw...),
		ReceivedAt: ev.ReceivedAt,
		SourceURL:  firstNonEmpty(ev.JobURL, ev.PipelineURL, ev.URL),
		// A CI binding receives every event on it; an actor not on the allow
		// list is an inferred candidate, never an explicit request.
		ReceiveAll: true,
		Explicit:   false,
	}, true, nil
}

// --- status mapping ---------------------------------------------------------

// ciEventKind maps a provider status to one of the four CI kinds. A successful
// run whose previous state was a failure is a recovery, which is what closes the
// fix loop.
func ciEventKind(status, previous, hint string) (string, bool) {
	if kind, ok := ciStatusKind(status); ok {
		if kind == intake.EventKindCISucceeded && previousFailed(previous) {
			return intake.EventKindCIRecovered, true
		}
		return kind, true
	}
	switch strings.ToLower(strings.TrimSpace(hint)) {
	case intake.EventKindCIFailed:
		return intake.EventKindCIFailed, true
	case intake.EventKindCISucceeded:
		if previousFailed(previous) {
			return intake.EventKindCIRecovered, true
		}
		return intake.EventKindCISucceeded, true
	case intake.EventKindCIRecovered, intake.EventKindCICanceled:
		return strings.ToLower(strings.TrimSpace(hint)), true
	}
	return "", false
}

func ciStatusKind(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "failure", "error", "errored", "timed_out", "soft_failed",
		"unstable", "broken", "unauthorized":
		return intake.EventKindCIFailed, true
	case "succeeded", "success", "passed", "pass":
		return intake.EventKindCISucceeded, true
	case "canceled", "cancelled", "aborted":
		return intake.EventKindCICanceled, true
	}
	return "", false
}

func previousFailed(status string) bool {
	kind, ok := ciStatusKind(status)
	return ok && kind == intake.EventKindCIFailed
}

// --- provider parsers -------------------------------------------------------

func parseGenericCI(raw []byte) (ciEvent, error) {
	var p struct {
		ID             string          `json:"id"`
		EventID        string          `json:"event_id"`
		Event          string          `json:"event"`
		Repository     string          `json:"repository"`
		Branch         string          `json:"branch"`
		Commit         string          `json:"commit"`
		PullRequest    json.RawMessage `json:"pull_request"`
		ThreadID       string          `json:"thread_id"`
		Pipeline       ciEndpoint      `json:"pipeline"`
		Job            ciEndpoint      `json:"job"`
		Status         string          `json:"status"`
		PreviousStatus string          `json:"previous_status"`
		Conclusion     string          `json:"conclusion"`
		Summary        string          `json:"summary"`
		LogExcerpt     string          `json:"log_excerpt"`
		Actor          json.RawMessage `json:"actor"`
		URL            string          `json:"url"`
		ReceivedAt     string          `json:"received_at"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ciEvent{}, fmt.Errorf("webhooksource: decode ci payload: %w", err)
	}
	actorID, actorName, isBot := parseActor(p.Actor)
	return ciEvent{
		ExplicitID:     firstNonEmpty(p.ID, p.EventID),
		Status:         firstNonEmpty(p.Status, p.Conclusion),
		PreviousStatus: p.PreviousStatus,
		EventHint:      p.Event,
		Repository:     strings.TrimSpace(p.Repository),
		Branch:         strings.TrimSpace(p.Branch),
		Commit:         strings.TrimSpace(p.Commit),
		PullRequest:    parsePullRequest(p.PullRequest),
		ThreadID:       strings.TrimSpace(p.ThreadID),
		PipelineID:     strings.TrimSpace(p.Pipeline.ID),
		PipelineName:   strings.TrimSpace(p.Pipeline.Name),
		PipelineURL:    strings.TrimSpace(p.Pipeline.URL),
		JobID:          strings.TrimSpace(p.Job.ID),
		JobName:        strings.TrimSpace(p.Job.Name),
		JobURL:         strings.TrimSpace(p.Job.URL),
		Summary:        strings.TrimSpace(p.Summary),
		LogExcerpt:     p.LogExcerpt,
		Actor:          intake.ActorRef{ExtID: actorID, Name: actorName, IsBot: isBot},
		URL:            strings.TrimSpace(p.URL),
		ReceivedAt:     parseTime(p.ReceivedAt),
	}, nil
}

type ciEndpoint struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

func parseGitLabCI(raw []byte) (ciEvent, error) {
	var p struct {
		ObjectKind       string `json:"object_kind"`
		ObjectAttributes struct {
			ID             int64  `json:"id"`
			Status         string `json:"status"`
			PreviousStatus string `json:"previous_status"`
			Ref            string `json:"ref"`
			Sha            string `json:"sha"`
			Name           string `json:"name"`
			URL            string `json:"url"`
		} `json:"object_attributes"`
		PreviousStatus string `json:"previous_status"`
		Project struct {
			PathWithNamespace string `json:"path_with_namespace"`
			Name              string `json:"name"`
			WebURL            string `json:"web_url"`
		} `json:"project"`
		MergeRequest *struct {
			IID int64 `json:"iid"`
		} `json:"merge_request"`
		User struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Username string `json:"username"`
		} `json:"user"`
		// Build (job) events carry their fields at the top level.
		BuildID     int64  `json:"build_id"`
		BuildName   string `json:"build_name"`
		BuildStatus string `json:"build_status"`
		PipelineID  int64  `json:"pipeline_id"`
		Sha         string `json:"sha"`
		Ref         string `json:"ref"`
		ProjectName string `json:"project_name"`
		Repository  struct {
			Name     string `json:"name"`
			Homepage string `json:"homepage"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ciEvent{}, fmt.Errorf("webhooksource: decode gitlab payload: %w", err)
	}

	ev := ciEvent{
		Status:         firstNonEmpty(p.ObjectAttributes.Status, p.BuildStatus),
		PreviousStatus: firstNonEmpty(p.ObjectAttributes.PreviousStatus, p.PreviousStatus),
		Repository:     firstNonEmpty(p.Project.PathWithNamespace, p.ProjectName, p.Repository.Name),
		Branch:       firstNonEmpty(p.ObjectAttributes.Ref, p.Ref),
		Commit:       firstNonEmpty(p.ObjectAttributes.Sha, p.Sha),
		PipelineName: strings.TrimSpace(p.ObjectAttributes.Name),
		PipelineURL:  strings.TrimSpace(p.ObjectAttributes.URL),
		URL:          strings.TrimSpace(p.ObjectAttributes.URL),
	}
	if pr := p.MergeRequest; pr != nil {
		ev.PullRequest = pr.IID
	}
	if p.ObjectAttributes.ID != 0 {
		ev.PipelineID = strconv.FormatInt(p.ObjectAttributes.ID, 10)
	}
	if p.PipelineID != 0 {
		ev.PipelineID = strconv.FormatInt(p.PipelineID, 10)
	}
	if p.BuildID != 0 {
		ev.JobID = strconv.FormatInt(p.BuildID, 10)
		ev.JobName = strings.TrimSpace(p.BuildName)
		if homepage := strings.TrimSpace(p.Repository.Homepage); homepage != "" {
			ev.JobURL = strings.TrimSuffix(homepage, "/") + "/-/jobs/" + ev.JobID
		}
	}
	if p.User.ID != 0 || p.User.Name != "" {
		ev.Actor = intake.ActorRef{
			ExtID: firstNonEmpty(strconv.FormatInt(p.User.ID, 10), p.User.Name, p.User.Username),
			Name:  firstNonEmpty(p.User.Name, p.User.Username),
		}
		if p.User.ID == 0 {
			ev.Actor.ExtID = firstNonEmpty(p.User.Username, p.User.Name)
		}
	}
	return ev, nil
}

func parseBuildkiteCI(raw []byte) (ciEvent, error) {
	var p struct {
		Event string `json:"event"`
		Build struct {
			ID            string `json:"id"`
			Number        int64  `json:"number"`
			State         string `json:"state"`
			PreviousState string `json:"previous_state"`
			Branch        string `json:"branch"`
			Commit        string `json:"commit"`
			Message       string `json:"message"`
			WebURL        string `json:"web_url"`
			URL           string `json:"url"`
		} `json:"build"`
		Pipeline struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Repository string `json:"repository"`
			URL        string `json:"url"`
		} `json:"pipeline"`
		Sender struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"sender"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ciEvent{}, fmt.Errorf("webhooksource: decode buildkite payload: %w", err)
	}

	ev := ciEvent{
		Status:         strings.TrimSpace(p.Build.State),
		PreviousStatus: strings.TrimSpace(p.Build.PreviousState),
		Repository:     ciRepoSlug(p.Pipeline.Repository),
		Branch:       strings.TrimSpace(p.Build.Branch),
		Commit:       strings.TrimSpace(p.Build.Commit),
		PipelineID:   firstNonEmpty(p.Pipeline.ID, p.Build.ID, buildkiteNumber(p.Build.Number)),
		PipelineName: strings.TrimSpace(p.Pipeline.Name),
		PipelineURL:  firstNonEmpty(p.Pipeline.URL, p.Build.WebURL, p.Build.URL),
		Summary:      strings.TrimSpace(p.Build.Message),
		URL:          firstNonEmpty(p.Build.WebURL, p.Build.URL),
	}
	if p.Sender.ID != "" || p.Sender.Name != "" {
		ev.Actor = intake.ActorRef{
			ExtID: firstNonEmpty(p.Sender.ID, p.Sender.Email, p.Sender.Name),
			Name:  firstNonEmpty(p.Sender.Name, p.Sender.Email),
		}
	}
	return ev, nil
}

func parseCircleCICI(raw []byte) (ciEvent, error) {
	var p struct {
		Type       string         `json:"type"`
		ID         string         `json:"id"`
		HappenedAt string         `json:"happened_at"`
		Workflow   circleWorkflow `json:"workflow"`
		Pipeline   circlePipeline `json:"pipeline"`
		Project    struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"project"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ciEvent{}, fmt.Errorf("webhooksource: decode circleci payload: %w", err)
	}

	pipe := p.Workflow.Pipeline
	if pipe.ID == "" {
		pipe = p.Pipeline
	}
	ev := ciEvent{
		ExplicitID:     strings.TrimSpace(p.ID),
		Status:         strings.TrimSpace(p.Workflow.Status),
		PreviousStatus: strings.TrimSpace(p.Workflow.PreviousStatus),
		Repository:     firstNonEmpty(pipe.Repository.FullName, ciRepoSlug(p.Project.Slug), p.Project.Name),
		Branch:       strings.TrimSpace(pipe.VCS.Branch),
		Commit:       strings.TrimSpace(pipe.VCS.Revision),
		PipelineID:   strings.TrimSpace(pipe.ID),
		PipelineName: strings.TrimSpace(p.Workflow.Name),
		PipelineURL:  strings.TrimSpace(p.Workflow.URL),
		URL:          strings.TrimSpace(p.Workflow.URL),
		ReceivedAt:   parseTime(p.HappenedAt),
	}
	return ev, nil
}

type circleWorkflow struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Status         string         `json:"status"`
	PreviousStatus string         `json:"previous_status"`
	URL            string         `json:"url"`
	Pipeline       circlePipeline `json:"pipeline"`
}

type circlePipeline struct {
	ID          string `json:"id"`
	Number      int64  `json:"number"`
	ProjectSlug string `json:"project_slug"`
	VCS         struct {
		Branch   string `json:"branch"`
		Revision string `json:"revision"`
	} `json:"vcs"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		URL      string `json:"url"`
	} `json:"repository"`
}

// --- trigger assembly -------------------------------------------------------

// ciEventID prefers the provider's own stable id; when absent it composes one
// from the pipeline/job/status/commit tuple so a retry of the same event still
// dedupes. A payload with none of those falls back to the body hash.
func ciEventID(ev ciEvent, provider string, raw []byte) string {
	if id := strings.TrimSpace(ev.ExplicitID); id != "" {
		return id
	}
	var parts []string
	if ev.PipelineID != "" {
		parts = append(parts, "pipeline:"+ev.PipelineID)
	}
	if ev.JobID != "" {
		parts = append(parts, "job:"+ev.JobID)
	}
	if len(parts) > 0 {
		id := provider + ":" + strings.Join(parts, ":")
		if s := strings.ToLower(strings.TrimSpace(ev.Status)); s != "" {
			id += ":" + s
		}
		if s := strings.TrimSpace(ev.Commit); s != "" {
			id += ":" + s
		}
		return id
	}
	return "body:" + bodyHash(raw)
}

// ciThreadID binds repeat failures of the same pipeline to one conversation:
// an explicit thread wins, then the pull request, then the commit.
func ciThreadID(ev ciEvent) string {
	if t := strings.TrimSpace(ev.ThreadID); t != "" {
		return t
	}
	repo := strings.TrimSpace(ev.Repository)
	if repo == "" {
		repo = "ci"
	}
	if ev.PullRequest > 0 {
		return repo + "#pr:" + strconv.FormatInt(ev.PullRequest, 10)
	}
	branch := strings.TrimSpace(ev.Branch)
	commit := strings.TrimSpace(ev.Commit)
	if branch == "" && commit == "" {
		return ""
	}
	return repo + "#" + branch + "#" + commit
}

func ciActor(ev ciEvent, provider string) intake.ActorRef {
	if ev.Actor.ExtID != "" || ev.Actor.Name != "" {
		if ev.Actor.ExtID == "" {
			ev.Actor.ExtID = ev.Actor.Name
		}
		if ev.Actor.Name == "" {
			ev.Actor.Name = ev.Actor.ExtID
		}
		return ev.Actor
	}
	return intake.ActorRef{ExtID: "ci:" + provider, Name: provider + " CI", IsBot: true}
}

func ciText(ev ciEvent, kind string) string {
	blocks := make([]string, 0, 3)
	if ev.Summary != "" {
		blocks = append(blocks, ev.Summary)
	}
	var lines []string
	if v := firstNonEmpty(ev.PipelineName, ev.PipelineID); v != "" {
		lines = append(lines, "pipeline: "+v)
	}
	if v := firstNonEmpty(ev.JobName, ev.JobID); v != "" {
		lines = append(lines, "job: "+v)
	}
	if ev.Branch != "" {
		lines = append(lines, "branch: "+ev.Branch)
	}
	if ev.Commit != "" {
		lines = append(lines, "commit: "+ev.Commit)
	}
	if len(lines) > 0 {
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	if ev.LogExcerpt != "" {
		blocks = append(blocks, ev.LogExcerpt)
	}
	if len(blocks) == 0 {
		return kind
	}
	return strings.Join(blocks, "\n\n")
}

// --- small helpers ----------------------------------------------------------

func parsePullRequest(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	var obj struct {
		IID    int64 `json:"iid"`
		Number int64 `json:"number"`
		ID     int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0
	}
	switch {
	case obj.IID > 0:
		return obj.IID
	case obj.Number > 0:
		return obj.Number
	default:
		return obj.ID
	}
}

// ciRepoSlug reduces a clone URL or provider slug to "org/repo".
func ciRepoSlug(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "git@")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.Replace(s, ":", "/", 1)
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")
	parts := strings.Split(s, "/")
	// CircleCI uses a VCS-prefixed slug: gh/org/repo, bb/org/repo.
	if len(parts) > 2 && (parts[0] == "gh" || parts[0] == "bb") {
		parts = parts[1:]
	}
	if len(parts) > 2 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		parts = parts[1:]
	}
	return strings.Join(parts, "/")
}

func buildkiteNumber(n int64) string {
	if n <= 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
