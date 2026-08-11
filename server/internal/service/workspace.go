package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	stdpath "path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// branchInfoFetcher is the narrow subset of RepositoryService that
// GetIssueDefaultRepos needs. Defined as an interface so tests can
// substitute a fake without constructing real repositories on disk.
type branchInfoFetcher interface {
	GetBranchInfo(ctx context.Context, id string) (BranchInfo, error)
}

type WorkspaceService struct {
	q             *store.Queries
	db            *store.DB
	cfg           *config.WorkspaceConfig
	dataDir       string // root data directory (e.g. ~/.niuniu)
	notifyHub     *notify.NotificationHub
	harnessSvc    *HarnessService
	authz         *Authz
	agentProxy    *agentproxy.AgentProxy
	repoSvc       branchInfoFetcher     // wired via SetRepositoryService
	perm          *PermissionService    // wired via SetPermissionService
	askUser       *AskUserService       // wired via SetAskUserService
	mcpGen        *MCPConfigGenerator   // wired via SetMCPGenerator
	sceneLayers   *SceneLayerService    // wired via SetSceneLayerService
	sceneProj     *SceneProjector       // wired via SetSceneProjector
	eventBus      *event.Bus            // wired via SetEventBus (optional; nil in tests)
	// onCreated is an optional post-creation hook (wired via SetWorkspaceCreatedHook).
	// The Epic execution engine uses it to auto-link a workspace by its issue type:
	// an Epic workspace becomes an orchestration workspace; a child workspace gets
	// kicked off with the issue's title + description. Runs in its own goroutine so
	// it never blocks Create. Nil-safe.
	onCreated func(context.Context, store.Workspace)
}

func NewWorkspaceService(q *store.Queries, db *sql.DB, cfg *config.WorkspaceConfig, dataDir string, notifyHub *notify.NotificationHub, authz *Authz) *WorkspaceService {
	return &WorkspaceService{q: q, db: store.Wrap(db), cfg: cfg, dataDir: dataDir, notifyHub: notifyHub, authz: authz}
}

// SetEventBus wires the in-process event bus so workspace lifecycle transitions
// can notify subscribers (e.g. the Epic execution engine). Optional; nil-safe.
func (s *WorkspaceService) SetEventBus(b *event.Bus) {
	s.eventBus = b
}

// SetWorkspaceCreatedHook wires a post-creation callback invoked (in its own
// goroutine) after a workspace is successfully created. Used by the Epic engine
// to auto-link the workspace based on its issue type. Optional; nil-safe.
func (s *WorkspaceService) SetWorkspaceCreatedHook(fn func(context.Context, store.Workspace)) {
	s.onCreated = fn
}

// SetHarnessService wires the harness service for CLAUDE.md injection.
func (s *WorkspaceService) SetHarnessService(hs *HarnessService) {
	s.harnessSvc = hs
}

// SetAgentProxy injects the agentproxy.AgentProxy used by enrichSidebarMeta to
// read in-flight bg-task state. Called from server.New after both services are
// constructed.
func (s *WorkspaceService) SetAgentProxy(p *agentproxy.AgentProxy) {
	s.agentProxy = p
}

// SetRepositoryService injects the RepositoryService used by
// GetIssueDefaultRepos to fetch per-repo branch info.
func (s *WorkspaceService) SetRepositoryService(r *RepositoryService) {
	s.repoSvc = r
}

// SetPermissionService injects the permission service so Archive can cancel
// pending permission requests when a workspace is archived.
func (s *WorkspaceService) SetPermissionService(p *PermissionService) {
	s.perm = p
}

// SetAskUserService injects the ask-user service so Archive can cancel
// pending ask-user requests when a workspace is archived.
func (s *WorkspaceService) SetAskUserService(a *AskUserService) {
	s.askUser = a
}

// SetMCPGenerator injects the MCP config generator so Create can drop a
// default .claude/settings.json (with WorktreeCreate/WorktreeRemove hooks)
// alongside the workspace skeleton. Optional; when unset, settings.json
// generation is skipped at create-time and falls back to backfill on the
// next agent spawn (see agent.go / agentproxy/proxy.go).
func (s *WorkspaceService) SetMCPGenerator(g *MCPConfigGenerator) {
	s.mcpGen = g
}

// SetSceneLayerService wires the scene layer service so Create can ensure
// every new workspace has an empty base layer (and prefills project-default
// scenes when a project_id is provided). Optional — when unset, base-layer
// creation is deferred to first scene attach.
func (s *WorkspaceService) SetSceneLayerService(sl *SceneLayerService) {
	s.sceneLayers = sl
}

// SetSceneProjector wires the projector so UpdateMCPServers can re-run
// projection after writing through to the base layer. Optional.
func (s *WorkspaceService) SetSceneProjector(sp *SceneProjector) {
	s.sceneProj = sp
}

func (s *WorkspaceService) List(ctx context.Context) ([]store.Workspace, error) {
	return s.q.ListWorkspaces(ctx)
}

// WorkspaceListFilter optionally narrows the result returned by
// ListForUser / ListWithSidebarMetaForUser. Zero-value means no narrowing.
type WorkspaceListFilter struct {
	// CreatorID, when non-nil, restricts to workspaces with
	// created_by == *CreatorID, OR (NULL fallback) the caller's own
	// personal-space rows that have NULL created_by (legacy data).
	CreatorID *int64
}

// ListForUser returns workspaces accessible to userID, optionally
// narrowed by filter. Pass WorkspaceListFilter{} for unfiltered.
func (s *WorkspaceService) ListForUser(ctx context.Context, userID int64, filter WorkspaceListFilter) ([]store.Workspace, error) {
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	// Column3 = 0 is the "no filter" sentinel: `? = 0` → TRUE, the whole
	// AND-group is a no-op. When a filter is set, Column3 = cid and
	// `cid = 0` → FALSE → the created_by = cid / IS NULL branches fire.
	// This avoids both the 42P18 untyped-param issue (each ? is anchored)
	// and the sqlc.narg() numbered-param issue on SQLite.
	params := store.ListWorkspacesForOwnersParams{
		OwnerID: owners.UserID,
		OrgIds:  orgIDs,
		Column3: int64(0),
	}
	if filter.CreatorID != nil {
		cid := *filter.CreatorID
		params.Column3 = cid
		params.CreatedBy = sql.NullInt64{Int64: cid, Valid: true}
		params.OwnerID_2 = cid
	}
	// When no filter, Column3 stays nil (interface{} zero) so `? IS NULL`
	// short-circuits the predicate and the legacy-NULL branch is unreached.
	// CreatedBy stays {Valid:false} which never matches `created_by = ?`
	// (correct: no rows slip through that branch when caller didn't filter).
	return s.q.ListWorkspacesForOwners(ctx, params)
}

