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
	info, err := svc.Create(ctx, wsID, env.UserA, "pass1234", expires)
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

	info, err := svc.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(time.Hour))
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
	info2, err := svc.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(time.Hour))
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
	if _, err := svc.Create(ctx, wsID, env.UserA, "abc", time.Now().UTC().Add(time.Hour)); err == nil {
		t.Fatal("short password should be rejected")
	}
	// 过去时间 / 超过 30 天
	if _, err := svc.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(-time.Hour)); err == nil {
		t.Fatal("past expiry should be rejected")
	}
	if _, err := svc.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(31*24*time.Hour)); err == nil {
		t.Fatal(">30d expiry should be rejected")
	}
}

func TestWorkspaceShare_UnlockCookie(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	svcA, _ := newShareSvc(t, env)
	ctx := context.Background()
	wsID := newShareTestWorkspace(t, env, "ws-share-4")

	info, err := svcA.Create(ctx, wsID, env.UserA, "pass1234", time.Now().UTC().Add(time.Hour))
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

	// grace 内的过期分享仍在列表（expires_at 相对注入时钟计算）
	base := *(*times)[0]
	if _, err := svc.Create(ctx, wsID, env.UserA, "pass1234", base.Add(time.Hour)); err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	if _, err := svc.Create(ctx, wsID, env.UserA, "pass1234", base.Add(2*time.Hour)); err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	// 推进 25h：第一条过期 1h（grace 24h 内，仍在列表），第二条未过期
	*(*times)[0] = base.Add(25 * time.Hour)
	shares, err := svc.List(ctx, wsID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(shares) != 2 {
		t.Fatalf("expected 2 shares within grace, got %d", len(shares))
	}
	// 再推进 0.5h（累计 +25.5h）：第一条过期 24.5h（超 grace，被清理），
	// 第二条过期 23.5h（grace 内，保留）
	*(*times)[0] = base.Add(25*time.Hour + 30*time.Minute)
	shares, err = svc.List(ctx, wsID)
	if err != nil {
		t.Fatalf("List 2: %v", err)
	}
	if len(shares) != 1 {
		t.Fatalf("expected 1 share after sweep, got %d", len(shares))
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
