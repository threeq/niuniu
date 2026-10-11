package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Team-edition integration authorization approvals (issue #741).
//
// The #726 integration epic defined a set of privileged "authorization
// actions" but left open who authorizes them. In the personal edition the
// resource owner authorizes themselves; in a team the decision must land on a
// role. This service parks each requested action as a pending approval request
// routed to members holding the required role (never the requester), records
// the decision in org_audit_log, invalidates a departing member's pending
// requests, and escalates requests that time out.
//
// Personal-edition integration flows never call into this service: a
// user-owned resource has no approval queue, so single-user behaviour is
// unchanged.

// ApprovalAction is a privileged integration action from the #726 list that a
// team member may not perform unilaterally.
type ApprovalAction string

const (
	// ApprovalDraftConfirm confirms the impact scope of an integration draft.
	ApprovalDraftConfirm ApprovalAction = "draft_confirm"
	// ApprovalScopeRelax widens a source/provider scope.
	ApprovalScopeRelax ApprovalAction = "scope_relax"
	// ApprovalEnableWrite turns on write access.
	ApprovalEnableWrite ApprovalAction = "enable_write"
	// ApprovalPublish applies/publishes the change.
	ApprovalPublish ApprovalAction = "publish"
)

// RequiredOrgRole returns the minimum org role required to approve the action.
// "project_admin" additionally accepts an explicit project admin.
func (a ApprovalAction) RequiredOrgRole() string {
	switch a {
	case ApprovalDraftConfirm:
		return "project_admin"
	case ApprovalScopeRelax, ApprovalEnableWrite:
		return "admin"
	case ApprovalPublish:
		return "owner"
	default:
		return ""
	}
}

// Valid reports whether the action is a known authorization action.
func (a ApprovalAction) Valid() bool { return a.RequiredOrgRole() != "" }

// ApprovalStatus is the lifecycle state of a request.
type ApprovalStatus string

const (
	ApprovalStatusPending     ApprovalStatus = "pending"
	ApprovalStatusApproved    ApprovalStatus = "approved"
	ApprovalStatusDenied      ApprovalStatus = "denied"
	ApprovalStatusExpired     ApprovalStatus = "expired"
	ApprovalStatusCancelled   ApprovalStatus = "cancelled"
	ApprovalStatusInvalidated ApprovalStatus = "invalidated"
)

// escalateRole returns the role one step up the approval chain. "owner" is
// terminal: a timed-out owner-level request keeps paging the owner rather than
// escalating nowhere.
func escalateRole(role string) string {
	switch role {
	case "project_admin":
		return "admin"
	case "admin":
		return "owner"
	default:
		return "owner"
	}
}

// ApprovalEventKind identifies an approval lifecycle notification.
type ApprovalEventKind string

const (
	ApprovalEventRequested   ApprovalEventKind = "requested"
	ApprovalEventDecided     ApprovalEventKind = "decided"
	ApprovalEventEscalated   ApprovalEventKind = "escalated"
	ApprovalEventInvalidated ApprovalEventKind = "invalidated"
)

// ApprovalEvents receives approval lifecycle notifications. Implementations fan
// out to the notify hub / SSE so approvers see a card. It is optional: a nil
// sink is a no-op (used by unit tests that do not exercise notifications).
type ApprovalEvents interface {
	ApprovalChanged(ctx context.Context, kind ApprovalEventKind, req store.IntegrationApprovalRequest, approverIDs []int64)
}

var (
	// ErrApprovalOrgOnly is returned when Submit is called for a user-owned
	// resource, which has no approval queue.
	ErrApprovalOrgOnly = errors.New("integration approvals apply to org-owned resources only")
	// ErrApprovalNotPermitted means the caller lacks the required role.
	ErrApprovalNotPermitted = errors.New("caller is not permitted to decide this integration approval")
	// ErrApprovalSelfDecide means the requester tried to decide their own request.
	ErrApprovalSelfDecide = errors.New("requester cannot decide their own integration approval")
	// ErrApprovalAlreadyDecided means the request is no longer pending.
	ErrApprovalAlreadyDecided = errors.New("integration approval request is not pending")
)

// IntegrationApprovalService owns the approval queue for org integration actions.
type IntegrationApprovalService struct {
	q      *store.Queries
	authz  *Authz
	events ApprovalEvents
	now    func() time.Time
	ttl    time.Duration
}

// DefaultApprovalTTL is how long a pending request waits before escalation.
const DefaultApprovalTTL = 24 * time.Hour

func NewIntegrationApprovalService(q *store.Queries, authz *Authz, events ApprovalEvents) *IntegrationApprovalService {
	return &IntegrationApprovalService{
		q:      q,
		authz:  authz,
		events: events,
		now:    time.Now,
		ttl:    DefaultApprovalTTL,
	}
}

// SetEvents wires the notification sink after construction (server DI order).
func (s *IntegrationApprovalService) SetEvents(events ApprovalEvents) { s.events = events }

// SubmitApprovalInput describes a requested authorization action.
type SubmitApprovalInput struct {
	OwnerType    string
	OwnerID      int64
	ProjectID    int64 // 0 = no project scope
	Action       ApprovalAction
	SubjectType  string
	SubjectID    int64
	SubjectLabel string
	Diff         string
	Summary      string
	RequestedBy  int64
}

// Submit parks a privileged action as a pending request routed to eligible
// approvers. The requester must be a member of the owning org.
func (s *IntegrationApprovalService) Submit(ctx context.Context, in SubmitApprovalInput) (store.IntegrationApprovalRequest, error) {
	if in.OwnerType != "org" {
		return store.IntegrationApprovalRequest{}, ErrApprovalOrgOnly
	}
	if !in.Action.Valid() {
		return store.IntegrationApprovalRequest{}, fmt.Errorf("unknown approval action %q", in.Action)
	}
	if err := s.authz.EnsureOwnerWritable(ctx, in.RequestedBy, OwnerRef{Type: "org", ID: in.OwnerID}); err != nil {
		return store.IntegrationApprovalRequest{}, err
	}
	// A project-scoped request must reference a project owned by this org:
	// otherwise a project admin of an unrelated project could end up routed a
	// decision for this org's request.
	if in.ProjectID > 0 {
		proj, err := s.q.GetProject(ctx, in.ProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return store.IntegrationApprovalRequest{}, ErrNotFound
		}
		if err != nil {
			return store.IntegrationApprovalRequest{}, err
		}
		if proj.OwnerType != "org" || proj.OwnerID != in.OwnerID {
			return store.IntegrationApprovalRequest{}, ErrForbidden
		}
	}

	params := store.CreateIntegrationApprovalParams{
		OwnerType:    "org",
		OwnerID:      in.OwnerID,
		Action:       string(in.Action),
		SubjectType:  in.SubjectType,
		SubjectID:    in.SubjectID,
		SubjectLabel: in.SubjectLabel,
		Diff:         in.Diff,
		Summary:      in.Summary,
		RequestedBy:  in.RequestedBy,
		RequiredRole: in.Action.RequiredOrgRole(),
		ExpiresAt:    s.now().Add(s.ttl),
	}
	if in.ProjectID > 0 {
		params.ProjectID = sql.NullInt64{Int64: in.ProjectID, Valid: true}
	}

	req, err := s.q.CreateIntegrationApproval(ctx, params)
	if err != nil {
		return store.IntegrationApprovalRequest{}, err
	}

	s.notify(ctx, ApprovalEventRequested, req)
	return req, nil
}

// Get loads a request and authorizes read access: the requester and anyone who
// may decide it (plus org admins) may read it.
func (s *IntegrationApprovalService) Get(ctx context.Context, callerUserID, id int64) (store.IntegrationApprovalRequest, error) {
	req, err := s.q.GetIntegrationApproval(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.IntegrationApprovalRequest{}, ErrNotFound
		}
		return store.IntegrationApprovalRequest{}, err
	}
	if callerUserID == req.RequestedBy {
		return req, nil
	}
	if err := s.authz.CanManageOrg(ctx, callerUserID, req.OwnerID); err == nil {
		return req, nil
	}
	if ok, err := s.canDecide(ctx, callerUserID, req); err == nil && ok {
		return req, nil
	}
	return store.IntegrationApprovalRequest{}, ErrForbidden
}

