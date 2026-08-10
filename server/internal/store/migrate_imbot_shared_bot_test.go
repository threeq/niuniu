package store_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// TestIMBotChannelsOwnerModel exercises the owner-level im_bot_channels model on
// both drivers (channels carry no project_id; a bot is owned by (owner_type,
// owner_id) and routing lives on im_bot_chats.project_id):
//   - a channel is created with owner columns only (no project_id);
//   - a chat routed to a project keeps its project_id;
//   - the name UNIQUE is per (owner_type, owner_id, channel_type, name);
//   - the partial UNIQUE forbids a second bot with the same
//     (owner, channel_type, fingerprint) while empty fingerprints are exempt.
func TestIMBotChannelsOwnerModel(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		db := openSchemaOnly(t, drv)
		w := store.Wrap(db)
		ctx := context.Background()

		store.Migrate(db)
		store.Migrate(db) // idempotent second pass

		mustExec(t, w, ctx, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'home', 'user', 42)`)
		mustExec(t, w, ctx, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (2, 'other', 'user', 42)`)

		// Owner-level channel: no project_id column exists.
		mustExec(t, w, ctx,
			`INSERT INTO im_bot_channels (id, owner_type, owner_id, channel_type, name) VALUES (1, 'user', 42, 'lark', 'bot')`)
		// A chat routed to project 2 keeps its routing target.
		mustExec(t, w, ctx,
			`INSERT INTO im_bot_chats (id, channel_id, project_id, chat_ext_id, status) VALUES (2, 1, 2, 'oc_b', 'active')`)

		var p2 sql.NullInt64
		mustScan(t, w, ctx, `SELECT project_id FROM im_bot_chats WHERE id = 2`, nil, &p2)
		if !p2.Valid || p2.Int64 != 2 {
			t.Fatalf("chat 2 project_id = %v, want 2", p2)
		}

		// The name UNIQUE is per (owner_type, owner_id, channel_type, name): a second
		// row with the same owner+type+name collides.
		if _, err := w.ExecContext(ctx,
			`INSERT INTO im_bot_channels (id, owner_type, owner_id, channel_type, name) VALUES (2, 'user', 42, 'lark', 'bot')`); err == nil {
			t.Fatalf("expected UNIQUE violation on duplicate (owner,type,name)")
		}
		// A different owner may reuse the same name.
		mustExec(t, w, ctx,
			`INSERT INTO im_bot_channels (id, owner_type, owner_id, channel_type, name) VALUES (3, 'user', 99, 'lark', 'bot')`)

		// Partial UNIQUE: two channels with the same owner+type+fingerprint collide,
		// but empty fingerprints are exempt (the default).
		mustExec(t, w, ctx,
			`INSERT INTO im_bot_channels (id, owner_type, owner_id, channel_type, name, credential_fingerprint)
			 VALUES (10, 'user', 42, 'lark', 'fp-a', 'deadbeef')`)
		if _, err := w.ExecContext(ctx,
			`INSERT INTO im_bot_channels (id, owner_type, owner_id, channel_type, name, credential_fingerprint)
			 VALUES (11, 'user', 42, 'lark', 'fp-a-dup', 'deadbeef')`); err == nil {
			t.Fatalf("expected UNIQUE violation on duplicate (owner,type,fingerprint)")
		}
		// Empty fingerprints are exempt from the partial UNIQUE (channel 1 already
		// has ''; a second '' row must be allowed).
		mustExec(t, w, ctx,
			`INSERT INTO im_bot_channels (id, owner_type, owner_id, channel_type, name, credential_fingerprint)
			 VALUES (12, 'user', 42, 'lark', 'empty-fp', '')`)
	})
}