// SearchWorkspaceIDsByUserContent returns the IDs of workspaces accessible to
// userID that contain at least one user-authored chat message whose text
// matches q (ASCII case-insensitive substring). The sidebar search uses this to
// extend the client-side name/id match with conversation-content matching.
// Returns an empty (non-nil) slice for a blank query.
func (s *WorkspaceService) SearchWorkspaceIDsByUserContent(ctx context.Context, userID int64, q string) ([]int64, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []int64{}, nil
	}
	// Cap on runes (not bytes) so a multi-byte CJK keyword is never split.
	if r := []rune(q); len(r) > 200 {
		q = string(r[:200])
	}
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	ids, err := s.q.SearchWorkspaceIDsByUserContentForOwners(ctx, store.SearchWorkspaceIDsByUserContentForOwnersParams{
		LOWER:   "%" + q + "%",
		OwnerID: owners.UserID,
		OrgIds:  orgIDs,
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// WorktreeSidebarInfo holds per-worktree git status for sidebar display.
type WorktreeSidebarInfo struct {
	Name         string `json:"name"`
	RepoName     string `json:"repo_name"`
	Branch       string `json:"branch"`
	BaseBranch   string `json:"base_branch"`
	ChangesCount int    `json:"changes_count"`
	AheadCount   int    `json:"ahead_count"`
}

// BgTaskHighlightMeta is the service-layer highlight before DTO conversion.
// Exactly one of StartedAt / ScheduledFor is non-zero, depending on Kind:
//   - bash / subagent → StartedAt set, ScheduledFor zero
//   - wakeup          → ScheduledFor set, StartedAt zero
type BgTaskHighlightMeta struct {
	Kind         string
	Title        string
	StartedAt    time.Time
	ScheduledFor time.Time
}

// WorkspaceSidebarMeta holds enriched sidebar data for one workspace.
type WorkspaceSidebarMeta struct {
	Workspace        store.Workspace
	ChangesCount     int
	AheadCount       int
	MessageCount     int64
	LastMessageAt    *time.Time
	ProjectName      string // empty if no project
	ProjectOwnerType string // "user" / "org" / ""
	ProjectOwnerID   int64  // 0 if no project
	LifecycleStatus  string // from linked issue
	IssueType        string // linked issue's type ('task'|'epic'); empty if no issue
	ParentIssueID    int64  // linked issue's parent (0 = top-level / no parent / no issue)
	TaskTotal        int
	TaskDone         int
	TaskCurrent      string // active_form of in_progress task; empty if none
	Worktrees        []WorktreeSidebarInfo
	ScheduleCount    int
	EnabledCronCount int
	BgAgentBusy      bool
	BgBashCount      int
	BgWakeupCount    int
	BgSubagentCount  int
	BgHighlight      *BgTaskHighlightMeta
}

// ListWithSidebarMeta returns all workspaces enriched with sidebar metadata.
func (s *WorkspaceService) ListWithSidebarMeta(ctx context.Context) ([]WorkspaceSidebarMeta, error) {
	workspaces, err := s.q.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	return s.enrichSidebarMeta(ctx, workspaces), nil
}

// ListWithSidebarMetaForUser returns workspaces accessible to userID enriched
// with the same sidebar metadata as ListWithSidebarMeta.
func (s *WorkspaceService) ListWithSidebarMetaForUser(ctx context.Context, userID int64, filter WorkspaceListFilter) ([]WorkspaceSidebarMeta, error) {
	workspaces, err := s.ListForUser(ctx, userID, filter)
	if err != nil {
		return nil, err
	}
	return s.enrichSidebarMeta(ctx, workspaces), nil
}

// enrichSidebarMeta fans out per-workspace enrichment with bounded concurrency,
// then merges schedule counts in one aggregate query.
func (s *WorkspaceService) enrichSidebarMeta(ctx context.Context, workspaces []store.Workspace) []WorkspaceSidebarMeta {
	results := make([]WorkspaceSidebarMeta, len(workspaces))

	// Batch the per-workspace DB lookups the sidebar used to run one-by-one
	// (project/lifecycle, worktrees+repo, latest task-batch stats) into three
	// queries total, so DB round-trips no longer scale with the workspace count.
	issueIDs := make([]int64, 0, len(workspaces))
	wsIDs := make([]int64, 0, len(workspaces))
	for _, ws := range workspaces {
		wsIDs = append(wsIDs, ws.ID)
		if ws.IssueID.Valid {
			issueIDs = append(issueIDs, ws.IssueID.Int64)
		}
	}

	projByIssue := map[int64]store.GetProjectAndLifecycleByIssueIDsRow{}
	if len(issueIDs) > 0 {
		if rows, err := s.q.GetProjectAndLifecycleByIssueIDs(ctx, issueIDs); err != nil {
			slog.Warn("enrichSidebarMeta: GetProjectAndLifecycleByIssueIDs failed", "error", err)
		} else {
			for _, r := range rows {
				projByIssue[r.IssueID] = r
			}
		}
	}

	worktreesByWs := map[int64][]store.ListWorktreesWithRepositoryForWorkspacesRow{}
	statsByWs := map[int64]store.GetLatestBatchTaskStatsForWorkspacesRow{}
	if len(wsIDs) > 0 {
		if rows, err := s.q.ListWorktreesWithRepositoryForWorkspaces(ctx, wsIDs); err != nil {
			slog.Warn("enrichSidebarMeta: ListWorktreesWithRepositoryForWorkspaces failed", "error", err)
		} else {
			for _, r := range rows {
				worktreesByWs[r.WorkspaceID] = append(worktreesByWs[r.WorkspaceID], r)
			}
		}
		if rows, err := s.q.GetLatestBatchTaskStatsForWorkspaces(ctx, wsIDs); err != nil {
			slog.Warn("enrichSidebarMeta: GetLatestBatchTaskStatsForWorkspaces failed", "error", err)
		} else {
			for _, r := range rows {
				statsByWs[r.WorkspaceID] = r
			}
		}
	}

	// fillSidebarMeta is now a pure in-memory merge of the pre-batched maps
	// (git moved to the lazy sidebar-git endpoint), so a sequential loop is
	// correct and cheaper than fanning out goroutines for map lookups.
	for i, ws := range workspaces {
		results[i].Workspace = ws
		s.fillSidebarMeta(&results[i], projByIssue, worktreesByWs, statsByWs)
	}

	counts, err := s.q.CountSchedulesByWorkspace(ctx)
	if err != nil {
		slog.Warn("enrichSidebarMeta: CountSchedulesByWorkspace failed", "error", err)
	} else {
		countMap := make(map[int64]int64, len(counts))
		for _, c := range counts {
			countMap[c.WorkspaceID] = c.Count
		}
		for i := range results {
			if cnt, ok := countMap[results[i].Workspace.ID]; ok {
				results[i].ScheduleCount = int(cnt)
			}
		}
	}

	enabledCounts, err := s.q.CountEnabledSchedulesByWorkspace(ctx)
	if err != nil {
		slog.Warn("enrichSidebarMeta: CountEnabledSchedulesByWorkspace failed", "error", err)
	} else {
		m := make(map[int64]int64, len(enabledCounts))
		for _, c := range enabledCounts {
			m[c.WorkspaceID] = c.Count
		}
		for i := range results {
			if cnt, ok := m[results[i].Workspace.ID]; ok {
				results[i].EnabledCronCount = int(cnt)
			}
		}
	}

	workspaceIDs := make([]int64, 0, len(results))
	for i := range results {
		workspaceIDs = append(workspaceIDs, results[i].Workspace.ID)
	}
	if len(workspaceIDs) > 0 {
		msgRows, err := s.q.AggregateWorkspaceMessagesForWorkspaces(ctx, workspaceIDs)
		if err != nil {
			slog.Warn("enrichSidebarMeta: AggregateWorkspaceMessagesForWorkspaces failed", "error", err)
		} else {
			m := make(map[int64]store.AggregateWorkspaceMessagesForWorkspacesRow, len(msgRows))
			for _, r := range msgRows {
				m[r.WorkspaceID] = r
			}
			for i := range results {
				if row, ok := m[results[i].Workspace.ID]; ok {
					results[i].MessageCount = row.MessageCount
					results[i].LastMessageAt = asTimePtr(row.LastMessageAt)
				}
			}
		}
	}

	// fillBgTaskMeta is intentionally sequential, not folded into the
	// fillSidebarMeta goroutine pool: each call is two map lookups + two
	// mutex acquisitions on in-process structures, so the per-workspace cost
	// is single-digit microseconds. Parallelizing N=100 workspaces saves
	// well under 1ms total — not worth the extra coordination.
	if s.agentProxy != nil {
		for i := range results {
			fillBgTaskMeta(&results[i], s.agentProxy)
		}
	}

	return results
}

// fillSidebarMeta populates one workspace's sidebar metadata from the
// pre-batched DB maps built by enrichSidebarMeta. It is intentionally DB-only
// and fast: the per-worktree git badges (changes_count / ahead_count) are left
// at zero here and loaded lazily by the client via GET /api/workspaces/sidebar-git,
// so the list returns without spawning any git subprocess or touching disk.
func (s *WorkspaceService) fillSidebarMeta(
	meta *WorkspaceSidebarMeta,
	projByIssue map[int64]store.GetProjectAndLifecycleByIssueIDsRow,
	worktreesByWs map[int64][]store.ListWorktreesWithRepositoryForWorkspacesRow,
	statsByWs map[int64]store.GetLatestBatchTaskStatsForWorkspacesRow,
) {
	ws := meta.Workspace

	// 1. Project name (via issue_id -> column -> project)
	if ws.IssueID.Valid {
		if row, ok := projByIssue[ws.IssueID.Int64]; ok {
			meta.ProjectName = row.ProjectName
			meta.ProjectOwnerType = row.ProjectOwnerType
			meta.ProjectOwnerID = row.ProjectOwnerID
			meta.LifecycleStatus = row.LifecycleStatus
			meta.IssueType = row.IssueType
			if row.ParentIssueID.Valid {
				meta.ParentIssueID = row.ParentIssueID.Int64
			}
		}
	}

	// 2. Worktree list from the DB (name/repo/branch/base_branch). The
	//    git-derived changes_count/ahead_count stay 0; the client fills them via
	//    the lazy sidebar-git endpoint. This keeps the list a pure DB read.
	for _, row := range worktreesByWs[ws.ID] {
		baseBranch := row.BaseBranch
		if baseBranch == "" && row.RDefaultBranch.Valid {
			// Fallback for old records without base_branch
			baseBranch = row.RDefaultBranch.String
		}
		meta.Worktrees = append(meta.Worktrees, WorktreeSidebarInfo{
			Name:       filepath.Base(row.WorktreePath),
			RepoName:   row.RName,
			Branch:     row.Branch,
			BaseBranch: baseBranch,
		})
	}

	// 3. Task stats (latest batch), pre-batched across all workspaces.
	if st, ok := statsByWs[ws.ID]; ok {
		meta.TaskTotal = int(st.Total)
		if st.Completed.Valid {
			meta.TaskDone = int(st.Completed.Float64)
		}
		meta.TaskCurrent = activeFormString(st.ActiveForm)
	}
}

// activeFormString coerces the interface{}-typed active_form column (produced by
// a MAX(CASE ...) aggregate) into a string, tolerating string/[]byte/nil.
func activeFormString(v interface{}) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return ""
	}
}

// listWorktreeDirs enumerates the worktree directories under a workspace path
// without a DB round-trip. Best-effort: any read error yields no worktrees, so
// the sidebar degrades gracefully instead of failing the whole request.
func listWorktreeDirs(wsPath string) []WorktreeGroup {
	worktreesPath := filepath.Join(wsPath, ".worktrees")
	entries, err := os.ReadDir(worktreesPath)
	if err != nil {
		return nil
	}
	groups := make([]WorktreeGroup, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			groups = append(groups, WorktreeGroup{
				Name: entry.Name(),
				Path: filepath.Join(worktreesPath, entry.Name()),
			})
		}
	}
	return groups
}

// worktreeGitInfoCached returns the sidebar git badges for a worktree, serving
// from the process-wide TTL cache when fresh and otherwise recomputing via git.
func worktreeGitInfoCached(worktreePath, baseBranch string) worktreeGitInfo {
	now := time.Now()
	if gi, ok := sidebarGitCache.get(worktreePath, now); ok {
		return gi
	}
	gi := computeWorktreeGitInfo(worktreePath, baseBranch)
	sidebarGitCache.set(worktreePath, gi, now)
	return gi
}

// computeWorktreeGitInfo runs the git subprocesses that back the sidebar's
// change/ahead badges for one worktree. Failures degrade to zero counts (the
// badges are approximate), matching the previous per-worktree behavior.
func computeWorktreeGitInfo(worktreePath, baseBranch string) worktreeGitInfo {
	var gi worktreeGitInfo
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return gi
	}

	// Changes: every porcelain entry counts as one change (matching the prior
	// sum of Modified+Added+Deleted+Untracked, which bucketed every entry).
	if statuses, err := git.Status(worktreePath); err == nil {
		gi.changesCount = len(statuses)
	}

	// Ahead count: compare current branch against its base branch.
	targetBranch := baseBranch
	if targetBranch == "" {
		targetBranch = git.DetectDefaultBranch(worktreePath)
	}
	if currentBranch, err := git.CurrentBranch(worktreePath); err == nil && currentBranch != targetBranch {
		if ahead, err := git.AheadCount(worktreePath, targetBranch, currentBranch); err == nil {
			gi.aheadCount = ahead
		}
	}
	return gi
}

// sidebarFanoutConcurrency bounds the per-workspace git fan-out. The work is
// dominated by git subprocesses (status + rev-list per worktree), which are
// CPU/IO-bound and hold no DB connection between calls, so we scale with the CPU
// count rather than a fixed 5. Capped at 16 to bound the git-process fan-out.
func sidebarFanoutConcurrency() int {
	n := runtime.NumCPU() * 2
	if n < 5 {
		return 5
	}
	if n > 16 {
		return 16
	}
	return n
}

// WorktreeGitStatus is the lazily-computed git badge data for one worktree.
type WorktreeGitStatus struct {
	Name         string
	ChangesCount int
	AheadCount   int
}

// WorkspaceGitStatus is the lazily-computed git badge data for one workspace,
// served by GET /api/workspaces/sidebar-git after the (DB-only) sidebar list.
type WorkspaceGitStatus struct {
	WorkspaceID  int64
	ChangesCount int
	AheadCount   int
	Worktrees    []WorktreeGitStatus
}

// SidebarGitStatusForUser computes the git badges (changes/ahead per worktree)
// for every workspace accessible to userID under the given filter. It mirrors
// ListWithSidebarMetaForUser's access scope so the client can request the git
// status for exactly the workspaces the sidebar listed.
func (s *WorkspaceService) SidebarGitStatusForUser(ctx context.Context, userID int64, filter WorkspaceListFilter) ([]WorkspaceGitStatus, error) {
	workspaces, err := s.ListForUser(ctx, userID, filter)
	if err != nil {
		return nil, err
	}
	return s.sidebarGitStatus(ctx, workspaces), nil
}

// SidebarGitStatus is the no-auth variant (single-user / auth-disabled mode).
func (s *WorkspaceService) SidebarGitStatus(ctx context.Context) ([]WorkspaceGitStatus, error) {
	workspaces, err := s.q.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	return s.sidebarGitStatus(ctx, workspaces), nil
}

// sidebarGitStatus runs the git subprocess fan-out (memoized via sidebarGitCache,
// bounded concurrency, honoring ctx cancellation) for the given workspaces and
// returns their per-worktree change/ahead counts. base_branch comes from one
// batched worktrees query; the on-disk worktree dirs drive the git calls.
func (s *WorkspaceService) sidebarGitStatus(ctx context.Context, workspaces []store.Workspace) []WorkspaceGitStatus {
	results := make([]WorkspaceGitStatus, len(workspaces))

	// One batched query for base_branch / repo default branch keyed by dir name.
	baseByWs := make(map[int64]map[string]string, len(workspaces))
	if len(workspaces) > 0 {
		wsIDs := make([]int64, 0, len(workspaces))
		for _, ws := range workspaces {
			wsIDs = append(wsIDs, ws.ID)
		}
		if rows, err := s.q.ListWorktreesWithRepositoryForWorkspaces(ctx, wsIDs); err != nil {
			slog.Warn("sidebarGitStatus: ListWorktreesWithRepositoryForWorkspaces failed", "error", err)
		} else {
			for _, row := range rows {
				bb := row.BaseBranch
				if bb == "" && row.RDefaultBranch.Valid {
					bb = row.RDefaultBranch.String
				}
				m := baseByWs[row.WorkspaceID]
				if m == nil {
					m = map[string]string{}
					baseByWs[row.WorkspaceID] = m
				}
				m[filepath.Base(row.WorktreePath)] = bb
			}
		}
	}

	sem := make(chan struct{}, sidebarFanoutConcurrency())
	var wg sync.WaitGroup
	for i, ws := range workspaces {
		results[i].WorkspaceID = ws.ID
		wg.Add(1)
		go func(idx int, ws store.Workspace) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Skip the git fan-out if the client already disconnected.
			if ctx.Err() != nil {
				return
			}
			baseByName := baseByWs[ws.ID]
			for _, group := range listWorktreeDirs(ws.Path) {
				gi := worktreeGitInfoCached(group.Path, baseByName[group.Name])
				results[idx].ChangesCount += gi.changesCount
				results[idx].AheadCount += gi.aheadCount
				results[idx].Worktrees = append(results[idx].Worktrees, WorktreeGitStatus{
					Name:         group.Name,
					ChangesCount: gi.changesCount,
					AheadCount:   gi.aheadCount,
				})
			}
		}(i, ws)
	}
	wg.Wait()
	return results
}

