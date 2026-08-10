package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ErrInvalidResourceType is returned by DeleteUserResource when resType is not
// one of project|workspace|repository. Handlers map it to HTTP 400.
var ErrInvalidResourceType = errors.New("invalid resource type: must be project, workspace or repository")

// PurgeGuardError is returned by PurgeUser when a safety guard blocks the purge.
// Reason is a stable, machine-readable token the HTTP handler maps to 409:
//   - "self"                       — an admin tried to purge their own account
//   - "last_admin"                 — the target is the only remaining admin
//   - "last_owner_of_org:<slug>"   — the target is the sole owner of that org
type PurgeGuardError struct {
	Reason string
}

func (e *PurgeGuardError) Error() string { return "purge blocked: " + e.Reason }

// purgeOwnedTablesDeleteOrder lists the personal-scoped top-level tables cleared
// in the purge transaction, in dependency-safe order. workspaces MUST come before
// repositories: worktrees.repository_id has no ON DELETE CASCADE, so the child
// worktree rows (which DO cascade from workspaces) have to be gone first.
// projects cascades its columns/issues and project_repositories rows.
//
// harness_specs is intentionally absent: it is a single GLOBAL library with no
// owner_type/owner_id columns (see store/owner_schema.go), so it is never
// personally owned and querying it by owner would fail with "no such column".
//
// This list mirrors store.topLevelOwnedTables (the authoritative owner-model
// list) minus the global harness_specs. The data_sources / saved_queries /
// dashboards / knowledge_bases rows each ON DELETE CASCADE their children, and
// none of them RESTRICT-references another, so ordering among them is free.
var purgeOwnedTablesDeleteOrder = []string{
	"workspaces",
	"projects",
	"repositories",
	"env_presets",
	"quick_actions",
	"agents",
	"scenes",
	"data_sources",
	"saved_queries",
	"dashboards",
	"knowledge_bases",
}

// UserOrgMembership is one org the user belongs to, with the sole-owner flag the
// SPA uses to warn that purging the user would strand the org.
type UserOrgMembership struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	IsLastOwner bool   `json:"is_last_owner"`
}

// ResourceCounts holds the lightweight owner-scoped table counts. harness_specs
// is always 0 (global library — see purgeOwnedTablesDeleteOrder note) but is
// carried for API-shape compatibility with the spec DTO.
type ResourceCounts struct {
	EnvPresets     int64 `json:"env_presets"`
	QuickActions   int64 `json:"quick_actions"`
	Agents         int64 `json:"agents"`
	Scenes         int64 `json:"scenes"`
	DataSources    int64 `json:"data_sources"`
	SavedQueries   int64 `json:"saved_queries"`
	Dashboards     int64 `json:"dashboards"`
	KnowledgeBases int64 `json:"knowledge_bases"`
	HarnessSpecs   int64 `json:"harness_specs"`
}

// UserResourceSummary is the payload for ListUserResources.
type UserResourceSummary struct {
	User         store.User
	Orgs         []UserOrgMembership
	Projects     []store.Project
	Workspaces   []store.Workspace
	Repositories []store.Repository
	Counts       ResourceCounts
	// WorkspaceProjectIDs maps workspace ID -> its project ID (resolved via the
	// linked issue's column). Only issue-linked workspaces appear; the DTO layer
	// renders a null project_id for the rest.
	WorkspaceProjectIDs map[int64]int64
}

// PurgeSummary reports how much was deleted by PurgeUser (pre-delete counts).
type PurgeSummary struct {
	Projects       int64 `json:"projects"`
	Workspaces     int64 `json:"workspaces"`
	Repositories   int64 `json:"repositories"`
	EnvPresets     int64 `json:"env_presets"`
	QuickActions   int64 `json:"quick_actions"`
	Agents         int64 `json:"agents"`
	Scenes         int64 `json:"scenes"`
	DataSources    int64 `json:"data_sources"`
	SavedQueries   int64 `json:"saved_queries"`
	Dashboards     int64 `json:"dashboards"`
	KnowledgeBases int64 `json:"knowledge_bases"`
	HarnessSpecs   int64 `json:"harness_specs"`
	OrgsLeft       int64 `json:"orgs_left"`
}

