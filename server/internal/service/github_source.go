package service

// GitHub Source adapter for the personal edition (Epic #755 / design §6.10).
//
// The poller (internal/githubsource) turns repository events into intake
// Triggers. This file supplies the pipeline's GitHub collaborators and the
// polling lifecycle:
//
//	githubRepoConfigs   project_external_sources(provider=github) -> repos + events
//	githubPollStateStore github_source_poll_state cursors/backoff
//	githubResolver      repo#number -> an existing issue/workspace (resume)
//	githubExecutor      route / deliver / provenance / brief
//
// Polling is outbound only, so no public webhook endpoint is required.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/niuniu-dev/niuniu/internal/githubsource"
	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/store"
)

const githubProviderName = "github"

// GithubSourceService owns GitHub polling and the adapter's intake pipeline.
type GithubSourceService struct {
	db     *store.DB
	q      *store.Queries
	creds  *ExternalCredentialService
	poller *githubsource.Poller
	intake *intake.Service

	mu        sync.RWMutex
	dispatch  TaskRouter
	deliverer MessageDeliverer
	running   atomic.Bool
}

// NewGithubSourceService builds the poller and its intake pipeline. It performs
// no network I/O until RunOnce is called.
func NewGithubSourceService(rawDB *sql.DB, q *store.Queries, creds *ExternalCredentialService) *GithubSourceService {
	s := &GithubSourceService{db: store.Wrap(rawDB), q: q, creds: creds}
	s.poller = &githubsource.Poller{
		Client:  &http.Client{Timeout: 30 * time.Second},
		Store:   githubPollStateStore{db: s.db},
		Sink:    githubIntakeSink{svc: s},
		BaseURL: githubsource.DefaultBaseURL,
	}
	s.intake = intake.NewService(intake.Deps{
		Enabled:  true,
		Store:    imbotIntakeStore{q: q},
		Policies: imbotPolicy{q: q},
		Resolver: githubResolver{db: s.db},
		Executor: githubExecutor{svc: s},
	})
	return s
}

// SetPollerBaseURL points polling at another API host (GitHub Enterprise or a
// test server). Empty restores the public API.
func (s *GithubSourceService) SetPollerBaseURL(base string) {
	if strings.TrimSpace(base) == "" {
		base = githubsource.DefaultBaseURL
	}
	s.poller.BaseURL = base
}

// SetInbound wires the task router and agent deliverer. Called from server.go
// after the shared dispatch/proxy services exist.
func (s *GithubSourceService) SetInbound(dispatch TaskRouter, deliverer MessageDeliverer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispatch = dispatch
	s.deliverer = deliverer
}

func (s *GithubSourceService) router() TaskRouter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dispatch
}

func (s *GithubSourceService) messageDeliverer() MessageDeliverer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deliverer
}

// RunOnce polls every bound GitHub repository once. It is safe to call from the
// scheduler: overlapping ticks are skipped, and one repository's failure does
// not stop the others.
func (s *GithubSourceService) RunOnce(ctx context.Context) {
	if s == nil || s.poller == nil {
		return
	}
	if !s.running.CompareAndSwap(false, true) {
		return
	}
	defer s.running.Store(false)

	bindings, err := s.repoBindings(ctx)
	if err != nil {
		slog.Warn("github source: load repo bindings failed", "error", err)
		return
	}
	for _, b := range bindings {
		if ctx.Err() != nil {
			return
		}
		token, err := s.tokenFor(ctx, b.credentialID)
		if err != nil {
			slog.Warn("github source: load credential failed", "repo", b.cfg.Repo, "error", err)
			continue
		}
		if token == "" {
			slog.Warn("github source: repo has no GitHub token", "repo", b.cfg.Repo)
			continue
		}
		b.cfg.Token = token
		if err := s.poller.PollRepo(ctx, b.cfg); err != nil {
			slog.Warn("github source: poll failed", "repo", b.cfg.Repo, "error", err)
		}
	}
}