func (s *WorkspaceService) Rename(ctx context.Context, id int64, name string) error {
	return s.q.UpdateWorkspaceName(ctx, store.UpdateWorkspaceNameParams{
		Name: name,
		ID:   id,
	})
}

func (s *WorkspaceService) Get(ctx context.Context, id int64) (store.Workspace, error) {
	return s.q.GetWorkspace(ctx, id)
}

func (s *WorkspaceService) GetWorkspacesByIssue(ctx context.Context, issueID int64) ([]store.Workspace, error) {
	return s.q.GetWorkspacesByIssue(ctx, sql.NullInt64{Int64: issueID, Valid: true})
}

func (s *WorkspaceService) ListAvailableIssuesForWorkspace(ctx context.Context) ([]store.ListAvailableIssuesForWorkspaceRow, error) {
	return s.q.ListAvailableIssuesForWorkspace(ctx)
}

type CreateWorkspaceInput struct {
	IssueID *int64 // nullable - pointer to detect if provided
	// ProjectID lets callers create a workspace under a project without
	// linking to a specific issue. Used by scene prefill to look up
	// project_default_scenes. Ignored when IssueID is set (issue's project
	// wins).
	ProjectID       *int64
	Name            string
	Repos           []RepoBranch // New format: per-repo branch selection
	OwnerType       string
	OwnerID         int64
	CreatedBy       *int64 // nullable; nil + owner_type='user' falls back to OwnerID
	// MCPServers is the workspace-scoped list of extra MCP server names to
	// surface to Claude (names only; resolved against the bound claude
	// account's registry at .mcp.json generation time).
	MCPServers []string
	// CliType picks which agent CLI runs in the workspace. Empty string
	// defaults to "claude" in the SQL layer via COALESCE.
	CliType string
	// IsStudio marks a workspace created via the studio "from local directory"
	// flow (issue #232). Studio workspaces get a preset git Bash allowlist and a
	// delivery hint in the IDE. Defaults to false (regular dev workspace).
	IsStudio bool
	// NoRepo creates a plain owner-isolated directory with NO git worktrees
	// attached — used for office / non-code tasks that do not need a repo.
	// When true, any Repos are ignored and the worktree-provisioning loop is
	// skipped entirely. A no-repo workspace is simply a workspace with zero
	// worktree rows; see docs/architecture/workspace-model.md.
	NoRepo bool
	// Language is the creating user's UI language code (e.g. "zh-CN", "zh-TW",
	// "en"), forwarded by handlers from the X-Niuniu-Language request header.
	// It seeds the "User Language" directive in the generated CLAUDE.md /
	// AGENTS.md so the agent defaults to the user's language. Empty or an
	// unrecognized code falls back to a generic "follow the user" directive.
	Language string
	// PermissionMode overrides the seeded NIUNIU_PERMISSION_MODE for this
	// workspace. Empty defaults to "autohost" (bypassPermissions + the
	// auto-continue watchdog). Interactive flows that must wait for the user
	// (e.g. IM-bot onboarding) pass "bypassPermissions" so the agent still skips
	// permission prompts but the watchdog does NOT auto-continue turns.
	PermissionMode string
}

// ValidCliTypes is the closed set accepted by the cli_type CHECK constraint
// in workspaces.schema. Empty string is accepted as input and normalized to
// "claude" at the SQL layer (see queries/workspaces.sql:CreateWorkspace).
var ValidCliTypes = map[string]struct{}{
	"":       {},
	"claude": {},
	"codex":  {},
	"qwen":   {},
	"omp":    {},
	"goose":  {},
}

// ErrInvalidCliType is returned by Create when input.CliType is outside the
// closed set in ValidCliTypes. API handlers should map this to HTTP 400
// (BadRequest) rather than the generic 500, both so monitoring does not
// page on user-input mistakes and so clients get an actionable response.
var ErrInvalidCliType = errors.New("invalid cli_type: must be 'claude', 'codex', 'qwen', 'omp' or 'goose'")

// ErrCodexSandboxNotCodexWorkspace is returned by UpdateCodexSandbox when the
// target workspace is not cli_type='codex'. UI should not surface the option
// for claude workspaces, but this is the server-side guard for direct API
// callers.
var ErrCodexSandboxNotCodexWorkspace = errors.New("codex sandbox/approval can only be set on codex workspaces")

// UpdateCodexSandbox updates the per-workspace codex sandbox + approval policy.
// Either field is optional (nil = no change). Workspace must be cli_type='codex'.
//
// The CHECK constraint on schema fresh-deploy enforces enum values; legacy
// rows without the constraint rely on the handler's enum validation. Empty
// fields here are silently skipped — there is no need to "clear" since the
// defaults backfill.
func (s *WorkspaceService) UpdateCodexSandbox(ctx context.Context, workspaceID int64, sandboxMode, approvalPolicy *string) error {
	cfg, err := s.q.GetWorkspaceCodexConfig(ctx, workspaceID)
	if err != nil {
		return err
	}
	if cfg.CliType != "codex" {
		return ErrCodexSandboxNotCodexWorkspace
	}
	newSandbox := cfg.CodexSandboxMode
	if newSandbox == "" {
		newSandbox = "danger-full-access"
	}
	newApproval := cfg.CodexApprovalPolicy
	if newApproval == "" {
		newApproval = "never"
	}
	if sandboxMode != nil {
		newSandbox = *sandboxMode
	}
	if approvalPolicy != nil {
		newApproval = *approvalPolicy
	}
	return s.q.SetWorkspaceCodexSandbox(ctx, store.SetWorkspaceCodexSandboxParams{
		CodexSandboxMode:    newSandbox,
		CodexApprovalPolicy: newApproval,
		ID:                  workspaceID,
	})
}

type RepoBranch struct {
	RepoID int64
	// Branch is the fork point: the worktree's new branch (ws-<id>/<Branch>) is
	// created from this ref.
	Branch string
	// Base, when non-empty, is the branch the worktree's diff/ahead view is computed
	// against (recorded as base_branch), decoupled from Branch (the fork point).
	// Empty => base == Branch (the common case). The Epic engine sets it so the
	// epic's OWN workspace forks from the epic feature branch (holding the accumulated
	// child work, and review fixes land on a branch that merges back into it) yet
	// diffs against the real baseline (the project default branch).
	Base string
}

// resolveIssueOrEpicCreator implements fallbacks 2 and 3 of the workspace
// creator chain: the issue's own created_by, else (when the issue is a child)
// its governing epic — the parent issue — created_by. Returns false when
// neither is recorded (e.g. legacy issues created before created_by existed, or
// agent/system-created issues), letting the caller fall through to the
// personal-space owner fallback. Read-only; failures degrade to "not found".
func (s *WorkspaceService) resolveIssueOrEpicCreator(ctx context.Context, issueID int64) (int64, bool) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return 0, false
	}
	if issue.CreatedBy.Valid {
		return issue.CreatedBy.Int64, true
	}
	if issue.ParentIssueID.Valid {
		if epic, err := s.q.GetIssue(ctx, issue.ParentIssueID.Int64); err == nil && epic.CreatedBy.Valid {
			return epic.CreatedBy.Int64, true
		}
	}
	return 0, false
}

