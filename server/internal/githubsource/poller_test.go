package githubsource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

type memStore struct {
	mu     sync.Mutex
	states map[string]State
}

func newMemStore() *memStore { return &memStore{states: map[string]State{}} }

func stateKey(projectID int64, repo, stream string) string {
	return fmt.Sprintf("%d|%s|%s", projectID, repo, stream)
}

func (m *memStore) Load(_ context.Context, projectID int64, repo, stream string) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[stateKey(projectID, repo, stream)], nil
}

func (m *memStore) Save(_ context.Context, st State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[stateKey(st.ProjectID, st.Repo, st.Stream)] = st
	return nil
}

func (m *memStore) get(projectID int64, repo, stream string) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[stateKey(projectID, repo, stream)]
}

type recordingSink struct {
	mu       sync.Mutex
	triggers []intake.Trigger
}

func (s *recordingSink) Handle(_ context.Context, t intake.Trigger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.triggers = append(s.triggers, t)
	return nil
}

func (s *recordingSink) snapshot() []intake.Trigger {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]intake.Trigger(nil), s.triggers...)
}

func issueCommentJSON(id int64, number int, body string, updated time.Time) string {
	return fmt.Sprintf(`{"id":%d,"body":%q,"issue_url":"https://api.github.com/repos/acme/foo/issues/%d","html_url":"https://github.com/acme/foo/issues/%d#issuecomment-%d","updated_at":%q,"created_at":%q,"user":{"login":"alice","type":"User"}}`,
		id, body, number, number, id, updated.Format(time.RFC3339), updated.Format(time.RFC3339))
}

func TestPollRepo_SinceCursorAndNoReconsume(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	commentTime := now.Add(-5 * time.Minute)

	var mu sync.Mutex
	var sinces []string
	var auth []string
	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/foo/issues/comments" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		requests++
		n := requests
		sinces = append(sinces, r.URL.Query().Get("since"))
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		if n == 1 {
			w.Header().Set("ETag", `"e1"`)
			fmt.Fprint(w, "["+issueCommentJSON(1, 42, "hello", commentTime)+"]")
			return
		}
		fmt.Fprint(w, "[]")
	}))
	defer srv.Close()

	store := newMemStore()
	sink := &recordingSink{}
	poller := &Poller{
		BaseURL:         srv.URL,
		Store:           store,
		Sink:            sink,
		Clock:           func() time.Time { return now },
		InitialLookback: time.Hour,
		Overlap:         2 * time.Minute,
	}
	cfg := RepoConfig{ProjectID: 1, Repo: "acme/foo", Token: "tok", Events: []string{EventIssueComment}}

	if err := poller.PollRepo(context.Background(), cfg); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	got := sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("first poll delivered %d triggers, want 1", len(got))
	}
	if got[0].Thread.ThreadExtID != "42" || got[0].Text != "hello" {
		t.Fatalf("unexpected trigger: %+v", got[0])
	}

	st := store.get(1, "acme/foo", EventIssueComment)
	if want := now.Add(-2 * time.Minute); !st.Since.Equal(want) {
		t.Fatalf("cursor = %s, want %s", st.Since, want)
	}
	if st.ETag != `"e1"` {
		t.Fatalf("etag = %q", st.ETag)
	}

	if err := poller.PollRepo(context.Background(), cfg); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if n := len(sink.snapshot()); n != 1 {
		t.Fatalf("second poll re-delivered events (%d total)", n)
	}
	if len(sinces) != 2 {
		t.Fatalf("requests = %d", len(sinces))
	}
	if want := now.Add(-time.Hour).Format(time.RFC3339); sinces[0] != want {
		t.Fatalf("first since = %q, want %q", sinces[0], want)
	}
	if want := now.Add(-2 * time.Minute).Format(time.RFC3339); sinces[1] != want {
		t.Fatalf("second since = %q, want %q", sinces[1], want)
	}
	if auth[0] != "Bearer tok" {
		t.Fatalf("authorization header = %q", auth[0])
	}
}

