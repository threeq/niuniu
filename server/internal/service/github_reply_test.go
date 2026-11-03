package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/githubsource"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
)

// githubReplyFixture is the shared IM fixture plus the GitHub bits a reply
// needs: an enabled github provider, a repo binding with a credential, an open
// thread binding, and a fake GitHub API that records posted comments.
type githubReplyFixture struct {
	*imbotFixture
	svc      *GithubSourceService
	server   *httptest.Server
	mu       sync.Mutex
	requests []postedComment
	provider int64
}

type postedComment struct {
	Path string
	Body string
	Auth string
}

func newGithubReplyFixture(t *testing.T, writeEnabled bool) *githubReplyFixture {
	t.Helper()
	f := newIMBotFixture(t)
	ctx := context.Background()

	kr, err := crypto.LoadOrCreate(t.TempDir() + "/integration_secret")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	creds := NewExternalCredentialService(f.q, f.db, kr, integration.NewRegistry())
	cred, err := creds.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider: integration.ProviderGitHub, Alias: "gh",
		RawConfig: map[string]any{"token": "ghp_test"},
	})
	if err != nil {
		t.Fatalf("credential: %v", err)
	}

	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO external_providers (name, api_base_url, enabled)
		VALUES ('github', 'https://api.github.com', 1)`); err != nil {
		t.Fatalf("provider: %v", err)
	}
	var providerID int64
	if err := f.db.QueryRowContext(ctx,
		`SELECT id FROM external_providers WHERE name = 'github'`).Scan(&providerID); err != nil {
		t.Fatalf("provider id: %v", err)
	}
	if writeEnabled {
		if _, err := f.db.ExecContext(ctx, `
			INSERT INTO external_api_write_prefs (owner_type, owner_id, provider_id, enabled)
			VALUES ('user', 1, ?, 1)`, providerID); err != nil {
			t.Fatalf("write prefs: %v", err)
		}
	}
	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO project_external_sources (project_id, provider, source_key, credential_id)
		VALUES (?, 'github', 'acme/foo', ?)`, f.projectID, cred.ID); err != nil {
		t.Fatalf("repo binding: %v", err)
	}
	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO github_thread_bindings
			(project_id, repo, thread_ext_id, issue_id, workspace_id, state)
		VALUES (?, 'acme/foo', '42', ?, ?, 'open')`, f.projectID, f.issueID, f.wsID); err != nil {
		t.Fatalf("thread binding: %v", err)
	}

	fx := &githubReplyFixture{imbotFixture: f, provider: providerID}
	fx.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(raw, &body)
		fx.mu.Lock()
		fx.requests = append(fx.requests, postedComment{
			Path: r.URL.Path, Body: body.Body, Auth: r.Header.Get("Authorization"),
		})
		fx.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":9001,"html_url":"https://github.com/acme/foo/pull/42#issuecomment-9001"}`))
	}))
	t.Cleanup(fx.server.Close)

	svc := NewGithubSourceService(f.db, f.q, creds)
	svc.SetPollerBaseURL(fx.server.URL)
	fx.svc = svc
	return fx
}

func (fx *githubReplyFixture) posted() []postedComment {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]postedComment(nil), fx.requests...)
}

func (fx *githubReplyFixture) deliveryStatus(t *testing.T, eventKey string) (status, errText, url string, commentID int64) {
	t.Helper()
	err := fx.db.QueryRow(`
		SELECT status, error, comment_url, comment_id FROM github_reply_deliveries
		WHERE workspace_id = ? AND event_key = ?`, fx.wsID, eventKey,
	).Scan(&status, &errText, &url, &commentID)
	if err != nil {
		t.Fatalf("delivery row: %v", err)
	}
	return status, errText, url, commentID
}

// A finished turn in a workspace bound to an open PR thread is posted back once,
// with the repo credential; a duplicate bus event is a no-op.
func TestGithubReply_DeliversToBoundThreadOnce(t *testing.T) {
	fx := newGithubReplyFixture(t, true)
	ctx := context.Background()

	if err := fx.svc.DeliverReply(ctx, fx.wsID, "agent_done:1700000000000", "  fixed the bug\n"); err != nil {
		t.Fatalf("DeliverReply: %v", err)
	}
	got := fx.posted()
	if len(got) != 1 {
		t.Fatalf("requests = %d, want 1", len(got))
	}
	if got[0].Path != "/repos/acme/foo/issues/42/comments" {
		t.Fatalf("path = %q", got[0].Path)
	}
	if got[0].Body != "fixed the bug" {
		t.Fatalf("body = %q", got[0].Body)
	}
	if got[0].Auth != "Bearer ghp_test" {
		t.Fatalf("auth = %q", got[0].Auth)
	}
	status, _, url, commentID := fx.deliveryStatus(t, "agent_done:1700000000000")
	if status != githubReplySent || commentID != 9001 || url == "" {
		t.Fatalf("delivery = %q %d %q", status, commentID, url)
	}

	// Same turn delivered again (bus replay / restart) -> no second comment.
	if err := fx.svc.DeliverReply(ctx, fx.wsID, "agent_done:1700000000000", "fixed the bug"); err != nil {
		t.Fatalf("second DeliverReply: %v", err)
	}
	if n := len(fx.posted()); n != 1 {
		t.Fatalf("duplicate delivery posted %d comments", n)
	}
}

