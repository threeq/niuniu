package api_test

import (
	"database/sql"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeleteBotRequiresConfirmation(t *testing.T) {
	r, _ := setupOpsBot(t)
	id := createTestBot(t, r, "1", "brand")
	path := "/api/ops/bots/" + strconv.FormatInt(id, 10)

	w := doJSON(t, r, http.MethodDelete, path, "1", "")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "OPS_CONFIRM_REQUIRED")

	// The bot is still there.
	w = doJSON(t, r, http.MethodGet, path, "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestDeleteBotRemovesBotAndUnbindsChats(t *testing.T) {
	r, db := setupOpsBot(t)
	id := createTestBot(t, r, "1", "brand")
	mustExec(t, db, `INSERT INTO im_bot_channels (id, owner_type, owner_id, channel_type, name)
		VALUES (3, 'user', 1, 'lark', 'test')`)
	mustExec(t, db, `INSERT INTO im_bot_chats (id, channel_id, chat_ext_id, ops_bot_id)
		VALUES (3, 3, 'ext-3', ?)`, id)

	path := "/api/ops/bots/" + strconv.FormatInt(id, 10)
	w := doJSON(t, r, http.MethodDelete, path+"?confirm=true", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"deleted":true`)

	w = doJSON(t, r, http.MethodGet, path, "1", "")
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	var opsBotID sql.NullInt64
	require.NoError(t, db.QueryRow(`SELECT ops_bot_id FROM im_bot_chats WHERE id = 3`).Scan(&opsBotID))
	require.False(t, opsBotID.Valid)
}

func TestDeleteBotRequiresOwner(t *testing.T) {
	r, _ := setupOpsBot(t)
	id := createTestBot(t, r, "1", "brand")

	w := doJSON(t, r, http.MethodDelete,
		"/api/ops/bots/"+strconv.FormatInt(id, 10)+"?confirm=true", "2", "")
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}
