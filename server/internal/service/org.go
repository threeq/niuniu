package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

var (
	ErrLastOwner    = errors.New("cannot remove the last owner of an org")
	ErrSlugConflict = errors.New("slug already exists")
)

// orgBlockingTables lists the heavyweight, user-created resource tables that
// must be empty before an org can be deleted. Each has a filesystem footprint
// (worktrees / repo clones) or many children, so DeleteOrg refuses and makes
// the user remove them explicitly rather than silently cascading.
var orgBlockingTables = []string{
	"projects",
	"repositories",
	"workspaces",
}

// orgConfigTables lists lightweight config/preset tables that are auto-seeded
// or auto-stamped and therefore must NOT block org deletion. In particular the
// owner-model upgrade migration (stampOwnerColumns) historically stamped the
// global owner_id=0 sentinel rows of these tables onto the "Default" org, which
// then permanently blocked that org's deletion. These rows carry no filesystem
// state and their inbound FKs are ON DELETE CASCADE or absent, so DeleteOrg
// cleans them up instead of blocking. The cleanup runs only after
// orgBlockingTables is confirmed empty, so no project/workspace child rows
// reference them at that point.
//
// Note: harness_specs is intentionally absent — engineering-standards specs are
// a single GLOBAL library (owner_id=0 sentinel), never owned by an org, so they
// are outside the org lifecycle entirely.
var orgConfigTables = []string{
	"agents",
	"quick_actions",
	"env_presets",
}

// agentStopper is the minimal interface required by OrgService to terminate
// active PTY sessions when a user is removed from an org.
type agentStopper interface {
	Stop(ctx context.Context, workspaceID int64) error
}

// userDisconnector is the minimal interface required by OrgService to close
// active notify (WS) or SSE connections for a user that has been removed.
type userDisconnector interface {
	DisconnectUser(userID int64)
}

// orgNotifyHub combines disconnect and broadcast capabilities used by OrgService.
type orgNotifyHub interface {
	userDisconnector
	Broadcast(n notify.Notification)
}

// OrgService implements business logic for organizations.
type OrgService struct {
	q          *store.Queries
	db         *store.DB
	authz      *Authz
	agentMgr   agentStopper  // may be nil; wired after construction via SetAgentManager
	notifyHub  orgNotifyHub  // may be nil; wired after construction via SetNotifyHub
	sseHub     userDisconnector // may be nil; wired after construction via SetSSEHub
}

func NewOrgService(q *store.Queries, db *sql.DB, authz *Authz) *OrgService {
	return &OrgService{q: q, db: store.Wrap(db), authz: authz}
}

// SetAgentManager wires in the AgentManager so RemoveMember can terminate
// active PTY sessions for the removed user.
func (s *OrgService) SetAgentManager(mgr agentStopper) {
	s.agentMgr = mgr
}

// SetNotifyHub wires the WebSocket notification hub so RemoveMember can close
// the removed user's active WS streams and broadcast org_member change events.
func (s *OrgService) SetNotifyHub(hub orgNotifyHub) {
	s.notifyHub = hub
}

// SetSSEHub wires the SSE session hub so RemoveMember can close the removed
// user's active SSE streams.
func (s *OrgService) SetSSEHub(hub userDisconnector) {
	s.sseHub = hub
}

// appendAudit writes an audit log entry within the transaction tx.
func (s *OrgService) appendAudit(ctx context.Context, tx *store.Tx, orgID, actorID int64, actorLabel, action, targetType string, targetID int64, payload string) {
	q := tx.Queries()
	_ = q.AppendOrgAudit(ctx, store.AppendOrgAuditParams{
		OrgID:       orgID,
		ActorUserID: sql.NullInt64{Int64: actorID, Valid: actorID != 0},
		ActorLabel:  actorLabel,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Payload:     payload,
	})
}