func TestPollRepo_ETagNotModified(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	var (
		mu          sync.Mutex
		ifNoneMatch []string
		requests    int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		ifNoneMatch = append(ifNoneMatch, r.Header.Get("If-None-Match"))
		n := requests
		mu.Unlock()
		if n == 1 {
			w.Header().Set("ETag", `"abc"`)
			fmt.Fprint(w, "["+issueCommentJSON(1, 3, "x", now.Add(-time.Minute))+"]")
			return
		}
		if got := r.Header.Get("If-None-Match"); got != `"abc"` {
			t.Errorf("If-None-Match = %q", got)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	store := newMemStore()
	sink := &recordingSink{}
	poller := &Poller{
		BaseURL: srv.URL, Store: store, Sink: sink,
		Clock: func() time.Time { return now }, InitialLookback: time.Hour,
	}
	cfg := RepoConfig{ProjectID: 1, Repo: "acme/foo", Events: []string{EventIssueComment}}
	if err := poller.PollRepo(context.Background(), cfg); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	if err := poller.PollRepo(context.Background(), cfg); err != nil {
		t.Fatalf("poll 2 (304): %v", err)
	}
	if n := len(sink.snapshot()); n != 1 {
		t.Fatalf("triggers = %d, want 1", n)
	}
	if st := store.get(1, "acme/foo", EventIssueComment); st.ETag != `"abc"` {
		t.Fatalf("etag after 304 = %q", st.ETag)
	}
}

func TestPollRepo_RateLimitBackoffRecovers(t *testing.T) {
	base := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	now := base
	reset := base.Add(2 * time.Minute)

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", reset.Unix()))
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"API rate limit exceeded for 1.2.3.4"}`)
			return
		}
		fmt.Fprint(w, "[]")
	}))
	defer srv.Close()

	store := newMemStore()
	poller := &Poller{
		BaseURL: srv.URL, Store: store, Sink: &recordingSink{},
		Clock: func() time.Time { return now },
	}
	cfg := RepoConfig{ProjectID: 1, Repo: "acme/foo", Events: []string{EventIssueComment}}

	err := poller.PollRepo(context.Background(), cfg)
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("want RateLimitError, got %v", err)
	}
	st := store.get(1, "acme/foo", EventIssueComment)
	if !st.BackoffUntil.Equal(reset) || st.FailureCount != 1 {
		t.Fatalf("state after limit = %+v", st)
	}

	// Still inside the (short) initial cursor backoff: no request at all.
	now = base.Add(30 * time.Second)
	if err := poller.PollRepo(context.Background(), cfg); err != nil {
		t.Fatalf("poll during backoff: %v", err)
	}
	mu.Lock()
	n := requests
	mu.Unlock()
	if n != 1 {
		t.Fatalf("requested %d times while backed off, want 1", n)
	}

	// After the reset the stream polls again and recovers.
	now = reset.Add(time.Second)
	if err := poller.PollRepo(context.Background(), cfg); err != nil {
		t.Fatalf("poll after reset: %v", err)
	}
	st = store.get(1, "acme/foo", EventIssueComment)
	if st.FailureCount != 0 || !st.BackoffUntil.IsZero() {
		t.Fatalf("backoff not cleared: %+v", st)
	}
	mu.Lock()
	n = requests
	mu.Unlock()
	if n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

func TestPollRepo_ServerErrorExponentialBackoff(t *testing.T) {
	base := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	now := base

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"message":"boom"}`)
			return
		}
		fmt.Fprint(w, "[]")
	}))
	defer srv.Close()

	store := newMemStore()
	poller := &Poller{
		BaseURL: srv.URL, Store: store, Sink: &recordingSink{},
		Clock:       func() time.Time { return now },
		BackoffBase: time.Second,
		BackoffMax:  8 * time.Second,
	}
	cfg := RepoConfig{ProjectID: 1, Repo: "acme/foo", Events: []string{EventIssueComment}}

	if err := poller.PollRepo(context.Background(), cfg); err == nil {
		t.Fatal("expected first failure")
	}
	if st := store.get(1, "acme/foo", EventIssueComment); st.FailureCount != 1 || !st.BackoffUntil.Equal(base.Add(time.Second)) {
		t.Fatalf("state after failure 1 = %+v", st)
	}

	// Before the backoff expires: skipped.
	now = base.Add(500 * time.Millisecond)
	_ = poller.PollRepo(context.Background(), cfg)
	mu.Lock()
	n := requests
	mu.Unlock()
	if n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}

	// After it expires: second failure doubles the backoff.
	now = base.Add(2 * time.Second)
	if err := poller.PollRepo(context.Background(), cfg); err == nil {
		t.Fatal("expected second failure")
	}
	if st := store.get(1, "acme/foo", EventIssueComment); st.FailureCount != 2 || !st.BackoffUntil.Equal(base.Add(4*time.Second)) {
		t.Fatalf("state after failure 2 = %+v", st)
	}

	now = base.Add(5 * time.Second)
	if err := poller.PollRepo(context.Background(), cfg); err != nil {
		t.Fatalf("recovery poll: %v", err)
	}
	if st := store.get(1, "acme/foo", EventIssueComment); st.FailureCount != 0 || !st.BackoffUntil.IsZero() {
		t.Fatalf("state after recovery = %+v", st)
	}
}

