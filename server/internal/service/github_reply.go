package service

// Outbound result delivery for the GitHub Source (Epic #755 / design §6.10).
//
// When a workspace's turn finishes and that workspace's task is bound to a
// GitHub thread, the agent's final reply is posted back into the PR/issue as a
// comment. Everything here is a guard around that single write:
//
//	thread alive   a github_thread_bindings row in state=open for THIS workspace
//	               and THIS issue — a stale/reassigned workspace sends nothing
//	owner grant    the project owner's github write_enabled pref (existing
//	               egress rule §6.10) and an enabled provider
//	credential     the repo's project_external_sources binding has a token
//	idempotency    one github_reply_deliveries row per (workspace, turn)
//
// A guard rejection is recorded as a blocked delivery rather than an error: it
// is a permission outcome, not a failure, and must never page an operator.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/githubsource"
	"github.com/niuniu-dev/niuniu/internal/store"
)

const (
	githubReplySent    = "sent"
	githubReplyFailed  = "failed"
	githubReplyBlocked = "blocked"
)

// GithubReplyDispatcher subscribes to the in-process event bus and posts each
// finished turn's reply back to the GitHub thread its workspace is bound to.
type GithubReplyDispatcher struct {
	bus *event.Bus
	svc *GithubSourceService

	mu   sync.Mutex
	ch   chan event.OutputEvent
	stop chan struct{}
	done chan struct{}
}

// NewGithubReplyDispatcher builds the dispatcher; Start enables it.
func NewGithubReplyDispatcher(bus *event.Bus, svc *GithubSourceService) *GithubReplyDispatcher {
	return &GithubReplyDispatcher{bus: bus, svc: svc}
}

// Start subscribes to the bus and runs the reply loop until Stop.
func (d *GithubReplyDispatcher) Start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ch != nil || d.bus == nil {
		return
	}
	d.ch = d.bus.Subscribe()
	d.stop = make(chan struct{})
	d.done = make(chan struct{})
	go d.loop(d.ch, d.stop, d.done)
	slog.Info("github source: reply dispatcher started")
}

// Stop unsubscribes and waits for the loop to exit.
func (d *GithubReplyDispatcher) Stop() {
	d.mu.Lock()
	ch, stop, done := d.ch, d.stop, d.done
	d.ch, d.stop, d.done = nil, nil, nil
	d.mu.Unlock()
	if ch == nil {
		return
	}
	close(stop)
	d.bus.Unsubscribe(ch)
	<-done
}

func (d *GithubReplyDispatcher) loop(ch chan event.OutputEvent, stop, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-stop:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			d.handle(ev)
		}
	}
}

func (d *GithubReplyDispatcher) handle(ev event.OutputEvent) {
	if ev.Type != event.EventAgentDone || ev.WorkspaceId == 0 {
		return
	}
	text := replyText(ev.Content)
	// "completed" is the proxy's sentinel for a turn with no visible answer;
	// there is nothing to put in the thread.
	if text == "" || text == "completed" {
		return
	}
	key := fmt.Sprintf("%s:%d", ev.Type, ev.Ts)
	if err := d.svc.DeliverReply(context.Background(), ev.WorkspaceId, key, text); err != nil {
		slog.Warn("github source: reply delivery failed",
			"workspace", ev.WorkspaceId, "error", err)
	}
}

// replyText prepares a turn's answer for a public thread. The autohost
// completion sentinel is an internal control token — posting it to a PR would
// leak it to everyone watching the repository.
func replyText(content string) string {
	text := strings.ReplaceAll(content, "[AUTOHOST_DONE]", "")
	return strings.TrimSpace(text)
}

// githubReplyTarget is the resolved thread a workspace's result goes back to.
type githubReplyTarget struct {
	ProjectID   int64
	Repo        string
	ThreadExtID string
	IssueID     int64
}

