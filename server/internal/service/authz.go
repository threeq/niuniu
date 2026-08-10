package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Authz centralizes access checks for multi-tenant resources.
// It enforces ownership (personal vs org) and caches org-membership lookups.
type Authz struct {
	q     *store.Queries
	db    *store.DB
	cache *AccessCache
}

func NewAuthz(q *store.Queries, db *sql.DB) *Authz {
	return &Authz{q: q, db: store.Wrap(db), cache: NewAccessCache()}
}

// InvalidateUser drops the org-membership cache entry for userID.
// Call this after any org_members mutation (add/remove member, role change).
func (a *Authz) InvalidateUser(userID int64) { a.cache.Invalidate(userID) }

// DB returns the wrapped DB. Used by services that have access to Authz but
// no separate db reference (e.g. HarnessService for audit writes). Routing
// through *store.DB guarantees driver-aware placeholder rewriting.
func (a *Authz) DB() *store.DB { return a.db }

var (
	ErrNotFound      = errors.New("resource not found")
	ErrForbidden     = errors.New("forbidden")
	ErrUserNotFound  = errors.New("user not found")
	ErrAlreadyMember = errors.New("user is already a member of this organization")
	ErrInvalidRole   = errors.New("invalid role")
)

// ownerTable is the set of tables that carry (owner_type, owner_id). Using a
// named type rather than a plain string prevents callers from passing arbitrary
// SQL fragments into loadOwner's string-concatenated query.
type ownerTable string

const (
	ownerTableProjects     ownerTable = "projects"
	ownerTableWorkspaces   ownerTable = "workspaces"
	ownerTableRepositories ownerTable = "repositories"
	ownerTableEnvPresets   ownerTable = "env_presets"
	ownerTableQuickActions ownerTable = "quick_actions"
	ownerTableHarnesses    ownerTable = "harnesses"
	ownerTableAgents       ownerTable = "agents"
	ownerTableScenes       ownerTable = "scenes"
	ownerTableDashboards   ownerTable = "dashboards"
	ownerTableSavedQueries ownerTable = "saved_queries"
)

