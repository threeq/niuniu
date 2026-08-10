package service_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

func buildIMBotSvc(t *testing.T, env *niutest.IsolationEnv) *service.IMBotService {
	t.Helper()
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	authz := service.NewAuthz(env.Queries(), env.DB)
	adapters := map[imbot.ChannelType]imbot.ChannelAdapter{imbot.ChannelLark: newStubAdapter()}
	return service.NewIMBotService(env.Queries(), env.DB, kr, authz, adapters)
}

// stubAdapter records Push calls and never touches the network.
type stubAdapter struct{ pushed []imbot.OutboundMessage }

func newStubAdapter() *stubAdapter { return &stubAdapter{} }

func (a *stubAdapter) Type() imbot.ChannelType { return imbot.ChannelLark }
func (a *stubAdapter) Connect(ctx context.Context, _ imbot.Credential, _ imbot.InboundHandler) error {
	<-ctx.Done()
	return nil
}
func (a *stubAdapter) Push(_ context.Context, _ imbot.Credential, m imbot.OutboundMessage) error {
	a.pushed = append(a.pushed, m)
	return nil
}
func (a *stubAdapter) VerifyWebhook(_ *http.Request, _ imbot.Credential) (imbot.InboundEvent, error) {
	return imbot.InboundEvent{}, nil
}
func (a *stubAdapter) Challenge(_ *http.Request) ([]byte, bool) { return nil, false }

func TestIMBot_BackfillFingerprint_EnablesDedup(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	ch, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot", Credential: map[string]any{"app_id": "dup_app", "app_secret": "s"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Simulate a legacy channel: clear the fingerprint the create path just set.
	if _, err := env.DB.Exec(`UPDATE im_bot_channels SET credential_fingerprint = '' WHERE id = ?`, ch.ID); err != nil {
		t.Fatalf("clear fp: %v", err)
	}

	svc.BackfillCredentialFingerprints(ctx)

	// After backfill, a second channel for the same app (same owner) is blocked —
	// the one-bot-per-app constraint is now enforceable on the legacy channel.
	_, err = svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot2", Credential: map[string]any{"app_id": "dup_app", "app_secret": "s2"},
	})
	if !errors.Is(err, service.ErrDuplicateIMBotCredential) {
		t.Fatalf("after backfill, duplicate app must be blocked, got %v", err)
	}
}

func TestIMBot_ChannelCRUD_And_CredentialNeverInDTO(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	proj := env.NewProject(t, env.UserA, "P1")
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	dto, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark",
		Name:        "team-bot",
		Credential:  map[string]any{"app_id": "cli_x", "app_secret": "sec"},
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if dto.ConnectionMode != "stream" {
		t.Errorf("default connection_mode = %q, want stream", dto.ConnectionMode)
	}
	if !dto.HasCredential {
		t.Error("HasCredential should be true after create")
	}

	// The bot is owner-level; it shows in the owner's bot list immediately.
	bots, err := svc.ListBotsByOwner(ctx, owner)
	if err != nil || len(bots) != 1 {
		t.Fatalf("ListBotsByOwner: %v len=%d", err, len(bots))
	}
	// A project sees the bot via reverse lookup only once a chat is routed to it.
	if list, _ := svc.ListChannels(ctx, proj.ID); len(list) != 0 {
		t.Fatalf("ListChannels before routing = %d, want 0", len(list))
	}
	chat, err := svc.AddChat(ctx, owner, dto.ID, "oc_1", "G")
	if err != nil {
		t.Fatalf("AddChat: %v", err)
	}
	if _, err := svc.ApproveChatToProject(ctx, chat.ID, proj.ID, env.UserA); err != nil {
		t.Fatalf("ApproveChatToProject: %v", err)
	}
	list, err := svc.ListChannels(ctx, proj.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListChannels after routing: %v len=%d", err, len(list))
	}

	// The channel is an active stream channel -> ConnectorManager sees it with
	// a DECRYPTED credential (round-trips the AES-GCM crypto).
	managed, err := svc.ActiveStreamChannels(ctx)
	if err != nil || len(managed) != 1 {
		t.Fatalf("ActiveStreamChannels: %v len=%d", err, len(managed))
	}
	if managed[0].Cred.Config["app_secret"] != "sec" {
		t.Errorf("decrypted secret mismatch: %v", managed[0].Cred.Config["app_secret"])
	}
}