type githubRepoBinding struct {
	cfg          githubsource.RepoConfig
	credentialID int64
}

func (s *GithubSourceService) repoBindings(ctx context.Context) ([]githubRepoBinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, source_key, credential_id, config
		FROM project_external_sources
		WHERE LOWER(provider) = ?
		ORDER BY project_id, source_key`, githubProviderName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]githubRepoBinding, 0)
	for rows.Next() {
		var (
			projectID int64
			sourceKey string
			credID    sql.NullInt64
			config    string
		)
		if err := rows.Scan(&projectID, &sourceKey, &credID, &config); err != nil {
			return nil, err
		}
		repo := strings.TrimSpace(sourceKey)
		if repo == "" {
			continue
		}
		out = append(out, githubRepoBinding{
			cfg: githubsource.RepoConfig{
				ProjectID: projectID,
				Repo:      repo,
				Events:    githubEventsFromConfig(config),
			},
			credentialID: credID.Int64,
		})
	}
	return out, rows.Err()
}

// githubEventsFromConfig reads the per-repo event selection. An absent/empty
// list means "all known events" (the pre-configuration default).
func githubEventsFromConfig(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var cfg struct {
		Events []string `json:"events"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Events))
	for _, e := range cfg.Events {
		if githubsource.IsKnownEvent(e) {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *GithubSourceService) tokenFor(ctx context.Context, credentialID int64) (string, error) {
	if credentialID <= 0 || s.creds == nil {
		return "", nil
	}
	cred, err := s.creds.GetBoundByID(ctx, credentialID)
	if err != nil {
		return "", err
	}
	return credToken(cred.Config), nil
}

// githubIntakeSink hands triggers to the shared pipeline. Duplicates are the
// pipeline's normal no-op (the event was already recorded), not an error.
type githubIntakeSink struct{ svc *GithubSourceService }

func (k githubIntakeSink) Handle(ctx context.Context, t intake.Trigger) error {
	res, err := k.svc.intake.Handle(ctx, t)
	if err != nil {
		return err
	}
	if !res.Duplicate {
		slog.Debug("github source: event handled",
			"repo", t.SourceRef, "event", t.EventID, "decision", res.Decision, "reason", res.ReasonCode)
	}
	return nil
}

// --- Poll state -------------------------------------------------------------

type githubPollStateStore struct{ db *store.DB }

func (st githubPollStateStore) Load(ctx context.Context, projectID int64, repo, stream string) (githubsource.State, error) {
	var (
		since, backoff, lastPolled sql.NullString
		failures                   int64
		etag, lastErr              string
	)
	err := st.db.QueryRowContext(ctx, `
		SELECT since_ts, etag, failure_count, backoff_until, last_polled_at, last_error
		FROM github_source_poll_state
		WHERE project_id = ? AND repo = ? AND stream = ?`,
		projectID, repo, stream,
	).Scan(&since, &etag, &failures, &backoff, &lastPolled, &lastErr)
	if errors.Is(err, sql.ErrNoRows) {
		return githubsource.State{ProjectID: projectID, Repo: repo, Stream: stream}, nil
	}
	if err != nil {
		return githubsource.State{}, err
	}
	return githubsource.State{
		ProjectID:    projectID,
		Repo:         repo,
		Stream:       stream,
		Since:        parseDBTime(since),
		ETag:         etag,
		FailureCount: int(failures),
		BackoffUntil: parseDBTime(backoff),
		LastPolledAt: parseDBTime(lastPolled),
		LastError:    lastErr,
	}, nil
}

func (st githubPollStateStore) Save(ctx context.Context, s githubsource.State) error {
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO github_source_poll_state
			(project_id, repo, stream, since_ts, etag, failure_count, backoff_until, last_polled_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, repo, stream) DO UPDATE SET
			since_ts = excluded.since_ts,
			etag = excluded.etag,
			failure_count = excluded.failure_count,
			backoff_until = excluded.backoff_until,
			last_polled_at = excluded.last_polled_at,
			last_error = excluded.last_error`,
		s.ProjectID, s.Repo, s.Stream,
		formatDBTime(s.Since), s.ETag, s.FailureCount,
		formatDBTime(s.BackoffUntil), formatDBTime(s.LastPolledAt), s.LastError)
	return err
}

func formatDBTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseDBTime(v sql.NullString) time.Time {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

// --- Resolver ---------------------------------------------------------------

// githubResolver resumes an existing task when the same repo#number already
// produced one. Unbound threads resolve to nothing and the pipeline routes a new
// task.
type githubResolver struct{ db *store.DB }

func (r githubResolver) Resolve(ctx context.Context, t intake.Trigger) (intake.Resolution, error) {
	thread := t.Thread
	extID := githubExternalID(t.SourceRef, thread.ThreadExtID)
	if extID == "" {
		return intake.Resolution{Thread: thread}, nil
	}
	var issueID int64
	err := r.db.QueryRowContext(ctx, `
		SELECT i.id
		FROM issues i
		JOIN columns c ON c.id = i.column_id
		WHERE c.project_id = ? AND i.external_source = ? AND i.external_id = ?
		ORDER BY i.id DESC LIMIT 1`,
		t.ProjectID, githubProviderName, extID,
	).Scan(&issueID)
	if errors.Is(err, sql.ErrNoRows) {
		return intake.Resolution{Thread: thread}, nil
	}
	if err != nil {
		return intake.Resolution{}, err
	}

	var workspaceID int64
	err = r.db.QueryRowContext(ctx, `
		SELECT id FROM workspaces
		WHERE issue_id = ? AND is_archived = 0
		ORDER BY id DESC LIMIT 1`, issueID,
	).Scan(&workspaceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return intake.Resolution{}, err
	}
	thread.IssueID, thread.WorkspaceID = issueID, workspaceID
	return intake.Resolution{
		Thread:      thread,
		IssueID:     issueID,
		WorkspaceID: workspaceID,
		Bound:       workspaceID != 0,
	}, nil
}

func githubExternalID(repo, thread string) string {
	repo, thread = strings.TrimSpace(repo), strings.TrimSpace(thread)
	if repo == "" || thread == "" {
		return ""
	}
	return repo + "#" + thread
}

// --- Executor ---------------------------------------------------------------

type githubExecutor struct{ svc *GithubSourceService }

func (e githubExecutor) Execute(ctx context.Context, plan intake.ExecutionPlan) (intake.ExecutionResult, error) {
	if plan.Decision == intake.DecisionResume {
		if plan.WorkspaceID == 0 {
			return intake.ExecutionResult{}, errors.New("github intake: resume without a workspace")
		}
		e.svc.deliver(ctx, plan.WorkspaceID, plan.Trigger)
		return intake.ExecutionResult{IssueID: plan.IssueID, WorkspaceID: plan.WorkspaceID}, nil
	}

	dispatch := e.svc.router()
	if dispatch == nil {
		return intake.ExecutionResult{}, errors.New("github intake: no task router wired")
	}
	owner, ok := e.svc.projectOwner(ctx, plan.Trigger.ProjectID)
	if !ok {
		return intake.ExecutionResult{}, errors.New("github intake: project owner unresolved")
	}
	hint := RouteHint{
		ForceNew:  plan.Trigger.ForceNew || plan.IssueID == 0,
		TitleHint: strings.TrimSpace(plan.Brief.Goal),
	}
	if plan.IssueID > 0 {
		// The thread already has a task but no workspace (e.g. its workspace was
		// removed); continue that task instead of opening a second one.
		hint.ActiveIssueID = plan.IssueID
	}
	target, err := dispatch.RouteInProject(ctx, owner, plan.Trigger.ProjectID, plan.Trigger.Text, hint)
	if err != nil {
		return intake.ExecutionResult{}, fmt.Errorf("github intake: route: %w", err)
	}
	if target.IssueID == 0 || target.WorkspaceID == 0 {
		return intake.ExecutionResult{}, fmt.Errorf("%w: issue=%d workspace=%d",
			errRouteIncomplete, target.IssueID, target.WorkspaceID)
	}

	e.svc.recordProvenance(ctx, target.IssueID, plan.Trigger)
	e.svc.persistBrief(ctx, target.IssueID, plan)
	e.svc.deliver(ctx, target.WorkspaceID, plan.Trigger)
	return intake.ExecutionResult{IssueID: target.IssueID, WorkspaceID: target.WorkspaceID}, nil
}

func (s *GithubSourceService) projectOwner(ctx context.Context, projectID int64) (OwnerRef, bool) {
	p, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return OwnerRef{}, false
	}
	return OwnerRef{Type: p.OwnerType, ID: p.OwnerID}, true
}

// deliver forwards the event text into the workspace's agent session.
func (s *GithubSourceService) deliver(ctx context.Context, workspaceID int64, t intake.Trigger) {
	d := s.messageDeliverer()
	if d == nil || workspaceID == 0 {
		return
	}
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		slog.Warn("github intake: load workspace failed", "workspace", workspaceID, "error", err)
		return
	}
	if _, _, err := d.Deliver(ctx, workspaceID, ws.Path, githubDeliveryText(t), ""); err != nil {
		slog.Warn("github intake: deliver failed", "workspace", workspaceID, "error", err)
	}
}

func githubDeliveryText(t intake.Trigger) string {
	text := strings.TrimSpace(t.Text)
	if t.SourceURL == "" {
		return text
	}
	if text == "" {
		return "来源: " + t.SourceURL
	}
	return text + "\n\n来源: " + t.SourceURL
}

// recordProvenance writes the upstream origin onto a newly created task
// (§6.2). An issue that already carries a source is left alone.
func (s *GithubSourceService) recordProvenance(ctx context.Context, issueID int64, t intake.Trigger) {
	if issueID == 0 {
		return
	}
	extID := githubExternalID(t.SourceRef, t.Thread.ThreadExtID)
	if extID == "" {
		extID = t.SourceRef + "#" + t.EventID
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE issues SET external_source = ?, external_id = ?, external_url = ?
		 WHERE id = ? AND (external_source IS NULL OR external_source = '')`,
		githubProviderName, extID, t.SourceURL, issueID,
	); err != nil {
		slog.Warn("github intake: write issue provenance failed", "issue", issueID, "error", err)
	}
}

