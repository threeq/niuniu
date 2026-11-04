// Package githubsource is the GitHub Source adapter for the personal edition
// (Epic #755 / design §6.10). It polls the GitHub REST API with since/ETag
// cursors — no public webhook endpoint is required — normalizes each event into
// the platform-neutral intake.Trigger, and hands it to the shared Intake
// pipeline, which owns dedupe/authorization/execution.
//
// External payload text is data, never instructions: normalization only maps
// fields, it never derives authority from message content.
package githubsource

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

// Event kinds emitted by this adapter. They match the delivered event types.
const (
	EventIssueComment      = intake.EventKindIssueComment
	EventReview            = intake.EventKindReview
	EventReviewComment     = intake.EventKindReviewComment
	EventCheckRun          = intake.EventKindCheckRun
	EventWorkflowRun       = intake.EventKindWorkflowRun
	EventPullRequestClosed = intake.EventKindPullRequestClosed
)

// KnownEvents is the configurable event set, in display order.
var KnownEvents = []string{
	EventIssueComment,
	EventReview,
	EventReviewComment,
	EventCheckRun,
	EventWorkflowRun,
	EventPullRequestClosed,
}

// IsKnownEvent reports whether kind is one of the configurable event types.
func IsKnownEvent(kind string) bool {
	for _, k := range KnownEvents {
		if k == kind {
			return true
		}
	}
	return false
}

// user is the subset of a GitHub user/actor we keep.
type user struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// appRef is the app fragment on a check run. Slug is the app's stable
// identifier; its bot-user login is "<slug>[bot]".
type appRef struct {
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Owner *user  `json:"owner"`
}

// Comment shapes from the REST list endpoints. The list endpoints omit the
// issue/pull request object, so the number is recovered from issue_url /
// pull_request_url.

type issueComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	IssueURL  string    `json:"issue_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	User      user      `json:"user"`
}

type pullRequestReview struct {
	ID             int64      `json:"id"`
	Body           string     `json:"body"`
	State          string     `json:"state"`
	HTMLURL        string     `json:"html_url"`
	PullRequestURL string     `json:"pull_request_url"`
	SubmittedAt    *time.Time `json:"submitted_at"`
	User           user       `json:"user"`
}

type pullRequestReviewComment struct {
	ID             int64     `json:"id"`
	Body           string    `json:"body"`
	HTMLURL        string    `json:"html_url"`
	PullRequestURL string    `json:"pull_request_url"`
	Path           string    `json:"path"`
	Line           int       `json:"line"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	User           user      `json:"user"`
}

type checkRun struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	HTMLURL     string     `json:"html_url"`
	HeadSHA     string     `json:"head_sha"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	App         *appRef `json:"app"`
	CheckSuite struct {
		PullRequests []pullRef `json:"pull_requests"`
	} `json:"check_suite"`
}

type workflowRun struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Event        string     `json:"event"`
	Status       string     `json:"status"`
	Conclusion   string     `json:"conclusion"`
	HeadBranch   string     `json:"head_branch"`
	HeadSHA      string     `json:"head_sha"`
	HTMLURL      string     `json:"html_url"`
	CreatedAt    time.Time  `json:"created_at"`
	RunStartedAt *time.Time `json:"run_started_at"`
	Actor        *user      `json:"actor"`
	PullRequests []pullRef  `json:"pull_requests"`
}

type pullRef struct {
	Number int `json:"number"`
}

type pullRequest struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	Merged    bool       `json:"merged"`
	MergedAt  *time.Time `json:"merged_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	HTMLURL   string     `json:"html_url"`
	User      user       `json:"user"`
	Head      struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// searchPull is the pull_request fragment on a /search/issues item.
type searchPull struct {
	MergedAt *time.Time `json:"merged_at"`
}

// searchIssue is the /search/issues item shape used for closed PRs.
type searchIssue struct {
	Number    int         `json:"number"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	State     string      `json:"state"`
	ClosedAt  *time.Time  `json:"closed_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	HTMLURL   string      `json:"html_url"`
	User      user        `json:"user"`
	Pull      *searchPull `json:"pull_request"`
}