// Decide approves or denies a pending request. Authorization is enforced here
// (not merely in the UI): the caller must hold the required role and must not
// be the requester.
func (s *IntegrationApprovalService) Decide(ctx context.Context, callerUserID int64, callerLabel string, id int64, approve bool, note string) (store.IntegrationApprovalRequest, error) {
	req, err := s.q.GetIntegrationApproval(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.IntegrationApprovalRequest{}, ErrNotFound
		}
		return store.IntegrationApprovalRequest{}, err
	}
	if req.Status != string(ApprovalStatusPending) {
		return store.IntegrationApprovalRequest{}, ErrApprovalAlreadyDecided
	}
	if callerUserID == req.RequestedBy {
		return store.IntegrationApprovalRequest{}, ErrApprovalSelfDecide
	}
	ok, err := s.canDecide(ctx, callerUserID, req)
	if err != nil {
		return store.IntegrationApprovalRequest{}, err
	}
	if !ok {
		return store.IntegrationApprovalRequest{}, ErrApprovalNotPermitted
	}

	status := string(ApprovalStatusDenied)
	action := "integration.approval.denied"
	if approve {
		status = string(ApprovalStatusApproved)
		action = "integration.approval.approved"
	}

	rows, err := s.q.DecideIntegrationApproval(ctx, store.DecideIntegrationApprovalParams{
		Status:       status,
		DecidedBy:    sql.NullInt64{Int64: callerUserID, Valid: true},
		DecisionNote: note,
		ID:           id,
	})
	if err != nil {
		return store.IntegrationApprovalRequest{}, err
	}
	if rows == 0 {
		// Lost a race with another decider.
		return store.IntegrationApprovalRequest{}, ErrApprovalAlreadyDecided
	}

	updated, err := s.q.GetIntegrationApproval(ctx, id)
	if err != nil {
		return store.IntegrationApprovalRequest{}, err
	}

	s.audit(ctx, action, callerUserID, callerLabel, updated, approvalAuditExtra{
		Decision: status,
		DecidedBy: callerUserID,
		Note:     note,
	})
	s.notify(ctx, ApprovalEventDecided, updated)
	return updated, nil
}