// DeliverReply posts one finished turn back to the workspace's GitHub thread.
// A non-nil error means the delivery was attempted and failed; a skipped or
// blocked delivery returns nil (it is recorded for audit).
func (s *GithubSourceService) DeliverReply(ctx context.Context, workspaceID int64, eventKey, text string) error {
	if s == nil || s.db == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	target, ok, err := s.replyTarget(ctx, workspaceID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if allowed, reason := s.replyAllowed(ctx, target.ProjectID); !allowed {
		s.recordReply(ctx, target, workspaceID, eventKey, githubReplyBlocked, 0, "", reason)
		slog.Debug("github source: result not sent", "reason", reason,
			"repo", target.Repo, "thread", target.ThreadExtID)
		return nil
	}
	token, err := s.repoToken(ctx, target.ProjectID, target.Repo)
	if err != nil {
		return err
	}
	if token == "" {
		s.recordReply(ctx, target, workspaceID, eventKey, githubReplyBlocked, 0, "", "credential_missing")
		return nil
	}

	claimed, err := s.claimReply(ctx, target, workspaceID, eventKey)
	if err != nil || !claimed {
		return err
	}
	comment, err := githubsource.PostComment(ctx, s.poller.Client, s.poller.BaseURL, githubsource.ReplyConfig{
		Repo:   target.Repo,
		Number: target.ThreadExtID,
		Token:  token,
		Body:   text,
	})
	if err != nil {
		s.finishReply(ctx, target, workspaceID, eventKey, githubReplyFailed, 0, "", err.Error())
		return err
	}
	s.finishReply(ctx, target, workspaceID, eventKey, githubReplySent, comment.ID, comment.URL, "")
	slog.Info("github source: result delivered",
		"repo", target.Repo, "thread", target.ThreadExtID, "comment", comment.ID)
	return nil
}

// replyTarget resolves the open GitHub thread bound to this workspace. The
// binding must belong to the workspace's own issue: a task whose workspace was
// replaced or archived must not answer on behalf of the old one.
func (s *GithubSourceService) replyTarget(ctx context.Context, workspaceID int64) (githubReplyTarget, bool, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return githubReplyTarget{}, false, nil
		}
		return githubReplyTarget{}, false, err
	}
	if !ws.IssueID.Valid || ws.IssueID.Int64 == 0 {
		return githubReplyTarget{}, false, nil
	}

	var t githubReplyTarget
	err = s.db.QueryRowContext(ctx, `
		SELECT project_id, repo, thread_ext_id, issue_id
		FROM github_thread_bindings
		WHERE workspace_id = ? AND state = 'open'
		ORDER BY updated_at DESC, id DESC
		LIMIT 1`, workspaceID,
	).Scan(&t.ProjectID, &t.Repo, &t.ThreadExtID, &t.IssueID)
	if errors.Is(err, sql.ErrNoRows) {
		return githubReplyTarget{}, false, nil
	}
	if err != nil {
		return githubReplyTarget{}, false, err
	}
	if t.IssueID != ws.IssueID.Int64 || t.ThreadExtID == "" || t.Repo == "" {
		return githubReplyTarget{}, false, nil
	}
	return t, true, nil
}

// replyAllowed applies the existing egress rule: the project owner must have
// turned on writes for the github provider. Reads are unaffected by design.
func (s *GithubSourceService) replyAllowed(ctx context.Context, projectID int64) (bool, string) {
	p, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return false, "project_unresolved"
	}
	prov, err := s.q.GetProviderByName(ctx, githubProviderName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, "provider_missing"
		}
		return false, "provider_unavailable"
	}
	if prov.Enabled == 0 {
		return false, "provider_disabled"
	}
	prefs, err := s.q.GetAPIWritePrefs(ctx, store.GetAPIWritePrefsParams{
		OwnerType:  p.OwnerType,
		OwnerID:    p.OwnerID,
		ProviderID: prov.ID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, "write_disabled"
		}
		slog.Warn("github source: write prefs lookup failed; fail-closed",
			"project", projectID, "error", err)
		return false, "write_prefs_unavailable"
	}
	if prefs.Enabled != 1 {
		return false, "write_disabled"
	}
	return true, ""
}

// repoToken loads the credential bound to (project, github, repo). An unbound
// repo has no token, so nothing is ever posted to a repository the project did
// not explicitly attach.
func (s *GithubSourceService) repoToken(ctx context.Context, projectID int64, repo string) (string, error) {
	var credID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT credential_id FROM project_external_sources
		WHERE project_id = ? AND LOWER(provider) = ? AND source_key = ?`,
		projectID, githubProviderName, repo,
	).Scan(&credID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !credID.Valid || credID.Int64 <= 0 || s.creds == nil {
		return "", nil
	}
	return s.tokenFor(ctx, credID.Int64)
}

// claimReply reserves the (workspace, turn) delivery slot. A false return means
// this turn was already handled (a duplicated bus event or a retry).
func (s *GithubSourceService) claimReply(ctx context.Context, t githubReplyTarget, workspaceID int64, eventKey string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO github_reply_deliveries
			(project_id, repo, thread_ext_id, issue_id, workspace_id, event_key, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		t.ProjectID, t.Repo, t.ThreadExtID, t.IssueID, workspaceID, eventKey)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// recordReply writes a delivery outcome that never reached the network.
func (s *GithubSourceService) recordReply(ctx context.Context, t githubReplyTarget, workspaceID int64, eventKey, status string, commentID int64, commentURL, reason string) {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO github_reply_deliveries
			(project_id, repo, thread_ext_id, issue_id, workspace_id, event_key, status, comment_id, comment_url, error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		t.ProjectID, t.Repo, t.ThreadExtID, t.IssueID, workspaceID, eventKey, status, commentID, commentURL, reason)
	if err != nil {
		slog.Warn("github source: record reply outcome failed", "error", err)
	}
}

// finishReply updates the claimed row with the network outcome.
func (s *GithubSourceService) finishReply(ctx context.Context, t githubReplyTarget, workspaceID int64, eventKey, status string, commentID int64, commentURL, reason string) {
	if reason != "" && len(reason) > 500 {
		reason = reason[:500]
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE github_reply_deliveries
		SET status = ?, comment_id = ?, comment_url = ?, error = ?, updated_at = ?
		WHERE workspace_id = ? AND event_key = ?`,
		status, commentID, commentURL, reason, time.Now().UTC().Format(time.RFC3339Nano),
		workspaceID, eventKey)
	if err != nil {
		slog.Warn("github source: update reply outcome failed", "error", err)
	}
}