func (s *WorkspaceService) Create(ctx context.Context, input CreateWorkspaceInput) (*WorkspaceResult, error) {
	ownerType := input.OwnerType
	if ownerType == "" {
		ownerType = "user"
	}
	owner := OwnerRef{Type: ownerType, ID: input.OwnerID}

	// No-repo workspaces are plain directories: drop any repos so the worktree
	// loop below is a no-op and the workspace ends up with zero worktree rows.
	if input.NoRepo {
		input.Repos = nil
	}

	// 1. Insert workspace record first to get the real ID.
	// Use a placeholder path until we know the ID.
	var issueID sql.NullInt64
	if input.IssueID != nil {
		issueID = sql.NullInt64{Int64: *input.IssueID, Valid: true}
	}

	// Use a temp path under the legacy base dir for the initial insert only.
	tempDir := filepath.Join(s.cfg.BaseDir, "ws-tmp-pending")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("create temp workspace directory: %w", err)
	}

	// Resolve the workspace creator by priority (工作空间创建人确定顺序):
	//   1) the explicit caller (current logged-in user, set by HTTP handlers)
	//   2) the bound issue's creator
	//   3) the governing epic (parent issue)'s creator
	//   4) personal-space fallback: a user-owned workspace's owner
	// Steps 2-3 matter for async paths (epic orchestration / start_workspace MCP
	// / scheduler) that have no logged-in caller — without them org-owned derived
	// workspaces show "未指定".
	var createdBy sql.NullInt64
	switch {
	case input.CreatedBy != nil:
		createdBy = sql.NullInt64{Int64: *input.CreatedBy, Valid: true}
	case input.IssueID != nil:
		if uid, ok := s.resolveIssueOrEpicCreator(ctx, *input.IssueID); ok {
			createdBy = sql.NullInt64{Int64: uid, Valid: true}
		}
	}
	if !createdBy.Valid && ownerType == "user" {
		// Personal-space fallback: a user-owned workspace with unknown caller is
		// owned by the same user who created it. Covers admin CLI / harness /
		// scheduler paths where neither caller nor issue creator is recorded.
		createdBy = sql.NullInt64{Int64: input.OwnerID, Valid: true}
	}
	if _, ok := ValidCliTypes[input.CliType]; !ok {
		os.RemoveAll(tempDir)
		// Wrap the sentinel so callers can `errors.Is(err, ErrInvalidCliType)`
		// and translate to HTTP 400. The %q preserves the offending value in
		// the message for log readability.
		return nil, fmt.Errorf("%w: got %q", ErrInvalidCliType, input.CliType)
	}
	workspace, err := s.q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   issueID,
		Name:      input.Name,
		Path:      tempDir,
		Status:    "created",
		OwnerType: ownerType,
		OwnerID:   input.OwnerID,
		CreatedBy: createdBy,
		CliType:   input.CliType,
		Language:  input.Language,
	})
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("create workspace record: %w", err)
	}
	// Persist the workspace-scoped MCP server name list. JSON-encoded so the
	// column shape stays stable across SQLite (TEXT) and Postgres (JSONB);
	// CreateWorkspace omits the column from its INSERT so we explicitly
	// UPDATE only when the caller passed names — the DEFAULT '[]' covers the
	// empty case.
	if len(input.MCPServers) > 0 {
		body, _ := json.Marshal(input.MCPServers)
		if err := s.q.UpdateWorkspaceMcpServers(ctx, store.UpdateWorkspaceMcpServersParams{
			McpServers: string(body),
			ID:         workspace.ID,
		}); err != nil {
			// Match the rollback convention used elsewhere in Create: if a
			// post-insert step fails, delete the orphan workspace + tempDir
			// so the caller's 500 is consistent with "nothing was created".
			// CLAUDE.md notes UpdateMcpServers uses sqlc-generated method
			// which goes through the *store.DB wrapper, so it's safe on PG.
			_ = s.q.DeleteWorkspace(ctx, workspace.ID)
			os.RemoveAll(tempDir)
			return nil, fmt.Errorf("save mcp_servers: %w", err)
		}
	}

	// 2. Build the canonical OwnerRef-based path and move the directory there.
	wsDir := owner.WorkspacePath(s.dataDir, workspace.ID)
	if err := os.MkdirAll(filepath.Dir(wsDir), 0755); err != nil {
		s.q.DeleteWorkspace(ctx, workspace.ID)
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("create workspace parent directory: %w", err)
	}
	if err := os.Rename(tempDir, wsDir); err != nil {
		s.q.DeleteWorkspace(ctx, workspace.ID)
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("rename workspace directory: %w", err)
	}

	// 3. Update the DB record with the canonical path.
	workspace, err = s.q.UpdateWorkspacePath(ctx, store.UpdateWorkspacePathParams{
		ID:   workspace.ID,
		Path: wsDir,
	})
	if err != nil {
		os.RemoveAll(wsDir)
		return nil, fmt.Errorf("update workspace path: %w", err)
	}

	// Mark studio workspaces (issue #232). Best-effort: a failed flag write does
	// not invalidate the workspace, so log and continue (mirrors the claude /
	// codex account binding convention above).
	if input.IsStudio {
		if err := s.q.SetWorkspaceStudio(ctx, store.SetWorkspaceStudioParams{
			IsStudio: 1,
			ID:       workspace.ID,
		}); err != nil {
			slog.Warn("workspace.Create: set is_studio failed",
				"workspace_id", workspace.ID, "error", err)
		} else {
			workspace.IsStudio = 1
		}
	}

	// Create worktrees directory (.worktrees is hidden)
	worktreesDir := filepath.Join(wsDir, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		return nil, fmt.Errorf("create worktrees directory: %w", err)
	}

	// Create team inbox directory for MCP workspace tools
	teamInboxDir := filepath.Join(wsDir, ".team", "inboxes")
	if err := os.MkdirAll(teamInboxDir, 0755); err != nil {
		return nil, fmt.Errorf("create team inbox directory: %w", err)
	}

	// Write the initial agent config. Failure is non-fatal: the spawn path
	// regenerates the same config with a live session token as a safety net.
	if s.mcpGen != nil {
		configDir := ""
		opts := config.MCPGenerateOptions{
			WorkspaceID: workspace.ID,
			InboxDir:    filepath.Join(wsDir, ".team", "inboxes"),
		}
		if input.CliType == "codex" {
			if _, err := s.mcpGen.GenerateCodexConfigTomlWithExtras(wsDir, opts, input.MCPServers, configDir); err != nil {
				slog.Warn("workspace.Create: generate .codex/config.toml failed",
					"workspace_id", workspace.ID, "error", err)
			}
		} else {
			if err := s.mcpGen.GenerateClaudeSettings(wsDir); err != nil {
				slog.Warn("workspace.Create: generate .claude/settings.json failed",
					"workspace_id", workspace.ID, "error", err)
			}
			if _, err := s.mcpGen.Generate(wsDir, opts, input.MCPServers, configDir); err != nil {
				slog.Warn("workspace.Create: generate .mcp.json failed",
					"workspace_id", workspace.ID, "error", err)
			}
		}
	}

	// 4. Add repositories as worktrees (partial failure handling)
	result := &WorkspaceResult{
		Workspace: workspace,
		Repos:     []WorkspaceRepoResult{},
		Errors:    []WorkspaceRepoError{},
	}

	for _, rb := range input.Repos {
		// Get repo first (needed for name and path)
		repo, err := s.q.GetRepository(ctx, rb.RepoID)
		if err != nil {
			slog.Warn("workspace.Create: repository not found", "repo_id", rb.RepoID, "workspace_id", workspace.ID)
			result.Errors = append(result.Errors, WorkspaceRepoError{
				RepositoryID: strconv.FormatInt(rb.RepoID, 10),
				Error:        "repository not found",
			})
			continue
		}

		branch, err := resolveBaseBranch(repo, rb.Branch)
		if err != nil {
			slog.Warn("workspace.Create: cannot resolve base branch", "repo_id", rb.RepoID, "workspace_id", workspace.ID, "error", err)
			result.Errors = append(result.Errors, WorkspaceRepoError{
				RepositoryID: strconv.FormatInt(rb.RepoID, 10),
				Error:        err.Error(),
			})
			continue
		}

		// Diff base recorded on the worktree: an explicit rb.Base (decoupled from the
		// fork point) when set, else the fork point itself (the common case where a
		// worktree diffs against the branch it forked from).
		baseBranch := branch
		if rb.Base != "" {
			baseBranch, err = resolveBaseBranch(repo, rb.Base)
			if err != nil {
				slog.Warn("workspace.Create: cannot resolve diff base branch", "repo_id", rb.RepoID, "workspace_id", workspace.ID, "error", err)
				result.Errors = append(result.Errors, WorkspaceRepoError{
					RepositoryID: strconv.FormatInt(rb.RepoID, 10),
					Error:        err.Error(),
				})
				continue
			}
		}

		// Generate worktree path: {wsDir}/.worktrees/{repoName}-{branch}
		worktreePath := GenerateWorktreePath(wsDir, rb.RepoID, repo.Name, branch)

		// Check if worktree path already exists
		if _, err := os.Stat(worktreePath); err == nil {
			slog.Warn("workspace.Create: worktree path already exists", "repo_id", rb.RepoID, "workspace_id", workspace.ID, "path", worktreePath)
			result.Errors = append(result.Errors, WorkspaceRepoError{
				RepositoryID: strconv.FormatInt(rb.RepoID, 10),
				Error:        "worktree path already exists",
			})
			continue
		}

		// Git branch name with workspace prefix: ws-<id>/<branch>
		wtBranch := GenerateWorktreeBranch(workspace.ID, branch)

		// Create git worktree forked from the user-specified base branch.
		if err := git.WorktreeAdd(repo.Path, worktreePath, wtBranch, branch); err != nil {
			slog.Warn("workspace.Create: git worktree add failed", "repo_id", rb.RepoID, "workspace_id", workspace.ID, "base_branch", branch, "error", err)
			result.Errors = append(result.Errors, WorkspaceRepoError{
				RepositoryID: strconv.FormatInt(rb.RepoID, 10),
				Error:        err.Error(),
			})
			continue
		}

		// Create DB record
		wsRepo, err := s.q.CreateWorktree(ctx, store.CreateWorktreeParams{
			WorkspaceID:  workspace.ID,
			RepositoryID: rb.RepoID,
			WorktreePath: worktreePath,
			Branch:       wtBranch,
			BaseBranch:   baseBranch,
		})
		if err != nil {
			slog.Warn("workspace.Create: persist worktree row failed; rolling back disk", "repo_id", rb.RepoID, "workspace_id", workspace.ID, "error", err)
			git.WorktreeRemove(repo.Path, worktreePath)
			result.Errors = append(result.Errors, WorkspaceRepoError{
				RepositoryID: strconv.FormatInt(rb.RepoID, 10),
				Error:        err.Error(),
			})
			continue
		}

		result.Repos = append(result.Repos, WorkspaceRepoResult{
			RepositoryID: wsRepo.RepositoryID,
			WorktreePath: wsRepo.WorktreePath,
			Branch:       wsRepo.Branch,
		})
	}

	// 5. Generate agent instructions describing workspace structure.
	s.generateWorkspaceAgentInstructions(ctx, wsDir, input.Name, input.CliType, result.Repos, input.NoRepo, input.Language)

	// Seed the permission mode for newly created workspaces. Default is
	// "autohost" — niuniu's superset of bypassPermissions (skip CLI permission
	// prompts + run the watchdog that auto-continues turns). Interactive flows
	// (e.g. IM-bot onboarding) pass "bypassPermissions" so the agent skips
	// prompts but the watchdog does NOT auto-continue — it waits for the user.
	// Users can switch modes anytime via the PermissionSelector. Non-fatal.
	permMode := input.PermissionMode
	if permMode == "" {
		permMode = "autohost"
	}
	if err := s.q.SetWorkspaceEnv(ctx, store.SetWorkspaceEnvParams{
		WorkspaceID: workspace.ID,
		Key:         "NIUNIU_PERMISSION_MODE",
		Value:       permMode,
	}); err != nil {
		slog.Warn("workspace.Create: seed permission mode default failed",
			"workspace_id", workspace.ID, "mode", permMode, "error", err)
	}

	// Scene base layer + project-default prefill (M1).
	// Every workspace gets an empty base layer at create time so the projection
	// stack invariant holds. Then, if the workspace was created under a project
	// with default scenes configured, attach each in order (best-effort — a
	// per-scene attach failure logs but does NOT fail the workspace create).
	if s.sceneLayers != nil {
		if _, err := s.sceneLayers.EnsureBaseLayer(ctx, workspace.ID); err != nil {
			slog.Warn("workspace.Create: ensure base scene layer failed",
				"workspace_id", workspace.ID, "error", err)
		}
		// Resolve project_id. Priority: workspace's bound issue (path:
		// issue -> column -> project) wins; falling back to the explicit
		// input.ProjectID for issue-less project-scoped workspaces.
		var projectID int64
		if issueID.Valid {
			if issue, err := s.q.GetIssue(ctx, issueID.Int64); err == nil {
				if col, err := s.q.GetColumn(ctx, issue.ColumnID); err == nil {
					projectID = col.ProjectID
				}
			}
		}
		if projectID == 0 && input.ProjectID != nil && *input.ProjectID > 0 {
			projectID = *input.ProjectID
		}
		if projectID > 0 {
			defaults, err := s.q.ListProjectDefaults(ctx, projectID)
			if err == nil {
				for _, d := range defaults {
					if _, err := s.sceneLayers.Attach(ctx, workspace.ID, d.SceneID, nil); err != nil {
						slog.Warn("workspace.Create: prefill scene failed",
							"workspace_id", workspace.ID, "scene_id", d.SceneID, "error", err)
					}
				}
			}
		}
	}

	// Audit log for org-owned workspace creation.
	appendResourceAudit(ctx, s.db, nil, ownerType, input.OwnerID, 0, "workspace.created", "workspace", workspace.ID,
		resourceAuditPayload(input.Name))

	// Broadcast workspace creation notification with owner metadata so the hub
	// can filter per-connection (spec §5.9).
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicWorkspace,
			Action:    "created",
			ID:        workspace.ID,
			OwnerType: ownerType,
			OwnerID:   input.OwnerID,
		})
	}

	// Post-creation hook (Epic engine auto-linkage by issue type). Runs in its
	// own goroutine with a background context so it never blocks Create and is
	// unaffected by the request context being cancelled after we return.
	if s.onCreated != nil {
		ws := result.Workspace
		go s.onCreated(context.Background(), ws)
	}

	return result, nil
}