// Cancel withdraws a pending request. Allowed for the requester and for org
// admins (who may clear a stale card).
func (s *IntegrationApprovalService) Cancel(ctx context.Context, callerUserID int64, callerLabel string, id int64) (store.IntegrationApprovalRequest, error) {
	req, err := s.q.GetIntegrationApproval(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.IntegrationApprovalRequest{}, ErrNotFound
		}
		return store.IntegrationApprovalRequest{}, err
	}
	if req.Status != string(ApprovalStatusPending) {
		return store.IntegrationApprovalRequest{}, ErrApprovalAlreadyDecided
	}
	if callerUserID != req.RequestedBy {
		if err := s.authz.CanManageOrg(ctx, callerUserID, req.OwnerID); err != nil {
			return store.IntegrationApprovalRequest{}, ErrForbidden
		}
	}
	rows, err := s.q.CancelIntegrationApproval(ctx, id)
	if err != nil {
		return store.IntegrationApprovalRequest{}, err
	}
	if rows == 0 {
		return store.IntegrationApprovalRequest{}, ErrApprovalAlreadyDecided
	}
	updated, err := s.q.GetIntegrationApproval(ctx, id)
	if err != nil {
		return store.IntegrationApprovalRequest{}, err
	}
	s.audit(ctx, "integration.approval.cancelled", callerUserID, callerLabel, updated, approvalAuditExtra{})
	s.notifyNoApprovers(ctx, ApprovalEventInvalidated, updated)
	return updated, nil
}

// canDecide reports whether callerUserID holds the role required by the request.
func (s *IntegrationApprovalService) canDecide(ctx context.Context, callerUserID int64, req store.IntegrationApprovalRequest) (bool, error) {
	switch req.RequiredRole {
	case "owner":
		return s.authz.CanAdminOrg(ctx, callerUserID, req.OwnerID) == nil, nil
	case "admin":
		return s.authz.CanManageOrg(ctx, callerUserID, req.OwnerID) == nil, nil
	case "project_admin":
		if s.authz.CanManageOrg(ctx, callerUserID, req.OwnerID) == nil {
			return true, nil
		}
		if req.ProjectID.Valid {
			return s.authz.IsProjectAdmin(ctx, callerUserID, req.ProjectID.Int64)
		}
		return false, nil
	default:
		return false, nil
	}
}

