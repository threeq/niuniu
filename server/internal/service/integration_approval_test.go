package service

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- test double for ApprovalEvents ------------------------------------------

type approvalEventCall struct {
	Kind      ApprovalEventKind
	RequestID int64
	Approvers []int64
}

type recordingApprovalEvents struct {
	mu    sync.Mutex
	calls []approvalEventCall
}

func (r *recordingApprovalEvents) ApprovalChanged(_ context.Context, kind ApprovalEventKind, req store.IntegrationApprovalRequest, approverIDs []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, approvalEventCall{Kind: kind, RequestID: req.ID, Approvers: append([]int64(nil), approverIDs...)})
}

func (r *recordingApprovalEvents) kinds() []ApprovalEventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ApprovalEventKind, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, c.Kind)
	}
	return out
}

func (r *recordingApprovalEvents) lastApprovers() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1].Approvers
}

// ---- fixtures ----------------------------------------------------------------

type approvalFixture struct {
	svc     *IntegrationApprovalService
	projSvc *ProjectMemberService
	orgSvc  *OrgService
	events  *recordingApprovalEvents
	db      *sql.DB
	q       *store.Queries

	orgID    int64
	ownerID  int64 // org role owner
	adminID  int64 // org role admin
	memberID int64 // org role member
}

// newApprovalFixture builds an org with three members at distinct org roles and
// an IntegrationApprovalService wired to a recording event sink. Uses
// store.NewQueries so the same `?` SQL routes through the PG rewriter.
func newApprovalFixture(t *testing.T, drv string) *approvalFixture {
	t.Helper()
	db := openOrgTestDBForDriver(t, drv)
	q := store.NewQueries(db)
	authz := NewAuthz(q, db)
	orgSvc := NewOrgService(q, db, authz)
	events := &recordingApprovalEvents{}
	svc := NewIntegrationApprovalService(q, authz, events)
	projSvc := NewProjectMemberService(q, authz)

	ownerID := newOrgTestUserDriver(t, db, "ap-owner", "member")
	adminID := newOrgTestUserDriver(t, db, "ap-admin", "member")
	memberID := newOrgTestUserDriver(t, db, "ap-member", "member")

	ctx := context.Background()
	org, err := orgSvc.Create(ctx, ownerID, "ap-owner", "Acme", "acme-"+drv, "")
	require.NoError(t, err)
	require.NoError(t, orgSvc.AddMember(ctx, ownerID, "ap-owner", org.ID, adminID, "admin"))
	require.NoError(t, orgSvc.AddMember(ctx, ownerID, "ap-owner", org.ID, memberID, "member"))

	return &approvalFixture{
		svc: svc, projSvc: projSvc, orgSvc: orgSvc, events: events, db: db, q: q,
		orgID: org.ID, ownerID: ownerID, adminID: adminID, memberID: memberID,
	}
}

func (f *approvalFixture) submit(t *testing.T, action ApprovalAction, by int64, projectID int64) store.IntegrationApprovalRequest {
	t.Helper()
	req, err := f.svc.Submit(context.Background(), SubmitApprovalInput{
		OwnerType:    "org",
		OwnerID:      f.orgID,
		ProjectID:    projectID,
		Action:       action,
		SubjectType:  "data_source",
		SubjectID:    42,
		SubjectLabel: "orders-db",
		Diff:         `{"scope_config":{"tables_allow":["orders","customers"]}}`,
		Summary:      "widen scope to orders+customers",
		RequestedBy:  by,
	})
	require.NoError(t, err)
	return req
}

// ---- required-role matrix ----------------------------------------------------

func TestIntegrationApprovalRequiredRoleMatrix(t *testing.T) {
	cases := map[ApprovalAction]string{
		ApprovalDraftConfirm: "project_admin",
		ApprovalScopeRelax:   "admin",
		ApprovalEnableWrite:  "admin",
		ApprovalPublish:      "owner",
	}
	for action, want := range cases {
		assert.Equal(t, want, string(action.RequiredOrgRole()), "action %s", action)
	}
	assert.False(t, ApprovalAction("bogus").Valid())
	assert.True(t, ApprovalPublish.Valid())
}

func TestIntegrationApprovalSubmitComputesRequiredRole(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)

		req := f.submit(t, ApprovalPublish, f.memberID, 0)
		assert.Equal(t, "owner", req.RequiredRole)
		assert.Equal(t, "pending", req.Status)
		assert.Equal(t, f.memberID, req.RequestedBy)
		assert.True(t, req.ExpiresAt.After(time.Now()), "expiry must be in the future")
	})
}