// CanAccessScene verifies userID may access the scene. Builtin scenes
// (source='builtin', owner_type='user', owner_id=0) are universally readable;
// for those, returns the (user,0) owner without an access check.
func (a *Authz) CanAccessScene(ctx context.Context, userID, sceneID int64) (OwnerRef, error) {
	var t, source string
	var id int64
	err := a.db.QueryRowContext(ctx,
		`SELECT owner_type, owner_id, source FROM scenes WHERE id = ?`, sceneID).Scan(&t, &id, &source)
	if err == sql.ErrNoRows {
		return OwnerRef{}, ErrNotFound
	}
	if err != nil {
		return OwnerRef{}, err
	}
	owner := OwnerRef{Type: t, ID: id}
	if source == "builtin" {
		return owner, nil
	}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// loadOwner fetches owner_type and owner_id from table where id=resourceID.
func (a *Authz) loadOwner(ctx context.Context, table ownerTable, resourceID int64) (OwnerRef, error) {
	var t string
	var id int64
	err := a.db.QueryRowContext(ctx,
		`SELECT owner_type, owner_id FROM `+string(table)+` WHERE id = ?`, resourceID).Scan(&t, &id)
	if err == sql.ErrNoRows {
		return OwnerRef{}, ErrNotFound
	}
	if err != nil {
		return OwnerRef{}, err
	}
	return OwnerRef{Type: t, ID: id}, nil
}

// CanAccessOwner verifies userID may access resources owned by owner
// (personal: owner.ID==userID; org: caller is a member). It is the read-access
// equivalent of EnsureOwnerWritable for resources whose owner is already known
// (e.g. a project blueprint loaded by its service).
func (a *Authz) CanAccessOwner(ctx context.Context, userID int64, owner OwnerRef) error {
	return a.checkAccess(ctx, userID, owner)
}

// CanAccessProject verifies userID may access projectID.
// Returns the project's OwnerRef on success.
func (a *Authz) CanAccessProject(ctx context.Context, userID, projectID int64) (OwnerRef, error) {
	owner, err := a.loadOwner(ctx, ownerTableProjects, projectID)
	if err != nil {
		return OwnerRef{}, err
	}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// CanAccessWorkspace verifies userID may access workspaceID.
func (a *Authz) CanAccessWorkspace(ctx context.Context, userID, workspaceID int64) (OwnerRef, error) {
	owner, err := a.loadOwner(ctx, ownerTableWorkspaces, workspaceID)
	if err != nil {
		return OwnerRef{}, err
	}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// CanAccessRepository verifies userID may access repoID.
func (a *Authz) CanAccessRepository(ctx context.Context, userID, repoID int64) (OwnerRef, error) {
	owner, err := a.loadOwner(ctx, ownerTableRepositories, repoID)
	if err != nil {
		return OwnerRef{}, err
	}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// CanAccessIssue verifies userID may access issueID by resolving the issue's
// project owner via: issues → columns → projects.
func (a *Authz) CanAccessIssue(ctx context.Context, userID, issueID int64) (OwnerRef, error) {
	var t string
	var id int64
	err := a.db.QueryRowContext(ctx, `
		SELECT p.owner_type, p.owner_id
		FROM issues i
		JOIN columns c ON c.id = i.column_id
		JOIN projects p ON p.id = c.project_id
		WHERE i.id = ?`, issueID).Scan(&t, &id)
	if err == sql.ErrNoRows {
		return OwnerRef{}, ErrNotFound
	}
	if err != nil {
		return OwnerRef{}, err
	}
	owner := OwnerRef{Type: t, ID: id}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// CanAccessQuickAction verifies userID may access the quick_action with the given id.
func (a *Authz) CanAccessQuickAction(ctx context.Context, userID, id int64) (OwnerRef, error) {
	owner, err := a.loadOwner(ctx, ownerTableQuickActions, id)
	if err != nil {
		return OwnerRef{}, err
	}
	// System defaults seeded by QuickActionService.SeedDefaults carry the
	// (owner_type='user', owner_id=0) sentinel and are surfaced to every caller
	// by quick_actions_owner_filter.sql. Mirror that "globally visible" semantic
	// here so users can edit/delete the seeded actions without hitting 403.
	if owner.Type == "user" && owner.ID == 0 {
		return owner, nil
	}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// CanAccessEnvPreset verifies userID may access the env_preset with the given id.
//
// System defaults seeded by EnvPresetService.SeedDefaults carry the
// (owner_type='user', owner_id=0) sentinel and are surfaced to every caller by
// env_presets_owner_filter.sql. Mirror that "globally visible" semantic at the
// authz layer so personal-edition users (and org members in team edition) can
// edit or delete the seeded providers like 智谱/MiniMax/DeepSeek without
// hitting 403.
func (a *Authz) CanAccessEnvPreset(ctx context.Context, userID, id int64) (OwnerRef, error) {
	owner, err := a.loadOwner(ctx, ownerTableEnvPresets, id)
	if err != nil {
		return OwnerRef{}, err
	}
	if owner.Type == "user" && owner.ID == 0 {
		return owner, nil
	}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// CanAccessHarness verifies userID may access the harness with the given id.
func (a *Authz) CanAccessHarness(ctx context.Context, userID, id int64) (OwnerRef, error) {
	owner, err := a.loadOwner(ctx, ownerTableHarnesses, id)
	if err != nil {
		return OwnerRef{}, err
	}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// CanAccessHarnessSpec verifies the harness_spec exists. harness_specs is a
// single GLOBAL engineering-standards library (no owner columns), so every
// authenticated caller may read/manage it; the returned OwnerRef is the global
// sentinel (user,0) for callers that key floor-permission logic off it.
func (a *Authz) CanAccessHarnessSpec(ctx context.Context, userID, id int64) (OwnerRef, error) {
	_ = userID
	var exists int
	err := a.db.QueryRowContext(ctx, `SELECT 1 FROM harness_specs WHERE id = ?`, id).Scan(&exists)
	if err == sql.ErrNoRows {
		return OwnerRef{}, ErrNotFound
	}
	if err != nil {
		return OwnerRef{}, err
	}
	return OwnerRef{Type: "user", ID: 0}, nil
}

// CanAccessPermissionRequest verifies userID may access the permission request
// with the given id by resolving its parent workspace and reusing the standard
// workspace access check.
func (a *Authz) CanAccessPermissionRequest(ctx context.Context, userID, requestID int64) error {
	var workspaceID int64
	err := a.db.QueryRowContext(ctx,
		`SELECT workspace_id FROM agent_permission_requests WHERE id = ?`, requestID).Scan(&workspaceID)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := a.CanAccessWorkspace(ctx, userID, workspaceID); err != nil {
		return err
	}
	return nil
}

// CanAccessAskUserRequest verifies userID may access the ask-user request with
// the given id by resolving its parent workspace and reusing the workspace
// access check. Mirrors CanAccessPermissionRequest.
func (a *Authz) CanAccessAskUserRequest(ctx context.Context, userID, requestID int64) error {
	var workspaceID int64
	err := a.db.QueryRowContext(ctx,
		`SELECT workspace_id FROM agent_ask_user_requests WHERE id = ?`, requestID).Scan(&workspaceID)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := a.CanAccessWorkspace(ctx, userID, workspaceID); err != nil {
		return err
	}
	return nil
}

// CanAccessDashboard verifies userID may access the dashboard with the given id.
func (a *Authz) CanAccessDashboard(ctx context.Context, userID, id int64) (OwnerRef, error) {
	owner, err := a.loadOwner(ctx, ownerTableDashboards, id)
	if err != nil {
		return OwnerRef{}, err
	}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// CanAccessSavedQuery verifies userID may access the saved_query with the given id.
func (a *Authz) CanAccessSavedQuery(ctx context.Context, userID, id int64) (OwnerRef, error) {
	owner, err := a.loadOwner(ctx, ownerTableSavedQueries, id)
	if err != nil {
		return OwnerRef{}, err
	}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// CanAccessAgent verifies userID may access agentID.
func (a *Authz) CanAccessAgent(ctx context.Context, userID, agentID int64) (OwnerRef, error) {
	owner, err := a.loadOwner(ctx, ownerTableAgents, agentID)
	if err != nil {
		return OwnerRef{}, err
	}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// IsProjectAdmin reports whether userID has admin-level rights on the project:
//
//	personal project (owner_type='user') → owner == userID
//	org project      (owner_type='org')  → user is org owner or admin
//
// Returns (false, nil) when the project does not exist (caller can layer their
// own ErrNotFound semantics on top if needed) or when the user has no admin
// rights. Errors are returned only for unexpected DB failures.
func (a *Authz) IsProjectAdmin(ctx context.Context, userID, projectID int64) (bool, error) {
	var ownerType string
	var ownerID int64
	err := a.db.QueryRowContext(ctx,
		`SELECT owner_type, owner_id FROM projects WHERE id = ?`, projectID,
	).Scan(&ownerType, &ownerID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch ownerType {
	case "user":
		return ownerID == userID, nil
	case "org":
		var role string
		err := a.db.QueryRowContext(ctx,
			`SELECT role FROM org_members WHERE org_id = ? AND user_id = ?`,
			ownerID, userID,
		).Scan(&role)
		if err == sql.ErrNoRows {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return role == "owner" || role == "admin", nil
	default:
		return false, nil
	}
}

// IsGlobalAdmin reports whether userID holds the global 'admin' role in the
// users table. Global admins get full management access to every org
// (create/update/delete, member management) regardless of org membership.
func (a *Authz) IsGlobalAdmin(ctx context.Context, userID int64) bool {
	var role string
	if err := a.db.QueryRowContext(ctx,
		`SELECT role FROM users WHERE id = ?`, userID).Scan(&role); err != nil {
		return false
	}
	return role == "admin"
}

// CanManageOrg checks that userID has role 'owner' or 'admin' in orgID, or is a
// global admin (who may manage every org).
func (a *Authz) CanManageOrg(ctx context.Context, userID, orgID int64) error {
	if a.IsGlobalAdmin(ctx, userID) {
		return nil
	}
	var role string
	err := a.db.QueryRowContext(ctx,
		`SELECT role FROM org_members WHERE org_id = ? AND user_id = ?`, orgID, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if role != "owner" && role != "admin" {
		return ErrForbidden
	}
	return nil
}

// CanAdminOrg checks that userID has role 'owner' in orgID, or is a global admin
// (who may administer every org).
func (a *Authz) CanAdminOrg(ctx context.Context, userID, orgID int64) error {
	if a.IsGlobalAdmin(ctx, userID) {
		return nil
	}
	var role string
	err := a.db.QueryRowContext(ctx,
		`SELECT role FROM org_members WHERE org_id = ? AND user_id = ?`, orgID, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if role != "owner" {
		return ErrForbidden
	}
	return nil
}

// IsOrgManagerSomewhere returns true iff userID has role 'owner' or 'admin'
// in at least one org. Used by features that read deployment-level data
// shared across orgs (e.g. claude-usage panel in team mode).
func (a *Authz) IsOrgManagerSomewhere(ctx context.Context, userID int64) (bool, error) {
	var n int
	err := a.db.QueryRowContext(ctx,
		`SELECT 1 FROM org_members WHERE user_id = ? AND role IN ('owner','admin') LIMIT 1`,
		userID).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// EnsureOwnerWritable verifies userID may write to resources owned by owner.
// Per spec §6.5:
//   - personal: owner.ID must equal userID.
//   - org: userID must be a member of the org (any role is sufficient).
func (a *Authz) EnsureOwnerWritable(ctx context.Context, userID int64, owner OwnerRef) error {
	if owner.Type == "user" {
		if owner.ID != userID {
			return ErrForbidden
		}
		return nil
	}
	// org: any membership role is sufficient for resource creation (spec §6.5).
	var role string
	if err := a.db.QueryRowContext(ctx,
		`SELECT role FROM org_members WHERE org_id = ? AND user_id = ?`, owner.ID, userID).Scan(&role); err != nil {
		return ErrForbidden
	}
	return nil
}

// CanAccessMemory verifies userID may access the memory (any state, including
// soft-deleted) and returns its OwnerRef on success. memories is owner-scoped
// but not in the ownerTable enum (net-new table), so this loads owner directly.
func (a *Authz) CanAccessMemory(ctx context.Context, userID, memoryID int64) (OwnerRef, error) {
	var t string
	var id int64
	err := a.db.QueryRowContext(ctx,
		`SELECT owner_type, owner_id FROM memories WHERE id = ?`, memoryID).Scan(&t, &id)
	if err == sql.ErrNoRows {
		return OwnerRef{}, ErrNotFound
	}
	if err != nil {
		return OwnerRef{}, err
	}
	owner := OwnerRef{Type: t, ID: id}
	if err := a.checkAccess(ctx, userID, owner); err != nil {
		return OwnerRef{}, err
	}
	return owner, nil
}

// EnsureOwnerReadable verifies userID may read resources owned by owner
// (personal == self, org == member). Used by owner-scoped list endpoints.
func (a *Authz) EnsureOwnerReadable(ctx context.Context, userID int64, owner OwnerRef) error {
	return a.checkAccess(ctx, userID, owner)
}

// EnsureOwnerExists verifies that the referenced owner entity exists in the DB.
func (a *Authz) EnsureOwnerExists(ctx context.Context, owner OwnerRef) error {
	var exists int
	var err error
	if owner.Type == "user" {
		err = a.db.QueryRowContext(ctx,
			`SELECT 1 FROM users WHERE id = ?`, owner.ID).Scan(&exists)
	} else {
		err = a.db.QueryRowContext(ctx,
			`SELECT 1 FROM organizations WHERE id = ?`, owner.ID).Scan(&exists)
	}
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	return err
}