// persistBrief stores the accepted trigger's Brief as version 1 when the task
// has none. A routed existing task keeps its current definition of done.
func (s *GithubSourceService) persistBrief(ctx context.Context, issueID int64, plan intake.ExecutionPlan) {
	if s.db == nil || issueID == 0 {
		return
	}
	if _, err := s.q.GetLatestTaskBrief(ctx, issueID); err == nil {
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("github intake: read current task brief failed", "issue", issueID, "error", err)
		return
	}
	brief := plan.Brief
	if brief.Provenance.DecisionID == 0 {
		brief.Provenance.DecisionID = plan.DecisionID
	}
	if _, err := NewTaskBriefWriter(s.db).Append(ctx, issueID, brief, 0); err != nil {
		slog.Warn("github intake: persist task brief failed", "issue", issueID, "error", err)
	}
}

// --- Configuration surface --------------------------------------------------

// GithubRepoConfigDTO is one bound repository with its event selection and
// latest poll health.
type GithubRepoConfigDTO struct {
	SourceKey    string     `json:"source_key"`
	Events       []string   `json:"events"`
	LastPolledAt *time.Time `json:"last_polled_at,omitempty"`
	BackoffUntil *time.Time `json:"backoff_until,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

// GithubConfigView is the GET response.
type GithubConfigView struct {
	ProjectID   int64                 `json:"project_id"`
	Repos       []GithubRepoConfigDTO `json:"repos"`
	KnownEvents []string              `json:"known_events"`
}

// GithubRepoEventsInput sets one repository's event selection.
type GithubRepoEventsInput struct {
	SourceKey string   `json:"source_key"`
	Events    []string `json:"events"`
}

// UpdateGithubConfigRequest is the PUT body.
type UpdateGithubConfigRequest struct {
	Repos []GithubRepoEventsInput `json:"repos"`
}

// ListGitHubConfig returns every GitHub repo bound to the project, its enabled
// events, and the latest poll state.
func (s *GithubSourceService) ListGitHubConfig(ctx context.Context, projectID int64) (GithubConfigView, error) {
	rows, err := s.q.ListProjectExternalSources(ctx, projectID)
	if err != nil {
		return GithubConfigView{}, err
	}
	stateStore := githubPollStateStore{db: s.db}
	view := GithubConfigView{
		ProjectID:   projectID,
		KnownEvents: append([]string(nil), githubsource.KnownEvents...),
		Repos:       make([]GithubRepoConfigDTO, 0, len(rows)),
	}
	for _, row := range rows {
		if !strings.EqualFold(strings.TrimSpace(row.Provider), githubProviderName) {
			continue
		}
		dto := GithubRepoConfigDTO{
			SourceKey: row.SourceKey,
			Events:    githubEventsFromConfig(row.Config),
		}
		if len(dto.Events) == 0 {
			dto.Events = append([]string(nil), githubsource.KnownEvents...)
		}
		var latestPoll, backoff time.Time
		for _, stream := range githubsource.KnownEvents {
			st, err := stateStore.Load(ctx, projectID, row.SourceKey, stream)
			if err != nil {
				return GithubConfigView{}, err
			}
			if st.LastPolledAt.After(latestPoll) {
				latestPoll = st.LastPolledAt
			}
			if st.BackoffUntil.After(backoff) {
				backoff = st.BackoffUntil
			}
			if dto.LastError == "" && st.LastError != "" {
				dto.LastError = st.LastError
			}
		}
		if !latestPoll.IsZero() {
			v := latestPoll
			dto.LastPolledAt = &v
		}
		if !backoff.IsZero() {
			v := backoff
			dto.BackoffUntil = &v
		}
		view.Repos = append(view.Repos, dto)
	}
	sort.Slice(view.Repos, func(i, j int) bool { return view.Repos[i].SourceKey < view.Repos[j].SourceKey })
	return view, nil
}

// UpdateGitHubConfig changes the enabled event set for the supplied repos. A
// repo that is not bound to the project is rejected (never silently ignored)
// and unknown event names are dropped.
func (s *GithubSourceService) UpdateGitHubConfig(ctx context.Context, projectID int64, req UpdateGithubConfigRequest) (GithubConfigView, error) {
	if len(req.Repos) == 0 {
		return GithubConfigView{}, fmt.Errorf("repos must not be empty")
	}
	rows, err := s.q.ListProjectExternalSources(ctx, projectID)
	if err != nil {
		return GithubConfigView{}, err
	}
	bound := make(map[string]store.ProjectExternalSource, len(rows))
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.Provider), githubProviderName) {
			bound[row.SourceKey] = row
		}
	}
	for _, in := range req.Repos {
		key := strings.TrimSpace(in.SourceKey)
		row, ok := bound[key]
		if !ok {
			return GithubConfigView{}, fmt.Errorf("repo %q is not bound to this project", key)
		}
		events := make([]string, 0, len(in.Events))
		for _, e := range in.Events {
			if githubsource.IsKnownEvent(e) {
				events = append(events, e)
			}
		}
		if len(events) == 0 {
			return GithubConfigView{}, fmt.Errorf("repo %q must enable at least one event", key)
		}
		cfg := map[string]any{}
		if strings.TrimSpace(row.Config) != "" {
			_ = json.Unmarshal([]byte(row.Config), &cfg)
		}
		cfg["events"] = events
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return GithubConfigView{}, err
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE project_external_sources SET config = ?
			 WHERE id = ? AND project_id = ?`, string(encoded), row.ID, projectID,
		); err != nil {
			return GithubConfigView{}, err
		}
	}
	return s.ListGitHubConfig(ctx, projectID)
}