func TestIntegrationApprovalSubmitRejectsNonOrgOwner(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		_, err := f.svc.Submit(context.Background(), SubmitApprovalInput{
			OwnerType: "user", OwnerID: f.memberID, Action: ApprovalPublish, RequestedBy: f.memberID,
		})
		assert.ErrorIs(t, err, ErrApprovalOrgOnly)
	})
}

func TestIntegrationApprovalSubmitRejectsNonMember(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		outsider := newOrgTestUserDriver(t, f.db, "ap-outsider", "member")
		_, err := f.svc.Submit(context.Background(), SubmitApprovalInput{
			OwnerType: "org", OwnerID: f.orgID, Action: ApprovalScopeRelax, RequestedBy: outsider,
		})
		assert.ErrorIs(t, err, ErrForbidden)
	})
}

func TestIntegrationApprovalSubmitRejectsForeignProject(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		// A project owned by a DIFFERENT org.
		otherOrgProject := newApprovalTestProject(t, f.db, f.orgID+999, "foreign")
		_, err := f.svc.Submit(context.Background(), SubmitApprovalInput{
			OwnerType: "org", OwnerID: f.orgID, ProjectID: otherOrgProject,
			Action: ApprovalDraftConfirm, RequestedBy: f.memberID,
		})
		assert.ErrorIs(t, err, ErrForbidden)
	})
}

// ACCEPTANCE: the confirmation card is routed to role holders, never the
// requester.
func TestIntegrationApprovalRoutesToRoleHoldersExcludingRequester(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)

		// scope_relax needs admin → owner + admin are routed (member is not).
		f.submit(t, ApprovalScopeRelax, f.memberID, 0)
		assert.ElementsMatch(t, []int64{f.ownerID, f.adminID}, f.events.lastApprovers())
		assert.NotContains(t, f.events.lastApprovers(), f.memberID)

		// publish needs owner → only the owner.
		f.submit(t, ApprovalPublish, f.memberID, 0)
		assert.Equal(t, []int64{f.ownerID}, f.events.lastApprovers())

		// The requester is never routed their own card, even when they hold the role.
		f.submit(t, ApprovalScopeRelax, f.adminID, 0)
		assert.NotContains(t, f.events.lastApprovers(), f.adminID)
		assert.Equal(t, []int64{f.ownerID}, f.events.lastApprovers())
	})
}

// ---- decide: authorization gate (ACCEPTANCE: 无权限成员无法批准) ---------------

func TestIntegrationApprovalAdminApprovesMemberRequest(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		req := f.submit(t, ApprovalDraftConfirm, f.memberID, 0)

		got, err := f.svc.Decide(context.Background(), f.adminID, "ap-admin", req.ID, true, "looks scoped")
		require.NoError(t, err)
		assert.Equal(t, "approved", got.Status)
		assert.True(t, got.DecidedBy.Valid)
		assert.Equal(t, f.adminID, got.DecidedBy.Int64)
		assert.Equal(t, "looks scoped", got.DecisionNote)

		// Approval must be audit-logged for the org (who / when / what diff).
		var n int
		require.NoError(t, f.db.QueryRow(
			`SELECT COUNT(*) FROM org_audit_log WHERE org_id = ? AND action = 'integration.approval.approved' AND target_id = ?`,
			f.orgID, req.ID).Scan(&n))
		assert.Equal(t, 1, n, "approval decision must be written to org_audit_log")

		var payload string
		require.NoError(t, f.db.QueryRow(
			`SELECT payload FROM org_audit_log WHERE org_id = ? AND action = 'integration.approval.approved' AND target_id = ?`,
			f.orgID, req.ID).Scan(&payload))
		assert.Contains(t, payload, "data_source")
		assert.Contains(t, payload, "customers", "audit must record the approved diff")
	})
}

func TestIntegrationApprovalPlainMemberCannotApprove(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		// member asks for a scope relaxation; another plain member must NOT be
		// able to approve it — the API/service layer rejects, not just the UI.
		otherMember := newOrgTestUserDriver(t, f.db, "ap-member2", "member")
		require.NoError(t, f.orgSvc.AddMember(context.Background(), f.ownerID, "ap-owner", f.orgID, otherMember, "member"))

		req := f.submit(t, ApprovalScopeRelax, f.memberID, 0)
		_, err := f.svc.Decide(context.Background(), otherMember, "ap-member2", req.ID, true, "")
		assert.ErrorIs(t, err, ErrApprovalNotPermitted)

		// still pending — a rejected decision must not mutate the row
		loaded, err := f.svc.Get(context.Background(), f.adminID, req.ID)
		require.NoError(t, err)
		assert.Equal(t, "pending", loaded.Status)
	})
}

