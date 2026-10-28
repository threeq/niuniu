package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

func newShareSvc(t *testing.T, env *niutest.IsolationEnv) (*service.WorkspaceShareService, *[]*time.Time) {
	t.Helper()
	q := store.Wrap(env.DB).Queries()
	secret := []byte("test-secret-bytes-0123456789abcdef")
	settings := service.NewServerSettingsService(store.Wrap(env.DB))
	svc := service.NewWorkspaceShareService(q, settings, secret)
	// now 注入：测试通过推进这个指针控制时间。
	now := time.Now().UTC()
	times := []*time.Time{&now}
	svc.SetNowFunc(func() time.Time { return *times[0] })
	return svc, &times
}

func TestWorkspaceShare_CreateAndAccess(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc, _ := newShareSvc(t, env)
	ctx := context.Background()

	wsID := newShareTestWorkspace(t, env, "ws-share-1")

	expires := time.Now().UTC().Add(24 * time.Hour)
	info, err := svc.Create(ctx, wsID, env.UserA, "pass1234", expires, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.RawToken == "" || info.Path != "/share/ws/"+info.RawToken {
		t.Fatalf("unexpected token/path: %q %q", info.RawToken, info.Path)
	}
	if info.Share.ID <= 0 {
		t.Fatalf("share row not persisted: %+v", info.Share)
	}
	// token 只存哈希
	if info.Share.TokenHash == info.RawToken || len(info.Share.TokenHash) != 64 {
		t.Fatalf("token_hash should be sha256 hex, got %q", info.Share.TokenHash)
	}

	// 正常解析
	resolved, err := svc.ResolveActive(ctx, info.RawToken)
	if err != nil {
		t.Fatalf("ResolveActive: %v", err)
	}
	if resolved.WorkspaceID != wsID || resolved.WorkspaceName != "ws-share-1" {
		t.Fatalf("unexpected resolved: %+v", resolved)
	}

	// 未知 token → ErrShareUnavailable
	if _, err := svc.ResolveActive(ctx, "bogus-token"); !errors.Is(err, service.ErrShareUnavailable) {
		t.Fatalf("unknown token err = %v, want ErrShareUnavailable", err)
	}

	// 密码
	if err := svc.VerifyPassword(ctx, resolved.Share, "wrong"); !errors.Is(err, service.ErrSharePassword) {
		t.Fatalf("wrong password err = %v, want ErrSharePassword", err)
	}
	if err := svc.VerifyPassword(ctx, resolved.Share, "pass1234"); err != nil {
		t.Fatalf("correct password: %v", err)
	}
}

func TestWorkspaceShare_ExpiryAndRevoke(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc, times := newShareSvc(t, env)
	ctx := context.Background()
	wsID := newShareTestWorkspace(t, env, "ws-share-2")

	info, err := svc.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(time.Hour), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 撤销后不可访问
	if err := svc.Revoke(ctx, wsID, info.Share.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := svc.ResolveActive(ctx, info.RawToken); !errors.Is(err, service.ErrShareUnavailable) {
		t.Fatalf("revoked token err = %v, want ErrShareUnavailable", err)
	}

	// 过期后不可访问（新分享，直接把时钟推过 expires_at）
	info2, err := svc.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(time.Hour), false)
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	*(*times)[0] = time.Now().UTC().Add(2 * time.Hour)
	if _, err := svc.ResolveActive(ctx, info2.RawToken); !errors.Is(err, service.ErrShareUnavailable) {
		t.Fatalf("expired token err = %v, want ErrShareUnavailable", err)
	}
}

func TestWorkspaceShare_CreateValidation(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc, _ := newShareSvc(t, env)
	ctx := context.Background()
	wsID := newShareTestWorkspace(t, env, "ws-share-3")

	// 密码太短
	if _, err := svc.Create(ctx, wsID, env.UserA, "abc", time.Now().UTC().Add(time.Hour), false); err == nil {
		t.Fatal("short password should be rejected")
	}
	// 过去时间 / 超过 30 天
	if _, err := svc.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(-time.Hour), false); err == nil {
		t.Fatal("past expiry should be rejected")
	}
	if _, err := svc.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(31*24*time.Hour), false); err == nil {
		t.Fatal(">30d expiry should be rejected")
	}
}