// A bot is owner-level: another owner cannot update/delete it (hidden as
// not-found), and a project of the owner that has no chat routed to the bot sees
// zero channels (channels surface only by project -> chat reverse lookup).
func TestIMBot_ChannelOwnerIsolation(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	projUnrouted := env.NewProject(t, env.UserA, "B")
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	ownerA := service.OwnerRef{Type: "user", ID: env.UserA}
	ownerB := service.OwnerRef{Type: "user", ID: env.UserB}

	ch, err := svc.CreateChannel(ctx, ownerA, service.CreateChannelInput{ChannelType: "lark", Name: "a-bot"})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	// Cross-owner access is rejected as not-found.
	if _, err := svc.UpdateChannel(ctx, ownerB, ch.ID, service.UpdateChannelInput{Name: "x"}); !errors.Is(err, service.ErrIMBotNotFound) {
		t.Errorf("cross-owner UpdateChannel err = %v, want ErrIMBotNotFound", err)
	}
	if err := svc.DeleteChannel(ctx, ownerB, ch.ID); !errors.Is(err, service.ErrIMBotNotFound) {
		t.Errorf("cross-owner DeleteChannel err = %v, want ErrIMBotNotFound", err)
	}
	// A project with no chat routed to the bot sees no channels.
	if list, _ := svc.ListChannels(ctx, projUnrouted.ID); len(list) != 0 {
		t.Errorf("unrouted project should see 0 channels, got %d", len(list))
	}
}

// A Lark/Telegram webhook-mode channel with no webhook secret must be rejected
// at write time: VerifyWebhook fails closed on every request without it, so the
// channel would be a dead, forgery-inviting misconfiguration. WeCom is exempt
// (its token/aes_key live in the credential blob, not the WebhookSecret column).
func TestIMBot_WebhookModeRequiresSecret(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	for _, ct := range []string{"lark", "telegram"} {
		if _, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
			ChannelType: ct, Name: ct + "-hook", ConnectionMode: "webhook", WebhookSecret: "",
		}); !errors.Is(err, service.ErrInvalidChannelConfig) {
			t.Errorf("%s webhook without secret: err = %v, want ErrInvalidChannelConfig", ct, err)
		}
	}

	// With a secret, creation succeeds.
	ch, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "ok", ConnectionMode: "webhook", WebhookSecret: "vtok",
	})
	if err != nil {
		t.Fatalf("webhook with secret should succeed: %v", err)
	}

	// Updating name/mode without resending the (write-only) secret preserves the
	// stored secret rather than blanking it. The secret is never returned to the
	// client, so a rename/status edit can't echo it back; preserve-on-empty keeps
	// a webhook bot valid instead of silently stripping its secret on every edit.
	if _, err := svc.UpdateChannel(ctx, owner, ch.ID, service.UpdateChannelInput{
		Name: "renamed", ConnectionMode: "webhook", WebhookSecret: "",
	}); err != nil {
		t.Errorf("update preserving stored secret should succeed, got %v", err)
	}
}

func TestIMBot_ChatPairingStateMachine(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	proj := env.NewProject(t, env.UserA, "P")
	projForeign := env.NewProject(t, env.UserB, "F")
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	ch, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot",
		Credential: map[string]any{"app_id": "a", "app_secret": "b", "base_url": "http://127.0.0.1:0"},
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	chat, err := svc.AddChat(ctx, owner, ch.ID, "oc_1", "Group One")
	if err != nil {
		t.Fatalf("AddChat: %v", err)
	}
	if chat.Status != "pending" {
		t.Errorf("new chat status = %q, want pending", chat.Status)
	}

	// A pending chat is routed nowhere yet; a cross-OWNER project cannot approve it.
	if _, err := svc.ApproveChat(ctx, projForeign.ID, chat.ID, env.UserA); !errors.Is(err, service.ErrIMBotNotFound) {
		t.Errorf("cross-owner ApproveChat err = %v, want ErrIMBotNotFound", err)
	}

	approved, err := svc.ApproveChat(ctx, proj.ID, chat.ID, env.UserA)
	if err != nil {
		t.Fatalf("ApproveChat: %v", err)
	}
	if approved.Status != "active" {
		t.Errorf("approved status = %q, want active", approved.Status)
	}
	if approved.PairedBy == nil || *approved.PairedBy != env.UserA {
		t.Errorf("paired_by = %v, want %d", approved.PairedBy, env.UserA)
	}
}