func TestIntegrationApprovalRequesterCannotSelfApprove(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		req := f.submit(t, ApprovalEnableWrite, f.adminID, 0)

		_, err := f.svc.Decide(context.Background(), f.adminID, "ap-admin", req.ID, true, "")
		assert.ErrorIs(t, err, ErrApprovalSelfDecide)
	})
}

func TestIntegrationApprovalPublishRequiresOwner(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		req := f.submit(t, ApprovalPublish, f.memberID, 0)

		// admin is not enough for publish
		_, err := f.svc.Decide(context.Background(), f.adminID, "ap-admin", req.ID, true, "")
		assert.ErrorIs(t, err, ErrApprovalNotPermitted)

		// owner can
		got, err := f.svc.Decide(context.Background(), f.ownerID, "ap-owner", req.ID, true, "")
		require.NoError(t, err)
		assert.Equal(t, "approved", got.Status)
	})
}

func TestIntegrationApprovalDeny(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		req := f.submit(t, ApprovalEnableWrite, f.memberID, 0)

		got, err := f.svc.Decide(context.Background(), f.adminID, "ap-admin", req.ID, false, "too broad")
		require.NoError(t, err)
		assert.Equal(t, "denied", got.Status)

		var n int
		require.NoError(t, f.db.QueryRow(
			`SELECT COUNT(*) FROM org_audit_log WHERE org_id = ? AND action = 'integration.approval.denied' AND target_id = ?`,
			f.orgID, req.ID).Scan(&n))
		assert.Equal(t, 1, n)
	})
}

func TestIntegrationApprovalDecideTwiceRejected(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		req := f.submit(t, ApprovalDraftConfirm, f.memberID, 0)
		_, err := f.svc.Decide(context.Background(), f.adminID, "ap-admin", req.ID, true, "")
		require.NoError(t, err)

		_, err = f.svc.Decide(context.Background(), f.ownerID, "ap-owner", req.ID, false, "")
		assert.ErrorIs(t, err, ErrApprovalAlreadyDecided)
	})
}

// ---- project-level role ------------------------------------------------------

func TestIntegrationApprovalProjectAdminCanApproveDraftConfirm(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		pm := newOrgTestUserDriver(t, f.db, "ap-pm", "member")
		require.NoError(t, f.orgSvc.AddMember(context.Background(), f.ownerID, "ap-owner", f.orgID, pm, "member"))

		projID := newApprovalTestProject(t, f.db, f.orgID, "proj-1")
		require.NoError(t, f.projSvc.Grant(context.Background(), f.ownerID, projID, pm))

		req := f.submit(t, ApprovalDraftConfirm, f.memberID, projID)
		got, err := f.svc.Decide(context.Background(), pm, "ap-pm", req.ID, true, "")
		require.NoError(t, err)
		assert.Equal(t, "approved", got.Status)
	})
}

func TestIntegrationApprovalProjectAdminCannotApprovePublish(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		pm := newOrgTestUserDriver(t, f.db, "ap-pm2", "member")
		require.NoError(t, f.orgSvc.AddMember(context.Background(), f.ownerID, "ap-owner", f.orgID, pm, "member"))
		projID := newApprovalTestProject(t, f.db, f.orgID, "proj-2")
		require.NoError(t, f.projSvc.Grant(context.Background(), f.ownerID, projID, pm))

		req := f.submit(t, ApprovalPublish, f.memberID, projID)
		_, err := f.svc.Decide(context.Background(), pm, "ap-pm2", req.ID, true, "")
		assert.ErrorIs(t, err, ErrApprovalNotPermitted)
	})
}

// ---- member lifecycle (ACCEPTANCE: 移除成员后待批项自动失效) -------------------

func TestIntegrationApprovalInvalidateByRequester(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		mine := f.submit(t, ApprovalScopeRelax, f.memberID, 0)
		other := f.submit(t, ApprovalScopeRelax, f.adminID, 0)

		n, err := f.svc.InvalidateByRequester(context.Background(), f.orgID, f.memberID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		got, err := f.svc.Get(context.Background(), f.adminID, mine.ID)
		require.NoError(t, err)
		assert.Equal(t, "invalidated", got.Status)

		// a different requester's pending row is untouched
		kept, err := f.svc.Get(context.Background(), f.adminID, other.ID)
		require.NoError(t, err)
		assert.Equal(t, "pending", kept.Status)
	})
}

