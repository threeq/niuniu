package opsbot

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/store"
)

type fakeDirectorAccounts struct {
	bots []store.OpsBot
	err  error
}

func (f *fakeDirectorAccounts) Accounts(_ context.Context, owner OwnerRef, _ int64) ([]store.OpsBot, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]store.OpsBot, 0, len(f.bots))
	for _, bot := range f.bots {
		if bot.OwnerType != owner.Type || bot.OwnerID != owner.ID {
			continue
		}
		out = append(out, bot)
	}
	return out, nil
}

type fakeDirectorPlanner struct {
	created map[int64][]int64
	errs    map[int64]error
	calls   []BotRef
}

func (f *fakeDirectorPlanner) PlanWeek(_ context.Context, ref BotRef, _ string) ([]int64, error) {
	f.calls = append(f.calls, ref)
	if err := f.errs[ref.ID]; err != nil {
		return nil, err
	}
	return f.created[ref.ID], nil
}

type rescheduleCall struct {
	owner     OwnerRef
	issueID   int64
	startDate string
	dueDate   string
}

type fakeDirectorCalendar struct {
	entries       []CalendarEntry
	calls         []rescheduleCall
	err           error
	rescheduleErr error
}

func (f *fakeDirectorCalendar) List(context.Context, CalendarFilter) ([]CalendarEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]CalendarEntry(nil), f.entries...), nil
}

func (f *fakeDirectorCalendar) Reschedule(_ context.Context, owner OwnerRef, issueID int64, startDate, dueDate string) error {
	f.calls = append(f.calls, rescheduleCall{owner: owner, issueID: issueID, startDate: startDate, dueDate: dueDate})
	if f.rescheduleErr != nil {
		return f.rescheduleErr
	}
	for i := range f.entries {
		if f.entries[i].IssueID == issueID {
			f.entries[i].StartDate = startDate
			f.entries[i].DueDate = dueDate
		}
	}
	return nil
}

type fakeProxyApprover struct {
	lastDecision Decision
	lastApproval int64
	err          error
	getErr       error
	botID        int64
	calls        int
}

func (f *fakeProxyApprover) Get(_ context.Context, approvalID int64) (ApprovalRequest, error) {
	if f.getErr != nil {
		return ApprovalRequest{}, f.getErr
	}
	return ApprovalRequest{
		ID:           approvalID,
		Status:       ApprovalStatusPending,
		MetadataJSON: `{"bot_id":` + strconv.FormatInt(f.botID, 10) + `}`,
	}, nil
}

func (f *fakeProxyApprover) Decide(_ context.Context, approvalID int64, d Decision) (ApprovalRequest, error) {
	f.calls++
	f.lastApproval = approvalID
	f.lastDecision = d
	return ApprovalRequest{ID: approvalID, Status: ApprovalStatusAnswered}, f.err
}

func testAccounts() []store.OpsBot {
	return []store.OpsBot{
		{ID: 1, Role: RoleAccount, OwnerType: defaultOwnerType, OwnerID: 2, DisplayName: "账号 A"},
		{ID: 2, Role: RoleAccount, OwnerType: defaultOwnerType, OwnerID: 2, DisplayName: "账号 B"},
	}
}

func newTestDirector(t *testing.T) *Director {
	t.Helper()
	saved := map[int64]float64{}
	return &Director{
		Owner:      OwnerRef{Type: defaultOwnerType, ID: 2},
		BotID:      7,
		OperatorID: 1,
		Bots:       &fakeDirectorAccounts{bots: testAccounts()},
		Budget: &Budget{
			SaveLimit: func(_ context.Context, botID int64, limit float64, _ string) error {
				saved[botID] = limit
				return nil
			},
			Resume: func(context.Context, int64) error { return nil },
		},
		Approvals: &fakeProxyApprover{botID: 1},
	}
}

func TestDirectorDedupesAcrossAccounts(t *testing.T) {
	d := newTestDirector(t)
	topics := []Topic{
		{BotID: 1, Title: "秋季护肤三步"},
		{BotID: 2, Title: "秋季护肤 3 步"},
		{BotID: 3, Title: "通勤穿搭"},
	}
	kept, dropped := d.DedupeTopics(topics, 0.8)
	require.Len(t, kept, 2)
	require.Len(t, dropped, 1)
	require.Equal(t, int64(1), kept[0].BotID)
	require.Equal(t, int64(3), kept[1].BotID)
	require.Equal(t, int64(2), dropped[0].BotID)
	require.Equal(t, "秋季护肤三步", dropped[0].DuplicateOf)
}