// TestMigrateIMBotChannelsDropProjectID_LegacySQLite recreates a legacy
// im_bot_channels (with project_id + old UNIQUE, FK-referenced by im_bot_chats)
// and verifies the drop-project-id migration rebuilds the table without
// project_id while preserving the row id so child chat FKs remain valid.
func TestMigrateIMBotChannelsDropProjectID_LegacySQLite(t *testing.T) {
	db := openSchemaOnly(t, pgtest.DriverSQLite)
	w := store.Wrap(db)
	ctx := context.Background()

	// Replace the fresh (already-dropped) table with the legacy shape carrying
	// project_id + the old UNIQUE(project_id, channel_type, name).
	mustExec(t, w, ctx, `PRAGMA foreign_keys = OFF`)
	mustExec(t, w, ctx, `DROP TABLE im_bot_inbox`)
	mustExec(t, w, ctx, `DROP TABLE im_bot_threads`)
	mustExec(t, w, ctx, `DROP TABLE im_bot_chats`)
	mustExec(t, w, ctx, `DROP TABLE im_bot_channels`)
	mustExec(t, w, ctx, `CREATE TABLE im_bot_channels (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		owner_type      TEXT NOT NULL DEFAULT 'user',
		owner_id        INTEGER NOT NULL DEFAULT 0,
		credential_fingerprint TEXT NOT NULL DEFAULT '',
		channel_type    TEXT NOT NULL,
		name            TEXT NOT NULL,
		connection_mode TEXT NOT NULL DEFAULT 'stream',
		credential_enc  TEXT NOT NULL DEFAULT '',
		webhook_secret  TEXT NOT NULL DEFAULT '',
		status          TEXT NOT NULL DEFAULT 'active',
		created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(project_id, channel_type, name)
	)`)
	mustExec(t, w, ctx, `CREATE TABLE im_bot_chats (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id      INTEGER NOT NULL REFERENCES im_bot_channels(id) ON DELETE CASCADE,
		project_id      INTEGER REFERENCES projects(id) ON DELETE CASCADE,
		chat_ext_id     TEXT NOT NULL,
		chat_name       TEXT NOT NULL DEFAULT '',
		bind_mode       TEXT NOT NULL DEFAULT 'project',
		pinned_issue_id INTEGER,
		active_issue_id INTEGER,
		status          TEXT NOT NULL DEFAULT 'pending',
		paired_by       INTEGER,
		created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(channel_id, chat_ext_id)
	)`)
	mustExec(t, w, ctx, `PRAGMA foreign_keys = ON`)

	mustExec(t, w, ctx, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'p', 'user', 42)`)
	mustExec(t, w, ctx, `INSERT INTO im_bot_channels (id, project_id, owner_type, owner_id, channel_type, name) VALUES (7, 1, 'user', 42, 'lark', 'bot')`)
	mustExec(t, w, ctx, `INSERT INTO im_bot_chats (id, channel_id, project_id, chat_ext_id, status) VALUES (3, 7, 1, 'oc', 'active')`)

	store.Migrate(db)
	store.Migrate(db) // idempotent

	// project_id column is gone.
	var cnt int
	mustScan(t, w, ctx, `SELECT COUNT(*) FROM pragma_table_info('im_bot_channels') WHERE name = 'project_id'`, nil, &cnt)
	if cnt != 0 {
		t.Fatalf("im_bot_channels.project_id still present after migration")
	}

	// The channel row survived with its id preserved, so the chat FK is still valid.
	var name string
	mustScan(t, w, ctx, `SELECT name FROM im_bot_channels WHERE id = 7`, nil, &name)
	if name != "bot" {
		t.Fatalf("channel id 7 not preserved: name=%q", name)
	}
	var chChannel, chProject int64
	mustScan(t, w, ctx, `SELECT channel_id, project_id FROM im_bot_chats WHERE id = 3`, nil, &chChannel, &chProject)
	if chChannel != 7 || chProject != 1 {
		t.Fatalf("chat FK/route not preserved: channel_id=%d project_id=%d", chChannel, chProject)
	}

	// New per-owner name UNIQUE is enforced.
	if _, err := w.ExecContext(ctx,
		`INSERT INTO im_bot_channels (owner_type, owner_id, channel_type, name) VALUES ('user', 42, 'lark', 'bot')`); err == nil {
		t.Fatalf("expected UNIQUE violation on duplicate (owner,type,name) after rebuild")
	}
}

// TestApplySchemaOnLegacyIMBotChannels reproduces the 0.8.1 -> 0.8.2 upgrade
// crash: a DB that already carries the legacy im_bot_channels (project_id, no
// owner_type/owner_id) must survive a re-run of the embedded schema. Because
// ApplySchema runs the whole schema.sql on every startup BEFORE Migrate adds the
// owner columns, any `CREATE INDEX ... ON im_bot_channels(owner_type, owner_id)`
// living in schema.sql fails with "no such column: owner_type" -> Open() errors
// -> the embedded server exits before the ready handshake ("bundle: server
// exited before ready handshake"). The owner index must live in migrate.go only.
func TestApplySchemaOnLegacyIMBotChannels(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"

	// Legacy 0.8.1 im_bot_channels: project_id NOT NULL, no owner columns. This
	// is what an upgrading user's ~/.niuniu/niuniu.db already contains, so the
	// current schema's CREATE TABLE IF NOT EXISTS is a no-op against it.
	if _, err := db.Exec(`CREATE TABLE im_bot_channels (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id      INTEGER NOT NULL,
		channel_type    TEXT NOT NULL,
		name            TEXT NOT NULL,
		connection_mode TEXT NOT NULL DEFAULT 'stream',
		credential_enc  TEXT NOT NULL DEFAULT '',
		webhook_secret  TEXT NOT NULL DEFAULT '',
		status          TEXT NOT NULL DEFAULT 'active',
		created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(project_id, channel_type, name)
	)`); err != nil {
		t.Fatalf("seed legacy im_bot_channels: %v", err)
	}

	// The real upgrade path: Open() -> ApplySchema on the existing DB. This is
	// where 0.8.2 crash-looped.
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema on legacy DB failed (upgrade crash): %v", err)
	}

	// Then Migrate adds the owner columns + the owner index and drops project_id.
	store.Migrate(db)
	store.Migrate(db) // idempotent

	w := store.Wrap(db)
	ctx := context.Background()
	var ownerCols int
	mustScan(t, w, ctx,
		`SELECT COUNT(*) FROM pragma_table_info('im_bot_channels') WHERE name = 'owner_type'`,
		nil, &ownerCols)
	if ownerCols != 1 {
		t.Fatalf("owner_type column missing after migrate: %d", ownerCols)
	}
	var idxCount int
	mustScan(t, w, ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_im_bot_channels_owner'`,
		nil, &idxCount)
	if idxCount != 1 {
		t.Fatalf("idx_im_bot_channels_owner missing after migrate: %d", idxCount)
	}
}

