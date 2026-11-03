package githubsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

// DefaultBaseURL is the public GitHub REST API. GitHub Enterprise instances can
// override it (the credential/UI wiring passes the host through).
const DefaultBaseURL = "https://api.github.com"

// HTTPClient is the subset of *http.Client the poller needs (fakeable in tests).
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// RepoConfig is one repository to poll.
type RepoConfig struct {
	ProjectID int64
	Repo      string // "owner/name"
	Token     string
	// Events is the enabled event-type subset. Empty means every known event.
	Events []string
}

// State is the persisted cursor for one (project, repo, stream).
type State struct {
	ProjectID    int64
	Repo         string
	Stream       string
	Since        time.Time
	ETag         string
	FailureCount int
	BackoffUntil time.Time
	LastPolledAt time.Time
	LastError    string
}

// StateStore persists poller cursors and backoff state. Load must return a zero
// State (no error) when no row exists.
type StateStore interface {
	Load(ctx context.Context, projectID int64, repo, stream string) (State, error)
	Save(ctx context.Context, st State) error
}

// Sink receives normalized triggers. It is the Intake pipeline.
type Sink interface {
	Handle(ctx context.Context, t intake.Trigger) error
}

// RateLimitError reports a GitHub rate-limit response. Backoff runs until
// ResetAt so the source recovers automatically when the window resets.
type RateLimitError struct {
	Status  int
	ResetAt time.Time
	Message string
}

func (e *RateLimitError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("github rate limited: %s", e.Message)
	}
	return fmt.Sprintf("github rate limited (status %d)", e.Status)
}

// APIError is a non-success response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github api status %d: %s", e.Status, e.Message)
}

// Poller polls repositories with since/ETag cursors and feeds the Intake
// pipeline. It does not dedupe itself — the pipeline's
// UNIQUE(source_kind, source_ref, event_ext_id) does, which also makes the
// overlapping since window safe.
type Poller struct {
	Client HTTPClient
	// BaseURL defaults to DefaultBaseURL.
	BaseURL string
	Store   StateStore
	Sink    Sink
	// Clock defaults to time.Now.
	Clock func() time.Time

	// InitialLookback is how far back a first poll starts. Default 24h.
	InitialLookback time.Duration
	// Overlap re-reads a small window at each cursor boundary so events whose
	// timestamps tie with the cursor are not lost. Default 2m.
	Overlap time.Duration
	// BackoffBase/BackoffMax bound exponential retry after non-rate failures.
	// Defaults 30s / 30m.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// MaxPages caps pagination per stream. Default 10 (1000 items).
	MaxPages int

	Log *slog.Logger
}

func (p *Poller) now() time.Time {
	if p.Clock != nil {
		return p.Clock()
	}
	return time.Now()
}

func (p *Poller) baseURL() string {
	if b := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/"); b != "" {
		return b
	}
	return DefaultBaseURL
}

func (p *Poller) initialLookback() time.Duration {
	if p.InitialLookback > 0 {
		return p.InitialLookback
	}
	return 24 * time.Hour
}

func (p *Poller) overlap() time.Duration {
	if p.Overlap > 0 {
		return p.Overlap
	}
	return 2 * time.Minute
}