func TestDirectorDedupeKeepsBestGoalMatch(t *testing.T) {
	d := newTestDirector(t)
	topics := []Topic{
		{BotID: 1, Title: "秋季护肤三步", GoalMatch: 0.2},
		{BotID: 2, Title: "秋季护肤3步", GoalMatch: 0.9},
		{BotID: 3, Title: "秋季护肤三步", GoalMatch: 0.5},
	}
	kept, dropped := d.DedupeTopics(topics, 0)
	require.Len(t, kept, 1)
	require.Equal(t, int64(2), kept[0].BotID)
	require.Len(t, dropped, 2)
	require.Equal(t, int64(1), dropped[0].BotID)
	require.Equal(t, int64(3), dropped[1].BotID)
}

func TestDirectorDedupeTransitiveGroup(t *testing.T) {
	d := newTestDirector(t)
	topics := []Topic{
		{BotID: 1, Title: "秋季护肤三步"},
		{BotID: 2, Title: "秋季护肤 三步"},
		{BotID: 3, Title: "秋季护肤3步"},
	}
	kept, dropped := d.DedupeTopics(topics, 0.8)
	require.Len(t, kept, 1)
	require.Len(t, dropped, 2)
}

func TestDirectorAllocatesBudgetAndProxiesApproval(t *testing.T) {
	d := newTestDirector(t)
	require.NoError(t, d.AllocateBudget(context.Background(), map[int64]float64{1: 100, 2: 200}))
	require.Equal(t, float64(100), d.budget.limits[1])
	require.Equal(t, float64(200), d.budget.limits[2])
	require.NoError(t, d.ProxyApprove(context.Background(), 9, 1, "总监代审"))
	require.Equal(t, int64(9), d.approvals.lastProxyFor)

	approver := d.Approvals.(*fakeProxyApprover)
	require.Equal(t, int64(1), approver.lastApproval)
	require.Equal(t, int64(1), approver.lastDecision.OperatorID)
	require.Equal(t, int64(9), approver.lastDecision.ProxyFor)
	require.Equal(t, ActionApproved, approver.lastDecision.Action)
	require.Equal(t, "总监代审", approver.lastDecision.Reason)
	require.Len(t, d.ProxyApprovals(), 1)
	require.Equal(t, int64(9), d.ProxyApprovals()[0].ProxyFor)
}

func TestDirectorProxyApproveRequiresReasonAndOperator(t *testing.T) {
	d := newTestDirector(t)
	require.ErrorIs(t, d.ProxyApprove(context.Background(), 9, 1, "  "), ErrInvalidInput)

	d.OperatorID = 0
	require.ErrorIs(t, d.ProxyApprove(context.Background(), 9, 1, "原因"), ErrDirectorNotConfigured)

	approver := d.Approvals.(*fakeProxyApprover)
	require.Zero(t, approver.calls)
}

func TestDirectorAllocateBudgetRejectsInvalidLimit(t *testing.T) {
	d := newTestDirector(t)
	require.ErrorIs(t, d.AllocateBudget(context.Background(), map[int64]float64{1: -1}), ErrInvalidBudgetLimit)
}

func TestDirectorAllocateBudgetPersistsAndNotifies(t *testing.T) {
	var saved map[int64]float64
	var resumed []int64
	var notified []int64
	d := newTestDirector(t)
	d.Budget = &Budget{
		SaveLimit: func(_ context.Context, botID int64, limit float64, _ string) error {
			if saved == nil {
				saved = map[int64]float64{}
			}
			saved[botID] = limit
			return nil
		},
		Resume: func(_ context.Context, botID int64) error {
			resumed = append(resumed, botID)
			return nil
		},
	}
	d.Messenger = directorMessengerFunc(func(_ context.Context, botID int64, _ string) error {
		notified = append(notified, botID)
		return nil
	})

	require.NoError(t, d.AllocateBudget(context.Background(), map[int64]float64{1: 100, 2: 200}))
	require.Equal(t, map[int64]float64{1: 100, 2: 200}, saved)
	require.ElementsMatch(t, []int64{1, 2}, resumed)
	require.ElementsMatch(t, []int64{1, 2}, notified)

	allocations := d.BudgetAllocations()
	require.Len(t, allocations, 2)
	require.Equal(t, int64(1), allocations[0].OperatorID)
	require.Equal(t, defaultDirectorCurrency, allocations[0].Currency)
}

func TestDirectorAllocateBudgetRejectsBotOutsideScope(t *testing.T) {
	d := newTestDirector(t)
	err := d.AllocateBudget(context.Background(), map[int64]float64{99: 100})
	require.ErrorIs(t, err, ErrNotFound)
	require.Empty(t, d.BudgetAllocations())
}