func TestWorkspaceShare_UnlockCookie(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svcA, _ := newShareSvc(t, env)
	ctx := context.Background()
	wsID := newShareTestWorkspace(t, env, "ws-share-4")

	info, err := svcA.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(time.Hour), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	resolved, err := svcA.ResolveActive(ctx, info.RawToken)
	if err != nil {
		t.Fatalf("ResolveActive: %v", err)
	}

	cookie := svcA.SignUnlock(resolved.Share.ID, resolved.Share.TokenHash)
	if !svcA.VerifyUnlock(resolved.Share.ID, resolved.Share.TokenHash, cookie) {
		t.Fatal("freshly signed cookie should verify")
	}
	// 错的 share/token 绑定不通过
	if svcA.VerifyUnlock(resolved.Share.ID+1, resolved.Share.TokenHash, cookie) {
		t.Fatal("cookie must be bound to its share")
	}
	if svcA.VerifyUnlock(resolved.Share.ID, "deadbeef"+"0123456789abcdef0123456789abcdef012345", cookie) {
		t.Fatal("cookie must be bound to its token hash")
	}
	// 篡改
	if svcA.VerifyUnlock(resolved.Share.ID, resolved.Share.TokenHash, cookie+"x") {
		t.Fatal("tampered cookie must not verify")
	}
	// 过期 cookie：时钟推 2h（TTL 1h）
	now := time.Now().UTC()
	svcA.SetNowFunc(func() time.Time { return now.Add(2 * time.Hour) })
	if svcA.VerifyUnlock(resolved.Share.ID, resolved.Share.TokenHash, cookie) {
		t.Fatal("expired cookie must not verify")
	}
}

func TestWorkspaceShare_ListSweepsExpired(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc, times := newShareSvc(t, env)
	ctx := context.Background()
	wsID := newShareTestWorkspace(t, env, "ws-share-5")

	// expires_at 相对注入时钟计算
	base := *(*times)[0]
	if _, err := svc.Create(ctx, wsID, env.UserA, "pass1234", base.Add(time.Hour), false); err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	if _, err := svc.Create(ctx, wsID, env.UserA, "pass1234", base.Add(2*time.Hour), false); err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	// 推进 1.5h：第一条过期 0.5h，第二条未过期
	*(*times)[0] = base.Add(90 * time.Minute)
	shares, err := svc.List(ctx, wsID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(shares) != 1 {
		t.Fatalf("expected 1 share (expired one physically deleted), got %d", len(shares))
	}
	if shares[0].ExpiresAt.Sub(base) != 2*time.Hour {
		t.Fatalf("surviving share should be the 2h one, got %+v", shares[0])
	}

	// 过期行被物理删除：直接查表确认行数
	var n int
	if err := env.DB.QueryRow(`SELECT COUNT(*) FROM workspace_shares`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expired share row must be physically deleted, count=%d", n)
	}
}

func TestWorkspaceShare_ResolveExpiredDeletes(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc, times := newShareSvc(t, env)
	ctx := context.Background()
	wsID := newShareTestWorkspace(t, env, "ws-share-6")

	base := *(*times)[0]
	info, err := svc.Create(ctx, wsID, env.UserA, "pass1234", base.Add(time.Hour), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 推过 expires_at 后访问：404 且行被物理删除（密码哈希不残留）
	*(*times)[0] = base.Add(2 * time.Hour)
	if _, err := svc.ResolveActive(ctx, info.RawToken); !errors.Is(err, service.ErrShareUnavailable) {
		t.Fatalf("expired token err = %v, want ErrShareUnavailable", err)
	}
	var n int
	if err := env.DB.QueryRow(`SELECT COUNT(*) FROM workspace_shares WHERE id = ?`, info.Share.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expired share row must be gone after ResolveActive, count=%d", n)
	}
}

func TestWorkspaceShare_AllowChatToggle(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svc, _ := newShareSvc(t, env)
	ctx := context.Background()
	wsID := newShareTestWorkspace(t, env, "ws-share-7")

	// 默认只读
	info, err := svc.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(time.Hour), false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Share.AllowChat != 0 {
		t.Fatalf("allow_chat must default to 0, got %d", info.Share.AllowChat)
	}
	resolved, err := svc.ResolveActive(ctx, info.RawToken)
	if err != nil {
		t.Fatalf("ResolveActive: %v", err)
	}
	if resolved.CanChat() {
		t.Fatal("default share must not CanChat")
	}

	// 开启后 CanChat
	if err := svc.SetAllowChat(ctx, wsID, info.Share.ID, true); err != nil {
		t.Fatalf("SetAllowChat: %v", err)
	}
	resolved, err = svc.ResolveActive(ctx, info.RawToken)
	if err != nil {
		t.Fatalf("ResolveActive 2: %v", err)
	}
	if !resolved.CanChat() {
		t.Fatal("share with allow_chat=1 must CanChat")
	}

	// 关闭
	if err := svc.SetAllowChat(ctx, wsID, info.Share.ID, false); err != nil {
		t.Fatalf("SetAllowChat 2: %v", err)
	}
	resolved, _ = svc.ResolveActive(ctx, info.RawToken)
	if resolved.CanChat() {
		t.Fatal("share with allow_chat=0 must not CanChat")
	}
}

// newShareTestWorkspace 建一个最小可用的 user-owned 工作空间行。
func newShareTestWorkspace(t *testing.T, env *niutest.IsolationEnv, name string) int64 {
	t.Helper()
	r, err := env.DB.Exec(
		`INSERT INTO workspaces (name, path, owner_type, owner_id, created_by) VALUES (?, ?, 'user', ?, ?)`,
		name, "/tmp/"+name, env.UserA, env.UserA,
	)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
