package opsbot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/stretchr/testify/require"
)

func newTestOnboarding(t *testing.T) *Onboarding {
	t.Helper()
	svc := newTestService(t)
	svc.Packs = onboardingFakePacks()
	return NewOnboarding(svc)
}

// onboardingFakePacks adds the content schema the onboarding flow inspects for
// video capability (小红书 has a video format).
func onboardingFakePacks() *fakePacks {
	fp := defaultFakePacks()
	fp.manifests["xiaohongshu"][0].ContentSchema = packs.ContentSchema{Formats: map[string]packs.ContentFormat{
		"note":  {MaxTitleChars: 20, ImageRatio: "3:4", MaxImages: 18},
		"video": {MaxTitleChars: 20, VideoSec: 900},
	}}
	return fp
}

func step(t *testing.T, o *Onboarding, chatID int64, owner OwnerRef, projectID int64, in, want string) (string, bool) {
	t.Helper()
	reply, done, err := o.Handle(context.Background(), chatID, owner, projectID, in)
	require.NoError(t, err)
	require.Contains(t, reply, want)
	return reply, done
}

func TestOnboardingCreateAccount(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{"user", 1}
	steps := []struct{ in, want string }{
		{"接入公众号", "账号名称"},
		{"品牌 A", "目标"},
		{"涨粉", "每周"},
		{"每周 2 篇，热点加更", "预算"},
		{"每月 200", "确认"},
		{"需要", "已创建"},
	}
	for _, s := range steps {
		reply, done := step(t, o, 10, owner, 42, s.in, s.want)
		if s.want == "已创建" {
			require.True(t, done)
			require.Contains(t, reply, "每月 ¥200")
			require.Contains(t, reply, "每周 2 篇（热点加更）")
			require.Contains(t, reply, "发布/回复/视频 需确认")
		}
	}

	bots, err := o.Service.ListByOwner(context.Background(), owner)
	require.NoError(t, err)
	require.Len(t, bots, 1)
	require.Equal(t, "wechat_mp", bots[0].PackID)
	require.Equal(t, "品牌 A", bots[0].AccountRef)
	require.Equal(t, "{}", bots[0].CredentialRefsJson)
}

func TestOnboardingSecondAccountReusesPack(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{"user", 1}
	o.Handle(context.Background(), 10, owner, 42, "接入公众号")
	for _, in := range []string{"A", "涨粉", "每周 2 篇", "每月 100", "需要"} {
		_, _, err := o.Handle(context.Background(), 10, owner, 42, in)
		require.NoError(t, err)
	}
	reply, _, err := o.Handle(context.Background(), 11, owner, 42, "再接入一个公众号")
	require.NoError(t, err)
	require.Contains(t, reply, "账号名称")
	require.Contains(t, reply, "wechat_mp")

	for _, in := range []string{"B", "品牌", "每周 1 篇", "不限", "不需要"} {
		_, _, err := o.Handle(context.Background(), 11, owner, 42, in)
		require.NoError(t, err)
	}

	bots, err := o.Service.ListByOwner(context.Background(), owner)
	require.NoError(t, err)
	require.Len(t, bots, 2)
	require.Equal(t, bots[0].PackID, bots[1].PackID, "both accounts share the same pack")
	require.NotEqual(t, bots[0].AccountRef, bots[1].AccountRef)
	require.NotEqual(t, bots[0].GoalsJson, bots[1].GoalsJson, "config is per-account")
}

func TestOnboardingXiaohongshuAsksDataSourceAndVideoModel(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{"user", 1}
	steps := []struct{ in, want string }{
		{"接入小红书", "账号名称"},
		{"Red A", "数据源"},
		{"不接入", "视频"},
		{"默认", "目标"},
		{"涨粉", "每周"},
		{"每周 3 篇", "预算"},
		{"不限", "确认"},
		{"不需要", "已创建"},
	}
	for _, s := range steps {
		step(t, o, 20, owner, 42, s.in, s.want)
	}

	bots, err := o.Service.ListByOwner(context.Background(), owner)
	require.NoError(t, err)
	require.Len(t, bots, 1)
	require.Contains(t, bots[0].KnowledgeScopeJson, `"data_source":{"enabled":false}`)
	require.Contains(t, bots[0].KnowledgeScopeJson, `"video_model":{"provider":"default"}`)
	require.Contains(t, bots[0].BudgetJson, `"unlimited":true`)
}

type fakeGenerator struct {
	packs     *fakePacks
	drafted   bool
	tested    bool
	published bool
}

func (f *fakeGenerator) Draft(_ context.Context, _ OwnerRef, channel string) (PackDraft, error) {
	f.drafted = true
	return PackDraft{
		PackID:       "facebook",
		Version:      1,
		DisplayName:  "Facebook",
		Capabilities: []string{"publish：官方 Graph API", "metrics：官方 Graph API"},
		Risks:        []string{"需要 Page 权限与 App 审核"},
	}, nil
}