// generateWorkspaceAgentInstructions creates the root instruction file used by
// the workspace's selected CLI so it understands the multi-repo structure.
// When noRepo is true (or no worktrees were provisioned) it instead writes a
// plain-directory brief: the workspace is just an owner-isolated folder with no
// git worktrees, suitable for office / non-code tasks.
func (s *WorkspaceService) generateWorkspaceAgentInstructions(ctx context.Context, wsDir, wsName, cliType string, repos []WorkspaceRepoResult, noRepo bool, language string) {
	instructionFile := "CLAUDE.md"
	worktreeInstructionFiles := []string{"CLAUDE.md"}
	switch cliType {
	case "codex":
		instructionFile = "AGENTS.md"
		worktreeInstructionFiles = []string{"AGENTS.md", "CLAUDE.md"}
	case "qwen":
		// Qwen Code (Gemini-CLI fork) reads QWEN.md as its context file. Also
		// reference CLAUDE.md so repos that only ship Claude instructions still
		// surface to the qwen agent.
		instructionFile = "QWEN.md"
		worktreeInstructionFiles = []string{"QWEN.md", "CLAUDE.md"}
	}

	if noRepo || len(repos) == 0 {
		s.generateNoRepoAgentInstructions(ctx, wsDir, wsName, instructionFile, language)
		return
	}

	var sb strings.Builder
	sb.WriteString("# Workspace: " + wsName + "\n\n")
	sb.WriteString("This workspace contains multiple repository worktrees under `.worktrees/`.\n")
	sb.WriteString("Each subdirectory is an independent git repository.\n")
	sb.WriteString(languageDirective(language))
	sb.WriteString("\n## Repositories\n\n")

	for _, r := range repos {
		dirName := filepath.Base(r.WorktreePath)
		repo, err := s.q.GetRepository(ctx, r.RepositoryID)
		repoName := dirName
		if err == nil {
			repoName = repo.Name
		}
		sb.WriteString(fmt.Sprintf("- **%s** — `.worktrees/%s/` (branch: `%s`)\n", repoName, dirName, r.Branch))
	}

	// Codex workspaces should surface repository-level AGENTS.md instructions
	// before workspace-level guidance so repo norms take precedence.
	if cliType == "codex" {
		var repoRules []string
		for _, r := range repos {
			dirName := filepath.Base(r.WorktreePath)
			agentsPath := filepath.Join(wsDir, ".worktrees", dirName, "AGENTS.md")
			data, err := os.ReadFile(agentsPath)
			if err != nil {
				continue
			}
			repo, err := s.q.GetRepository(ctx, r.RepositoryID)
			label := dirName
			if err == nil && repo.Name != "" {
				label = repo.Name
			}
			var rule strings.Builder
			rule.WriteString(fmt.Sprintf("### `.worktrees/%s/AGENTS.md` (%s)\n\n", dirName, label))
			rule.WriteString(strings.TrimSpace(string(data)))
			rule.WriteString("\n")
			repoRules = append(repoRules, rule.String())
		}
		if len(repoRules) > 0 {
			sb.WriteString("\n## Repository Rules\n\n")
			sb.WriteString("Follow the repository's own `AGENTS.md` first when working inside that repo. Workspace-level guidance applies only when it does not conflict with the repository file.\n\n")
			for _, rule := range repoRules {
				sb.WriteString(rule)
				sb.WriteString("\n")
			}
		}
	}

	// Reference instruction files found inside each worktree so agents
	// automatically follow repo-level rules.
	var wtInstructions []string
	for _, r := range repos {
		dirName := filepath.Base(r.WorktreePath)
		for _, fileName := range worktreeInstructionFiles {
			instructionPath := filepath.Join(wsDir, ".worktrees", dirName, fileName)
			if _, err := os.Stat(instructionPath); err == nil {
				repo, err := s.q.GetRepository(ctx, r.RepositoryID)
				label := dirName
				if err == nil {
					label = repo.Name
				}
				wtInstructions = append(wtInstructions, fmt.Sprintf("- `.worktrees/%s/%s` — %s repo instructions", dirName, fileName, label))
				break
			}
		}
	}
	if len(wtInstructions) > 0 {
		sb.WriteString("\n## Worktree Instructions\n\n")
		sb.WriteString("When working on code inside a worktree, also read and follow that worktree's instruction file:\n\n")
		for _, line := range wtInstructions {
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString("\n## Git Operations\n\n")
	sb.WriteString("Each repository has its own git history. Use `git -C .worktrees/<dir>` for operations:\n\n")
	sb.WriteString("```bash\n")
	sb.WriteString("# Check status of a repo\n")
	sb.WriteString("git -C .worktrees/<dir> status\n\n")
	sb.WriteString("# Commit changes in a repo\n")
	sb.WriteString("git -C .worktrees/<dir> add -A && git -C .worktrees/<dir> commit -m \"message\"\n")
	sb.WriteString("```\n")

	// Append harness engineering standards if available.
	if s.harnessSvc != nil {
		specs, err := s.harnessSvc.ResolveForProject(ctx, nil)
		if err == nil {
			rules := harness.GenerateCLAUDEMDRules(specs)
			if rules != "" {
				sb.WriteString(rules)
			}
		}
	}

	sb.WriteString("\n## Project Learnings\n\n")
	sb.WriteString("See `.learnings.generated.md` in the workspace root for accumulated project experience.\n")
	sb.WriteString("Use the `memory_generate` MCP tool to record new discoveries (patterns, gotchas, decisions, error fixes).\n")

	os.WriteFile(filepath.Join(wsDir, instructionFile), []byte(sb.String()), 0644)
}

// generateNoRepoAgentInstructions writes the root instruction file for a
// no-repo (plain-directory) workspace. There are no `.worktrees/` checkouts and
// no per-repo git history; files the agent creates live directly under the
// workspace root.
func (s *WorkspaceService) generateNoRepoAgentInstructions(ctx context.Context, wsDir, wsName, instructionFile string, language string) {
	var sb strings.Builder
	sb.WriteString("# Workspace: " + wsName + "（纯文件目录 / 无 repo 模式）\n\n")
	sb.WriteString("This workspace is a plain, owner-isolated directory with **no git worktrees**.\n")
	sb.WriteString("It is intended for office / non-code tasks. No repositories are attached, ")
	sb.WriteString("there are no `.worktrees/` checkouts, and there is no per-repo git history to manage.\n\n")
	sb.WriteString("Create and edit files directly under the workspace root. ")
	sb.WriteString("Do not run `git worktree` operations — there are no repos to operate on.\n")
	sb.WriteString(languageDirective(language))

	// Append harness engineering standards if available (global specs only;
	// a no-repo workspace has no project-scoped repo to resolve against).
	if s.harnessSvc != nil {
		if specs, err := s.harnessSvc.ResolveForProject(ctx, nil); err == nil {
			if rules := harness.GenerateCLAUDEMDRules(specs); rules != "" {
				sb.WriteString(rules)
			}
		}
	}

	sb.WriteString("\n## Project Learnings\n\n")
	sb.WriteString("See `.learnings.generated.md` in the workspace root for accumulated project experience.\n")
	sb.WriteString("Use the `memory_generate` MCP tool to record new discoveries (patterns, gotchas, decisions, error fixes).\n")

	os.WriteFile(filepath.Join(wsDir, instructionFile), []byte(sb.String()), 0644)
}

// userLanguageName maps a UI language code to its human-readable name. The
// second return reports whether the code is one we recognize; unknown/empty
// codes get the generic language directive instead of a concrete default.
func userLanguageName(lang string) (string, bool) {
	switch lang {
	case "zh-CN":
		return "简体中文", true
	case "zh-TW":
		return "繁體中文", true
	case "en":
		return "English", true
	default:
		return "", false
	}
}

// languageDirective returns the "User Language" markdown block injected into a
// workspace's agent instruction file. When lang is a recognized UI language it
// pins a concrete default (so even autonomous / scheduled runs with no user
// message to detect from address the user correctly), while still deferring to
// the user if they switch languages mid-conversation. Empty or unrecognized
// codes yield a generic "follow the user" directive.
func languageDirective(lang string) string {
	var sb strings.Builder
	sb.WriteString("\n## 用户语言 / User Language\n\n")
	if name, ok := userLanguageName(lang); ok {
		sb.WriteString(fmt.Sprintf("默认使用**%s**回复用户、撰写解释/总结/问答等面向用户的内容；若用户在对话中改用其他语言，则跟随用户当前使用的语言。代码、标识符、命令、API 字段名等技术内容保持原有约定，不强行翻译。\n\n", name))
		sb.WriteString(fmt.Sprintf("Default to addressing the user in **%s** (`%s`). If the user switches languages mid-conversation, follow their current language. Keep code, identifiers, commands and API field names in their conventional form.\n", name, lang))
	} else {
		sb.WriteString("跟随用户在对话中使用的语言回复（用户用中文则用中文，用英文则用英文）。代码、标识符、命令等技术内容保持原有约定，不强行翻译。\n\n")
		sb.WriteString("Reply in the same language the user writes in. Keep code, identifiers, commands and technical content in their conventional form.\n")
	}
	return sb.String()
}

type WorkspaceResult struct {
	Workspace store.Workspace
	Repos     []WorkspaceRepoResult
	Errors    []WorkspaceRepoError
}

type WorkspaceRepoResult struct {
	RepositoryID int64
	WorktreePath string
	Branch       string
}

type WorkspaceRepoError struct {
	RepositoryID string
	Error        string
}

// WorktreeGroup represents a worktree group with its name and path
type WorktreeGroup struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// CheckWorkspaceChanges checks all worktrees in a workspace for uncommitted/unmerged changes.
func (s *WorkspaceService) CheckWorkspaceChanges(ctx context.Context, workspaceID int64) ([]git.WorktreeChangeStatus, error) {
	// List worktrees with repository info
	rows, err := s.q.ListWorktreesWithRepository(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	var results []git.WorktreeChangeStatus
	for _, row := range rows {
		status := git.WorktreeChangeCheck(row.WorktreePath, row.RName, row.Branch, row.BaseBranch)
		if git.WorktreeHasChanges(status) {
			results = append(results, status)
		}
	}

	return results, nil
}

func (s *WorkspaceService) Delete(ctx context.Context, workspaceID int64) error {
	workspace, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	return s.performDelete(ctx, workspace)
}

// performDelete runs the actual destructive cleanup for a single workspace
// (git worktree/branch removal + on-disk dir + DB row) and emits the audit +
// "deleted" notification. It assumes the caller already loaded the row and, for
// active workspaces, already stopped any running agent/PTY. Shared by the
// synchronous Delete path and the async BatchDelete goroutine.
func (s *WorkspaceService) performDelete(ctx context.Context, workspace store.Workspace) error {
	workspaceID := workspace.ID

	if workspace.IsArchived == 1 {
		repos, err := s.q.ListWorktrees(ctx, workspaceID)
		if err == nil {
			for _, r := range repos {
				_ = s.q.DeleteWorktree(ctx, r.ID)
			}
		}
		if err := s.q.DeleteWorkspace(ctx, workspaceID); err != nil {
			return err
		}
	} else {
		if err := s.cleanupWorkspace(ctx, workspaceID, workspace.Path); err != nil {
			return err
		}
	}

	appendResourceAudit(ctx, s.db, nil, workspace.OwnerType, workspace.OwnerID, 0, "workspace.deleted", "workspace", workspaceID,
		resourceAuditPayload(workspace.Name))

	s.broadcastWorkspaceAction(workspace.OwnerType, workspace.OwnerID, workspaceID, "deleted")

	return nil
}

// broadcastWorkspaceAction emits a workspace-topic notification (nil-safe when
// no hub is wired, e.g. in tests). The SPA's notification dispatcher invalidates
// the ['workspaces'] query for any workspace event, so "deleting" /
// "delete_failed" / "deleted" all refresh the sidebar + overview lists.
func (s *WorkspaceService) broadcastWorkspaceAction(ownerType string, ownerID, id int64, action string) {
	if s.notifyHub == nil {
		return
	}
	s.notifyHub.Broadcast(notify.Notification{
		Topic:     notify.TopicWorkspace,
		Action:    action,
		ID:        id,
		OwnerType: ownerType,
		OwnerID:   ownerID,
	})
}

// BatchSkippedItem records one workspace that BatchDelete declined to delete,
// with a machine-readable reason the SPA maps to a localized toast.
type BatchSkippedItem struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"` // not_found | has_changes | already_deleting | error
}

// BatchDeleteResult is the synchronous response of BatchDelete. Accepted ids are
// marked 'deleting' and have a background cleanup goroutine running; the actual
// removal completes asynchronously and emits a "deleted" notification per id.
type BatchDeleteResult struct {
	Accepted []int64            `json:"accepted"`
	Skipped  []BatchSkippedItem `json:"skipped"`
}

// BatchDelete marks each workspace 'deleting' and spawns a detached cleanup
// goroutine for it, returning immediately. The "deleting" status is the dedup
// gate: MarkWorkspaceDeleting only flips a row that is not already deleting, so a
// repeated or concurrent batch request that includes an in-flight id is skipped
// (reason "already_deleting") instead of starting a second cleanup.
//
// When force is false, a workspace with uncommitted/unmerged changes is skipped
// (reason "has_changes") rather than destroyed. stop is an optional callback
// (wired by the handler) that terminates the workspace's agent/PTY/proxy session
// before the on-disk cleanup runs; it is invoked inside the goroutine.
func (s *WorkspaceService) BatchDelete(ctx context.Context, ids []int64, force bool, stop func(context.Context, int64)) BatchDeleteResult {
	res := BatchDeleteResult{Accepted: []int64{}, Skipped: []BatchSkippedItem{}}

	for _, id := range ids {
		ws, err := s.q.GetWorkspace(ctx, id)
		if err != nil {
			res.Skipped = append(res.Skipped, BatchSkippedItem{ID: id, Reason: "not_found"})
			continue
		}

		if !force {
			changes, err := s.CheckWorkspaceChanges(ctx, id)
			if err != nil {
				slog.Warn("BatchDelete: change check failed", "id", id, "error", err)
				res.Skipped = append(res.Skipped, BatchSkippedItem{ID: id, Reason: "error"})
				continue
			}
			if len(changes) > 0 {
				res.Skipped = append(res.Skipped, BatchSkippedItem{ID: id, Reason: "has_changes"})
				continue
			}
		}

		// Atomic dedup gate: 0 rows affected => already 'deleting' (another
		// request claimed it), so skip without starting a second goroutine.
		rows, err := s.q.MarkWorkspaceDeleting(ctx, id)
		if err != nil {
			slog.Warn("BatchDelete: mark deleting failed", "id", id, "error", err)
			res.Skipped = append(res.Skipped, BatchSkippedItem{ID: id, Reason: "error"})
			continue
		}
		if rows == 0 {
			res.Skipped = append(res.Skipped, BatchSkippedItem{ID: id, Reason: "already_deleting"})
			continue
		}

		// Surface the "deleting" marker to clients immediately.
		s.broadcastWorkspaceAction(ws.OwnerType, ws.OwnerID, id, "deleting")
		res.Accepted = append(res.Accepted, id)

		// Detach the cleanup from the request context so it survives the HTTP
		// response. ws.Status is captured for rollback if cleanup fails.
		go s.asyncDelete(ws, ws.Status, stop)
	}

	return res
}

// asyncDelete is the background half of BatchDelete: it stops the workspace's
// agent (via stop), runs performDelete, and on failure reverts the 'deleting'
// marker to the prior status (so the workspace isn't stranded and can be
// retried) and emits a "delete_failed" notification.
func (s *WorkspaceService) asyncDelete(workspace store.Workspace, prevStatus string, stop func(context.Context, int64)) {
	ctx := context.Background()
	id := workspace.ID

	if stop != nil {
		stop(ctx, id)
	}

	if err := s.performDelete(ctx, workspace); err != nil {
		slog.Error("BatchDelete: async cleanup failed", "id", id, "error", err)
		if rerr := s.q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{Status: prevStatus, ID: id}); rerr != nil {
			slog.Error("BatchDelete: failed to revert deleting status", "id", id, "error", rerr)
		}
		s.broadcastWorkspaceAction(workspace.OwnerType, workspace.OwnerID, id, "delete_failed")
	}
}

// ErrWorkspaceArchived is returned when an operation is attempted on an archived workspace.
var ErrWorkspaceArchived = errors.New("workspace is archived")

// CheckNotArchived returns ErrWorkspaceArchived if the workspace with the given id is archived.
func (s *WorkspaceService) CheckNotArchived(ctx context.Context, id int64) error {
	ws, err := s.q.GetWorkspace(ctx, id)
	if err != nil {
		return err
	}
	if ws.IsArchived == 1 {
		return ErrWorkspaceArchived
	}
	return nil
}

func (s *WorkspaceService) Archive(ctx context.Context, workspaceID int64) error {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	if ws.IsArchived == 1 {
		return ErrWorkspaceArchived
	}

	repos, err := s.q.ListWorktrees(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, wsRepo := range repos {
		repo, err := s.q.GetRepository(ctx, wsRepo.RepositoryID)
		if err != nil {
			slog.Warn("Archive: error getting repository", "id", wsRepo.RepositoryID, "error", err)
			continue
		}
		if err := git.WorktreeRemove(repo.Path, wsRepo.WorktreePath); err != nil {
			slog.Warn("Archive: error removing worktree", "path", wsRepo.WorktreePath, "error", err)
		}
		if wsRepo.Branch != "" {
			if err := git.DeleteBranch(repo.Path, wsRepo.Branch); err != nil {
				slog.Warn("Archive: error deleting branch", "branch", wsRepo.Branch, "error", err)
			}
		}
	}

	removeDirectoryWithProcessCleanup(ws.Path)

	// Disable all scheduled tasks for this workspace
	if err := s.q.DisableSchedulesByWorkspace(ctx, workspaceID); err != nil {
		slog.Warn("Archive: error disabling schedules", "id", workspaceID, "error", err)
	}

	if err := s.q.ArchiveWorkspace(ctx, workspaceID); err != nil {
		return err
	}

	if s.perm != nil {
		_ = s.perm.CancelByWorkspace(context.Background(), workspaceID, "workspace_archived")
	}
	if s.askUser != nil {
		_ = s.askUser.CancelByWorkspace(context.Background(), workspaceID, "workspace_archived")
	}

	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicWorkspace,
			Action:    "archived",
			ID:        workspaceID,
			OwnerType: ws.OwnerType,
			OwnerID:   ws.OwnerID,
		})
	}

	// Executable Epic: archiving an issue-linked workspace that was NOT completed
	// is the deterministic "this child terminated without success" signal. Emit a
	// workspace_completed{success:false} so the Epic execution engine applies its
	// failure policy (stop/continue/pause) instead of stalling forever. A
	// completed workspace already emitted success=true via MarkWorkspaceDone, so
	// skip it here to avoid flipping a done child to failed.
	if s.eventBus != nil && ws.IssueID.Valid && ws.Status != "completed" {
		payload, err := json.Marshal(event.WorkspaceCompletedEvent{
			WorkspaceID: workspaceID,
			IssueID:     ws.IssueID.Int64,
			Success:     false,
		})
		if err != nil {
			slog.Error("Archive: marshal workspace_completed event", "error", err, "workspaceID", workspaceID)
		} else {
			s.eventBus.Publish(event.OutputEvent{
				Type:        event.EventWorkspaceCompleted,
				Content:     string(payload),
				Role:        "system",
				Ts:          time.Now().UnixMilli(),
				WorkspaceId: workspaceID,
			})
		}
	}
	return nil
}