// TestIMBotChannelsAllowWechat verifies the fresh-install schema admits the
// 'wechat' channel_type / platform on both drivers (the CHECK enums were widened
// for 微信ClawBot), while still rejecting an unknown type.
func TestIMBotChannelsAllowWechat(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		db := openSchemaOnly(t, drv)
		w := store.Wrap(db)
		ctx := context.Background()
		store.Migrate(db)

		mustExec(t, w, ctx, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'p', 'user', 42)`)
		mustExec(t, w, ctx,
			`INSERT INTO im_bot_channels (owner_type, owner_id, channel_type, name) VALUES ('user', 42, 'wechat', 'clawbot')`)
		mustExec(t, w, ctx,
			`INSERT INTO im_bot_onboarding_tokens (token_hash, project_id, platform, expires_at)
			 VALUES ('h1', 1, 'wechat', CURRENT_TIMESTAMP)`)

		if _, err := w.ExecContext(ctx,
			`INSERT INTO im_bot_channels (owner_type, owner_id, channel_type, name) VALUES ('user', 42, 'bogus', 'x')`); err == nil {
			t.Fatalf("expected CHECK violation for unknown channel_type")
		}
	})
}

// TestMigrateIMBotAllowWechat_LegacySQLite recreates im_bot_channels /
// im_bot_onboarding_tokens with the pre-wechat CHECK enum and verifies the
// migration rebuilds them so a 'wechat' row is admitted on upgrade.
func TestMigrateIMBotAllowWechat_LegacySQLite(t *testing.T) {
	db := openSchemaOnly(t, pgtest.DriverSQLite)
	w := store.Wrap(db)
	ctx := context.Background()

	// Replace the fresh tables with the legacy (no-wechat) CHECK enums.
	mustExec(t, w, ctx, `PRAGMA foreign_keys = OFF`)
	mustExec(t, w, ctx, `DROP TABLE im_bot_inbox`)
	mustExec(t, w, ctx, `DROP TABLE im_bot_threads`)
	mustExec(t, w, ctx, `DROP TABLE im_bot_chats`)
	mustExec(t, w, ctx, `DROP TABLE im_bot_channels`)
	mustExec(t, w, ctx, `DROP TABLE im_bot_onboarding_tokens`)
	mustExec(t, w, ctx, `CREATE TABLE im_bot_channels (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		owner_type      TEXT NOT NULL DEFAULT 'user',
		owner_id        INTEGER NOT NULL DEFAULT 0,
		credential_fingerprint TEXT NOT NULL DEFAULT '',
		channel_type    TEXT NOT NULL CHECK (channel_type IN ('lark','dingtalk','telegram','wework')),
		name            TEXT NOT NULL,
		connection_mode TEXT NOT NULL DEFAULT 'stream',
		credential_enc  TEXT NOT NULL DEFAULT '',
		webhook_secret  TEXT NOT NULL DEFAULT '',
		status          TEXT NOT NULL DEFAULT 'active',
		created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(owner_type, owner_id, channel_type, name)
	)`)
	mustExec(t, w, ctx, `CREATE TABLE im_bot_onboarding_tokens (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		token_hash      TEXT NOT NULL UNIQUE,
		project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		platform        TEXT NOT NULL CHECK (platform IN ('lark','dingtalk','telegram','wework')),
		channel_name    TEXT NOT NULL DEFAULT '',
		connection_mode TEXT NOT NULL DEFAULT 'stream',
		expires_at      TIMESTAMP NOT NULL,
		used_at         TIMESTAMP,
		created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, w, ctx, `PRAGMA foreign_keys = ON`)

	// A pre-existing row must survive the rebuild.
	mustExec(t, w, ctx, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'p', 'user', 42)`)
	mustExec(t, w, ctx,
		`INSERT INTO im_bot_channels (id, owner_type, owner_id, channel_type, name) VALUES (5, 'user', 42, 'lark', 'kept')`)

	store.Migrate(db)
	store.Migrate(db) // idempotent

	var name string
	mustScan(t, w, ctx, `SELECT name FROM im_bot_channels WHERE id = 5`, nil, &name)
	if name != "kept" {
		t.Fatalf("existing channel not preserved through rebuild: name=%q", name)
	}
	// The widened CHECK now admits 'wechat'.
	mustExec(t, w, ctx,
		`INSERT INTO im_bot_channels (owner_type, owner_id, channel_type, name) VALUES ('user', 42, 'wechat', 'clawbot')`)
	mustExec(t, w, ctx,
		`INSERT INTO im_bot_onboarding_tokens (token_hash, project_id, platform, expires_at)
		 VALUES ('h1', 1, 'wechat', CURRENT_TIMESTAMP)`)
}