func (f *fakeGenerator) Test(_ context.Context, _ OwnerRef, _ string, _ int64) error {
	f.tested = true
	return nil
}

func (f *fakeGenerator) Publish(_ context.Context, _ OwnerRef, packID string, version int64) error {
	f.published = true
	f.packs.manifests[packID] = append(f.packs.manifests[packID], packs.Manifest{
		ID: packID, Version: int(version), Status: "published", DisplayName: "Facebook",
	})
	return nil
}

func TestOnboardingNewChannelDraftsTestsAndPublishesPack(t *testing.T) {
	svc := newTestService(t)
	fp := onboardingFakePacks()
	svc.Packs = fp
	gen := &fakeGenerator{packs: fp}
	o := NewOnboarding(svc)
	o.Generator = gen
	owner := OwnerRef{"user", 1}

	steps := []struct{ in, want string }{
		{"接入 Facebook", "能力矩阵"},
		{"继续", "测试账号"},
		{"跑", "已发布"},
		{"Page A", "目标"},
		{"品牌", "每周"},
		{"每周 1 篇", "预算"},
		{"每月 50", "确认"},
		{"需要", "已创建"},
	}
	for _, s := range steps {
		step(t, o, 30, owner, 42, s.in, s.want)
	}
	require.True(t, gen.drafted)
	require.True(t, gen.tested)
	require.True(t, gen.published)

	bots, err := svc.ListByOwner(context.Background(), owner)
	require.NoError(t, err)
	require.Len(t, bots, 1)
	require.Equal(t, "facebook", bots[0].PackID)
	require.Equal(t, int64(1), bots[0].PackVersion)
}

type fakeLinker struct {
	called bool
	link   string
}

func (f *fakeLinker) IssueAccountLink(_ context.Context, _ OwnerRef, _ int64, _, _ string) (string, error) {
	f.called = true
	return f.link, nil
}

func TestOnboardingCredentialLinkNeverStoresSecrets(t *testing.T) {
	o := newTestOnboarding(t)
	linker := &fakeLinker{link: "https://example.test/cred/once"}
	o.Credentials = linker
	owner := OwnerRef{"user", 1}

	step(t, o, 40, owner, 42, "接入公众号", "账号名称")
	for _, in := range []string{"品牌 A", "涨粉", "每周 2 篇", "每月 200"} {
		reply, _, err := o.Handle(context.Background(), 40, owner, 42, in)
		require.NoError(t, err)
		require.NotContains(t, reply, "https://example.test")
	}
	reply, done, err := o.Handle(context.Background(), 40, owner, 42, "需要")
	require.NoError(t, err)
	require.False(t, done)
	require.Contains(t, reply, "https://example.test/cred/once")
	require.NotContains(t, reply, "已创建")
	require.True(t, linker.called)

	// The credential stage must not capture whatever the user types as a secret.
	const secret = "sk-live-SECRET"
	reply, done, err = o.Handle(context.Background(), 40, owner, 42, secret)
	require.NoError(t, err)
	require.True(t, done)
	require.Contains(t, reply, "已创建")
	require.NotContains(t, reply, secret)

	bots, err := o.Service.ListByOwner(context.Background(), owner)
	require.NoError(t, err)
	require.Len(t, bots, 1)
	require.Equal(t, "{}", bots[0].CredentialRefsJson)
	require.NotContains(t, bots[0].PersonaJson, secret)
	require.NotContains(t, bots[0].KnowledgeScopeJson, secret)
	require.NotContains(t, bots[0].GoalsJson, secret)
	require.NotContains(t, bots[0].CadenceJson, secret)
	require.NotContains(t, bots[0].BudgetJson, secret)
	require.NotContains(t, bots[0].ApprovalPolicyJson, secret)
}

func TestOnboardingCancelAndSessionExpiry(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{"user", 1}
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	o.Now = func() time.Time { return now }

	step(t, o, 50, owner, 42, "接入公众号", "账号名称")
	reply, _, err := o.Handle(context.Background(), 50, owner, 42, "/cancel")
	require.NoError(t, err)
	require.Contains(t, reply, "已取消")

	reply, _, err = o.Handle(context.Background(), 50, owner, 42, "涨粉")
	require.NoError(t, err)
	require.Empty(t, reply, "cancelled session must not consume later messages")

	// A session idle beyond the TTL is dropped.
	step(t, o, 51, owner, 42, "接入公众号", "账号名称")
	now = now.Add(31 * time.Minute)
	reply, _, err = o.Handle(context.Background(), 51, owner, 42, "品牌 A")
	require.NoError(t, err)
	require.Empty(t, reply)

	reply, _, err = o.Handle(context.Background(), 51, owner, 42, "接入公众号")
	require.NoError(t, err)
	require.Contains(t, reply, "账号名称")
}