// AdminUserService implements the admin "view / delete a user's personal
// resources + one-click purge account" flows. It composes the existing
// per-resource services (so heavyweight deletes reuse their git/worktree/dir
// cleanup) plus OrgService (for member-removal cleanup) and Authz.
//
// It is assembled in server.go (not via a service-package constructor chain) to
// avoid an import cycle. agentMgr / notifyHub / sseHub are optional and wired
// after construction, mirroring OrgService.
type AdminUserService struct {
	q            *store.Queries
	db           *store.DB
	dataDir      string
	projectSvc   *ProjectService
	workspaceSvc *WorkspaceService
	repoSvc      *RepositoryService
	orgSvc       *OrgService
	authz        *Authz

	agentMgr  agentStopper     // may be nil; wired via SetAgentManager
	notifyHub orgNotifyHub     // may be nil; wired via SetNotifyHub
	sseHub    userDisconnector // may be nil; wired via SetSSEHub
}

// NewAdminUserService constructs the service. db is the raw *sql.DB; it is
// wrapped so raw statements get driver-aware placeholder rewriting.
func NewAdminUserService(
	q *store.Queries,
	db *sql.DB,
	dataDir string,
	projectSvc *ProjectService,
	workspaceSvc *WorkspaceService,
	repoSvc *RepositoryService,
	orgSvc *OrgService,
	authz *Authz,
) *AdminUserService {
	return &AdminUserService{
		q:            q,
		db:           store.Wrap(db),
		dataDir:      dataDir,
		projectSvc:   projectSvc,
		workspaceSvc: workspaceSvc,
		repoSvc:      repoSvc,
		orgSvc:       orgSvc,
		authz:        authz,
	}
}

// SetAgentManager wires the AgentManager so purge can terminate the user's PTY
// sessions before cleanup. Optional; nil-safe.
func (s *AdminUserService) SetAgentManager(mgr agentStopper) { s.agentMgr = mgr }

// SetNotifyHub wires the WS notification hub so purge can close the user's
// active notify streams. Optional; nil-safe.
func (s *AdminUserService) SetNotifyHub(hub orgNotifyHub) { s.notifyHub = hub }

// SetSSEHub wires the SSE hub so purge can close the user's active SSE streams.
// Optional; nil-safe.
func (s *AdminUserService) SetSSEHub(hub userDisconnector) { s.sseHub = hub }

// ListUserResources enumerates a user's personal (owner_type='user') resources.
// It bypasses the caller-facing Accessible gate and queries strictly by owner,
// which is correct for an admin-only endpoint.
func (s *AdminUserService) ListUserResources(ctx context.Context, userID int64) (*UserResourceSummary, error) {
	user, err := s.q.GetUserByID(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// Single-owner filter: personal space only. OrgIds is the "no orgs" sentinel
	// so the owner-filter queries never fold in any org-owned rows.
	noOrgs := []int64{-1}
	projects, err := s.q.ListProjectsForOwners(ctx, store.ListProjectsForOwnersParams{
		OwnerID: userID, OrgIds: noOrgs,
	})
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	workspaces, err := s.q.ListWorkspacesForOwners(ctx, store.ListWorkspacesForOwnersParams{
		OwnerID: userID, OrgIds: noOrgs, Column3: int64(0),
	})
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	repos, err := s.q.ListRepositoriesForOwners(ctx, store.ListRepositoriesForOwnersParams{
		OwnerID: userID, OrgIds: noOrgs,
	})
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}

	counts := ResourceCounts{}
	if counts.EnvPresets, err = s.countOwned(ctx, "env_presets", userID); err != nil {
		return nil, err
	}
	if counts.QuickActions, err = s.countOwned(ctx, "quick_actions", userID); err != nil {
		return nil, err
	}
	if counts.Agents, err = s.countOwned(ctx, "agents", userID); err != nil {
		return nil, err
	}
	if counts.Scenes, err = s.countOwned(ctx, "scenes", userID); err != nil {
		return nil, err
	}
	if counts.DataSources, err = s.countOwned(ctx, "data_sources", userID); err != nil {
		return nil, err
	}
	if counts.SavedQueries, err = s.countOwned(ctx, "saved_queries", userID); err != nil {
		return nil, err
	}
	if counts.Dashboards, err = s.countOwned(ctx, "dashboards", userID); err != nil {
		return nil, err
	}
	if counts.KnowledgeBases, err = s.countOwned(ctx, "knowledge_bases", userID); err != nil {
		return nil, err
	}
	// harness_specs stays 0: global library, no owner columns.

	orgs, err := s.userOrgs(ctx, userID)
	if err != nil {
		return nil, err
	}

	return &UserResourceSummary{
		User:                user,
		Orgs:                orgs,
		Projects:            projects,
		Workspaces:          workspaces,
		Repositories:        repos,
		Counts:              counts,
		WorkspaceProjectIDs: s.workspaceProjectIDs(ctx, userID),
	}, nil
}

