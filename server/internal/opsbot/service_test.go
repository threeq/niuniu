package opsbot

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type fakePacks struct {
	manifests map[string][]packs.Manifest
}

func (f *fakePacks) Load(_ string, _ int64, packID string, version int) (packs.Manifest, error) {
	for _, m := range f.manifests[packID] {
		if m.Version == version {
			return m, nil
		}
	}
	return packs.Manifest{}, errors.New("pack version not found")
}

func (f *fakePacks) LatestPublished(_ string, _ int64, packID string) (packs.Manifest, error) {
	var best packs.Manifest
	for _, m := range f.manifests[packID] {
		if m.Status == "published" && m.Version > best.Version {
			best = m
		}
	}
	if best.Version == 0 {
		return packs.Manifest{}, errors.New("no published version")
	}
	return best, nil
}

func defaultFakePacks() *fakePacks {
	return &fakePacks{manifests: map[string][]packs.Manifest{
		"wechat_mp": {
			{ID: "wechat_mp", Version: 1, Status: "published", DisplayName: "公众号"},
			{ID: "wechat_mp", Version: 2, Status: "published", DisplayName: "公众号"},
			{ID: "wechat_mp", Version: 3, Status: "draft", DisplayName: "公众号"},
		},
		"xiaohongshu": {
			{ID: "xiaohongshu", Version: 1, Status: "published", DisplayName: "小红书"},
		},
	}}
}

type fakeCanceller struct {
	cancelled []int64
	err       error
}

func (f *fakeCanceller) CancelByBot(_ context.Context, botID int64) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.cancelled = append(f.cancelled, botID)
	return 1, nil
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	store.Driver = "sqlite"
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	return NewService(store.New(db), defaultFakePacks())
}

func TestCreateTwoAccountsSamePackAndRejectDuplicate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}

	a, err := svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-a", DisplayName: "A",
	})
	require.NoError(t, err)
	require.Equal(t, RoleAccount, a.Role)
	require.Equal(t, "acc-a", a.AccountRef)
	require.EqualValues(t, 2, a.PackVersion, "pack_version 0 resolves to latest published")
	require.Equal(t, StatusActive, a.Status)
	require.Equal(t, defaultApprovalPolicy, a.ApprovalPolicyJson, "approvals default to on")
	require.Equal(t, emptyJSON, a.PersonaJson)

	b, err := svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-b", DisplayName: "B",
	})
	require.NoError(t, err)
	require.NotEqual(t, a.ID, b.ID)

	_, err = svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-a", DisplayName: "A2",
	})
	require.ErrorIs(t, err, ErrAccountExists)

	// Same account_ref under another owner is a different bot.
	_, err = svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 2}, PackID: "wechat_mp", AccountRef: "acc-a",
	})
	require.NoError(t, err)

	bots, err := svc.ListByOwner(ctx, owner)
	require.NoError(t, err)
	require.Len(t, bots, 2)
}