func TestOnboardingMatchesOnlyClaimsItsOwnInput(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{"user", 1}

	require.True(t, o.Matches(70, "接入公众号"))
	require.True(t, o.Matches(70, "再接入一个小红书"))
	require.True(t, o.Matches(70, "/bots"))
	require.True(t, o.Matches(70, "/budget #1"))
	require.True(t, o.Matches(70, "/cancel"))
	require.False(t, o.Matches(70, "帮我写一篇推文"))
	require.False(t, o.Matches(70, "/new 任务"))
	require.False(t, o.Matches(70, "/stop"))

	// Once a flow is active, follow-up answers belong to it.
	step(t, o, 70, owner, 42, "接入公众号", "账号名称")
	require.True(t, o.Matches(70, "随便一句话"))
	require.False(t, o.Matches(71, "随便一句话"), "sessions are per-chat")
}

func TestOnboardingConfigCommands(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{"user", 1}

	reply, _, err := o.Handle(context.Background(), 60, owner, 42, "/bots")
	require.NoError(t, err)
	require.Contains(t, reply, "还没有运营 Bot")

	for _, in := range []string{"接入公众号", "品牌 A", "涨粉", "每周 2 篇", "每月 200", "需要"} {
		_, _, err := o.Handle(context.Background(), 60, owner, 42, in)
		require.NoError(t, err)
	}

	reply, _, err = o.Handle(context.Background(), 60, owner, 42, "/bots")
	require.NoError(t, err)
	require.Contains(t, reply, "品牌 A")
	require.Contains(t, reply, "wechat_mp v2")

	reply, _, err = o.Handle(context.Background(), 60, owner, 42, "/pause #1")
	require.NoError(t, err)
	require.Contains(t, reply, "已暂停")

	reply, _, err = o.Handle(context.Background(), 60, owner, 42, "/budget #1")
	require.NoError(t, err)
	require.Contains(t, reply, "每月 ¥200")
	require.Contains(t, reply, "paused")

	reply, _, err = o.Handle(context.Background(), 60, owner, 42, "/resume #1")
	require.NoError(t, err)
	require.Contains(t, reply, "已恢复")

	reply, _, err = o.Handle(context.Background(), 60, owner, 42, "/calendar")
	require.NoError(t, err)
	require.Contains(t, reply, "内容日历")
}

func TestOnboardingRequiresLoginVerification(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{"user", 1}
	calls := 0
	o.VerifyLogin = func(context.Context, int64) error {
		calls++
		return errors.New("session expired")
	}

	for _, in := range []string{"接入公众号", "品牌 A", "涨粉", "每周 2 篇", "每月 200"} {
		_, _, err := o.Handle(context.Background(), 20, owner, 42, in)
		require.NoError(t, err)
	}
	reply, done := step(t, o, 20, owner, 42, "需要", "登录态验证未通过")
	require.False(t, done)
	require.Contains(t, reply, "opsbot_browser_login")

	reply, done = step(t, o, 20, owner, 42, "完成", "登录态仍未通过")
	require.False(t, done)
	require.Equal(t, 2, calls)
}

func TestOnboardingVerifiesLoginBeforeCompletion(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{"user", 1}
	calls := 0
	o.VerifyLogin = func(context.Context, int64) error {
		calls++
		return nil
	}

	for _, in := range []string{"接入公众号", "品牌 A", "涨粉", "每周 2 篇", "每月 200"} {
		_, _, err := o.Handle(context.Background(), 21, owner, 42, in)
		require.NoError(t, err)
	}
	_, done := step(t, o, 21, owner, 42, "需要", "已创建")
	require.True(t, done)
	require.Equal(t, 1, calls)
}

func TestOnboardingCredentialCompletionVerifiesLogin(t *testing.T) {
	o := newTestOnboarding(t)
	o.Credentials = &fakeLinker{link: "https://example.test/cred/once"}
	owner := OwnerRef{"user", 1}

	for _, in := range []string{"接入公众号", "品牌 A", "涨粉", "每周 2 篇", "每月 200"} {
		_, _, err := o.Handle(context.Background(), 22, owner, 42, in)
		require.NoError(t, err)
	}
	step(t, o, 22, owner, 42, "需要", "https://example.test/cred/once")

	o.VerifyLogin = func(context.Context, int64) error { return errors.New("not logged in") }
	reply, done := step(t, o, 22, owner, 42, "完成", "登录态仍未通过")
	require.False(t, done)
	require.Contains(t, reply, "opsbot_browser_login")

	o.VerifyLogin = func(context.Context, int64) error { return nil }
	_, done = step(t, o, 22, owner, 42, "完成", "已创建")
	require.True(t, done)
}