func TestDirectorAllocateByWeight(t *testing.T) {
	d := newTestDirector(t)
	d.Bots = &fakeDirectorAccounts{bots: []store.OpsBot{
		{ID: 1, Role: RoleAccount, OwnerType: defaultOwnerType, OwnerID: 2, GoalsJson: `{"weight":3}`},
		{ID: 2, Role: RoleAccount, OwnerType: defaultOwnerType, OwnerID: 2, GoalsJson: `{"weight":1}`},
	}}

	limits, err := d.AllocateByWeight(context.Background(), 400)
	require.NoError(t, err)
	require.Equal(t, float64(300), limits[1])
	require.Equal(t, float64(100), limits[2])
	require.Equal(t, float64(300), d.budget.limits[1])
}

func TestDirectorAllocationWeight(t *testing.T) {
	require.Equal(t, float64(1), AllocationWeight(store.OpsBot{}))
	require.Equal(t, float64(3), AllocationWeight(store.OpsBot{GoalsJson: `{"budget_weight":"3"}`}))
	require.Equal(t, float64(0.5), AllocationWeight(store.OpsBot{Status: StatusPaused}))
	require.Equal(t, float64(0), AllocationWeight(store.OpsBot{Status: StatusDisabled}))
}

func TestDirectorPlanAllAggregatesCalendarAndReschedulesConflict(t *testing.T) {
	accounts := &fakeDirectorAccounts{bots: []store.OpsBot{
		{ID: 1, Role: RoleAccount, OwnerType: defaultOwnerType, OwnerID: 2, DisplayName: "账号 A"},
		{ID: 2, Role: RoleAccount, OwnerType: defaultOwnerType, OwnerID: 2, DisplayName: "账号 B"},
	}}
	planner := &fakeDirectorPlanner{created: map[int64][]int64{1: {10}, 2: {20}}}
	calendar := &fakeDirectorCalendar{entries: []CalendarEntry{
		{IssueID: 10, BotID: 1, StartDate: "2026-11-02"},
		{IssueID: 11, BotID: 1, StartDate: "2026-11-02", DueDate: "2026-11-03"},
		{IssueID: 20, BotID: 2, StartDate: "2026-11-02"},
	}}
	var boardKey, boardContent string
	d := newTestDirector(t)
	d.Bots = accounts
	d.Planner = planner
	d.Calendar = calendar
	d.Board = directorBoardFunc(func(_ context.Context, key, content string) error {
		boardKey, boardContent = key, content
		return nil
	})

	result, err := d.PlanAll(context.Background(), "2026-W45")
	require.NoError(t, err)
	require.Equal(t, "2026-W45", result.Week)
	require.Len(t, result.Accounts, 2)
	require.Equal(t, []int64{10}, result.Accounts[0].Created)
	require.Equal(t, []int64{20}, result.Accounts[1].Created)
	require.Len(t, planner.calls, 2)
	require.Empty(t, result.Conflicts)

	require.Len(t, calendar.calls, 1)
	require.Equal(t, int64(11), calendar.calls[0].issueID)
	require.Equal(t, OwnerRef{Type: defaultOwnerType, ID: 2}, calendar.calls[0].owner)
	require.Equal(t, "2026-11-03", calendar.calls[0].startDate)
	require.Equal(t, "2026-11-04", calendar.calls[0].dueDate)
	require.Equal(t, []int64{11}, result.Rescheduled)

	require.Equal(t, "ops-director/plan/2026-W45", boardKey)
	require.Contains(t, boardContent, `"week":"2026-W45"`)
}

func TestDirectorPlanAllReportsAccountFailure(t *testing.T) {
	accounts := &fakeDirectorAccounts{bots: []store.OpsBot{
		{ID: 1, Role: RoleAccount, OwnerType: defaultOwnerType, OwnerID: 2},
		{ID: 2, Role: RoleAccount, OwnerType: defaultOwnerType, OwnerID: 2},
	}}
	planner := &fakeDirectorPlanner{
		created: map[int64][]int64{1: {10}},
		errs:    map[int64]error{2: errors.New("boom")},
	}
	d := newTestDirector(t)
	d.Bots = accounts
	d.Planner = planner
	d.Calendar = &fakeDirectorCalendar{}

	result, err := d.PlanAll(context.Background(), "2026-W45")
	require.Error(t, err)
	require.Len(t, result.Accounts, 2)
	require.Equal(t, []int64{10}, result.Accounts[0].Created)
	require.Contains(t, result.Accounts[1].Error, "boom")
}

