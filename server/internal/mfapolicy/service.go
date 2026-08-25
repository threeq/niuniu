// Package mfapolicy decides whether a team member is REQUIRED to have TOTP
// two-factor authentication enabled, and whether they have satisfied that
// requirement yet.
//
// The MFA mechanism itself (secret provisioning, code validation, backup codes,
// trusted devices) lives in service.MFAService; this package owns only the
// *policy*: given the deployment config and a user's role, must they enroll?
//
// Enforcement is split the same way consent is:
//
//   - this package answers the question,
//   - api.MFAEnrollGuard / MFAEnrollRunGate block requests on the answer,
//   - the SPA's MfaEnrollGate renders the blocking enrollment page.
//
// Deciding server-side (not just in the browser) is what makes the requirement
// real: the AI execution interfaces (MCP tool calls, terminal/agent WebSocket
// streams) are gated on the same answer, so a member who skips the UI still
// cannot use the system.
package mfapolicy

import (
	"context"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Status is the UI-safe policy state for a single user. It drives the SPA
// enrollment gate.
type Status struct {
	// Enforced reports whether this user's role must have MFA enabled.
	Enforced bool `json:"enforced"`
	// Enabled reports whether they currently have it enabled.
	Enabled bool `json:"enabled"`
	// NeedsSetup is Enforced && !Enabled — the blocking condition.
	NeedsSetup bool `json:"needs_setup"`
}

// Service answers "must this user enroll in MFA, and have they?".
type Service struct {
	q *store.Queries
	// authEnabled mirrors config.Auth.Enabled. Personal/embedded edition runs
	// auth-disabled with a single local user and no login screen, so mandatory
	// enrollment is meaningless there and never applies.
	authEnabled bool
	// enforce mirrors config.Auth.MFA.Enforce.
	enforce bool
	// available reports whether the MFA subsystem itself came up (the keyring
	// loaded, so AuthService.MFA is non-nil). If it did not, enrollment is
	// IMPOSSIBLE — nobody could ever satisfy the requirement — so enforcement
	// must stay off or every member would be locked out permanently.
	available bool
	// requiredRoles holds the lower-cased roles that must enroll. Empty means
	// every role.
	requiredRoles []string
}

// NewService constructs the policy service. requiredRoles is normalized here so
// the hot path (RoleRequired) does no allocation.
func NewService(q *store.Queries, authEnabled, enforce, available bool, requiredRoles []string) *Service {
	roles := make([]string, 0, len(requiredRoles))
	for _, r := range requiredRoles {
		if r = strings.ToLower(strings.TrimSpace(r)); r != "" {
			roles = append(roles, r)
		}
	}
	return &Service{
		q:             q,
		authEnabled:   authEnabled,
		enforce:       enforce,
		available:     available,
		requiredRoles: roles,
	}
}

// Active reports whether mandatory enrollment is in force for this deployment
// at all. Used to skip the DB read entirely in the common personal-edition and
// opt-out cases.
func (s *Service) Active() bool {
	return s != nil && s.authEnabled && s.enforce && s.available
}

// RoleRequired reports whether the given `users.role` must enroll. An empty
// requiredRoles list means every role is required.
func (s *Service) RoleRequired(role string) bool {
	if !s.Active() {
		return false
	}
	if len(s.requiredRoles) == 0 {
		return true
	}
	role = strings.ToLower(strings.TrimSpace(role))
	for _, r := range s.requiredRoles {
		if r == role {
			return true
		}
	}
	return false
}

// NeedsSetup reports whether the user is blocked: their role requires MFA and
// they have not enabled it.
//
// Fails CLOSED on a DB read error — a hiccup must not silently open the gate,
// which is the same stance consent.Service.HasConsented takes. The user can
// still reach the enrollment page and their own MFA endpoints, since those are
// on the guard's allowlist.
func (s *Service) NeedsSetup(ctx context.Context, userID int64) bool {
	if !s.Active() {
		return false
	}
	user, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return true
	}
	if !s.RoleRequired(user.Role) {
		return false
	}
	return user.MfaEnabled != 1
}

// Status reports the policy state for the UI. Unlike NeedsSetup it surfaces the
// read error, so the SPA store can fail open on a transient failure rather than
// showing an un-dismissable page (the server-side guard remains the safety net).
func (s *Service) Status(ctx context.Context, userID int64) (Status, error) {
	if !s.Active() {
		return Status{}, nil
	}
	user, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return Status{}, err
	}
	st := Status{
		Enforced: s.RoleRequired(user.Role),
		Enabled:  user.MfaEnabled == 1,
	}
	st.NeedsSetup = st.Enforced && !st.Enabled
	return st, nil
}