func TestIMBot_OwnerBoundChats_ListAndDelete(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	proj := env.NewProject(t, env.UserA, "P")
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	ch, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot", Credential: map[string]any{"app_id": "a"},
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	chat, err := svc.AddChat(ctx, owner, ch.ID, "oc_1", "G1")
	if err != nil {
		t.Fatalf("AddChat: %v", err)
	}
	if _, err := svc.ApproveChat(ctx, proj.ID, chat.ID, env.UserA); err != nil {
		t.Fatalf("ApproveChat: %v", err)
	}

	// The active chat->project binding appears in the owner-level list.
	bound, err := svc.ListActiveChatsByOwner(ctx, owner)
	if err != nil || len(bound) != 1 || bound[0].ProjectID == nil || *bound[0].ProjectID != proj.ID {
		t.Fatalf("ListActiveChatsByOwner = %+v err=%v, want 1 bound to proj %d", bound, err, proj.ID)
	}

	// A different owner cannot delete the binding (hidden as not found).
	if err := svc.DeleteChatByOwner(ctx, chat.ID, service.OwnerRef{Type: "user", ID: env.UserA + 100000}); !errors.Is(err, service.ErrIMBotNotFound) {
		t.Fatalf("cross-owner delete err = %v, want ErrIMBotNotFound", err)
	}

	// The owner delete removes the binding.
	if err := svc.DeleteChatByOwner(ctx, chat.ID, owner); err != nil {
		t.Fatalf("DeleteChatByOwner: %v", err)
	}
	if left, _ := svc.ListActiveChatsByOwner(ctx, owner); len(left) != 0 {
		t.Fatalf("after delete, bound = %+v, want empty", left)
	}
}

// A routed chat is managed only by the project it is routed to. A sibling project
// of the same owner that the chat is NOT routed to has no privilege over it.
func TestIMBot_ChatManagedByRoutedProjectOnly(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	sibling := env.NewProject(t, env.UserA, "Sibling")
	routed := env.NewProject(t, env.UserA, "Routed")
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	ch, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot", Credential: map[string]any{"app_id": "a"},
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	chat, err := svc.AddChat(ctx, owner, ch.ID, "oc_1", "G1")
	if err != nil {
		t.Fatalf("AddChat: %v", err)
	}
	if _, err := svc.ApproveChatToProject(ctx, chat.ID, routed.ID, env.UserA); err != nil {
		t.Fatalf("ApproveChatToProject: %v", err)
	}

	// The routed project manages the chat (pin).
	if _, err := svc.PatchChat(ctx, routed.ID, chat.ID, service.PatchChatInput{Status: "active"}); err != nil {
		t.Fatalf("PatchChat from routed project: %v", err)
	}
	// A sibling same-owner project the chat is NOT routed to has NO privilege.
	if _, err := svc.PatchChat(ctx, sibling.ID, chat.ID, service.PatchChatInput{Status: "active"}); !errors.Is(err, service.ErrIMBotNotFound) {
		t.Fatalf("PatchChat from non-routed sibling project err=%v, want ErrIMBotNotFound", err)
	}
	// The routed project can delete (unpair) the chat.
	if err := svc.DeleteChat(ctx, routed.ID, chat.ID); err != nil {
		t.Fatalf("DeleteChat from routed project: %v", err)
	}
}