func TestPollRepo_AllEventKinds(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-5 * time.Minute).Format(time.RFC3339)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/foo/issues/comments":
			fmt.Fprint(w, "["+issueCommentJSON(1, 42, "issue comment", now.Add(-5*time.Minute))+"]")
		case r.URL.Path == "/repos/acme/foo/pulls/comments":
			fmt.Fprintf(w, `[{"id":2,"body":"inline","pull_request_url":"https://api.github.com/repos/acme/foo/pulls/42","html_url":"https://github.com/acme/foo/pull/42#discussion_r2","updated_at":%q,"created_at":%q,"user":{"login":"alice","type":"User"}}]`, ts, ts)
		case r.URL.Path == "/repos/acme/foo/pulls":
			fmt.Fprintf(w, `[{"number":42,"title":"PR","state":"open","updated_at":%q,"html_url":"https://github.com/acme/foo/pull/42","user":{"login":"alice"},"head":{"sha":"sha42"}}]`, ts)
		case r.URL.Path == "/repos/acme/foo/pulls/42/reviews":
			fmt.Fprintf(w, `[{"id":3,"body":"lgtm","state":"APPROVED","submitted_at":%q,"pull_request_url":"https://api.github.com/repos/acme/foo/pulls/42","html_url":"https://github.com/acme/foo/pull/42#pullrequestreview-3","user":{"login":"bob","type":"User"}}]`, ts)
		case r.URL.Path == "/repos/acme/foo/commits/sha42/check-runs":
			fmt.Fprintf(w, `{"check_runs":[{"id":4,"name":"ci","status":"completed","conclusion":"success","completed_at":%q,"head_sha":"sha42","html_url":"https://github.com/acme/foo/runs/4","check_suite":{"pull_requests":[{"number":42}]}},{"id":5,"name":"ci-2","status":"in_progress","conclusion":"","head_sha":"sha42"}]}`, ts)
		case r.URL.Path == "/repos/acme/foo/actions/runs":
			fmt.Fprintf(w, `{"workflow_runs":[{"id":6,"name":"build","status":"completed","conclusion":"failure","head_branch":"main","created_at":%q,"html_url":"https://github.com/acme/foo/actions/runs/6","actor":{"login":"alice","type":"User"}},{"id":7,"name":"build","status":"in_progress","conclusion":"","created_at":%q}]}`, ts, ts)
		case r.URL.Path == "/search/issues":
			fmt.Fprintf(w, `{"total_count":1,"items":[{"number":42,"title":"PR","state":"closed","closed_at":%q,"updated_at":%q,"html_url":"https://github.com/acme/foo/pull/42","user":{"login":"alice"},"pull_request":{"merged_at":%q}}]}`, ts, ts, ts)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	sink := &recordingSink{}
	poller := &Poller{
		BaseURL: srv.URL, Store: newMemStore(), Sink: sink,
		Clock: func() time.Time { return now }, InitialLookback: time.Hour,
	}
	if err := poller.PollRepo(context.Background(), RepoConfig{ProjectID: 1, Repo: "acme/foo"}); err != nil {
		t.Fatalf("poll: %v", err)
	}

	want := map[string]bool{
		EventIssueComment: true, EventReview: true, EventReviewComment: true,
		EventCheckRun: true, EventWorkflowRun: true, EventPullRequestClosed: true,
	}
	got := map[string]bool{}
	for _, trig := range sink.snapshot() {
		if trig.Thread.ThreadExtID == "" && trig.EventKind != EventWorkflowRun {
			t.Fatalf("%s trigger lost its thread: %+v", trig.EventKind, trig)
		}
		got[trig.EventKind] = true
	}
	if len(got) != len(want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	for kind := range want {
		if !got[kind] {
			t.Fatalf("missing event kind %s (got %v)", kind, got)
		}
	}
}

func TestPollRepo_Pagination(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/acme/foo/issues/comments?page=2>; rel="next"`, "http://"+r.Host))
			fmt.Fprint(w, "["+issueCommentJSON(1, 1, "one", now.Add(-4*time.Minute))+"]")
			return
		}
		fmt.Fprint(w, "["+issueCommentJSON(2, 2, "two", now.Add(-3*time.Minute))+"]")
	}))
	defer srv.Close()

	sink := &recordingSink{}
	poller := &Poller{BaseURL: srv.URL, Store: newMemStore(), Sink: sink, Clock: func() time.Time { return now }}
	if err := poller.PollRepo(context.Background(), RepoConfig{ProjectID: 1, Repo: "acme/foo", Events: []string{EventIssueComment}}); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if n := len(sink.snapshot()); n != 2 {
		t.Fatalf("delivered %d triggers, want 2", n)
	}
}