// ---- timeout escalation (ACCEPTANCE: 审批超时升级) ---------------------------

func TestIntegrationApprovalEscalateExpired(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		req := f.submit(t, ApprovalScopeRelax, f.memberID, 0)
		require.Equal(t, "admin", req.RequiredRole)

		// force the request past its deadline
		_, err := f.db.Exec(`UPDATE integration_approval_requests SET expires_at = ? WHERE id = ?`,
			time.Now().Add(-time.Minute), req.ID)
		require.NoError(t, err)

		n, err := f.svc.EscalateExpired(context.Background(), time.Now())
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		got, err := f.svc.Get(context.Background(), f.ownerID, req.ID)
		require.NoError(t, err)
		assert.Equal(t, "pending", got.Status, "escalation must not drop the request")
		assert.Equal(t, "owner", got.RequiredRole, "admin-level request escalates to owner")
		assert.Equal(t, int64(1), got.EscalationCount)
		assert.True(t, got.ExpiresAt.After(time.Now()), "escalation refreshes the deadline")

		assert.Contains(t, f.events.kinds(), ApprovalEventEscalated)
		var n2 int
		require.NoError(t, f.db.QueryRow(
			`SELECT COUNT(*) FROM org_audit_log WHERE org_id = ? AND action = 'integration.approval.escalated' AND target_id = ?`,
			f.orgID, req.ID).Scan(&n2))
		assert.Equal(t, 1, n2, "escalation must be audited")
	})
}

// Exercises the production clock path: Submit writes expires_at with the
// service clock, and EscalateExpired is handed a later wall-clock instant.
func TestIntegrationApprovalEscalateAfterTTL(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		base := time.Now()
		f.svc.now = func() time.Time { return base }

		req := f.submit(t, ApprovalScopeRelax, f.memberID, 0)
		assert.WithinDuration(t, base.Add(DefaultApprovalTTL), req.ExpiresAt, 2*time.Second)

		n, err := f.svc.EscalateExpired(context.Background(), base.Add(DefaultApprovalTTL+time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		got, err := f.svc.Get(context.Background(), f.ownerID, req.ID)
		require.NoError(t, err)
		assert.Equal(t, "owner", got.RequiredRole)
	})
}

func TestIntegrationApprovalEscalateOwnerStaysTerminal(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		req := f.submit(t, ApprovalPublish, f.memberID, 0)

		_, err := f.db.Exec(`UPDATE integration_approval_requests SET expires_at = ? WHERE id = ?`,
			time.Now().Add(-time.Minute), req.ID)
		require.NoError(t, err)

		_, err = f.svc.EscalateExpired(context.Background(), time.Now())
		require.NoError(t, err)

		got, err := f.svc.Get(context.Background(), f.ownerID, req.ID)
		require.NoError(t, err)
		assert.Equal(t, "owner", got.RequiredRole, "owner is the top of the chain")
		assert.Equal(t, int64(1), got.EscalationCount, "repeated timeout still reminds the owner")
	})
}

// ---- list --------------------------------------------------------------------

func TestIntegrationApprovalListPendingForUserExcludesOwn(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		_ = f.submit(t, ApprovalScopeRelax, f.memberID, 0) // approvable by admin
		_ = f.submit(t, ApprovalScopeRelax, f.adminID, 0)  // admin's own — not approvable by admin

		list, err := f.svc.ListPendingForApprover(context.Background(), f.adminID)
		require.NoError(t, err)
		require.Len(t, list, 1)
		assert.Equal(t, f.memberID, list[0].RequestedBy)

		// a plain member can approve nothing
		none, err := f.svc.ListPendingForApprover(context.Background(), f.memberID)
		require.NoError(t, err)
		assert.Empty(t, none)
	})
}

func TestIntegrationApprovalListByOwnerRequiresAdmin(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		_ = f.submit(t, ApprovalScopeRelax, f.memberID, 0)

		_, err := f.svc.ListByOwner(context.Background(), f.memberID, f.orgID, "")
		assert.ErrorIs(t, err, ErrForbidden)

		list, err := f.svc.ListByOwner(context.Background(), f.adminID, f.orgID, "")
		require.NoError(t, err)
		assert.Len(t, list, 1)
	})
}

// newApprovalTestProject inserts an org-owned project and returns its id.
func newApprovalTestProject(t *testing.T, db *sql.DB, orgID int64, name string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, store.Wrap(db).QueryRowContext(context.Background(),
		`INSERT INTO projects (owner_type, owner_id, name) VALUES ('org', ?, ?) RETURNING id`,
		orgID, name).Scan(&id))
	return id
}