// Create creates a new organization. The caller (callerUserID) must have
// global role='admin'. Caller becomes the owner of the new org.
//
// Slug handling:
//   - If slug is empty, deriveSlug(name) provides the initial candidate.
//     Collisions auto-suffix to base-2, base-3, ... up to 99 attempts.
//   - If slug is non-empty (user-provided), collisions return
//     ErrSlugConflict instead of silently renaming what the user typed.
//
// Each attempt opens its own transaction. This is necessary on PostgreSQL,
// where a failed INSERT poisons the surrounding transaction; we cannot
// just continue the loop inside one tx.
func (s *OrgService) Create(ctx context.Context, callerUserID int64, callerLabel, name, slug, description string) (*store.Organization, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}

	userProvidedSlug := slug != ""
	if !userProvidedSlug {
		slug = deriveSlug(name)
	}
	base := slug

	for attempt := 0; attempt < 100; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = fmt.Sprintf("%s-%d", base, attempt+1) // first retry -> base-2
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("begin tx: %w", err)
		}

		qtx := tx.Queries()
		org, err := qtx.CreateOrganization(ctx, store.CreateOrganizationParams{
			Slug:        candidate,
			Name:        name,
			Description: description,
			CreatedBy:   callerUserID,
		})
		if err != nil {
			_ = tx.Rollback()
			if isSlugUniqueViolation(err) {
				if userProvidedSlug {
					return nil, ErrSlugConflict
				}
				continue
			}
			return nil, fmt.Errorf("create organization: %w", err)
		}

		if err := qtx.AddOrgMember(ctx, store.AddOrgMemberParams{
			OrgID:  org.ID,
			UserID: callerUserID,
			Role:   "owner",
		}); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("add owner: %w", err)
		}

		s.appendAudit(ctx, tx, org.ID, callerUserID, callerLabel, "org.created", "org", org.ID, fmt.Sprintf(`{"name":%q,"slug":%q}`, name, candidate))

		if err := tx.Commit(); err != nil {
			// Defensive: PG can surface deferred constraint violations at
			// commit time. Treat the same as INSERT-time conflict.
			if isSlugUniqueViolation(err) {
				if userProvidedSlug {
					return nil, ErrSlugConflict
				}
				continue
			}
			return nil, fmt.Errorf("commit: %w", err)
		}
		return &org, nil
	}
	return nil, fmt.Errorf("could not generate unique slug after 100 attempts")
}

// Get returns the organization by ID. The caller must be a member, or a global
// admin (who may view and manage every org).
func (s *OrgService) Get(ctx context.Context, callerUserID, orgID int64) (*store.Organization, error) {
	if err := s.requireMember(ctx, callerUserID, orgID); err != nil {
		if !(errors.Is(err, ErrForbidden) && s.authz.IsGlobalAdmin(ctx, callerUserID)) {
			return nil, err
		}
	}
	org, err := s.q.GetOrganization(ctx, orgID)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &org, nil
}

// Update updates name, slug, and description. Caller must be owner or admin.
func (s *OrgService) Update(ctx context.Context, callerUserID int64, callerLabel string, orgID int64, name, slug, description string) (*store.Organization, error) {
	if err := s.authz.CanManageOrg(ctx, callerUserID, orgID); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := tx.Queries()
	if err := qtx.UpdateOrganization(ctx, store.UpdateOrganizationParams{
		ID:          orgID,
		Name:        name,
		Slug:        slug,
		Description: description,
	}); err != nil {
		return nil, fmt.Errorf("update organization: %w", err)
	}

	s.appendAudit(ctx, tx, orgID, callerUserID, callerLabel, "org.updated", "org", orgID, fmt.Sprintf(`{"name":%q,"slug":%q}`, name, slug))

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	org, err := s.q.GetOrganization(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return &org, nil
}

// Delete deletes the organization. Caller must be the owner (or a global
// admin). Rejected if the org still owns heavyweight resources
// (projects/repositories/workspaces); lightweight config/preset rows are
// cleaned up automatically (see orgConfigTables).
//
// The checks, cleanup, and DELETE run inside the same transaction so that a
// concurrent resource insert cannot slip through between the check and the
// delete (TOCTOU race). SQLite's default serializable isolation makes this
// sufficient. TODO: on PostgreSQL, add a `SELECT 1 FROM organizations WHERE
// id = ? FOR UPDATE` at the start of the transaction to hold a row-level lock
// and block concurrent inserts that reference this org via FK.
func (s *OrgService) Delete(ctx context.Context, callerUserID int64, callerLabel string, orgID int64) error {
	if err := s.authz.CanAdminOrg(ctx, callerUserID, orgID); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Reject if heavyweight resources remain — runs inside the transaction so the
	// check and DELETE are atomic under SQLite's serializable isolation.
	for _, table := range orgBlockingTables {
		var count int64
		err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+table+` WHERE owner_type = 'org' AND owner_id = ?`, orgID).Scan(&count)
		if err != nil {
			return fmt.Errorf("check %s: %w", table, err)
		}
		if count > 0 {
			return fmt.Errorf("org still owns resources in %s; cannot delete", table)
		}
	}

	// Clean up auto-managed config/preset rows so they don't permanently block
	// deletion. Safe here: the blocking check above guarantees no project/
	// workspace exists, so no child rows reference these config rows.
	for _, table := range orgConfigTables {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+table+` WHERE owner_type = 'org' AND owner_id = ?`, orgID); err != nil {
			return fmt.Errorf("cleanup %s: %w", table, err)
		}
	}

	s.appendAudit(ctx, tx, orgID, callerUserID, callerLabel, "org.deleted", "org", orgID, `{}`)

	qtx := tx.Queries()
	if err := qtx.DeleteOrganization(ctx, orgID); err != nil {
		return fmt.Errorf("delete organization: %w", err)
	}

	return tx.Commit()
}

