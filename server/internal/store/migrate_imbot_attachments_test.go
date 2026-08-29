package store

import (
	"database/sql"
	"testing"
)

// TestMigrateIMBotAttachments_RetrofitsExistingTable covers the upgrade path that
// CREATE TABLE IF NOT EXISTS cannot: a DB carrying the ORIGINAL #664 table shape
// (no attachments column). The create statement is a no-op there, so without the
// separate addColumnIfNotExists the column would never appear and every
// observation write would fail on an unknown column.
func TestMigrateIMBotAttachments_RetrofitsExistingTable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The pre-#679 shape, verbatim.
	if _, err := db.Exec(`CREATE TABLE im_bot_chats (id INTEGER PRIMARY KEY AUTOINCREMENT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE im_bot_chat_messages (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id       INTEGER NOT NULL REFERENCES im_bot_chats(id) ON DELETE CASCADE,
		actor_ext_id  TEXT NOT NULL DEFAULT '',
		actor_name    TEXT NOT NULL DEFAULT '',
		text          TEXT NOT NULL DEFAULT '',
		addressed     INTEGER NOT NULL DEFAULT 0,
		analyzed_at   TIMESTAMP,
		created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO im_bot_chats (id) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO im_bot_chat_messages (chat_id, text) VALUES (1, '旧数据')`); err != nil {
		t.Fatal(err)
	}

	migrateIMBotAgentEmployee(db)

	// The column exists and old rows read as "no attachments".
	var got string
	if err := db.QueryRow(`SELECT attachments FROM im_bot_chat_messages WHERE text = '旧数据'`).Scan(&got); err != nil {
		t.Fatalf("attachments column missing after migration: %v", err)
	}
	if got != "[]" {
		t.Errorf("pre-existing row attachments = %q, want %q", got, "[]")
	}
	// And a write using the column succeeds.
	if _, err := db.Exec(`INSERT INTO im_bot_chat_messages (chat_id, text, attachments) VALUES (1, '新数据', '[{"kind":"image","name":"a.png"}]')`); err != nil {
		t.Errorf("insert with attachments failed: %v", err)
	}

	// Idempotent: running twice must not error or clobber.
	migrateIMBotAgentEmployee(db)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM im_bot_chat_messages`).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows after second migration = %d (err %v), want 2", n, err)
	}
}