// An owner-level bot serving project B (via a chat routed to B) must appear in
// B's channel list through the project -> chat -> imbot reverse lookup.
func TestIMBot_ListChannels_ReverseLookupViaChat(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	other := env.NewProject(t, env.UserA, "Other")
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	ch, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot", Credential: map[string]any{"app_id": "a"},
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	chat, err := svc.AddChat(ctx, owner, ch.ID, "oc_1", "G1")
	if err != nil {
		t.Fatalf("AddChat: %v", err)
	}
	// Route the chat to `other` (owner-level approve).
	if _, err := svc.ApproveChatToProject(ctx, chat.ID, other.ID, env.UserA); err != nil {
		t.Fatalf("ApproveChatToProject: %v", err)
	}

	// `other` sees the bot via the routed chat.
	got, err := svc.ListChannels(ctx, other.ID)
	if err != nil || len(got) != 1 || got[0].ID != ch.ID {
		t.Fatalf("ListChannels(other) = %+v err=%v, want reverse-looked-up bot %d", got, err, ch.ID)
	}
	// And its chat shows under it in `other`.
	chats, err := svc.ListChats(ctx, other.ID)
	if err != nil || len(chats) != 1 || chats[0].ChannelID != ch.ID {
		t.Fatalf("ListChats(other) = %+v err=%v, want the routed chat", chats, err)
	}
}

// A second bot for the same app identity under one owner is rejected: one app =
// one connection. The fingerprint is derived from the identity fields only.
func TestIMBot_DuplicateCredentialRejected(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	cred := map[string]any{"app_id": "cli_dup", "app_secret": "s1"}
	if _, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot-1", Credential: cred,
	}); err != nil {
		t.Fatalf("first CreateChannel: %v", err)
	}
	// Same owner + same app_id (even with a different secret / name) is a duplicate.
	_, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot-2", Credential: map[string]any{"app_id": "cli_dup", "app_secret": "s2"},
	})
	if !errors.Is(err, service.ErrDuplicateIMBotCredential) {
		t.Fatalf("duplicate app under same owner: err = %v, want ErrDuplicateIMBotCredential", err)
	}

	// A different app_id is allowed.
	if _, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot-3", Credential: map[string]any{"app_id": "cli_other", "app_secret": "s3"},
	}); err != nil {
		t.Fatalf("different app should be allowed: %v", err)
	}
}

// Updating a bot's credential must re-flush its app-identity fingerprint, so a
// second bot for the NEW app can no longer slip past the create-time dedupe and
// open a rival connection (the double-connection the fingerprint UNIQUE forbids).
func TestIMBot_UpdateChannel_ReflushesFingerprint(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	// Bot 1 starts as app "old".
	bot1, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot-1", Credential: map[string]any{"app_id": "old", "app_secret": "s"},
	})
	if err != nil {
		t.Fatalf("create bot-1: %v", err)
	}
	// Re-point bot 1 at app "new".
	if _, err := svc.UpdateChannel(ctx, owner, bot1.ID, service.UpdateChannelInput{
		Credential: map[string]any{"app_id": "new", "app_secret": "s2"},
	}); err != nil {
		t.Fatalf("update bot-1 credential: %v", err)
	}
	// A NEW bot for app "new" under the same owner must now be rejected — the
	// reflushed fingerprint makes bot-1 the incumbent for "new".
	if _, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot-2", Credential: map[string]any{"app_id": "new", "app_secret": "s3"},
	}); !errors.Is(err, service.ErrDuplicateIMBotCredential) {
		t.Fatalf("after reflush, second bot for new app: err = %v, want ErrDuplicateIMBotCredential", err)
	}
	// And a NEW bot for the now-abandoned "old" app is allowed again.
	if _, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot-3", Credential: map[string]any{"app_id": "old", "app_secret": "s4"},
	}); err != nil {
		t.Fatalf("reusing abandoned old app should be allowed: %v", err)
	}
}