// ListForUser returns all organizations the user is a member of.
func (s *OrgService) ListForUser(ctx context.Context, userID int64) ([]store.ListOrganizationsForUserRow, error) {
	return s.q.ListOrganizationsForUser(ctx, userID)
}

// ListAll returns every organization (with the caller's own membership role,
// null when not a member). Restricted to global admins — a read-only overview
// of all orgs on the deployment. Non-admins get ErrForbidden.
func (s *OrgService) ListAll(ctx context.Context, callerUserID int64) ([]store.ListAllOrganizationsRow, error) {
	if !s.authz.IsGlobalAdmin(ctx, callerUserID) {
		return nil, ErrForbidden
	}
	return s.q.ListAllOrganizations(ctx, callerUserID)
}

// ListMembers returns all members of the org. Caller must be a member, or a
// global admin (who may view and manage every org).
func (s *OrgService) ListMembers(ctx context.Context, callerUserID, orgID int64) ([]store.ListOrgMembersRow, error) {
	if err := s.requireMember(ctx, callerUserID, orgID); err != nil {
		if !(errors.Is(err, ErrForbidden) && s.authz.IsGlobalAdmin(ctx, callerUserID)) {
			return nil, err
		}
	}
	return s.q.ListOrgMembers(ctx, orgID)
}

// AddMember adds targetUserID to orgID with the given role. Caller must be
// owner or admin. Returns ErrUserNotFound, ErrInvalidRole, or
// ErrAlreadyMember as appropriate.
func (s *OrgService) AddMember(ctx context.Context, callerUserID int64, callerLabel string, orgID int64, targetUserID int64, role string) error {
	if err := s.authz.CanManageOrg(ctx, callerUserID, orgID); err != nil {
		return err
	}
	if role != "owner" && role != "admin" && role != "member" {
		return ErrInvalidRole
	}
	// Minting an owner is an owner-only privilege — mirror UpdateMemberRole, so an
	// admin can't escalate the org's owner set by adding a fresh owner member.
	if role == "owner" {
		if err := s.authz.CanAdminOrg(ctx, callerUserID, orgID); err != nil {
			return err
		}
	}

	target, err := s.q.GetUserByID(ctx, targetUserID)
	if err == sql.ErrNoRows {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := tx.Queries()
	if err := qtx.AddOrgMember(ctx, store.AddOrgMemberParams{
		OrgID:  orgID,
		UserID: target.ID,
		Role:   role,
	}); err != nil {
		// SQLite: "UNIQUE constraint failed: org_members.org_id, org_members.user_id"
		// PG:     SQLSTATE 23505
		msg := err.Error()
		if strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "23505") {
			return ErrAlreadyMember
		}
		return fmt.Errorf("add member: %w", err)
	}

	s.appendAudit(ctx, tx, orgID, callerUserID, callerLabel, "member.added", "user", target.ID,
		fmt.Sprintf(`{"username":%q,"role":%q}`, target.Username, role))

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	s.authz.InvalidateUser(target.ID)
	s.broadcastOrgMember(orgID, "changed")
	return nil
}