func (p *Poller) backoffFor(failures int) time.Duration {
	base := p.BackoffBase
	if base <= 0 {
		base = 30 * time.Second
	}
	max := p.BackoffMax
	if max <= 0 {
		max = 30 * time.Minute
	}
	d := base
	for i := 1; i < failures && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

func (p *Poller) maxPages() int {
	if p.MaxPages > 0 {
		return p.MaxPages
	}
	return 10
}

func (p *Poller) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// EnabledEvents returns the configured event set (all known when empty),
// de-duplicated and in canonical order.
func EnabledEvents(events []string) []string {
	if len(events) == 0 {
		return append([]string(nil), KnownEvents...)
	}
	want := make(map[string]bool, len(events))
	for _, e := range events {
		want[strings.TrimSpace(e)] = true
	}
	out := make([]string, 0, len(want))
	for _, e := range KnownEvents {
		if want[e] {
			out = append(out, e)
		}
	}
	return out
}

func needsPullList(stream string) bool {
	return stream == EventReview || stream == EventCheckRun
}

// PollRepo advances every enabled stream for one repository. A failure on one
// stream records backoff for that stream and does not prevent the others from
// polling, except a rate limit, which is repo/account-wide and stops the run.
func (p *Poller) PollRepo(ctx context.Context, cfg RepoConfig) error {
	if p.Store == nil || p.Sink == nil {
		return errors.New("githubsource: poller needs a state store and a sink")
	}
	if _, _, ok := splitRepo(cfg.Repo); !ok {
		return fmt.Errorf("githubsource: invalid repo %q", cfg.Repo)
	}

	now := p.now()
	streams := EnabledEvents(cfg.Events)

	states := make(map[string]State, len(streams))
	for _, stream := range streams {
		st, err := p.Store.Load(ctx, cfg.ProjectID, cfg.Repo, stream)
		if err != nil {
			return err
		}
		if st.Since.IsZero() {
			st.Since = now.Add(-p.initialLookback())
		}
		st.ProjectID, st.Repo, st.Stream = cfg.ProjectID, cfg.Repo, stream
		states[stream] = st
	}

	// Both PR-child streams share one "pulls updated since" listing. Load it
	// from the earliest of their cursors so neither stream misses entries.
	var prSince time.Time
	for _, stream := range []string{EventReview, EventCheckRun} {
		if st, ok := states[stream]; ok {
			if prSince.IsZero() || st.Since.Before(prSince) {
				prSince = st.Since
			}
		}
	}
	run := &pollRun{cfg: cfg, now: now, prSince: prSince}

	var firstErr error
	for _, stream := range streams {
		st := states[stream]
		if st.BackoffUntil.After(now) {
			continue
		}
		if err := p.pollStream(ctx, run, stream, &st); err != nil {
			st.FailureCount++
			st.LastError = err.Error()
			st.LastPolledAt = now
			var rl *RateLimitError
			if errors.As(err, &rl) && !rl.ResetAt.IsZero() {
				st.BackoffUntil = rl.ResetAt
			} else {
				st.BackoffUntil = now.Add(p.backoffFor(st.FailureCount))
			}
			if serr := p.Store.Save(ctx, st); serr != nil {
				p.log().Warn("githubsource: save backoff state failed", "repo", cfg.Repo, "stream", stream, "error", serr)
			}
			if firstErr == nil {
				firstErr = err
			}
			// A rate limit is account-wide; stop instead of hammering every
			// remaining stream.
			if errors.As(err, &rl) {
				return err
			}
			continue
		}

		st.FailureCount = 0
		st.BackoffUntil = time.Time{}
		st.LastError = ""
		st.LastPolledAt = now
		if next := now.Add(-p.overlap()); next.After(st.Since) {
			st.Since = next
		}
		if err := p.Store.Save(ctx, st); err != nil {
			return err
		}
	}
	return firstErr
}

type pollRun struct {
	cfg     RepoConfig
	now     time.Time
	prSince time.Time

	prs       []pullRequest
	prsLoaded bool
}

func (p *Poller) pollStream(ctx context.Context, run *pollRun, stream string, st *State) error {
	switch stream {
	case EventIssueComment:
		return p.pollIssueComments(ctx, run, st)
	case EventReviewComment:
		return p.pollReviewComments(ctx, run, st)
	case EventReview:
		return p.pollReviews(ctx, run, st)
	case EventCheckRun:
		return p.pollCheckRuns(ctx, run, st)
	case EventWorkflowRun:
		return p.pollWorkflowRuns(ctx, run, st)
	case EventPullRequestClosed:
		return p.pollClosedPullRequests(ctx, run, st)
	default:
		return nil
	}
}

func (p *Poller) pollIssueComments(ctx context.Context, run *pollRun, st *State) error {
	target := fmt.Sprintf("%s/repos/%s/issues/comments?sort=updated&direction=asc&per_page=100&since=%s",
		p.baseURL(), repoPath(run.cfg.Repo), url.QueryEscape(st.Since.UTC().Format(time.RFC3339)))
	etag, err := p.paginate(ctx, run.cfg, target, st.ETag, func(item json.RawMessage) error {
		var c issueComment
		if err := json.Unmarshal(item, &c); err != nil {
			return err
		}
		if c.UpdatedAt.Before(st.Since) {
			return nil
		}
		if trig, ok := NormalizeIssueComment(run.cfg.ProjectID, run.cfg.Repo, c); ok {
			return p.Sink.Handle(ctx, trig)
		}
		return nil
	})
	st.ETag = etag
	return err
}

func (p *Poller) pollReviewComments(ctx context.Context, run *pollRun, st *State) error {
	target := fmt.Sprintf("%s/repos/%s/pulls/comments?sort=updated&direction=asc&per_page=100&since=%s",
		p.baseURL(), repoPath(run.cfg.Repo), url.QueryEscape(st.Since.UTC().Format(time.RFC3339)))
	etag, err := p.paginate(ctx, run.cfg, target, st.ETag, func(item json.RawMessage) error {
		var c pullRequestReviewComment
		if err := json.Unmarshal(item, &c); err != nil {
			return err
		}
		if c.UpdatedAt.Before(st.Since) {
			return nil
		}
		if trig, ok := NormalizeReviewComment(run.cfg.ProjectID, run.cfg.Repo, c); ok {
			return p.Sink.Handle(ctx, trig)
		}
		return nil
	})
	st.ETag = etag
	return err
}

func (p *Poller) pollReviews(ctx context.Context, run *pollRun, st *State) error {
	prs, err := p.updatedPullRequests(ctx, run)
	if err != nil {
		return err
	}
	for _, pr := range prs {
		if pr.UpdatedAt.Before(st.Since) {
			continue
		}
		target := fmt.Sprintf("%s/repos/%s/pulls/%d/reviews?per_page=100",
			p.baseURL(), repoPath(run.cfg.Repo), pr.Number)
		var reviews []pullRequestReview
		if _, _, _, err := p.getJSON(ctx, run.cfg, target, "", &reviews); err != nil {
			return err
		}
		for _, r := range reviews {
			if r.SubmittedAt == nil || r.SubmittedAt.Before(st.Since) {
				continue
			}
			if trig, ok := NormalizeReview(run.cfg.ProjectID, run.cfg.Repo, r); ok {
				if err := p.Sink.Handle(ctx, trig); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *Poller) pollCheckRuns(ctx context.Context, run *pollRun, st *State) error {
	// Check runs hang off a commit, and completing one does not bump the PR's
	// updated_at, so gate on the check run's own timestamp: scan every open PR
	// each tick instead of only the recently updated ones.
	prs, err := p.openPullRequests(ctx, run)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(prs))
	for _, pr := range prs {
		// Check runs hang off a commit, not a PR: fetch per head sha once.
		sha := pr.Head.SHA
		if sha == "" || seen[sha] {
			continue
		}
		seen[sha] = true
		target := fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?filter=all&per_page=100",
			p.baseURL(), repoPath(run.cfg.Repo), url.PathEscape(sha))
		var page struct {
			CheckRuns []checkRun `json:"check_runs"`
		}
		if _, _, _, err := p.getJSON(ctx, run.cfg, target, "", &page); err != nil {
			return err
		}
		for _, c := range page.CheckRuns {
			if ts := checkRunTime(c); ts.IsZero() || ts.Before(st.Since) {
				continue
			}
			if trig, ok := NormalizeCheckRun(run.cfg.ProjectID, run.cfg.Repo, c); ok {
				if err := p.Sink.Handle(ctx, trig); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *Poller) pollWorkflowRuns(ctx context.Context, run *pollRun, st *State) error {
	target := fmt.Sprintf("%s/repos/%s/actions/runs?per_page=100&created=%s",
		p.baseURL(), repoPath(run.cfg.Repo), url.QueryEscape(">="+st.Since.UTC().Format(time.RFC3339)))
	etag, err := p.paginate(ctx, run.cfg, target, st.ETag, func(item json.RawMessage) error {
		var w workflowRun
		if err := json.Unmarshal(item, &w); err != nil {
			return err
		}
		if w.CreatedAt.Before(st.Since) {
			return nil
		}
		if trig, ok := NormalizeWorkflowRun(run.cfg.ProjectID, run.cfg.Repo, w); ok {
			return p.Sink.Handle(ctx, trig)
		}
		return nil
	}, "workflow_runs")
	st.ETag = etag
	return err
}

func (p *Poller) pollClosedPullRequests(ctx context.Context, run *pollRun, st *State) error {
	q := fmt.Sprintf("repo:%s is:pr is:closed updated:>=%s",
		run.cfg.Repo, st.Since.UTC().Format(time.RFC3339))
	target := fmt.Sprintf("%s/search/issues?per_page=100&sort=updated&order=asc&q=%s",
		p.baseURL(), url.QueryEscape(q))
	etag, err := p.paginate(ctx, run.cfg, target, st.ETag, func(item json.RawMessage) error {
		var it searchIssue
		if err := json.Unmarshal(item, &it); err != nil {
			return err
		}
		ts := it.UpdatedAt
		if it.ClosedAt != nil {
			ts = *it.ClosedAt
		}
		if ts.Before(st.Since) {
			return nil
		}
		if trig, ok := NormalizeSearchClosedPullRequest(run.cfg.ProjectID, run.cfg.Repo, it); ok {
			return p.Sink.Handle(ctx, trig)
		}
		return nil
	}, "items")
	st.ETag = etag
	return err
}

// updatedPullRequests lists recently updated PRs once per poll (newest first),
// stopping as soon as entries fall behind the shared cursor.
func (p *Poller) updatedPullRequests(ctx context.Context, run *pollRun) ([]pullRequest, error) {
	if run.prsLoaded {
		return run.prs, nil
	}
	run.prsLoaded = true
	if run.prSince.IsZero() {
		return nil, nil
	}
	target := fmt.Sprintf("%s/repos/%s/pulls?state=all&sort=updated&direction=desc&per_page=100",
		p.baseURL(), repoPath(run.cfg.Repo))
	next := target
	for page := 0; page < p.maxPages() && next != ""; page++ {
		var batch []pullRequest
		_, link, _, err := p.getJSON(ctx, run.cfg, next, "", &batch)
		if err != nil {
			return nil, err
		}
		oldest := time.Time{}
		for _, pr := range batch {
			run.prs = append(run.prs, pr)
			if oldest.IsZero() || pr.UpdatedAt.Before(oldest) {
				oldest = pr.UpdatedAt
			}
		}
		// Newest-first: once a whole page is older than the cursor, done.
		if len(batch) == 0 || (!oldest.IsZero() && oldest.Before(run.prSince)) {
			break
		}
		next = link
	}
	return run.prs, nil
}

// openPullRequests lists the repository's open PRs so the check-run stream can
// look at their head commits even when the PR itself has not been updated.
func (p *Poller) openPullRequests(ctx context.Context, run *pollRun) ([]pullRequest, error) {
	target := fmt.Sprintf("%s/repos/%s/pulls?state=open&sort=updated&direction=desc&per_page=100",
		p.baseURL(), repoPath(run.cfg.Repo))
	var out []pullRequest
	next := target
	for page := 0; page < p.maxPages() && next != ""; page++ {
		var batch []pullRequest
		_, link, _, err := p.getJSON(ctx, run.cfg, next, "", &batch)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) == 0 {
			break
		}
		next = link
	}
	return out, nil
}

// paginate walks a list endpoint page by page, decoding each item through fn.
// arrayField names the wrapper key when the response is an object ({items:[...]}
// for search, {workflow_runs:[...]} for actions); empty means a bare array.
// It returns the first page's ETag so the caller can persist it.
func (p *Poller) paginate(ctx context.Context, cfg RepoConfig, target, etag string, fn func(json.RawMessage) error, arrayField ...string) (string, error) {
	field := ""
	if len(arrayField) > 0 {
		field = arrayField[0]
	}
	next := target
	newETag := ""
	for page := 0; page < p.maxPages() && next != ""; page++ {
		var body json.RawMessage
		status, link, respETag, err := p.getJSON(ctx, cfg, next, etag, &body)
		if err != nil {
			return newETag, err
		}
		if page == 0 {
			newETag = respETag
		}
		etag = "" // ETag applies to the first page only
		if status == http.StatusNotModified {
			return newETag, nil
		}
		items, err := listItems(body, field)
		if err != nil {
			return newETag, err
		}
		for _, item := range items {
			if err := fn(item); err != nil {
				return newETag, err
			}
		}
		if len(items) == 0 {
			return newETag, nil
		}
		next = link
	}
	return newETag, nil
}

// listItems extracts the item array from either a bare array or a wrapper key.
func listItems(body json.RawMessage, field string) ([]json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	if field == "" {
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("githubsource: decode list: %w", err)
		}
		return items, nil
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, fmt.Errorf("githubsource: decode list wrapper: %w", err)
	}
	raw, ok := wrapper[field]
	if !ok {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("githubsource: decode %s: %w", field, err)
	}
	return items, nil
}

// getJSON performs one request. It returns the HTTP status (200 or 304), the
// next-page link, the response ETag, and any error. A 304 is not an error.
func (p *Poller) getJSON(ctx context.Context, cfg RepoConfig, target, etag string, out any) (int, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "niuniu-github-source")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", "", err
	}

	switch {
	case resp.StatusCode == http.StatusNotModified:
		return http.StatusNotModified, "", etag, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out != nil {
			if raw, ok := out.(*json.RawMessage); ok {
				*raw = json.RawMessage(append([]byte(nil), body...))
			} else if err := json.Unmarshal(body, out); err != nil {
				return resp.StatusCode, "", "", fmt.Errorf("githubsource: decode response: %w", err)
			}
		}
		return resp.StatusCode, nextPageLink(resp.Header.Get("Link")), resp.Header.Get("ETag"), nil
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		var apiErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &apiErr)
		if rl := rateLimitError(resp, apiErr.Message, p.now()); rl != nil {
			return resp.StatusCode, "", "", rl
		}
		return resp.StatusCode, "", "", &APIError{Status: resp.StatusCode, Message: apiErr.Message}
	default:
		var apiErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &apiErr)
		return resp.StatusCode, "", "", &APIError{Status: resp.StatusCode, Message: apiErr.Message}
	}
}

