package service

import (
	"context"
	"errors"
)

// MemberIntegrationRevoker reclaims a departing member's integration-side
// state: pending approvals they raised, project-admin grants, org credentials
// they created, and unused credential-submission links they minted. Wired into
// OrgService.RemoveMember so a removed/offboarded member leaves nothing live
// behind (issue #741).
//
// Best-effort by contract: an error is returned for logging, but member
// removal itself has already committed and must not be rolled back.
type MemberIntegrationRevoker interface {
	RevokeMemberIntegrationArtifacts(ctx context.Context, orgID, userID int64) error
}

// IntegrationMemberRevoker is the default composite implementation. Any nil
// collaborator is skipped (personal edition / partial wiring).
type IntegrationMemberRevoker struct {
	Approvals   *IntegrationApprovalService
	Projects    *ProjectMemberService
	Credentials *ExternalCredentialService
	Onboarding  *IMBotService
	// CredLinks reclaims pending GENERIC credential-onboarding links (provider
	// credentials / data sources, issue #727) the member minted in the org
	// (issue #742). Distinct from the IM-Bot-specific Onboarding above.
	CredLinks *CredentialOnboardingService
}

// RevokeMemberIntegrationArtifacts invalidates every integration artifact the
// user owns within orgID. Errors are joined and returned for logging.
func (r *IntegrationMemberRevoker) RevokeMemberIntegrationArtifacts(ctx context.Context, orgID, userID int64) error {
	var errs []error
	if r.Approvals != nil {
		if _, err := r.Approvals.InvalidateByRequester(ctx, orgID, userID); err != nil {
			errs = append(errs, err)
		}
	}
	if r.Projects != nil {
		if _, err := r.Projects.RemoveAllForUserInOrg(ctx, orgID, userID); err != nil {
			errs = append(errs, err)
		}
	}
	if r.Credentials != nil {
		if _, err := r.Credentials.RevokeAllForUserInOwner(ctx, "org", orgID, userID); err != nil {
			errs = append(errs, err)
		}
	}
	if r.Onboarding != nil {
		if _, err := r.Onboarding.RevokeUnusedOnboardingTokensByIssuerInOrg(ctx, orgID, userID); err != nil {
			errs = append(errs, err)
		}
	}
	if r.CredLinks != nil {
		if _, err := r.CredLinks.RevokeUnusedOnboardingTokensByIssuerInOrg(ctx, orgID, userID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