// UpdateMemberRole changes the role of a member. Caller must be owner.
func (s *OrgService) UpdateMemberRole(ctx context.Context, callerUserID int64, callerLabel string, orgID, targetUserID int64, role string) error {
	if err := s.authz.CanAdminOrg(ctx, callerUserID, orgID); err != nil {
		return err
	}
	if role != "owner" && role != "admin" && role != "member" {
		return fmt.Errorf("invalid role %q", role)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := tx.Queries()
	if err := qtx.UpdateOrgMemberRole(ctx, store.UpdateOrgMemberRoleParams{
		OrgID:  orgID,
		UserID: targetUserID,
		Role:   role,
	}); err != nil {
		return fmt.Errorf("update role: %w", err)
	}

	s.appendAudit(ctx, tx, orgID, callerUserID, callerLabel, "member.role_changed", "user", targetUserID,
		fmt.Sprintf(`{"role":%q}`, role))

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	s.authz.InvalidateUser(targetUserID)
	s.broadcastOrgMember(orgID, "changed")
	return nil
}

// RemoveMember removes a member. Caller must be owner/admin OR the member
// themselves. Cannot remove the last owner.
func (s *OrgService) RemoveMember(ctx context.Context, callerUserID int64, callerLabel string, orgID, targetUserID int64) error {
	// Allow if caller == target (self-removal) OR caller is owner/admin
	isSelf := callerUserID == targetUserID
	if !isSelf {
		if err := s.authz.CanManageOrg(ctx, callerUserID, orgID); err != nil {
			return err
		}
	}

	// Check last-owner invariant
	target, err := s.q.GetOrgMember(ctx, store.GetOrgMemberParams{OrgID: orgID, UserID: targetUserID})
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	if target.Role == "owner" {
		count, err := s.q.CountOrgOwners(ctx, orgID)
		if err != nil {
			return fmt.Errorf("count owners: %w", err)
		}
		if count <= 1 {
			return ErrLastOwner
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := tx.Queries()
	if err := qtx.RemoveOrgMember(ctx, store.RemoveOrgMemberParams{
		OrgID:  orgID,
		UserID: targetUserID,
	}); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}

	// Cascade: clear the removed member's issue assignments inside this org's
	// projects only. Personal projects and projects in other orgs are untouched.
	// Records the affected issue IDs in an audit row before falling through to
	// the generic member.removed audit.
	affectedIDs, err := qtx.ListAffectedIssueIDsForUserInOrg(ctx, store.ListAffectedIssueIDsForUserInOrgParams{
		UserID:  targetUserID,
		OwnerID: orgID,
	})
	if err != nil {
		return fmt.Errorf("list affected issues: %w", err)
	}
	clearedCount, err := qtx.DeleteIssueAssigneesForUserInOrg(ctx, store.DeleteIssueAssigneesForUserInOrgParams{
		UserID:  targetUserID,
		OwnerID: orgID,
	})
	if err != nil {
		return fmt.Errorf("clear issue_assignees: %w", err)
	}
	if clearedCount > 0 {
		payload := fmt.Sprintf(`{"cleared_count":%d,"affected_issue_ids":%s,"removed_user_id":%d}`,
			clearedCount, jsonInt64Slice(affectedIDs), targetUserID)
		s.appendAudit(ctx, tx, orgID, callerUserID, callerLabel,
			"member.removed.assignee_cleanup", "user", targetUserID, payload)
	}

	s.appendAudit(ctx, tx, orgID, callerUserID, callerLabel, "member.removed", "user", targetUserID, `{}`)

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	s.authz.InvalidateUser(targetUserID)

	// Notify the org's members (including the removed user) so SPAs can
	// refresh their org store before the user's WS connection is closed.
	s.broadcastOrgMember(orgID, "changed")

	// Terminate any active PTY agent sessions the removed user owns inside
	// this org's workspaces. We query for workspaces where the session
	// identity matches the removed user so we only touch their sessions.
	s.terminateUserSessions(ctx, orgID, targetUserID)

	// Close the removed user's active WebSocket notification streams.
	if s.notifyHub != nil {
		s.notifyHub.DisconnectUser(targetUserID)
	}

	// Close the removed user's active SSE streams.
	if s.sseHub != nil {
		s.sseHub.DisconnectUser(targetUserID)
	}

	return nil
}

// terminateUserSessions stops all PTY sessions for targetUserID within the org.
// Non-fatal: errors are logged but do not fail the removal.
func (s *OrgService) terminateUserSessions(ctx context.Context, orgID, targetUserID int64) {
	if s.agentMgr == nil {
		return
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM workspaces WHERE owner_type = 'org' AND owner_id = ? AND current_session_user_id = ?`,
		orgID, targetUserID)
	if err != nil {
		slog.Warn("terminateUserSessions: query failed", "orgID", orgID, "userID", targetUserID, "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var wsID int64
		if err := rows.Scan(&wsID); err != nil {
			continue
		}
		if err := s.agentMgr.Stop(ctx, wsID); err != nil {
			slog.Warn("terminateUserSessions: stop agent failed", "workspaceID", wsID, "error", err)
		}
	}
}

// TransferOwnership demotes caller to admin and promotes targetUserID to owner
// in a single transaction. Caller must be owner; target must be an existing member.
func (s *OrgService) TransferOwnership(ctx context.Context, callerUserID int64, callerLabel string, orgID, targetUserID int64) error {
	if err := s.authz.CanAdminOrg(ctx, callerUserID, orgID); err != nil {
		return err
	}

	// Target must already be a member
	if _, err := s.q.GetOrgMember(ctx, store.GetOrgMemberParams{OrgID: orgID, UserID: targetUserID}); err == sql.ErrNoRows {
		return fmt.Errorf("target user is not a member of the org")
	} else if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := tx.Queries()

	// Demote caller to admin
	if err := qtx.UpdateOrgMemberRole(ctx, store.UpdateOrgMemberRoleParams{
		OrgID:  orgID,
		UserID: callerUserID,
		Role:   "admin",
	}); err != nil {
		return fmt.Errorf("demote caller: %w", err)
	}

	// Promote target to owner
	if err := qtx.UpdateOrgMemberRole(ctx, store.UpdateOrgMemberRoleParams{
		OrgID:  orgID,
		UserID: targetUserID,
		Role:   "owner",
	}); err != nil {
		return fmt.Errorf("promote target: %w", err)
	}

	s.appendAudit(ctx, tx, orgID, callerUserID, callerLabel, "ownership.transferred", "user", targetUserID, `{}`)

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	s.authz.InvalidateUser(callerUserID)
	s.authz.InvalidateUser(targetUserID)
	s.broadcastOrgMember(orgID, "changed")
	return nil
}

// ListAuditLog returns audit log entries for the org. Caller must be owner or admin.
// Limit is capped at 500.
func (s *OrgService) ListAuditLog(ctx context.Context, callerUserID, orgID int64, limit, offset int64) ([]store.OrgAuditLog, error) {
	if err := s.authz.CanManageOrg(ctx, callerUserID, orgID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	return s.q.ListOrgAudit(ctx, store.ListOrgAuditParams{
		OrgID:  orgID,
		Limit:  limit,
		Offset: offset,
	})
}

// broadcastOrgMember sends a TopicOrgMember notification to all members of the
// org (owner-filtered so only org members receive it). Non-fatal if hub is nil.
func (s *OrgService) broadcastOrgMember(orgID int64, action string) {
	if s.notifyHub == nil {
		return
	}
	s.notifyHub.Broadcast(notify.Notification{
		Topic:     notify.TopicOrgMember,
		Action:    action,
		ID:        orgID,
		OwnerType: "org",
		OwnerID:   orgID,
	})
}

// jsonInt64Slice formats a slice of int64 IDs as a JSON-array string,
// e.g. []int64{1,2,3} -> "[1,2,3]". Returns "[]" for an empty slice.
func jsonInt64Slice(ids []int64) string {
	if len(ids) == 0 {
		return "[]"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// requireMember verifies the caller is a member (any role) of the org.
func (s *OrgService) requireMember(ctx context.Context, userID, orgID int64) error {
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM org_members WHERE org_id = ? AND user_id = ?`, orgID, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return ErrForbidden
	}
	return err
}
