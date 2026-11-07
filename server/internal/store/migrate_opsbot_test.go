package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsBotSchemaFreshAndRepeatable(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	Migrate(db)

	for _, tbl := range []string{
		"ops_bots", "ops_publish_records", "ops_metrics_daily",
		"ops_engagements", "ops_browser_sessions",
	} {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n))
		require.Equal(t, 1, n, "table %s missing", tbl)
	}
	for _, c := range []struct{ table, column string }{
		{"im_bot_chats", "ops_bot_id"},
		{"agent_ask_user_requests", "kind"},
		{"agent_ask_user_requests", "metadata_json"},
	} {
		ok, err := columnExists(db, c.table, c.column)
		require.NoError(t, err)
		require.True(t, ok, "column %s.%s missing", c.table, c.column)
	}
}

func TestOpsBotLegacySchemaRetrofit(t *testing.T) {
	Driver = "sqlite"
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	for _, ddl := range []string{
		`CREATE TABLE im_bot_chats (id INTEGER PRIMARY KEY AUTOINCREMENT)`,
		`CREATE TABLE agent_ask_user_requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			status TEXT NOT NULL DEFAULT 'pending'
		)`,
	} {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}

	require.NoError(t, migrateOpsBot(db))
	require.NoError(t, migrateOpsBot(db))

	for _, c := range []struct{ table, column string }{
		{"im_bot_chats", "ops_bot_id"},
		{"agent_ask_user_requests", "kind"},
		{"agent_ask_user_requests", "metadata_json"},
	} {
		ok, err := columnExists(db, c.table, c.column)
		require.NoError(t, err)
		require.True(t, ok, "column %s.%s missing after retrofit", c.table, c.column)
	}
}

func TestOpsBotAccountUniqueAndEngagementDedupe(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	q := New(db)
	ctx := context.Background()

	bot, err := q.CreateOpsBot(ctx, CreateOpsBotParams{
		OwnerType: "user", OwnerID: 1, Role: "account",
		PackID: "wechat_mp", PackVersion: 1, AccountRef: "acc-a", DisplayName: "A",
		PersonaJson: "{}", CredentialRefsJson: "{}", KnowledgeScopeJson: "{}",
		GoalsJson: "{}", CadenceJson: "{}", ApprovalPolicyJson: "{}", BudgetJson: "{}",
	})
	require.NoError(t, err)

	_, err = q.CreateOpsBot(ctx, CreateOpsBotParams{
		OwnerType: "user", OwnerID: 1, Role: "account",
		PackID: "wechat_mp", PackVersion: 1, AccountRef: "acc-a", DisplayName: "A2",
		PersonaJson: "{}", CredentialRefsJson: "{}", KnowledgeScopeJson: "{}",
		GoalsJson: "{}", CadenceJson: "{}", ApprovalPolicyJson: "{}", BudgetJson: "{}",
	})
	require.Error(t, err, "duplicate account_ref must be rejected")

	_, err = q.UpsertOpsEngagement(ctx, UpsertOpsEngagementParams{
		BotID: bot.ID, PlatformEngagementID: "c1", Kind: "comment", Text: "hi",
	})
	require.NoError(t, err)
	_, err = q.UpsertOpsEngagement(ctx, UpsertOpsEngagementParams{
		BotID: bot.ID, PlatformEngagementID: "c1", Kind: "comment", Text: "hi again",
	})
	require.NoError(t, err, "upsert must not duplicate")

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM ops_engagements WHERE bot_id = ? AND platform_engagement_id = 'c1'`,
		bot.ID).Scan(&n))
	require.Equal(t, 1, n, "engagement must be deduped on (bot_id, platform_engagement_id)")
}

func TestOpsBrowserLeaseCAS(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	q := New(db)
	ctx := context.Background()
	bot, err := q.CreateOpsBot(ctx, CreateOpsBotParams{
		OwnerType: "user", OwnerID: 1, Role: "account",
		PackID: "xiaohongshu", PackVersion: 1, AccountRef: "x1", DisplayName: "X",
		PersonaJson: "{}", CredentialRefsJson: "{}", KnowledgeScopeJson: "{}",
		GoalsJson: "{}", CadenceJson: "{}", ApprovalPolicyJson: "{}", BudgetJson: "{}",
	})
	require.NoError(t, err)
	_, err = q.UpsertOpsBrowserSession(ctx, UpsertOpsBrowserSessionParams{
		BotID: bot.ID, Backend: "local", ProfileDir: "/tmp/x1",
	})
	require.NoError(t, err)

	n, err := q.AcquireOpsBrowserLease(ctx, AcquireOpsBrowserLeaseParams{
		BotID: bot.ID, Backend: "local", LeaseOwner: "w1",
		LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	n, err = q.AcquireOpsBrowserLease(ctx, AcquireOpsBrowserLeaseParams{
		BotID: bot.ID, Backend: "local", LeaseOwner: "w2",
		LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, n, "second lease must be rejected")

	released, err := q.ReleaseOpsBrowserLease(ctx, ReleaseOpsBrowserLeaseParams{
		BotID: bot.ID, Backend: "local", LeaseOwner: "w1",
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, released)

	n, err = q.AcquireOpsBrowserLease(ctx, AcquireOpsBrowserLeaseParams{
		BotID: bot.ID, Backend: "local", LeaseOwner: "w2",
		LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "released lease must be acquirable")
}