// eligibleApprovers lists the member ids that may decide the request (excluding
// the requester), used to route the confirmation card.
func (s *IntegrationApprovalService) eligibleApprovers(ctx context.Context, req store.IntegrationApprovalRequest) ([]int64, error) {
	allowed := map[string]bool{}
	switch req.RequiredRole {
	case "owner":
		allowed["owner"] = true
	case "admin", "project_admin":
		allowed["owner"] = true
		allowed["admin"] = true
	}

	out := make([]int64, 0)
	seen := map[int64]bool{}
	add := func(uid int64) {
		if uid == req.RequestedBy || seen[uid] {
			return
		}
		seen[uid] = true
		out = append(out, uid)
	}

	members, err := s.q.ListOrgMembers(ctx, req.OwnerID)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if allowed[m.Role] {
			add(m.UserID)
		}
	}
	if req.RequiredRole == "project_admin" && req.ProjectID.Valid {
		pms, err := s.q.ListProjectMembers(ctx, req.ProjectID.Int64)
		if err != nil {
			return nil, err
		}
		for _, pm := range pms {
			add(pm.UserID)
		}
	}
	return out, nil
}

// ListPendingForApprover returns the pending requests this user is entitled to
// decide (role-matched, never their own).
func (s *IntegrationApprovalService) ListPendingForApprover(ctx context.Context, callerUserID int64) ([]store.IntegrationApprovalRequest, error) {
	// Resolve the user's orgs and scan each org's pending queue for requests the
	// user is entitled to decide.
	orgIDs, err := s.q.ListOrgIDsForUser(ctx, callerUserID)
	if err != nil {
		return nil, err
	}
	out := make([]store.IntegrationApprovalRequest, 0)
	for _, orgID := range orgIDs {
		pending, err := s.q.ListPendingIntegrationApprovalsByOwner(ctx, orgID)
		if err != nil {
			return nil, err
		}
		for _, req := range pending {
			if req.RequestedBy == callerUserID {
				continue
			}
			ok, err := s.canDecide(ctx, callerUserID, req)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, req)
			}
		}
	}
	return out, nil
}

// ListByOwner returns an org's approval queue (owner/admin only). status=="" returns all.
func (s *IntegrationApprovalService) ListByOwner(ctx context.Context, callerUserID, orgID int64, status string) ([]store.IntegrationApprovalRequest, error) {
	if err := s.authz.CanManageOrg(ctx, callerUserID, orgID); err != nil {
		return nil, err
	}
	const limit = 200
	if status == "" {
		return s.q.ListIntegrationApprovalsByOwner(ctx, store.ListIntegrationApprovalsByOwnerParams{
			OwnerType: "org", OwnerID: orgID, Limit: limit,
		})
	}
	return s.q.ListIntegrationApprovalsByOwnerStatus(ctx, store.ListIntegrationApprovalsByOwnerStatusParams{
		OwnerType: "org", OwnerID: orgID, Status: status, Limit: limit,
	})
}

// InvalidateByRequester marks every pending request the departing member raised
// in orgID as invalidated. Returns the number of rows affected. Called from
// OrgService.RemoveMember.
func (s *IntegrationApprovalService) InvalidateByRequester(ctx context.Context, orgID, requesterUserID int64) (int64, error) {
	pending, err := s.q.ListPendingIntegrationApprovalsByOwner(ctx, orgID)
	if err != nil {
		return 0, err
	}
	var mine []store.IntegrationApprovalRequest
	for _, req := range pending {
		if req.RequestedBy == requesterUserID {
			mine = append(mine, req)
		}
	}
	if len(mine) == 0 {
		return 0, nil
	}
	n, err := s.q.InvalidateIntegrationApprovalsByRequesterInOwner(ctx, store.InvalidateIntegrationApprovalsByRequesterInOwnerParams{
		OwnerID:     orgID,
		RequestedBy: requesterUserID,
	})
	if err != nil {
		return 0, err
	}
	for _, req := range mine {
		req.Status = string(ApprovalStatusInvalidated)
		s.audit(ctx, "integration.approval.invalidated", 0, "", req, approvalAuditExtra{})
		s.notifyNoApprovers(ctx, ApprovalEventInvalidated, req)
	}
	return n, nil
}

