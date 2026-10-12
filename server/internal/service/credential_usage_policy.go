// Package service — CredentialUsagePolicyService is the team-edition "who may
// use this org credential" gate (issue #742).
//
// Ownership split: a credential is either personal (owner_type='user') or
// organizational (owner_type='org'); both are encrypted by the SAME integration
// keyring (ExternalCredentialService). This service adds the missing policy
// layer for the org case only:
//
//   - Default-minimal: a freshly created org credential is usable NOWHERE. It
//     becomes usable only in scopes an org admin explicitly grants
//     (credential_usage_grants, scope = project | workspace).
//   - Enforced in the SERVICE/API layer, not the UI: both the binding path
//     (ExternalSourceService.Add) and the call path
//     (ExternalProxyService.resolveCredential) consult IsAllowed, so hiding a
//     button is never the security boundary.
//   - Personal credentials are exempt: IsAllowed short-circuits to true for
//     owner_type='user', so single-user behavior is byte-for-byte unchanged.
//
// A project-scope grant covers the project and (transitively) its workspaces.
// A workspace-scope grant authorizes exactly one workspace; it is enough to
// bind the credential into the project, but only that workspace's agent may
// then use it.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Grant scope kinds.
const (
	GrantScopeProject   = "project"
	GrantScopeWorkspace = "workspace"
)

var (
	// ErrCredentialNotGranted is returned when an org credential is used
	// outside every scope its owner granted. Handlers map it to 403.
	ErrCredentialNotGranted = errors.New("org credential not authorized for this scope")
	// ErrGrantPersonalCredential is returned when a usage grant is attempted on
	// a personal credential — the policy layer only governs org credentials.
	ErrGrantPersonalCredential = errors.New("usage policy applies only to org-owned credentials")
	// ErrGrantScopeNotOwned is returned when the grant target scope does not
	// belong to the credential's owner (e.g. another org's project).
	ErrGrantScopeNotOwned = errors.New("grant scope does not belong to the credential's owner")
	// ErrGrantScopeInvalid is returned for an unknown scope_type.
	ErrGrantScopeInvalid = errors.New("grant scope_type must be 'project' or 'workspace'")
)

// CredentialUsageGrant is one authorized scope of an org credential.
type CredentialUsageGrant struct {
	ID           int64
	CredentialID int64
	ScopeType    string
	ScopeID      int64
	GrantedBy    int64
	CreatedAt    time.Time
}

// CredentialUsagePolicyService owns org-credential usage grants.
type CredentialUsagePolicyService struct {
	q     *store.Queries
	db    *store.DB
	creds *ExternalCredentialService
	authz *Authz
}

// NewCredentialUsagePolicyService wires the policy layer.
func NewCredentialUsagePolicyService(q *store.Queries, rawDB *sql.DB, creds *ExternalCredentialService, authz *Authz) *CredentialUsagePolicyService {
	return &CredentialUsagePolicyService{q: q, db: store.Wrap(rawDB), creds: creds, authz: authz}
}