// NumberFromURL extracts the trailing number from an issue/pull API URL such as
// https://api.github.com/repos/o/r/issues/42.
func NumberFromURL(raw, marker string) (int, bool) {
	idx := strings.Index(raw, marker)
	if idx < 0 {
		return 0, false
	}
	rest := raw[idx+len(marker):]
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		rest = rest[:end]
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func actorRef(u user) intake.ActorRef {
	return intake.ActorRef{
		ExtID: u.Login,
		Name:  u.Login,
		IsBot: IsBot(u),
	}
}

// IsBot reports whether a GitHub actor is a bot ([bot] suffix or a Bot account
// type). GitHub App bot users always have a "<app-slug>[bot]" login and type
// "Bot". It is a fact about the actor, not an authorization decision.
func IsBot(u user) bool {
	return strings.EqualFold(u.Type, "Bot") ||
		strings.HasSuffix(strings.ToLower(u.Login), "[bot]")
}

// appActor identifies the GitHub App that produced an event. Check runs carry no
// user object: the actor is the app, and its bot-user login is derived from the
// app slug so an allow/bot list entry like "github-actions[bot]" matches.
func appActor(app *appRef) user {
	actor := user{Type: "Bot"}
	if app == nil {
		return actor
	}
	switch {
	case strings.TrimSpace(app.Slug) != "":
		actor.Login = strings.TrimPrefix(strings.TrimSpace(app.Slug), "@") + "[bot]"
	case strings.TrimSpace(app.Name) != "":
		actor.Login = strings.TrimSpace(app.Name)
	}
	return actor
}

func baseTrigger(projectID int64, repo, eventID, kind, thread string, actor user, text, sourceURL string, payload any) intake.Trigger {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = nil
	}
	return intake.Trigger{
		ProjectID:  projectID,
		SourceKind: intake.SourceKindGitHub,
		SourceRef:  repo,
		EventID:    eventID,
		EventKind:  kind,
		Thread:     intake.ThreadRef{ThreadExtID: thread},
		Actor:      actorRef(actor),
		Text:       text,
		Raw:        raw,
		ReceivedAt: time.Now(),
		SourceURL:  sourceURL,
		// Polling reads the repository in full: unlisted actors become inferred
		// candidates (§6.2). Explicit authority still comes only from the allow
		// list, which Policy.Evaluate checks first.
		ReceiveAll: true,
	}
}