// EscalateExpired finds pending requests past their deadline, bumps each to the
// next role up the chain (owner is terminal — it keeps paging the owner), audits
// and notifies. Returns the number of requests escalated. Called by the periodic
// maintenance loop.
func (s *IntegrationApprovalService) EscalateExpired(ctx context.Context, now time.Time) (int64, error) {
	const batch = 200
	expired, err := s.q.ListExpiredPendingIntegrationApprovals(ctx, store.ListExpiredPendingIntegrationApprovalsParams{
		ExpiresAt: now, Limit: batch,
	})
	if err != nil {
		return 0, err
	}
	var affected int64
	for _, req := range expired {
		next := escalateRole(req.RequiredRole)
		n, err := s.q.EscalateIntegrationApproval(ctx, store.EscalateIntegrationApprovalParams{
			RequiredRole: next,
			ExpiresAt:    now.Add(s.ttl),
			ID:           req.ID,
		})
		if err != nil {
			return affected, err
		}
		if n == 0 {
			continue
		}
		affected += n
		updated, err := s.q.GetIntegrationApproval(ctx, req.ID)
		if err != nil {
			return affected, err
		}
		s.audit(ctx, "integration.approval.escalated", 0, "", updated, approvalAuditExtra{
			RequiredRole: next,
		})
		s.notify(ctx, ApprovalEventEscalated, updated)
	}
	return affected, nil
}

// ---- audit + notify helpers --------------------------------------------------

type approvalAuditExtra struct {
	Decision     string
	DecidedBy    int64
	Note         string
	RequiredRole string
}

type approvalAuditPayload struct {
	Action          string `json:"action"`
	SubjectType     string `json:"subject_type"`
	SubjectID       int64  `json:"subject_id"`
	SubjectLabel    string `json:"subject_label"`
	RequestedBy     int64  `json:"requested_by"`
	Diff            string `json:"diff,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Decision        string `json:"decision,omitempty"`
	DecidedBy       int64  `json:"decided_by,omitempty"`
	Note            string `json:"note,omitempty"`
	RequiredRole    string `json:"required_role,omitempty"`
	EscalationCount int64  `json:"escalation_count,omitempty"`
}

func (s *IntegrationApprovalService) audit(ctx context.Context, action string, actorUserID int64, actorLabel string, req store.IntegrationApprovalRequest, extra approvalAuditExtra) {
	payload := approvalAuditPayload{
		Action:          req.Action,
		SubjectType:     req.SubjectType,
		SubjectID:       req.SubjectID,
		SubjectLabel:    req.SubjectLabel,
		RequestedBy:     req.RequestedBy,
		Diff:            req.Diff,
		Summary:         req.Summary,
		Decision:        extra.Decision,
		DecidedBy:       extra.DecidedBy,
		Note:            extra.Note,
		RequiredRole:    extra.RequiredRole,
		EscalationCount: req.EscalationCount,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{}`)
	}
	_ = s.q.AppendOrgAudit(ctx, store.AppendOrgAuditParams{
		OrgID:       req.OwnerID,
		ActorUserID: sql.NullInt64{Int64: actorUserID, Valid: actorUserID != 0},
		ActorLabel:  actorLabel,
		Action:      action,
		TargetType:  "integration_approval",
		TargetID:    req.ID,
		Payload:     string(raw),
	})
}

func (s *IntegrationApprovalService) notify(ctx context.Context, kind ApprovalEventKind, req store.IntegrationApprovalRequest) {
	if s.events == nil {
		return
	}
	approvers, err := s.eligibleApprovers(ctx, req)
	if err != nil {
		approvers = nil
	}
	s.events.ApprovalChanged(ctx, kind, req, approvers)
}

func (s *IntegrationApprovalService) notifyNoApprovers(ctx context.Context, kind ApprovalEventKind, req store.IntegrationApprovalRequest) {
	if s.events == nil {
		return
	}
	s.events.ApprovalChanged(ctx, kind, req, nil)
}