// workspaceProjectIDs resolves workspace_id -> project_id via the issue -> column
// path for the user's issue-linked workspaces, in one batched query. Best-effort:
// a query error yields an empty map (workspaces then report a null project_id).
func (s *AdminUserService) workspaceProjectIDs(ctx context.Context, userID int64) map[int64]int64 {
	out := map[int64]int64{}
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.id, c.project_id
		FROM workspaces w
		JOIN issues i ON i.id = w.issue_id
		JOIN columns c ON c.id = i.column_id
		WHERE w.owner_type = 'user' AND w.owner_id = ?`, userID)
	if err != nil {
		slog.Warn("ListUserResources: workspaceProjectIDs query failed", "user_id", userID, "error", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var wsID, projID int64
		if err := rows.Scan(&wsID, &projID); err == nil {
			out[wsID] = projID
		}
	}
	return out
}

// userOrgs returns the user's org memberships with the last-owner flag computed
// per org.
func (s *AdminUserService) userOrgs(ctx context.Context, userID int64) ([]UserOrgMembership, error) {
	rows, err := s.q.ListOrganizationsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list user orgs: %w", err)
	}
	out := make([]UserOrgMembership, 0, len(rows))
	for _, r := range rows {
		m := UserOrgMembership{ID: r.ID, Slug: r.Slug, Name: r.Name, Role: r.Role}
		if r.Role == "owner" {
			n, err := s.q.CountOrgOwners(ctx, r.ID)
			if err != nil {
				return nil, fmt.Errorf("count owners for org %d: %w", r.ID, err)
			}
			m.IsLastOwner = n <= 1
		}
		out = append(out, m)
	}
	return out, nil
}

// countOwned returns the number of personal-scoped rows in table for userID. The
// table name comes from a fixed internal list (never user input), and the
// placeholder anchors to the typed owner_id column so it is dual-driver safe.
func (s *AdminUserService) countOwned(ctx context.Context, table string, userID int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+table+` WHERE owner_type = 'user' AND owner_id = ?`, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

// DeleteUserResource deletes a single personal resource of the given user. It
// first verifies the resource is owned by (user, userID) before delegating to
// the matching per-resource service (which performs git/worktree/dir cleanup).
// For repositories it also removes the on-disk directory, matching the existing
// api/repository.go delete-with-directory handler.
func (s *AdminUserService) DeleteUserResource(ctx context.Context, actorID, userID int64, resType string, resourceID int64) error {
	var table string
	switch resType {
	case "project":
		table = "projects"
	case "workspace":
		table = "workspaces"
	case "repository":
		table = "repositories"
	default:
		return ErrInvalidResourceType
	}

	var ownerType string
	var ownerID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT owner_type, owner_id FROM `+table+` WHERE id = ?`, resourceID).Scan(&ownerType, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if ownerType != "user" || ownerID != userID {
		// The resource exists but is not this user's personal resource.
		return ErrForbidden
	}

	switch resType {
	case "project":
		return s.projectSvc.Delete(ctx, resourceID)
	case "workspace":
		return s.workspaceSvc.Delete(ctx, resourceID)
	case "repository":
		idStr := strconv.FormatInt(resourceID, 10)
		repo, err := s.repoSvc.Get(ctx, idStr)
		if err != nil {
			return err
		}
		if err := s.repoSvc.Delete(ctx, idStr); err != nil {
			return err
		}
		// Directory removal is best-effort — the DB row is already gone.
		if err := s.repoSvc.DeleteDirectory(repo.Path); err != nil {
			slog.Error("DeleteUserResource: remove repository directory failed",
				"path", repo.Path, "repo_id", resourceID, "error", err)
		}
		return nil
	}
	return ErrInvalidResourceType
}