type ArchivedWorktreeInfo struct {
	RepoName   string
	Branch     string
	BaseBranch string
}

type ArchivedWorkspaceMeta struct {
	Row       store.ListArchivedWorkspacesWithMetaRow
	Worktrees []ArchivedWorktreeInfo
}

func (s *WorkspaceService) ListArchivedWorkspaces(ctx context.Context) ([]ArchivedWorkspaceMeta, error) {
	rows, err := s.q.ListArchivedWorkspacesWithMeta(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]ArchivedWorkspaceMeta, len(rows))
	for i, row := range rows {
		results[i].Row = row

		wts, err := s.q.ListWorktreesWithRepository(ctx, row.ID)
		if err == nil {
			infos := make([]ArchivedWorktreeInfo, len(wts))
			for j, wt := range wts {
				infos[j] = ArchivedWorktreeInfo{
					RepoName:   wt.RName,
					Branch:     wt.Branch,
					BaseBranch: wt.BaseBranch,
				}
			}
			results[i].Worktrees = infos
		}
	}
	return results, nil
}

func (s *WorkspaceService) ListWorktreesWithRepository(ctx context.Context, workspaceID int64) ([]store.ListWorktreesWithRepositoryRow, error) {
	return s.q.ListWorktreesWithRepository(ctx, workspaceID)
}

func (s *WorkspaceService) cleanupWorkspace(ctx context.Context, workspaceID int64, wsPath string) error {
	// Get all workspace repositories
	repos, err := s.q.ListWorktrees(ctx, workspaceID)
	if err != nil {
		return err
	}

	// Remove each worktree
	for _, wsRepo := range repos {
		// Get original repository to find its path
		repo, err := s.q.GetRepository(ctx, wsRepo.RepositoryID)
		if err != nil {
			// Log error but continue
			fmt.Printf("Error getting repository %d: %v\n", wsRepo.RepositoryID, err)
			continue
		}

		// Remove worktree
		if err := git.WorktreeRemove(repo.Path, wsRepo.WorktreePath); err != nil {
			fmt.Printf("Error removing worktree %s: %v\n", wsRepo.WorktreePath, err)
		}

		// Delete the associated branch
		if wsRepo.Branch != "" {
			if err := git.DeleteBranch(repo.Path, wsRepo.Branch); err != nil {
				fmt.Printf("Error deleting branch %s: %v\n", wsRepo.Branch, err)
			}
		}

		// Delete workspace_repository record
		if err := s.q.DeleteWorktree(ctx, wsRepo.ID); err != nil {
			fmt.Printf("Error deleting workspace repo record %d: %v\n", wsRepo.ID, err)
		}
	}

	// Kill any processes (e.g. dev servers) running inside the workspace directory
	// and remove the directory. On Windows, native modules (.node files) lock files
	// and prevent deletion until the process exits.
	removeDirectoryWithProcessCleanup(wsPath)

	// Delete workspace record
	return s.q.DeleteWorkspace(ctx, workspaceID)
}

func sanitizePath(s string) string {
	invalid := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	result := s
	for _, char := range invalid {
		result = strings.ReplaceAll(result, char, "-")
	}
	return result
}

type TreeNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"is_dir"`
	Children []TreeNode `json:"children,omitempty"`
}

func (s *WorkspaceService) GetTree(ctx context.Context, workspaceID int64) (TreeNode, error) {
	// Get workspace
	workspace, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return TreeNode{}, err
	}

	// Build tree from workspace directory
	root := TreeNode{
		Name:  filepath.Base(workspace.Path),
		Path:  "/",
		IsDir: true,
	}

	// nodeMap tracks directory paths to their TreeNode pointers for nesting
	nodeMap := map[string]*TreeNode{"/": &root}

	err = filepath.WalkDir(workspace.Path, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip root directory itself and hidden directories (.git, etc.)
		if path == workspace.Path {
			return nil
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return nil
		}

		// Build relative path
		relPath, err := filepath.Rel(workspace.Path, path)
		if err != nil {
			return err
		}

		treePath := "/" + filepath.ToSlash(relPath)
		parentPath := "/" + filepath.ToSlash(filepath.Dir(relPath))
		if parentPath == "/." {
			parentPath = "/"
		}

		node := TreeNode{
			Name:  d.Name(),
			Path:  treePath,
			IsDir: d.IsDir(),
		}

		// Find parent and append
		parent, ok := nodeMap[parentPath]
		if !ok {
			parent = &root
		}
		parent.Children = append(parent.Children, node)

		// Register directory in nodeMap for future children
		if d.IsDir() {
			// Get pointer to the node we just appended
			nodeMap[treePath] = &parent.Children[len(parent.Children)-1]
		}

		return nil
	})

	if err != nil {
		return TreeNode{}, fmt.Errorf("walk workspace directory: %w", err)
	}

	return root, nil
}

func (s *WorkspaceService) ListAllRepositories(ctx context.Context) ([]store.Repository, error) {
	return s.q.ListRepositories(ctx)
}

// GetMainTree returns the main workspace tree (excluding worktrees/)
func (s *WorkspaceService) GetMainTree(ctx context.Context, workspaceID int64, subPath string) (*TreeResult, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	dirPath := ws.Path
	if subPath != "" {
		dirPath = filepath.Join(dirPath, subPath)
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	items := make([]TreeItem, 0, len(entries))
	for _, entry := range entries {
		// The worktrees directory (.worktrees/<name>/...) is intentionally
		// included so the file tree exposes each repo's working-tree files —
		// e.g. agent-authored diagrams under `.worktrees/<name>/diagrams/`.
		info, _ := entry.Info()
		itemPath := entry.Name()
		if subPath != "" {
			itemPath = subPath + "/" + entry.Name()
		}
		items = append(items, TreeItem{
			Name: entry.Name(),
			Path: itemPath,
			Type: map[bool]string{true: "dir", false: "file"}[entry.IsDir()],
			Size: info.Size(),
		})
	}

	return &TreeResult{Path: dirPath, Items: items}, nil
}

// GetWorktreeTree returns the tree for a specific worktree group
func (s *WorkspaceService) GetWorktreeTree(ctx context.Context, workspaceID int64, worktreeName, subPath string) (*TreeResult, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	worktreePath := filepath.Join(ws.Path, ".worktrees", worktreeName)
	if subPath != "" {
		worktreePath = filepath.Join(worktreePath, subPath)
	}

	return buildTree(worktreePath)
}

// maxWorktreeFileBytes caps the size of a single embedded-canvas source write
// (e.g. a `.excalidraw` scene). Generous headroom over any realistic scene,
// while refusing pathological payloads.
const maxWorktreeFileBytes = 10 << 20 // 10 MB

// WriteWorktreeFile writes text content to a file inside a worktree so it
// becomes visible and diffable in the Git/Changes panel. relPath is relative to
// the worktree root; parent directories are created as needed. Returns the
// workspace-root-relative path of the written file (e.g.
// ".worktrees/<name>/docs/foo.excalidraw"), matching ChatAttachment.path
// semantics. Path traversal and writes into `.git/` are rejected.
func (s *WorkspaceService) WriteWorktreeFile(ctx context.Context, workspaceID int64, worktreeName, relPath, content string) (string, error) {
	if len(content) > maxWorktreeFileBytes {
		return "", fmt.Errorf("file too large (max %d bytes)", maxWorktreeFileBytes)
	}

	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return "", err
	}

	// Collapse the worktree name to a single path segment — it must never
	// escape the .worktrees directory.
	worktreeName = filepath.Base(filepath.Clean(worktreeName))
	if worktreeName == "" || worktreeName == "." || worktreeName == string(filepath.Separator) {
		return "", fmt.Errorf("invalid worktree name")
	}
	worktreeRoot := filepath.Join(ws.Path, ".worktrees", worktreeName)
	if info, err := os.Stat(worktreeRoot); err != nil || !info.IsDir() {
		return "", fmt.Errorf("worktree not found: %s", worktreeName)
	}

	// Anchor at the worktree root and collapse traversal. stdpath.Clean keeps
	// forward slashes (as sent by the web client) unlike filepath.Clean on
	// Windows.
	rel := strings.TrimPrefix(stdpath.Clean("/"+strings.ReplaceAll(relPath, "\\", "/")), "/")
	if rel == "" || rel == "." {
		return "", fmt.Errorf("invalid path")
	}
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return "", fmt.Errorf("writing into .git is not allowed")
	}

	fullPath := filepath.Join(worktreeRoot, filepath.FromSlash(rel))
	// Defence in depth: verify the resolved path stays within the worktree.
	if within, err := filepath.Rel(worktreeRoot, fullPath); err != nil ||
		within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid path")
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return stdpath.Join(".worktrees", worktreeName, rel), nil
}

// ListWorktreeGroups returns the list of worktree groups with their names and paths
func (s *WorkspaceService) ListWorktreeGroups(ctx context.Context, workspaceID int64) ([]WorktreeGroup, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	worktreesPath := filepath.Join(ws.Path, ".worktrees")
	entries, err := os.ReadDir(worktreesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []WorktreeGroup{}, nil
		}
		return nil, err
	}

	groups := make([]WorktreeGroup, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			groups = append(groups, WorktreeGroup{
				Name: entry.Name(),
				Path: filepath.Join(worktreesPath, entry.Name()),
			})
		}
	}
	return groups, nil
}

// GetWorktreeGitStatus returns the git status for a worktree by workspace ID and worktree name
func (s *WorkspaceService) GetWorktreeGitStatus(ctx context.Context, workspaceID int64, worktreeName string) (*GitStatusResult, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("workspace not found: %w", err)
	}

	worktreePath := filepath.Join(ws.Path, ".worktrees", worktreeName)
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("worktree not found: %s", worktreePath)
	}

	statuses, err := git.Status(worktreePath)
	if err != nil {
		// Distinguish "structurally broken worktree" (dangling gitlink — the
		// parent repo was moved or deleted) from transient/IO failures
		// (index.lock contention, ENOENT mid-fetch, missing git binary).
		// Only the gitlink class becomes a successful broken result;
		// everything else surfaces as a 5xx so we don't permanently mask
		// flaky failures behind a misleading "broken" badge. Detection reads
		// the worktree's `.git` gitlink file directly because git.Status
		// uses cmd.Output() and discards stderr, so we cannot rely on the
		// stderr text in err here.
		if hasDanglingGitlink(worktreePath) {
			return &GitStatusResult{
				Modified:  []string{},
				Added:     []string{},
				Deleted:   []string{},
				Untracked: []string{},
				Broken:    true,
				Reason:    "worktree gitlink could not be resolved",
			}, nil
		}
		return nil, fmt.Errorf("git status: %w", err)
	}

	result := &GitStatusResult{
		Modified:  []string{},
		Added:     []string{},
		Deleted:   []string{},
		Untracked: []string{},
	}

	for _, f := range statuses {
		switch f.Status {
		case "??":
			result.Untracked = append(result.Untracked, f.Path)
		case "A", "AM":
			result.Added = append(result.Added, f.Path)
		case "D":
			result.Deleted = append(result.Deleted, f.Path)
		default: // M, MM, T, R, etc.
			result.Modified = append(result.Modified, f.Path)
		}
	}

	return result, nil
}

// GetWorktreeCommits returns commits not yet merged into the base branch.
func (s *WorkspaceService) GetWorktreeCommits(ctx context.Context, workspaceID int64, worktreeName string) ([]git.LogEntry, error) {
	rows, err := s.q.ListWorktreesWithRepository(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}

	// Find the matching worktree row by directory name
	var worktreePath, baseBranch string
	for _, row := range rows {
		if filepath.Base(row.WorktreePath) == worktreeName {
			worktreePath = row.WorktreePath
			baseBranch = row.BaseBranch
			if baseBranch == "" && row.RDefaultBranch.Valid {
				baseBranch = row.RDefaultBranch.String
			}
			break
		}
	}
	if worktreePath == "" {
		return nil, fmt.Errorf("worktree not found: %s", worktreeName)
	}

	if baseBranch == "" {
		// No base branch to compare — return empty
		return []git.LogEntry{}, nil
	}

	return git.LogRange(worktreePath, baseBranch+"..HEAD")
}

// GetWorktreeCommitDetail returns detail info for a single commit in a worktree.
func (s *WorkspaceService) GetWorktreeCommitDetail(ctx context.Context, workspaceID int64, worktreeName, commitHash string) (*git.CommitDetail, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("workspace not found: %w", err)
	}

	worktreePath := filepath.Join(ws.Path, ".worktrees", worktreeName)
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("worktree not found: %s", worktreePath)
	}

	return git.GetCommitDetail(worktreePath, commitHash)
}

// WorkspaceRepoWithDetails holds a worktree with its repository details
type WorkspaceRepoWithDetails struct {
	ID           int64  `json:"id"`
	WorkspaceID  int64  `json:"workspace_id"`
	RepositoryID int64  `json:"repository_id"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	BaseBranch   string `json:"base_branch"`
	CreatedAt    string `json:"created_at"`
	Repository   *struct {
		ID            int64  `json:"id"`
		Name          string `json:"name"`
		Path          string `json:"path"`
		GitRemote     string `json:"git_remote,omitempty"`
		DefaultBranch string `json:"default_branch,omitempty"`
	} `json:"repository,omitempty"`
}

