package opsbot

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

type createdEpic struct {
	columnID    int64
	title       string
	description string
	byUserID    int64
}

type fakeIssues struct {
	n       int64
	created []createdEpic
	deleted []int64
}

func (f *fakeIssues) CreateEpic(_ context.Context, columnID int64, title, description string, byUserID int64) (int64, error) {
	f.n++
	f.created = append(f.created, createdEpic{columnID: columnID, title: title, description: description, byUserID: byUserID})
	return 900 + f.n, nil
}

func (f *fakeIssues) DeleteIssue(_ context.Context, issueID int64) error {
	f.deleted = append(f.deleted, issueID)
	return nil
}

type createdWorkspace struct {
	issueID   int64
	ownerType string
	ownerID   int64
	createdBy int64
}

type fakeWorkspaces struct {
	n       int64
	created []createdWorkspace
	deleted []int64
	fail    bool
}

func (f *fakeWorkspaces) CreateControl(_ context.Context, issueID int64, ownerType string, ownerID, createdBy int64) (int64, error) {
	if f.fail {
		return 0, errors.New("workspace backend down")
	}
	f.n++
	f.created = append(f.created, createdWorkspace{issueID: issueID, ownerType: ownerType, ownerID: ownerID, createdBy: createdBy})
	return 1600 + f.n, nil
}

func (f *fakeWorkspaces) DeleteWorkspace(_ context.Context, workspaceID int64) error {
	f.deleted = append(f.deleted, workspaceID)
	return nil
}

type fakeBots struct {
	bots  map[int64]store.OpsBot
	setOK bool
}

func newFakeBots(bots ...store.OpsBot) *fakeBots {
	m := make(map[int64]store.OpsBot, len(bots))
	for _, b := range bots {
		m[b.ID] = b
	}
	return &fakeBots{bots: m}
}

func (f *fakeBots) GetOpsBot(_ context.Context, id int64) (store.OpsBot, error) {
	b, ok := f.bots[id]
	if !ok {
		return store.OpsBot{}, errors.New("bot not found")
	}
	return b, nil
}