func TestDirectorReportRollsUpAccounts(t *testing.T) {
	fb := &Feedback{
		Bots: fakeBotGetter{goals: "{}"},
		Published: func(_ context.Context, botID int64, _, _ string) ([]PublishedContent, error) {
			switch botID {
			case 1:
				return []PublishedContent{
					{BotID: 1, IssueID: 1, Title: "秋季护肤三步", Views: 100, Interactions: 10},
					{BotID: 1, IssueID: 2, Title: "通勤穿搭", Views: 300, Interactions: 30},
				}, nil
			case 2:
				return []PublishedContent{{BotID: 2, IssueID: 3, Title: "秋冬配色", Views: 50, Interactions: 5}}, nil
			}
			return nil, nil
		},
	}

	d := newTestDirector(t)
	d.Bots = &fakeDirectorAccounts{bots: []store.OpsBot{
		{ID: 1, Role: RoleAccount, OwnerType: defaultOwnerType, OwnerID: 2, DisplayName: "账号 A"},
		{ID: 2, Role: RoleAccount, OwnerType: defaultOwnerType, OwnerID: 2, DisplayName: "账号 B"},
	}}
	d.Feedback = fb
	require.NoError(t, d.AllocateBudget(context.Background(), map[int64]float64{1: 100, 2: 200}))

	rep, err := d.Report(context.Background(), "2026-11-01", "2026-11-07")
	require.NoError(t, err)
	require.Equal(t, 2, rep.Accounts)
	require.Equal(t, 3, rep.Published)
	require.Equal(t, float64(450), rep.Views)
	require.Greater(t, rep.HitRate, float64(0))
	require.Equal(t, map[int64]float64{1: 100, 2: 200}, rep.Allocated)
}

func TestDirectorProxyApproveRejectsBotOutsideScope(t *testing.T) {
	d := newTestDirector(t)
	d.Approvals.(*fakeProxyApprover).botID = 99
	err := d.ProxyApprove(context.Background(), 9, 1, "总监代审")
	require.ErrorIs(t, err, ErrNotFound)

	approver := d.Approvals.(*fakeProxyApprover)
	require.Zero(t, approver.calls)
	require.Empty(t, d.ProxyApprovals())
}

func TestDirectorAccountsSQL(t *testing.T) {
	store.Driver = "sqlite"
	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)

	for _, stmt := range []string{
		`INSERT INTO ops_bots (id, owner_type, owner_id, role, account_ref, display_name) VALUES (1, 'user', 2, 'director', 'brand', '总监')`,
		`INSERT INTO ops_bots (id, owner_type, owner_id, role, account_ref, display_name, director_bot_id) VALUES (2, 'user', 2, 'account', 'a', 'A', 1)`,
		`INSERT INTO ops_bots (id, owner_type, owner_id, role, account_ref, display_name, director_bot_id) VALUES (3, 'user', 2, 'account', 'b', 'B', 5)`,
		`INSERT INTO ops_bots (id, owner_type, owner_id, role, account_ref, display_name) VALUES (4, 'user', 9, 'account', 'c', 'C')`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}

	accounts := &SQLDirectorAccounts{Q: store.Wrap(raw).Queries()}
	owner := OwnerRef{Type: "user", ID: 2}
	bots, err := accounts.Accounts(context.Background(), owner, 1)
	require.NoError(t, err)
	require.Len(t, bots, 1)
	require.Equal(t, int64(2), bots[0].ID)

	// Another director's accounts are never handed to this director, even when
	// this one has no assignments of its own.
	_, err = raw.Exec(`UPDATE ops_bots SET director_bot_id = NULL WHERE id = 2`)
	require.NoError(t, err)
	bots, err = accounts.Accounts(context.Background(), owner, 1)
	require.NoError(t, err)
	require.Empty(t, bots)

	// Nobody is linked at all: true single-director deployment, so fall back to
	// every account of the owner (and never another owner's bots).
	_, err = raw.Exec(`UPDATE ops_bots SET director_bot_id = NULL`)
	require.NoError(t, err)
	bots, err = accounts.Accounts(context.Background(), owner, 1)
	require.NoError(t, err)
	require.Len(t, bots, 2)
	require.Equal(t, int64(2), bots[0].ID)
	require.Equal(t, int64(3), bots[1].ID)

	// A bot that is not a director is rejected.
	_, err = accounts.Accounts(context.Background(), owner, 2)
	require.ErrorIs(t, err, ErrInvalidInput)

	// A director bot outside the authenticated owner scope is not found.
	_, err = accounts.Accounts(context.Background(), OwnerRef{Type: "user", ID: 9}, 1)
	require.ErrorIs(t, err, ErrNotFound)
}

type directorMessengerFunc func(context.Context, int64, string) error

func (f directorMessengerFunc) SendToBot(ctx context.Context, botID int64, text string) error {
	return f(ctx, botID, text)
}

type directorBoardFunc func(context.Context, string, string) error

func (f directorBoardFunc) Write(ctx context.Context, key, content string) error {
	return f(ctx, key, content)
}