func TestUpdateConfigValidatesJSON(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	bot, err := svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 1}, PackID: "wechat_mp",
		AccountRef: "acc-a", DisplayName: "A",
	})
	require.NoError(t, err)

	updated, err := svc.UpdateConfig(ctx, bot.ID, ConfigPatch{
		BudgetJSON: strptr(`{"monthly_limit":100,"currency":"CNY"}`),
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"monthly_limit":100,"currency":"CNY"}`, updated.BudgetJson)
	require.Equal(t, "A", updated.DisplayName, "untouched fields keep their value")

	for _, bad := range []string{`not-json`, `[]`, `"scalar"`, `null`, `{"a":1} trailing`} {
		_, err = svc.UpdateConfig(ctx, bot.ID, ConfigPatch{BudgetJSON: strptr(bad)})
		require.ErrorIs(t, err, ErrInvalidConfig, "budget_json %q must be rejected", bad)
	}

	_, err = svc.UpdateConfig(ctx, bot.ID, ConfigPatch{
		ApprovalPolicyJSON: strptr(`{"publish":true,"unknown":false}`),
	})
	require.ErrorIs(t, err, ErrInvalidConfig)

	_, err = svc.UpdateConfig(ctx, bot.ID, ConfigPatch{
		ApprovalPolicyJSON: strptr(`{"publish":"yes"}`),
	})
	require.ErrorIs(t, err, ErrInvalidConfig)

	updated, err = svc.UpdateConfig(ctx, bot.ID, ConfigPatch{
		DisplayName:        strptr("A renamed"),
		ApprovalPolicyJSON: strptr(`{"publish":true,"reply":false,"video":true}`),
	})
	require.NoError(t, err)
	require.Equal(t, "A renamed", updated.DisplayName)
	require.JSONEq(t, `{"publish":true,"reply":false,"video":true}`, updated.ApprovalPolicyJson)

	_, err = svc.UpdateConfig(ctx, bot.ID+999, ConfigPatch{DisplayName: strptr("x")})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestUpdateConfigPinsPackVersion(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	bot, err := svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 1}, PackID: "wechat_mp", AccountRef: "acc-a",
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, bot.PackVersion)

	updated, err := svc.UpdateConfig(ctx, bot.ID, ConfigPatch{PackVersion: i64ptr(1)})
	require.NoError(t, err)
	require.EqualValues(t, 1, updated.PackVersion)

	_, err = svc.UpdateConfig(ctx, bot.ID, ConfigPatch{PackVersion: i64ptr(99)})
	require.ErrorIs(t, err, ErrPackNotFound)
}

func TestCreateAccountRejectsUnknownPack(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.CreateAccountBot(context.Background(), CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 1}, PackID: "nope", AccountRef: "acc-a",
	})
	require.ErrorIs(t, err, ErrPackNotFound)

	_, err = svc.CreateAccountBot(context.Background(), CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 1}, PackID: "wechat_mp", AccountRef: "  ",
	})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestCreateDirector(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	d, err := svc.CreateDirector(ctx, CreateDirectorInput{
		Owner: OwnerRef{Type: "user", ID: 1}, DisplayName: "运营总监",
	})
	require.NoError(t, err)
	require.Equal(t, RoleDirector, d.Role)
	require.Empty(t, d.PackID)
	require.Empty(t, d.AccountRef)
	require.Equal(t, defaultApprovalPolicy, d.ApprovalPolicyJson)

	_, err = svc.CreateDirector(ctx, CreateDirectorInput{
		Owner: OwnerRef{Type: "user", ID: 1}, DisplayName: "另一个总监",
	})
	require.ErrorIs(t, err, ErrAccountExists)
}

func TestCreateAccountValidatesDirectorOwnership(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}
	director, err := svc.CreateDirector(ctx, CreateDirectorInput{Owner: owner, DisplayName: "总监"})
	require.NoError(t, err)

	account, err := svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-a", DirectorBotID: director.ID,
	})
	require.NoError(t, err)
	require.True(t, account.DirectorBotID.Valid)
	require.Equal(t, director.ID, account.DirectorBotID.Int64)

	// Another owner's director is not attachable.
	_, err = svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 2}, PackID: "wechat_mp", AccountRef: "acc-a",
		DirectorBotID: director.ID,
	})
	require.ErrorIs(t, err, ErrInvalidInput)

	// An account bot is not a director.
	_, err = svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-b", DirectorBotID: account.ID,
	})
	require.ErrorIs(t, err, ErrInvalidInput)

	_, err = svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-c", DirectorBotID: 9999,
	})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestSetStatusAndKillSwitch(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	canceller := &fakeCanceller{}
	svc.Approve = canceller

	bot, err := svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 1}, PackID: "wechat_mp", AccountRef: "acc-a",
	})
	require.NoError(t, err)

	paused, err := svc.SetStatus(ctx, bot.ID, StatusPaused)
	require.NoError(t, err)
	require.Equal(t, StatusPaused, paused.Status)

	active, err := svc.SetStatus(ctx, bot.ID, StatusActive)
	require.NoError(t, err)
	require.Equal(t, StatusActive, active.Status)

	_, err = svc.SetStatus(ctx, bot.ID, "bogus")
	require.ErrorIs(t, err, ErrInvalidStatus)

	killed, err := svc.KillSwitch(ctx, bot.ID)
	require.NoError(t, err)
	require.Equal(t, StatusDisabled, killed.Status)
	require.Equal(t, []int64{bot.ID}, canceller.cancelled)

	_, err = svc.KillSwitch(ctx, bot.ID+999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestKillSwitchSurfacesCancellationFailure(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	svc.Approve = &fakeCanceller{err: errors.New("boom")}

	bot, err := svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 1}, PackID: "wechat_mp", AccountRef: "acc-a",
	})
	require.NoError(t, err)

	killed, err := svc.KillSwitch(ctx, bot.ID)
	require.Error(t, err)
	require.Equal(t, StatusDisabled, killed.Status, "the bot is disabled even if approval cancellation fails")
}

func strptr(s string) *string { return &s }
func i64ptr(v int64) *int64   { return &v }