// IsAllowed is the enforcement predicate. Personal credentials are always
// allowed (unchanged single-user behavior). An org credential is allowed iff a
// project grant names projectID, or (when workspaceID > 0) a workspace grant
// names workspaceID. A missing credential returns ErrNoCredential so callers
// surface "not found" rather than an authorization failure.
func (s *CredentialUsagePolicyService) IsAllowed(ctx context.Context, credentialID, projectID, workspaceID int64) (bool, error) {
	if credentialID <= 0 {
		return false, ErrNoCredential
	}
	meta, err := s.creds.GetMetaByID(ctx, credentialID)
	if err != nil {
		return false, err
	}
	if meta.OwnerType != "org" {
		return true, nil
	}
	if projectID > 0 {
		ok, err := s.q.CredentialUsageGrantExistsForProject(ctx, store.CredentialUsageGrantExistsForProjectParams{
			CredentialID: credentialID,
			ScopeID:      projectID,
		})
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	if workspaceID > 0 {
		ok, err := s.q.CredentialUsageGrantExistsForWorkspace(ctx, store.CredentialUsageGrantExistsForWorkspaceParams{
			CredentialID: credentialID,
			ScopeID:      workspaceID,
		})
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// IsProjectAuthorized reports whether an org credential may be BOUND into the
// project (a project grant, or any workspace grant inside it). Personal
// credentials always pass. Used by ExternalSourceService.Add.
func (s *CredentialUsagePolicyService) IsProjectAuthorized(ctx context.Context, credentialID, projectID int64) (bool, error) {
	if credentialID <= 0 {
		return false, ErrNoCredential
	}
	meta, err := s.creds.GetMetaByID(ctx, credentialID)
	if err != nil {
		return false, err
	}
	if meta.OwnerType != "org" {
		return true, nil
	}
	return s.q.CredentialUsageGrantExistsInProject(ctx, store.CredentialUsageGrantExistsInProjectParams{
		CredentialID: credentialID,
		ScopeID:      projectID,
		ProjectID:    projectID,
	})
}

// Grant authorizes an org credential for a scope. The caller must be an org
// admin of the credential's owner, and the target scope must belong to that
// same owner (no cross-org grants). Idempotent.
func (s *CredentialUsagePolicyService) Grant(ctx context.Context, callerUserID, credentialID int64, scopeType string, scopeID int64) error {
	meta, err := s.creds.GetMetaByID(ctx, credentialID)
	if err != nil {
		return err
	}
	if meta.OwnerType != "org" {
		return ErrGrantPersonalCredential
	}
	if err := s.authz.CanManageOrg(ctx, callerUserID, meta.OwnerID); err != nil {
		return err
	}
	owner, err := s.scopeOwner(ctx, callerUserID, scopeType, scopeID)
	if err != nil {
		return err
	}
	if owner.Type != "org" || owner.ID != meta.OwnerID {
		return ErrGrantScopeNotOwned
	}
	if err := s.q.GrantCredentialUsage(ctx, store.GrantCredentialUsageParams{
		CredentialID: credentialID,
		ScopeType:    scopeType,
		ScopeID:      scopeID,
		GrantedBy:    callerUserID,
	}); err != nil {
		return err
	}
	s.auditUsageDecision(ctx, meta.OwnerID, callerUserID, credentialID, "integration.credential.usage_granted", scopeType, scopeID)
	return nil
}

// Revoke removes a usage grant. Same authorization as Grant.
func (s *CredentialUsagePolicyService) Revoke(ctx context.Context, callerUserID, credentialID int64, scopeType string, scopeID int64) error {
	meta, err := s.creds.GetMetaByID(ctx, credentialID)
	if err != nil {
		return err
	}
	if meta.OwnerType != "org" {
		return ErrGrantPersonalCredential
	}
	if err := s.authz.CanManageOrg(ctx, callerUserID, meta.OwnerID); err != nil {
		return err
	}
	if _, err := s.q.RevokeCredentialUsage(ctx, store.RevokeCredentialUsageParams{
		CredentialID: credentialID,
		ScopeType:    scopeType,
		ScopeID:      scopeID,
	}); err != nil {
		return err
	}
	s.auditUsageDecision(ctx, meta.OwnerID, callerUserID, credentialID, "integration.credential.usage_revoked", scopeType, scopeID)
	return nil
}

// auditUsageDecision appends an org-scoped audit row for a usage grant/revoke so
// the authorization chain is reconstructable (issue #742). Best effort.
func (s *CredentialUsagePolicyService) auditUsageDecision(ctx context.Context, orgID, actorUserID, credentialID int64, action, scopeType string, scopeID int64) {
	payload, err := json.Marshal(map[string]any{
		"credential_id": credentialID,
		"scope_type":    scopeType,
		"scope_id":      scopeID,
	})
	if err != nil {
		return
	}
	if err := s.q.AppendOrgAudit(ctx, store.AppendOrgAuditParams{
		OrgID:       orgID,
		ActorUserID: sql.NullInt64{Int64: actorUserID, Valid: actorUserID != 0},
		Action:      action,
		TargetType:  "external_provider_credential",
		TargetID:    credentialID,
		Payload:     string(payload),
	}); err != nil {
		slog.Warn("credential usage audit write failed", "action", action, "org_id", orgID, "error", err)
	}
}

// ListGrants returns every grant on the credential. Any member who can read the
// credential's owner may view the policy (visibility mirrors credential listing).
func (s *CredentialUsagePolicyService) ListGrants(ctx context.Context, callerUserID, credentialID int64) ([]CredentialUsageGrant, error) {
	meta, err := s.creds.GetMetaByID(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	if err := s.authz.EnsureOwnerReadable(ctx, callerUserID, OwnerRef{Type: meta.OwnerType, ID: meta.OwnerID}); err != nil {
		return nil, err
	}
	rows, err := s.q.ListCredentialUsageGrants(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	out := make([]CredentialUsageGrant, 0, len(rows))
	for _, r := range rows {
		out = append(out, CredentialUsageGrant{
			ID:           r.ID,
			CredentialID: r.CredentialID,
			ScopeType:    r.ScopeType,
			ScopeID:      r.ScopeID,
			GrantedBy:    r.GrantedBy,
			CreatedAt:    r.CreatedAt,
		})
	}
	return out, nil
}

// scopeOwner resolves + validates the scope target, requiring the caller to be
// able to access it (defense in depth; ownership is re-checked by the caller).
func (s *CredentialUsagePolicyService) scopeOwner(ctx context.Context, callerUserID int64, scopeType string, scopeID int64) (OwnerRef, error) {
	switch scopeType {
	case GrantScopeProject:
		return s.authz.CanAccessProject(ctx, callerUserID, scopeID)
	case GrantScopeWorkspace:
		return s.authz.CanAccessWorkspace(ctx, callerUserID, scopeID)
	default:
		return OwnerRef{}, fmt.Errorf("%w: %q", ErrGrantScopeInvalid, scopeType)
	}
}