// rateLimitError recognizes GitHub's primary/secondary rate limit responses.
func rateLimitError(resp *http.Response, message string, now time.Time) *RateLimitError {
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	reset := resp.Header.Get("X-RateLimit-Reset")
	retryAfter := resp.Header.Get("Retry-After")
	secondary := strings.Contains(strings.ToLower(message), "rate limit")
	if resp.StatusCode != http.StatusTooManyRequests && remaining != "0" && !secondary {
		// A plain 403 is a permission problem, not a limit.
		return nil
	}
	rl := &RateLimitError{Status: resp.StatusCode, Message: message}
	if retryAfter != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
			rl.ResetAt = now.Add(time.Duration(secs) * time.Second)
		}
	}
	if rl.ResetAt.IsZero() && reset != "" {
		if epoch, err := strconv.ParseInt(strings.TrimSpace(reset), 10, 64); err == nil && epoch > 0 {
			rl.ResetAt = time.Unix(epoch, 0)
		}
	}
	return rl
}

// nextPageLink extracts rel="next" from a Link header.
func nextPageLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		if !strings.Contains(segments[1], `rel="next"`) {
			continue
		}
		link := strings.TrimSpace(segments[0])
		link = strings.TrimPrefix(link, "<")
		link = strings.TrimSuffix(link, ">")
		return link
	}
	return ""
}

func splitRepo(repo string) (owner, name string, ok bool) {
	repo = strings.TrimSpace(repo)
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func repoPath(repo string) string {
	owner, name, _ := splitRepo(repo)
	return url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func checkRunTime(c checkRun) time.Time {
	if c.CompletedAt != nil {
		return *c.CompletedAt
	}
	if c.StartedAt != nil {
		return *c.StartedAt
	}
	return time.Time{}
}