func (f *fakeBots) ListOpsBotsByOwner(_ context.Context, arg store.ListOpsBotsByOwnerParams) ([]store.OpsBot, error) {
	var out []store.OpsBot
	for _, b := range f.bots {
		if b.OwnerType == arg.OwnerType && b.OwnerID == arg.OwnerID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeBots) SetOpsBotProvision(_ context.Context, arg store.SetOpsBotProvisionParams) (store.OpsBot, error) {
	b, ok := f.bots[arg.ID]
	if !ok {
		return store.OpsBot{}, errors.New("bot not found")
	}
	b.EpicIssueID = arg.EpicIssueID
	b.ControlWorkspaceID = arg.ControlWorkspaceID
	f.bots[arg.ID] = b
	f.setOK = true
	return b, nil
}

func newTestProvisioner(bots *fakeBots) (*Provisioner, *fakeIssues, *fakeWorkspaces) {
	issues := &fakeIssues{}
	workspaces := &fakeWorkspaces{}
	return &Provisioner{
		Issues:           issues,
		Workspaces:       workspaces,
		Columns:          stubColumns{1},
		Bots:             bots,
		DeleteIssues:     issues,
		DeleteWorkspaces: workspaces,
	}, issues, workspaces
}

type stubColumns struct{ id int64 }

func (s stubColumns) FirstColumn(_ context.Context, _ int64) (int64, error) { return s.id, nil }

func TestProvisionAccountAndDirector(t *testing.T) {
	bots := newFakeBots(
		store.OpsBot{ID: 7, OwnerType: "user", OwnerID: 1, Role: "account", DisplayName: "公众号 A"},
		store.OpsBot{ID: 8, OwnerType: "user", OwnerID: 1, Role: "director", DisplayName: "运营总监"},
	)
	p, issues, workspaces := newTestProvisioner(bots)
	ctx := context.Background()

	epicID, wsID, err := p.ProvisionAccount(ctx, "user", 1, 42, 7, "公众号 A", 1)
	require.NoError(t, err)
	require.EqualValues(t, 901, epicID)
	require.EqualValues(t, 1601, wsID)

	epicID, wsID, err = p.ProvisionDirector(ctx, "user", 1, 42, 8, "运营总监", 1)
	require.NoError(t, err)
	require.EqualValues(t, 902, epicID)
	require.EqualValues(t, 1602, wsID)

	require.Len(t, issues.created, 2)
	require.Equal(t, "运营 Bot：公众号 A", issues.created[0].title)
	require.EqualValues(t, 1, issues.created[0].columnID)
	require.EqualValues(t, 1, issues.created[0].byUserID)
	require.Equal(t, "运营总监：运营总监", issues.created[1].title)

	require.Len(t, workspaces.created, 2)
	require.EqualValues(t, 901, workspaces.created[0].issueID)
	require.Equal(t, "user", workspaces.created[0].ownerType)
	require.EqualValues(t, 1, workspaces.created[0].ownerID)
	require.EqualValues(t, 1, workspaces.created[0].createdBy)

	require.EqualValues(t, 901, bots.bots[7].EpicIssueID.Int64)
	require.EqualValues(t, 1601, bots.bots[7].ControlWorkspaceID.Int64)
	require.EqualValues(t, 902, bots.bots[8].EpicIssueID.Int64)
	require.EqualValues(t, 1602, bots.bots[8].ControlWorkspaceID.Int64)
}

func TestProvisionIsIdempotent(t *testing.T) {
	bots := newFakeBots(store.OpsBot{ID: 7, OwnerType: "user", OwnerID: 1, Role: "account", DisplayName: "A"})
	p, issues, workspaces := newTestProvisioner(bots)
	ctx := context.Background()

	epicID, wsID, err := p.ProvisionAccount(ctx, "user", 1, 42, 7, "A", 1)
	require.NoError(t, err)

	epicID2, wsID2, err := p.ProvisionAccount(ctx, "user", 1, 42, 7, "A", 1)
	require.NoError(t, err)
	require.Equal(t, epicID, epicID2)
	require.Equal(t, wsID, wsID2)
	require.Len(t, issues.created, 1)
	require.Len(t, workspaces.created, 1)
}

func TestProvisionRollsBackEpicWhenWorkspaceFails(t *testing.T) {
	bots := newFakeBots(store.OpsBot{ID: 7, OwnerType: "user", OwnerID: 1, Role: "account", DisplayName: "A"})
	p, issues, workspaces := newTestProvisioner(bots)
	workspaces.fail = true

	_, _, err := p.ProvisionAccount(context.Background(), "user", 1, 42, 7, "A", 1)
	require.Error(t, err)
	require.Equal(t, []int64{901}, issues.deleted)
	require.Empty(t, workspaces.deleted)
	require.False(t, bots.bots[7].EpicIssueID.Valid)
	require.False(t, bots.bots[7].ControlWorkspaceID.Valid)
}

func TestProvisionReturnsErrorWhenRollbackUnavailable(t *testing.T) {
	bots := newFakeBots(store.OpsBot{ID: 7, OwnerType: "user", OwnerID: 1, Role: "account", DisplayName: "A"})
	workspaces := &fakeWorkspaces{fail: true}
	p := &Provisioner{
		Issues:     &fakeIssues{},
		Workspaces: workspaces,
		Columns:    stubColumns{1},
		Bots:       bots,
	}

	_, _, err := p.ProvisionAccount(context.Background(), "user", 1, 42, 7, "A", 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rollback")
	require.False(t, bots.bots[7].EpicIssueID.Valid)
}

func TestEnsureAllBackfillsOnlyMissingProvisioning(t *testing.T) {
	both := store.OpsBot{ID: 1, OwnerType: "user", OwnerID: 1, Role: "account", DisplayName: "done",
		EpicIssueID: sql.NullInt64{Int64: 500, Valid: true}, ControlWorkspaceID: sql.NullInt64{Int64: 600, Valid: true}}
	epicOnly := store.OpsBot{ID: 2, OwnerType: "user", OwnerID: 1, Role: "account", DisplayName: "epic-only",
		EpicIssueID: sql.NullInt64{Int64: 501, Valid: true}}
	missing := store.OpsBot{ID: 3, OwnerType: "user", OwnerID: 1, Role: "director", DisplayName: "new"}
	otherOwner := store.OpsBot{ID: 4, OwnerType: "user", OwnerID: 2, Role: "account", DisplayName: "someone-else"}

	bots := newFakeBots(both, epicOnly, missing, otherOwner)
	p, issues, workspaces := newTestProvisioner(bots)
	ctx := context.Background()

	report, err := p.EnsureAll(ctx, "user", 1, 42, 1)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{1}, report.Skipped)
	require.ElementsMatch(t, []int64{2, 3}, report.Provisioned)

	require.Len(t, issues.created, 1) // only bot 3 needed a new epic
	require.Equal(t, "运营总监：new", issues.created[0].title)
	require.Len(t, workspaces.created, 2) // bot 2 backfill + bot 3
	require.EqualValues(t, 501, workspaces.created[0].issueID)
	require.EqualValues(t, 901, workspaces.created[1].issueID)
	require.False(t, bots.bots[4].EpicIssueID.Valid, "other owner must be untouched")

	report2, err := p.EnsureAll(ctx, "user", 1, 42, 1)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{1, 2, 3}, report2.Skipped)
	require.Empty(t, report2.Provisioned)
	require.Len(t, issues.created, 1)
	require.Len(t, workspaces.created, 2)
}

func TestProvisionRejectsForeignBot(t *testing.T) {
	bots := newFakeBots(store.OpsBot{ID: 7, OwnerType: "user", OwnerID: 1, Role: "account", DisplayName: "A"})
	p, issues, workspaces := newTestProvisioner(bots)

	_, _, err := p.ProvisionAccount(context.Background(), "user", 2, 42, 7, "A", 1)
	require.Error(t, err)
	require.Empty(t, issues.created)
	require.Empty(t, workspaces.created)
}
