package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// TestIMBotChatBindOpsBot covers the design §12 addition to PATCH
// /api/imbot/chats/:id: binding a chat to an Ops Bot is same-owner only, and a
// nil pointer leaves an existing binding untouched.
func TestIMBotChatBindOpsBot(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()

	channelRes, err := env.DB.Exec(`INSERT INTO im_bot_channels (owner_type, owner_id, channel_type, name)
		VALUES ('user', ?, 'lark', 'chan')`, env.UserA)
	if err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	channelID, _ := channelRes.LastInsertId()
	chatRes, err := env.DB.Exec(`INSERT INTO im_bot_chats (channel_id, chat_ext_id, chat_name, bind_mode, status)
		VALUES (?, 'ext-1', 'chat', 'project', 'active')`, channelID)
	if err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	chatID, _ := chatRes.LastInsertId()

	botA := insertOpsBot(t, env, env.UserA, "a")
	botB := insertOpsBot(t, env, env.UserB, "b")

	dto, err := svc.PatchChatByOwner(ctx, chatID, env.UserA, service.PatchChatInput{OpsBotID: &botA})
	if err != nil {
		t.Fatalf("bind own bot: %v", err)
	}
	if dto.OpsBotID == nil || *dto.OpsBotID != botA {
		t.Fatalf("ops_bot_id = %v, want %d", dto.OpsBotID, botA)
	}

	// Another owner's bot is rejected and the existing binding survives.
	if _, err := svc.PatchChatByOwner(ctx, chatID, env.UserA, service.PatchChatInput{OpsBotID: &botB}); !errors.Is(err, service.ErrInvalidOpsBotBinding) {
		t.Fatalf("cross-owner bind error = %v, want ErrInvalidOpsBotBinding", err)
	}
	var bound int64
	if err := env.DB.QueryRow(`SELECT ops_bot_id FROM im_bot_chats WHERE id = ?`, chatID).Scan(&bound); err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if bound != botA {
		t.Fatalf("binding changed to %d after rejected patch", bound)
	}

	// Omitting the field preserves the binding.
	if _, err := svc.PatchChatByOwner(ctx, chatID, env.UserA, service.PatchChatInput{Status: "active"}); err != nil {
		t.Fatalf("no-op patch: %v", err)
	}
	if err := env.DB.QueryRow(`SELECT ops_bot_id FROM im_bot_chats WHERE id = ?`, chatID).Scan(&bound); err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if bound != botA {
		t.Fatalf("binding = %d, want preserved %d", bound, botA)
	}

	// Zero clears the binding.
	zero := int64(0)
	dto, err = svc.PatchChatByOwner(ctx, chatID, env.UserA, service.PatchChatInput{OpsBotID: &zero})
	if err != nil {
		t.Fatalf("clear binding: %v", err)
	}
	if dto.OpsBotID != nil {
		t.Fatalf("ops_bot_id = %v, want nil", dto.OpsBotID)
	}
}

func insertOpsBot(t *testing.T, env *niutest.IsolationEnv, ownerID int64, accountRef string) int64 {
	t.Helper()
	res, err := env.DB.Exec(`INSERT INTO ops_bots (owner_type, owner_id, role, pack_id, pack_version, account_ref, display_name)
		VALUES ('user', ?, 'account', 'test_pack', 1, ?, 'bot')`, ownerID, accountRef)
	if err != nil {
		t.Fatalf("insert ops bot: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("ops bot id: %v", err)
	}
	return id
}
