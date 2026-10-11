package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ProjectMemberService manages project-level roles. Today the only project
// role is 'project_admin': a member delegated to administer one project's
// integration actions without being an org-wide admin. Org owner/admin remain
// implicit project admins (see Authz.IsProjectAdmin); an explicit grant is
// additive. Personal-edition projects (owner_type='user') have no project
// members — the owner is the only admin.
type ProjectMemberService struct {
	q     *store.Queries
	authz *Authz
}

func NewProjectMemberService(q *store.Queries, authz *Authz) *ProjectMemberService {
	return &ProjectMemberService{q: q, authz: authz}
}

// Grant gives targetUserID the project_admin role on projectID. The caller must
// be an org admin of the owning org (or the personal owner). The target must be
// a member of the owning org. Idempotent.
func (s *ProjectMemberService) Grant(ctx context.Context, callerUserID, projectID, targetUserID int64) error {
	proj, err := s.q.GetProject(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := s.authorizeManage(ctx, callerUserID, proj.OwnerType, proj.OwnerID); err != nil {
		return err
	}
	if proj.OwnerType == "org" {
		if _, err := s.q.GetOrgMember(ctx, store.GetOrgMemberParams{OrgID: proj.OwnerID, UserID: targetUserID}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("target user is not a member of the owning org")
			}
			return err
		}
	}
	if _, err := s.q.GetProjectMember(ctx, store.GetProjectMemberParams{ProjectID: projectID, UserID: targetUserID}); err == nil {
		return nil // already a project admin
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.q.AddProjectMember(ctx, store.AddProjectMemberParams{
		ProjectID: projectID, UserID: targetUserID, Role: "project_admin",
	})
}

// Revoke removes targetUserID's project_admin role on projectID.
func (s *ProjectMemberService) Revoke(ctx context.Context, callerUserID, projectID, targetUserID int64) error {
	proj, err := s.q.GetProject(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := s.authorizeManage(ctx, callerUserID, proj.OwnerType, proj.OwnerID); err != nil {
		return err
	}
	return s.q.RemoveProjectMember(ctx, store.RemoveProjectMemberParams{ProjectID: projectID, UserID: targetUserID})
}

// List returns the explicit project admins of projectID. Readable by anyone who
// can access the project.
func (s *ProjectMemberService) List(ctx context.Context, callerUserID, projectID int64) ([]store.ListProjectMembersRow, error) {
	proj, err := s.q.GetProject(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.authz.CanAccessOwner(ctx, callerUserID, OwnerRef{Type: proj.OwnerType, ID: proj.OwnerID}); err != nil {
		return nil, err
	}
	return s.q.ListProjectMembers(ctx, projectID)
}

// RemoveAllForUserInOrg drops the user's project-admin grants on projects owned
// by orgID only. Called from the member-removal lifecycle so a departure from
// one org does not strip the member's grants in other orgs.
func (s *ProjectMemberService) RemoveAllForUserInOrg(ctx context.Context, orgID, userID int64) (int64, error) {
	return s.q.DeleteProjectMembersForUserInOrg(ctx, store.DeleteProjectMembersForUserInOrgParams{
		UserID:  userID,
		OwnerID: orgID,
	})
}

func (s *ProjectMemberService) authorizeManage(ctx context.Context, callerUserID int64, ownerType string, ownerID int64) error {
	switch ownerType {
	case "user":
		// Project-level roles are a team-edition concept; a personal project's
		// owner is its only admin and cannot delegate.
		return ErrForbidden
	case "org":
		return s.authz.CanManageOrg(ctx, callerUserID, ownerID)
	default:
		return ErrForbidden
	}
}