// ListWorkspaceRepositories returns all repositories bound to a workspace
func (s *WorkspaceService) ListWorkspaceRepositories(ctx context.Context, workspaceID int64) ([]WorkspaceRepoWithDetails, error) {
	rows, err := s.q.ListWorktreesWithRepository(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	result := make([]WorkspaceRepoWithDetails, len(rows))
	for i, row := range rows {
		repoGitRemote := ""
		if row.RGitRemote.Valid {
			repoGitRemote = row.RGitRemote.String
		}
		repoDefaultBranch := ""
		if row.RDefaultBranch.Valid {
			repoDefaultBranch = row.RDefaultBranch.String
		}
		result[i] = WorkspaceRepoWithDetails{
			ID:           row.ID,
			WorkspaceID:  row.WorkspaceID,
			RepositoryID: row.RepositoryID,
			WorktreePath: row.WorktreePath,
			Branch:       row.Branch,
			BaseBranch:   row.BaseBranch,
			CreatedAt:    row.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			Repository: &struct {
				ID            int64  `json:"id"`
				Name          string `json:"name"`
				Path          string `json:"path"`
				GitRemote     string `json:"git_remote,omitempty"`
				DefaultBranch string `json:"default_branch,omitempty"`
			}{
				ID:            row.RID,
				Name:          row.RName,
				Path:          row.RPath,
				GitRemote:     repoGitRemote,
				DefaultBranch: repoDefaultBranch,
			},
		}
	}
	return result, nil
}

// GetWorkspaceEnv returns all env vars for a workspace
func (s *WorkspaceService) GetWorkspaceEnv(ctx context.Context, workspaceID int64) (map[string]string, error) {
	envs, err := sceneenv.Resolve(ctx, s.q, workspaceID)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, e := range envs {
		result[e.Key] = e.Value
	}
	return result, nil
}

// SetWorkspaceEnv sets an env var for a workspace
func (s *WorkspaceService) SetWorkspaceEnv(ctx context.Context, workspaceID int64, key, value string) error {
	if err := s.q.SetWorkspaceEnv(ctx, store.SetWorkspaceEnvParams{
		WorkspaceID: workspaceID,
		Key:         key,
		Value:       value,
	}); err != nil {
		return err
	}
	if key == permissionModeEnvKey {
		s.reprojectOnPermissionModeChange(ctx, workspaceID)
	}
	return nil
}

// permissionModeEnvKey is the workspace env that selects the agent permission
// mode (autohost / default / plan / acceptEdits). Office-mail gates write tools
// on it (autohost = read-only), so a change must re-project the workspace.
const permissionModeEnvKey = "NIUNIU_PERMISSION_MODE"

// reprojectOnPermissionModeChange re-runs scene projection so office-mail's
// write gating (autohost → read-only) tracks the new permission mode. The digest
// shifts (the email server's NIUNIU_OFFICE_MAIL_WRITE marker), so the restart
// banner prompts the user to restart the agent. Best-effort.
func (s *WorkspaceService) reprojectOnPermissionModeChange(ctx context.Context, workspaceID int64) {
	if s.sceneProj == nil {
		return
	}
	if _, err := s.sceneProj.Apply(ctx, workspaceID); err != nil {
		slog.Warn("workspace: reproject after permission-mode change failed",
			"workspace_id", workspaceID, "error", err)
	}
}

// SetWorkspaceEnvVars replaces all env vars for a workspace
func (s *WorkspaceService) SetWorkspaceEnvVars(ctx context.Context, workspaceID int64, envs map[string]string) error {
	// Delete all existing
	if err := s.q.DeleteAllWorkspaceEnv(ctx, workspaceID); err != nil {
		return err
	}
	// Insert new ones
	for key, value := range envs {
		if err := s.q.SetWorkspaceEnv(ctx, store.SetWorkspaceEnvParams{
			WorkspaceID: workspaceID,
			Key:         key,
			Value:       value,
		}); err != nil {
			return err
		}
	}
	// The permission mode (autohost/interactive) gates office-mail write tools;
	// re-project when it's part of this update so the change takes effect.
	if _, ok := envs[permissionModeEnvKey]; ok {
		s.reprojectOnPermissionModeChange(ctx, workspaceID)
	}
	return nil
}

// SetEnvProvider binds (or unbinds, when providerID=0) a subscription-platform
// provider directly to the workspace. At spawn, sceneenv.Resolve expands the
// bound provider per the workspace's cli_type — the common "this workspace uses
// DeepSeek" path that needs no scene.
func (s *WorkspaceService) SetEnvProvider(ctx context.Context, workspaceID, providerID int64) error {
	var v sql.NullInt64
	if providerID > 0 {
		v = sql.NullInt64{Int64: providerID, Valid: true}
	}
	return s.q.SetWorkspaceEnvProvider(ctx, store.SetWorkspaceEnvProviderParams{
		ID:            workspaceID,
		EnvProviderID: v,
	})
}

// WorktreeChangesSummary holds per-worktree change counts
type WorktreeChangesSummary struct {
	Name      string `json:"name"`
	Modified  int    `json:"modified"`
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Untracked int    `json:"untracked"`
}

// ChangesSummary aggregates git status across all worktrees in a workspace
type ChangesSummary struct {
	TotalFiles  int                      `json:"total_files"`
	PerWorktree []WorktreeChangesSummary `json:"per_worktree"`
}

// GetChangesSummary aggregates git status across all worktrees for a workspace
func (s *WorkspaceService) GetChangesSummary(ctx context.Context, workspaceID int64) (*ChangesSummary, error) {
	groups, err := s.ListWorktreeGroups(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	summary := &ChangesSummary{
		PerWorktree: []WorktreeChangesSummary{},
	}

	for _, group := range groups {
		status, err := s.GetWorktreeGitStatus(ctx, workspaceID, group.Name)
		if err != nil {
			// Skip worktrees that fail; don't abort entire summary
			continue
		}
		wt := WorktreeChangesSummary{
			Name:      group.Name,
			Modified:  len(status.Modified),
			Added:     len(status.Added),
			Deleted:   len(status.Deleted),
			Untracked: len(status.Untracked),
		}
		summary.PerWorktree = append(summary.PerWorktree, wt)
		summary.TotalFiles += wt.Modified + wt.Added + wt.Deleted + wt.Untracked
	}

	return summary, nil
}

// AddRepositoryToWorkspace adds a repository to a workspace by creating a git worktree
func (s *WorkspaceService) AddRepositoryToWorkspace(ctx context.Context, workspaceID, repoID int64, branch string) (*store.Worktree, error) {
	// Get workspace
	workspace, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("workspace not found: %w", err)
	}

	// Get repo
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("repository not found: %w", err)
	}

	branch, err = resolveBaseBranch(repo, branch)
	if err != nil {
		return nil, fmt.Errorf("resolve base branch: %w", err)
	}
	worktreePath := GenerateWorktreePath(workspace.Path, repoID, repo.Name, branch)
	wtBranch := GenerateWorktreeBranch(workspaceID, branch)

	// Create git worktree forked from the user-specified base branch.
	if err := git.WorktreeAdd(repo.Path, worktreePath, wtBranch, branch); err != nil {
		return nil, fmt.Errorf("git worktree add: %w", err)
	}

	// Create DB record
	wsRepo, err := s.q.CreateWorktree(ctx, store.CreateWorktreeParams{
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
		WorktreePath: worktreePath,
		Branch:       wtBranch,
		BaseBranch:   branch,
	})
	if err != nil {
		git.WorktreeRemove(repo.Path, worktreePath)
		return nil, fmt.Errorf("create worktree record: %w", err)
	}

	return &wsRepo, nil
}

// pickBgHighlight selects which task to surface in the sidebar highlight slot.
// Partition rules:
//  1. Non-wakeup tasks (bash + subagent) compete on StartedAt; newest wins.
//  2. Wakeups compete on ScheduledFor; soonest wins.
//  3. Non-wakeup tasks ALWAYS beat wakeups (so an active bash isn't hidden
//     behind a queued wakeup). Returns nil if there are no tasks.
func pickBgHighlight(tasks []agentproxy.BgTask) *BgTaskHighlightMeta {
	var newestRunning *agentproxy.BgTask
	var soonestWakeup *agentproxy.BgTask
	for i := range tasks {
		t := &tasks[i]
		switch t.Kind {
		case agentproxy.BgTaskBash, agentproxy.BgTaskSubagent:
			if newestRunning == nil || t.StartedAt.After(newestRunning.StartedAt) {
				newestRunning = t
			}
		case agentproxy.BgTaskWakeup:
			if soonestWakeup == nil || t.ScheduledFor.Before(soonestWakeup.ScheduledFor) {
				soonestWakeup = t
			}
		}
	}
	switch {
	case newestRunning != nil:
		return &BgTaskHighlightMeta{
			Kind:      string(newestRunning.Kind),
			Title:     newestRunning.Title,
			StartedAt: newestRunning.StartedAt,
		}
	case soonestWakeup != nil:
		return &BgTaskHighlightMeta{
			Kind:         string(soonestWakeup.Kind),
			Title:        soonestWakeup.Title,
			ScheduledFor: soonestWakeup.ScheduledFor,
		}
	}
	return nil
}

// fillBgTaskMeta populates the Bg* fields on meta from the workspace's live
// agentproxy session (if any). Highlight selection is delegated to
// pickBgHighlight which partitions by kind so we never compare past-time
// StartedAt against future-time ScheduledFor.
func fillBgTaskMeta(meta *WorkspaceSidebarMeta, ap *agentproxy.AgentProxy) {
	sess := ap.GetSession(meta.Workspace.ID)
	if sess == nil {
		return
	}
	// Note: Status() and BgTasks() take separate locks. The agent could
	// finish between the two reads, yielding e.g. BgAgentBusy=true with an
	// empty task list. Acceptable for sidebar display — the next list query
	// resolves it. Don't add cross-lock coordination here without measuring.
	meta.BgAgentBusy = sess.Status() == agentproxy.StatusRunning
	tasks := sess.BgTasks()

	for _, t := range tasks {
		switch t.Kind {
		case agentproxy.BgTaskBash:
			meta.BgBashCount++
		case agentproxy.BgTaskSubagent:
			meta.BgSubagentCount++
		case agentproxy.BgTaskWakeup:
			meta.BgWakeupCount++
		}
	}
	meta.BgHighlight = pickBgHighlight(tasks)
}

// IssueDefaultRepo is one entry returned by GetIssueDefaultRepos: the
// repository plus the branch list and the recommended branch to pre-select.
type IssueDefaultRepo struct {
	Repository      store.Repository `json:"repository"`
	Branches        []string         `json:"branches"`
	PreferredBranch string           `json:"preferred_branch"`
}

// GetIssueDefaultRepos returns all repositories linked to the project that
// owns the given issue's column, each with its branch list and a recommended
// preselect branch. The preferred branch favors the project-level override
// (project_repositories.default_branch) over the repo's own default; both
// then go through pickBranch to validate against the live branch list.
// Per-repo branch fetches are best-effort: a repo whose GetBranchInfo errors
// is dropped from the result and logged.
// GetIssueDefaultCliType returns the default agent CLI for an issue's project,
// for pre-selecting the agent in the issue-panel workspace-create UI. Falls
// back to "claude" when the issue/project can't be resolved.
func (s *WorkspaceService) GetIssueDefaultCliType(ctx context.Context, issueID int64) string {
	row, err := s.q.GetProjectAndLifecycleByIssueID(ctx, issueID)
	if err != nil {
		return "claude"
	}
	if _, ok := ValidCliTypes[row.ProjectDefaultCliType]; !ok || row.ProjectDefaultCliType == "" {
		return "claude"
	}
	return row.ProjectDefaultCliType
}

func (s *WorkspaceService) GetIssueDefaultRepos(ctx context.Context, issueID int64) ([]IssueDefaultRepo, error) {
	// Resolve issue → column → project_id. Mirrors KanbanHandler.GetIssue's
	// project_id resolution path (kanban.go:519). We deliberately reuse the
	// existing ListProjectRepositories query (which returns project_default_branch
	// alongside repo fields) rather than maintain a parallel one.
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		// Issue doesn't exist — treat as "no defaults" (handler authz layer
		// already 404's before reaching here, but be defensive).
		return []IssueDefaultRepo{}, nil
	}
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return []IssueDefaultRepo{}, nil
	}
	rows, err := s.q.ListProjectRepositories(ctx, col.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []IssueDefaultRepo{}, nil
	}
	if s.repoSvc == nil {
		slog.Warn("issue-defaults: repoSvc not wired, returning empty result", "issue_id", issueID)
		return []IssueDefaultRepo{}, nil
	}

	out := make([]IssueDefaultRepo, len(rows))
	var wg sync.WaitGroup
	for i, row := range rows {
		wg.Add(1)
		go func(i int, row store.ListProjectRepositoriesRow) {
			defer wg.Done()
			info, err := s.repoSvc.GetBranchInfo(ctx, strconv.FormatInt(row.ID, 10))
			if err != nil {
				slog.Warn("issue-defaults: skip repo",
					"repo_id", row.ID, "issue_id", issueID, "err", err)
				return // out[i] stays zero value, filtered below
			}
			// Project-level override (pr.default_branch) wins when set.
			// Empty string means "not configured" — fall back to repo's own
			// default. pickBranch then validates against the live branches.
			preferred := row.RepoDefaultBranch.String
			if row.ProjectDefaultBranch != "" {
				preferred = row.ProjectDefaultBranch
			}
			out[i] = IssueDefaultRepo{
				Repository: store.Repository{
					ID:            row.ID,
					Name:          row.Name,
					Path:          row.Path,
					GitRemote:     row.GitRemote,
					DefaultBranch: row.RepoDefaultBranch,
					OwnerType:     row.OwnerType,
					OwnerID:       row.OwnerID,
					CreatedAt:     row.CreatedAt,
					UpdatedAt:     row.UpdatedAt,
				},
				Branches:        info.Branches,
				PreferredBranch: pickBranch(preferred, info.Branches),
			}
		}(i, row)
	}
	wg.Wait()

	filtered := out[:0]
	for _, r := range out {
		if r.Repository.ID != 0 {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

// pickBranch returns defaultBranch if it appears in branches; otherwise the
// first branch; otherwise the defaultBranch unchanged (possibly "").
func pickBranch(defaultBranch string, branches []string) string {
	for _, b := range branches {
		if b == defaultBranch {
			return defaultBranch
		}
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return defaultBranch
}

// AlertableUserIDs returns the deduplicated, NULL-dropped union of:
//   - workspaces.created_by (if non-NULL)
//   - issue_assignees.user_id where issue_id = workspaces.issue_id
//
// Used by emit sites of workspace-targeted toast events to populate
// extra.should_alert_user_ids. Returns []int64{} (not nil) on no match
// so JSON-encoded events serialize as [] rather than null.
func (s *WorkspaceService) AlertableUserIDs(ctx context.Context, workspaceID int64) ([]int64, error) {
	// Skip the DB round-trip for trivial inputs; emit-site callers may pass 0
	// when no workspace context is available, and an empty result is the correct
	// answer either way.
	if workspaceID <= 0 {
		return []int64{}, nil
	}
	rows, err := s.q.GetWorkspaceAlertableUserIDs(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		if r.Valid {
			out = append(out, r.Int64)
		}
	}
	return out, nil
}