// Without the owner's write grant nothing leaves the machine; the refusal is
// recorded so the UI/audit can explain the silence.
func TestGithubReply_NoWriteGrantSendsNothing(t *testing.T) {
	fx := newGithubReplyFixture(t, false)
	ctx := context.Background()

	if err := fx.svc.DeliverReply(ctx, fx.wsID, "agent_done:1", "answer"); err != nil {
		t.Fatalf("DeliverReply: %v", err)
	}
	if n := len(fx.posted()); n != 0 {
		t.Fatalf("posted %d comments without a write grant", n)
	}
	status, reason, _, _ := fx.deliveryStatus(t, "agent_done:1")
	if status != githubReplyBlocked || reason != "write_disabled" {
		t.Fatalf("delivery = %q %q", status, reason)
	}
}

// A stale binding (the workspace's issue changed) and an ended thread both send
// nothing.
func TestGithubReply_StaleOrEndedThreadSendsNothing(t *testing.T) {
	ctx := context.Background()

	t.Run("ended", func(t *testing.T) {
		fx := newGithubReplyFixture(t, true)
		if _, err := fx.db.Exec(`
			UPDATE github_thread_bindings SET state = 'ended' WHERE workspace_id = ?`, fx.wsID); err != nil {
			t.Fatal(err)
		}
		if err := fx.svc.DeliverReply(ctx, fx.wsID, "agent_done:2", "answer"); err != nil {
			t.Fatalf("DeliverReply: %v", err)
		}
		if n := len(fx.posted()); n != 0 {
			t.Fatalf("ended thread got %d comments", n)
		}
	})

	t.Run("other issue", func(t *testing.T) {
		fx := newGithubReplyFixture(t, true)
		issueID, _ := fx.newWorkspace(t, "另一个任务", "/tmp/ws-other")
		if _, err := fx.db.Exec(`
			UPDATE github_thread_bindings SET issue_id = ? WHERE workspace_id = ?`,
			issueID, fx.wsID); err != nil {
			t.Fatal(err)
		}
		if err := fx.svc.DeliverReply(ctx, fx.wsID, "agent_done:3", "answer"); err != nil {
			t.Fatalf("DeliverReply: %v", err)
		}
		if n := len(fx.posted()); n != 0 {
			t.Fatalf("stale binding got %d comments", n)
		}
	})

	t.Run("unbound workspace", func(t *testing.T) {
		fx := newGithubReplyFixture(t, true)
		_, otherWS := fx.newWorkspace(t, "无绑定任务", "/tmp/ws-unbound")
		if err := fx.svc.DeliverReply(ctx, otherWS, "agent_done:4", "answer"); err != nil {
			t.Fatalf("DeliverReply: %v", err)
		}
		if n := len(fx.posted()); n != 0 {
			t.Fatalf("unbound workspace got %d comments", n)
		}
	})
}

// The dispatcher only reacts to a successful turn with real text.
func TestGithubReplyDispatcher_OnlyAgentDoneWithText(t *testing.T) {
	fx := newGithubReplyFixture(t, true)
	d := NewGithubReplyDispatcher(nil, fx.svc)
	d.handle(event.OutputEvent{Type: event.EventAgentDone, WorkspaceId: fx.wsID, Content: "completed"})
	d.handle(event.OutputEvent{Type: event.EventAgentFailed, WorkspaceId: fx.wsID, Content: "boom"})
	if n := len(fx.posted()); n != 0 {
		t.Fatalf("sentinel/failed turn posted %d comments", n)
	}
	d.handle(event.OutputEvent{
		Type: event.EventAgentDone, WorkspaceId: fx.wsID, Ts: 7,
		Content: "real answer\n[AUTOHOST_DONE]",
	})
	got := fx.posted()
	if len(got) != 1 || got[0].Body != "real answer" {
		t.Fatalf("posted = %+v", got)
	}
}

// githubReplyConfig_RequiresEverything pins the API-level preconditions.
func TestGithubReply_PostCommentGuards(t *testing.T) {
	client := &http.Client{}
	if _, err := githubsource.PostComment(context.Background(), client, "", githubsource.ReplyConfig{
		Repo: "bad", Number: "1", Token: "x", Body: "hi",
	}); err == nil {
		t.Fatal("invalid repo must fail")
	}
	if _, err := githubsource.PostComment(context.Background(), client, "", githubsource.ReplyConfig{
		Repo: "acme/foo", Number: "0", Token: "x", Body: "hi",
	}); err == nil {
		t.Fatal("invalid number must fail")
	}
	if _, err := githubsource.PostComment(context.Background(), client, "", githubsource.ReplyConfig{
		Repo: "acme/foo", Number: "1", Token: "", Body: "hi",
	}); err == nil {
		t.Fatal("missing token must fail")
	}
	if _, err := githubsource.PostComment(context.Background(), client, "", githubsource.ReplyConfig{
		Repo: "acme/foo", Number: "1", Token: "x", Body: "  ",
	}); err == nil {
		t.Fatal("empty body must fail")
	}
}