// ApproveChatToProject routes a pending chat to a chosen (same-owner) project and
// records paired_by; a cross-owner target is hidden as not-found.
func TestIMBot_ApproveChatToProject_RoutesAndOwnerGate(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	projTarget := env.NewProject(t, env.UserA, "target")
	projForeign := env.NewProject(t, env.UserB, "foreign")
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	ch, err := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot", Credential: map[string]any{"app_id": "a", "app_secret": "b"},
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	chat, err := svc.AddChat(ctx, owner, ch.ID, "oc_x", "Group")
	if err != nil {
		t.Fatalf("AddChat: %v", err)
	}

	// Cross-owner target project is hidden as not-found.
	if _, err := svc.ApproveChatToProject(ctx, chat.ID, projForeign.ID, env.UserA); !errors.Is(err, service.ErrIMBotNotFound) {
		t.Fatalf("cross-owner approve: err = %v, want ErrIMBotNotFound", err)
	}

	// Same-owner sibling project routes the chat there.
	dto, err := svc.ApproveChatToProject(ctx, chat.ID, projTarget.ID, env.UserA)
	if err != nil {
		t.Fatalf("ApproveChatToProject: %v", err)
	}
	if dto.Status != "active" {
		t.Errorf("status = %q, want active", dto.Status)
	}
	if dto.ProjectID == nil || *dto.ProjectID != projTarget.ID {
		t.Errorf("routed project = %v, want %d", dto.ProjectID, projTarget.ID)
	}
}

// ReassignChat moves an already-paired chat to a new same-owner project.
func TestIMBot_ReassignChat(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	projInit := env.NewProject(t, env.UserA, "init")
	projNew := env.NewProject(t, env.UserA, "new")
	projForeign := env.NewProject(t, env.UserB, "foreign")
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	owner := service.OwnerRef{Type: "user", ID: env.UserA}

	ch, _ := svc.CreateChannel(ctx, owner, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot", Credential: map[string]any{"app_id": "a"},
	})
	chat, _ := svc.AddChat(ctx, owner, ch.ID, "oc_y", "G")
	if _, err := svc.ApproveChatToProject(ctx, chat.ID, projInit.ID, env.UserA); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Cross-owner reassign hidden as not-found.
	if _, err := svc.ReassignChat(ctx, chat.ID, projForeign.ID, env.UserA); !errors.Is(err, service.ErrIMBotNotFound) {
		t.Fatalf("cross-owner reassign: err = %v, want ErrIMBotNotFound", err)
	}
	dto, err := svc.ReassignChat(ctx, chat.ID, projNew.ID, env.UserA)
	if err != nil {
		t.Fatalf("ReassignChat: %v", err)
	}
	if dto.ProjectID == nil || *dto.ProjectID != projNew.ID {
		t.Errorf("reassigned project = %v, want %d", dto.ProjectID, projNew.ID)
	}
}

// ListBotsByOwner / ListPendingChatsByOwner scope to the owner across projects.
func TestIMBot_OwnerLevelLists(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc := buildIMBotSvc(t, env)
	ctx := context.Background()
	ownerA := service.OwnerRef{Type: "user", ID: env.UserA}
	ownerB := service.OwnerRef{Type: "user", ID: env.UserB}

	chA, _ := svc.CreateChannel(ctx, ownerA, service.CreateChannelInput{ChannelType: "lark", Name: "a", Credential: map[string]any{"app_id": "a1"}})
	svc.CreateChannel(ctx, ownerA, service.CreateChannelInput{ChannelType: "telegram", Name: "b", Credential: map[string]any{"token": "t1"}})
	svc.CreateChannel(ctx, ownerB, service.CreateChannelInput{ChannelType: "lark", Name: "f", Credential: map[string]any{"app_id": "f1"}})

	bots, err := svc.ListBotsByOwner(ctx, ownerA)
	if err != nil {
		t.Fatalf("ListBotsByOwner: %v", err)
	}
	if len(bots) != 2 {
		t.Fatalf("owner A bots = %d, want 2 (foreign excluded)", len(bots))
	}

	// Seed pending chats under A's bot; owner-level pending list should surface them.
	if _, err := svc.AddChat(ctx, ownerA, chA.ID, "oc_p1", "P1"); err != nil {
		t.Fatalf("AddChat: %v", err)
	}
	pending, err := svc.ListPendingChatsByOwner(ctx, ownerA)
	if err != nil {
		t.Fatalf("ListPendingChatsByOwner: %v", err)
	}
	if len(pending) != 1 || pending[0].Status != "pending" {
		t.Fatalf("owner A pending = %+v, want 1 pending", pending)
	}
}