// PurgeUser deletes a user account and ALL their personal resources in one
// operation. Follows the design's "后端实现 §PurgeUser" 7 steps:
//  1. Guards: not self; keep >=1 admin; not the sole owner of any org.
//  2. Stop the user's PTY sessions, disconnect notify-WS + SSE, invalidate Authz.
//  3. Snapshot external repo paths referenced by the user's worktrees.
//  4. Remove the user from every org (reuses RemoveMember cleanup).
//  5. Single tx: clear owned tables + refresh_tokens + the users row.
//  6. Background: git worktree prune external repos + remove the user's dir tree.
//  7. Record a user.purged audit (structured log — no user-level audit table).
func (s *AdminUserService) PurgeUser(ctx context.Context, actorID, userID int64) (PurgeSummary, error) {
	var summary PurgeSummary

	user, err := s.q.GetUserByID(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return summary, ErrNotFound
	}
	if err != nil {
		return summary, err
	}

	// --- Step 1: guards ---
	if actorID == userID {
		return summary, &PurgeGuardError{Reason: "self"}
	}
	if user.Role == "admin" {
		var adminCount int64
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&adminCount); err != nil {
			return summary, fmt.Errorf("count admins: %w", err)
		}
		if adminCount <= 1 {
			return summary, &PurgeGuardError{Reason: "last_admin"}
		}
	}
	orgs, err := s.userOrgs(ctx, userID)
	if err != nil {
		return summary, err
	}
	for _, o := range orgs {
		if o.Role == "owner" && o.IsLastOwner {
			return summary, &PurgeGuardError{Reason: "last_owner_of_org:" + o.Slug}
		}
	}

	// --- Snapshot pre-delete counts for the summary ---
	if summary, err = s.snapshotCounts(ctx, userID, int64(len(orgs))); err != nil {
		return PurgeSummary{}, err
	}

	// --- Step 2: stop activity ---
	s.terminateOwnedSessions(ctx, userID)
	if s.notifyHub != nil {
		s.notifyHub.DisconnectUser(userID)
	}
	if s.sseHub != nil {
		s.sseHub.DisconnectUser(userID)
	}
	s.authz.InvalidateUser(userID)

	// --- Step 3: capture external repo paths (before rows vanish) ---
	externalRepos := s.externalRepoPaths(ctx, userID)

	// --- Step 4: leave every org (reuses RemoveMember cleanup) ---
	actorLabel := fmt.Sprintf("admin:%d", actorID)
	for _, o := range orgs {
		// callerUserID == targetUserID takes RemoveMember's self-removal path, so
		// it skips the CanManageOrg check (the admin actor need not be a member).
		// The last-owner guard above guarantees this never hits ErrLastOwner.
		if err := s.orgSvc.RemoveMember(ctx, userID, actorLabel, o.ID, userID); err != nil {
			slog.Warn("PurgeUser: remove from org failed",
				"user_id", userID, "org_id", o.ID, "error", err)
		}
	}

	// --- Step 5: single tx delete of owned rows + tokens + user ---
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PurgeSummary{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, table := range purgeOwnedTablesDeleteOrder {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+table+` WHERE owner_type = 'user' AND owner_id = ?`, userID); err != nil {
			return PurgeSummary{}, fmt.Errorf("purge %s: %w", table, err)
		}
	}
	// organizations.created_by REFERENCES users(id) ON DELETE RESTRICT, and it is
	// never rewritten after the org is created. If this user created any org, the
	// final DeleteUser would hit that RESTRICT and roll the whole purge back. The
	// org itself survives the purge, so reassign its provenance to a surviving
	// owner (falling back to any surviving admin — the last-admin guard guarantees
	// one exists) before deleting the user.
	if err := s.reassignCreatedOrgs(ctx, tx, userID); err != nil {
		return PurgeSummary{}, err
	}

	qtx := tx.Queries()
	if err := qtx.DeleteUserRefreshTokens(ctx, userID); err != nil {
		return PurgeSummary{}, fmt.Errorf("delete refresh tokens: %w", err)
	}
	if err := qtx.DeleteUser(ctx, userID); err != nil {
		return PurgeSummary{}, fmt.Errorf("delete user: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PurgeSummary{}, fmt.Errorf("commit: %w", err)
	}

	// --- Step 6: background filesystem cleanup ---
	baseDir := OwnerRef{Type: "user", ID: userID}.Root(s.dataDir)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("PurgeUser: filesystem cleanup panicked", "user_id", userID, "recover", r)
			}
		}()
		// Remove the user's directory tree FIRST. Only then does git worktree
		// prune do anything: prune drops registrations whose working dir is gone,
		// and those working dirs live under baseDir. External/org repos live
		// outside baseDir, so they survive and can be pruned of their now-stale
		// worktree entries.
		removeDirectoryWithProcessCleanup(baseDir)
		for _, repoPath := range externalRepos {
			// Skip repos that were themselves removed with the user's dir tree.
			if _, statErr := os.Stat(repoPath); statErr != nil {
				continue
			}
			if err := git.WorktreePrune(repoPath); err != nil {
				slog.Warn("PurgeUser: git worktree prune failed", "repo_path", repoPath, "error", err)
			}
		}
	}()

	// --- Step 7: audit (structured log; no user-level audit table exists) ---
	slog.Info("user.purged",
		"actor_id", actorID,
		"user_id", userID,
		"username", user.Username,
		"projects", summary.Projects,
		"workspaces", summary.Workspaces,
		"repositories", summary.Repositories,
		"env_presets", summary.EnvPresets,
		"quick_actions", summary.QuickActions,
		"agents", summary.Agents,
		"scenes", summary.Scenes,
		"orgs_left", summary.OrgsLeft,
	)

	return summary, nil
}