func threadID(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func commentText(body, fallback string) string {
	if t := strings.TrimSpace(body); t != "" {
		return t
	}
	return fallback
}

// NormalizeIssueComment maps an issues/comments list item (which covers both
// issue and pull-request comments) to a Trigger.
func NormalizeIssueComment(projectID int64, repo string, c issueComment) (intake.Trigger, bool) {
	num, ok := NumberFromURL(c.IssueURL, "/issues/")
	if !ok {
		return intake.Trigger{}, false
	}
	thread := threadID(num)
	return baseTrigger(projectID, repo,
		fmt.Sprintf("issue_comment:%d", c.ID), EventIssueComment, thread,
		c.User, commentText(c.Body, fmt.Sprintf("comment on #%d", num)),
		c.HTMLURL, c), true
}

// NormalizeReview maps a PR review to a Trigger. Pending reviews (no
// submitted_at) are skipped: they are not visible to anyone yet.
func NormalizeReview(projectID int64, repo string, r pullRequestReview) (intake.Trigger, bool) {
	num, ok := NumberFromURL(r.PullRequestURL, "/pulls/")
	if !ok || r.SubmittedAt == nil {
		return intake.Trigger{}, false
	}
	thread := threadID(num)
	text := commentText(r.Body, fmt.Sprintf("%s review on #%d", strings.ToLower(r.State), num))
	return baseTrigger(projectID, repo,
		fmt.Sprintf("pull_request_review:%d", r.ID), EventReview, thread,
		r.User, text, r.HTMLURL, r), true
}

// NormalizeReviewComment maps an inline PR review comment to a Trigger.
func NormalizeReviewComment(projectID int64, repo string, c pullRequestReviewComment) (intake.Trigger, bool) {
	num, ok := NumberFromURL(c.PullRequestURL, "/pulls/")
	if !ok {
		return intake.Trigger{}, false
	}
	thread := threadID(num)
	fallback := fmt.Sprintf("review comment on #%d", num)
	if c.Path != "" {
		fallback = fmt.Sprintf("review comment on %s in #%d", c.Path, num)
	}
	return baseTrigger(projectID, repo,
		fmt.Sprintf("pull_request_review_comment:%d", c.ID), EventReviewComment, thread,
		c.User, commentText(c.Body, fallback), c.HTMLURL, c), true
}

// NormalizeCheckRun maps a completed check run to a Trigger. In-flight runs are
// skipped so a run produces one event (its final result) instead of one per
// status transition.
func NormalizeCheckRun(projectID int64, repo string, c checkRun) (intake.Trigger, bool) {
	if c.Conclusion == "" {
		return intake.Trigger{}, false
	}
	num := 0
	if len(c.CheckSuite.PullRequests) > 0 {
		num = c.CheckSuite.PullRequests[0].Number
	}
	// Check runs are always produced by a GitHub App, so the actor is a bot
	// even when the payload omits the app block.
	actor := appActor(c.App)
	sha := c.HeadSHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	text := fmt.Sprintf("check %s: %s", c.Name, c.Conclusion)
	if sha != "" {
		text += " (" + sha + ")"
	}
	return baseTrigger(projectID, repo,
		fmt.Sprintf("check_run:%d", c.ID), EventCheckRun, threadID(num),
		actor, text, c.HTMLURL, c), true
}

// NormalizeWorkflowRun maps a completed workflow run to a Trigger.
func NormalizeWorkflowRun(projectID int64, repo string, w workflowRun) (intake.Trigger, bool) {
	if w.Status != "completed" || w.Conclusion == "" {
		return intake.Trigger{}, false
	}
	num := 0
	if len(w.PullRequests) > 0 {
		num = w.PullRequests[0].Number
	}
	var actor user
	if w.Actor != nil {
		actor = *w.Actor
	}
	text := fmt.Sprintf("workflow %s: %s", w.Name, w.Conclusion)
	if w.HeadBranch != "" {
		text += " (" + w.HeadBranch + ")"
	}
	return baseTrigger(projectID, repo,
		fmt.Sprintf("workflow_run:%d", w.ID), EventWorkflowRun, threadID(num),
		actor, text, w.HTMLURL, w), true
}

// NormalizeClosedPullRequest maps a closed/merged PR to a Trigger. The event id
// includes the close timestamp so a later reopen/re-close is a new event.
func NormalizeClosedPullRequest(projectID int64, repo string, pr pullRequest) (intake.Trigger, bool) {
	if pr.State != "closed" && pr.ClosedAt == nil {
		return intake.Trigger{}, false
	}
	return normalizeClosed(projectID, repo, pr.Number, pr.Title, pr.Body, pr.ClosedAt,
		pr.MergedAt, pr.HTMLURL, pr.User)
}

// NormalizeSearchClosedPullRequest maps a /search/issues closed-PR item.
func NormalizeSearchClosedPullRequest(projectID int64, repo string, it searchIssue) (intake.Trigger, bool) {
	if it.Pull == nil {
		return intake.Trigger{}, false
	}
	return normalizeClosed(projectID, repo, it.Number, it.Title, it.Body, it.ClosedAt,
		it.Pull.MergedAt, it.HTMLURL, it.User)
}

func normalizeClosed(projectID int64, repo string, num int, title, body string,
	closedAt, mergedAt *time.Time, url string, actor user) (intake.Trigger, bool) {
	if num <= 0 || closedAt == nil {
		return intake.Trigger{}, false
	}
	state := "closed"
	if mergedAt != nil {
		state = "merged"
	}
	text := fmt.Sprintf("PR #%d %s: %s", num, state, strings.TrimSpace(title))
	if b := strings.TrimSpace(body); b != "" {
		text += "\n\n" + b
	}
	return baseTrigger(projectID, repo,
		fmt.Sprintf("pull_request_closed:%d:%d", num, closedAt.Unix()),
		EventPullRequestClosed, threadID(num), actor, text, url,
		map[string]any{
			"number": num, "title": title, "state": state,
			"closed_at": closedAt, "merged_at": mergedAt, "html_url": url,
		}), true
}