// reassignCreatedOrgs points organizations.created_by away from userID (which is
// about to be deleted) so the ON DELETE RESTRICT FK doesn't abort the purge tx.
// For each org the user created, it picks a surviving owner of that org, falling
// back to any surviving admin. Runs inside the purge tx.
func (s *AdminUserService) reassignCreatedOrgs(ctx context.Context, tx *store.Tx, userID int64) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM organizations WHERE created_by = ?`, userID)
	if err != nil {
		return fmt.Errorf("list created orgs: %w", err)
	}
	var orgIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan created org: %w", err)
		}
		orgIDs = append(orgIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate created orgs: %w", err)
	}

	for _, orgID := range orgIDs {
		var newCreator int64
		// Prefer a surviving owner of this org; the user's own membership was
		// already removed in step 4, so this never selects userID.
		err := tx.QueryRowContext(ctx,
			`SELECT user_id FROM org_members WHERE org_id = ? AND role = 'owner' AND user_id != ? ORDER BY user_id LIMIT 1`,
			orgID, userID).Scan(&newCreator)
		if errors.Is(err, sql.ErrNoRows) {
			// Ownerless org (e.g. created then left long ago): fall back to any
			// surviving admin. The last-admin guard guarantees at least one exists.
			err = tx.QueryRowContext(ctx,
				`SELECT id FROM users WHERE role = 'admin' AND id != ? ORDER BY id LIMIT 1`,
				userID).Scan(&newCreator)
		}
		if err != nil {
			return fmt.Errorf("pick new creator for org %d: %w", orgID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE organizations SET created_by = ? WHERE id = ?`, newCreator, orgID); err != nil {
			return fmt.Errorf("reassign created_by for org %d: %w", orgID, err)
		}
	}
	return nil
}

// snapshotCounts fills a PurgeSummary with the pre-delete owner-scoped counts.
func (s *AdminUserService) snapshotCounts(ctx context.Context, userID, orgsLeft int64) (PurgeSummary, error) {
	var sm PurgeSummary
	var err error
	if sm.Projects, err = s.countOwned(ctx, "projects", userID); err != nil {
		return sm, err
	}
	if sm.Workspaces, err = s.countOwned(ctx, "workspaces", userID); err != nil {
		return sm, err
	}
	if sm.Repositories, err = s.countOwned(ctx, "repositories", userID); err != nil {
		return sm, err
	}
	if sm.EnvPresets, err = s.countOwned(ctx, "env_presets", userID); err != nil {
		return sm, err
	}
	if sm.QuickActions, err = s.countOwned(ctx, "quick_actions", userID); err != nil {
		return sm, err
	}
	if sm.Agents, err = s.countOwned(ctx, "agents", userID); err != nil {
		return sm, err
	}
	if sm.Scenes, err = s.countOwned(ctx, "scenes", userID); err != nil {
		return sm, err
	}
	if sm.DataSources, err = s.countOwned(ctx, "data_sources", userID); err != nil {
		return sm, err
	}
	if sm.SavedQueries, err = s.countOwned(ctx, "saved_queries", userID); err != nil {
		return sm, err
	}
	if sm.Dashboards, err = s.countOwned(ctx, "dashboards", userID); err != nil {
		return sm, err
	}
	if sm.KnowledgeBases, err = s.countOwned(ctx, "knowledge_bases", userID); err != nil {
		return sm, err
	}
	// harness_specs stays 0.
	sm.OrgsLeft = orgsLeft
	return sm, nil
}

// terminateOwnedSessions stops PTY sessions for every workspace the user owns
// personally. Non-fatal: failures are logged. Org workspaces where the user held
// a live session are covered by OrgService.RemoveMember (step 4).
func (s *AdminUserService) terminateOwnedSessions(ctx context.Context, userID int64) {
	if s.agentMgr == nil {
		return
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM workspaces WHERE owner_type = 'user' AND owner_id = ?`, userID)
	if err != nil {
		slog.Warn("PurgeUser: terminateOwnedSessions query failed", "user_id", userID, "error", err)
		return
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if err := s.agentMgr.Stop(ctx, id); err != nil {
			slog.Warn("PurgeUser: stop agent failed", "workspace_id", id, "error", err)
		}
	}
}

// externalRepoPaths returns the distinct on-disk repo paths referenced by the
// user's workspaces' worktrees. After the user's directory tree is removed, any
// of these that still exist (i.e. repos living outside the user's dir — org or
// externally registered repos) need `git worktree prune` to drop the now-stale
// worktree admin entries.
func (s *AdminUserService) externalRepoPaths(ctx context.Context, userID int64) []string {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT r.path
		FROM worktrees wt
		JOIN workspaces w ON w.id = wt.workspace_id
		JOIN repositories r ON r.id = wt.repository_id
		WHERE w.owner_type = 'user' AND w.owner_id = ?`, userID)
	if err != nil {
		slog.Warn("PurgeUser: externalRepoPaths query failed", "user_id", userID, "error", err)
		return nil
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil && p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}
