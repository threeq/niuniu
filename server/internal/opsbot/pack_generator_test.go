package opsbot

import (
	"context"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/stretchr/testify/require"
)

func TestPackDraftGeneratorAsksSourceThenMissingFacts(t *testing.T) {
	registry := &packs.Registry{DataDir: t.TempDir()}
	gen := &packs.Generator{
		Registry: registry,
		Owner:    packs.Owner{Type: "user", ID: 1},
		RecipeTester: func(context.Context, string, packs.Recipe, map[string]string) error {
			return nil
		},
	}
	adapter := &PackDraftGenerator{Generator: gen}
	owner := OwnerRef{"user", 1}
	ctx := context.Background()

	first, err := adapter.Draft(ctx, owner, "zhihu")
	require.NoError(t, err)
	require.Len(t, first.Questions, 1)
	require.Equal(t, qOpenAPIDocument, first.Questions[0].Key)

	answers := map[string]string{qOpenAPIDocument: "skip"}
	source, err := adapter.DraftWithAnswers(ctx, owner, "zhihu", answers)
	require.NoError(t, err)
	require.NotEmpty(t, source.Questions, "a browser draft must ask for its missing facts")

	for _, question := range source.Questions {
		answers[question.Key] = sampleAnswer(question.Key)
	}

	draft, err := adapter.DraftWithAnswers(ctx, owner, "zhihu", answers)
	require.NoError(t, err)
	require.Empty(t, draft.Questions)
	require.Equal(t, "zhihu", draft.PackID)
	require.Equal(t, int64(1), draft.Version)

	require.NoError(t, adapter.Test(ctx, owner, draft.PackID, draft.Version))
	require.NoError(t, adapter.Publish(ctx, owner, draft.PackID, draft.Version))

	published, err := registry.LatestPublished("user", 1, "zhihu")
	require.NoError(t, err)
	require.Equal(t, 1, published.Version)
}

func sampleAnswer(key string) string {
	switch {
	case key == "allowed_hosts":
		return "example.test"
	case key == qMetricsMap:
		return "view_num=views, like_num=likes"
	case strings.HasPrefix(key, "fixture."):
		return "<html><body><div class=\"fixture\">sample</div></body></html>"
	case strings.HasSuffix(key, "_url"):
		return "https://example.test/" + key
	default:
		return "." + key
	}
}

// --- onboarding question loop ---------------------------------------------

type fakeAnswerableGenerator struct {
	answersSeen map[string]string
	tested      bool
	published   bool
}

func (f *fakeAnswerableGenerator) Draft(context.Context, OwnerRef, string) (PackDraft, error) {
	return PackDraft{Questions: []PackQuestion{
		{Key: "host", Prompt: "主域名是什么？"},
		{Key: "selector", Prompt: "发布按钮选择器是什么？"},
	}}, nil
}

func (f *fakeAnswerableGenerator) DraftWithAnswers(_ context.Context, _ OwnerRef, _ string, answers map[string]string) (PackDraft, error) {
	f.answersSeen = answers
	return PackDraft{
		PackID:       "newchan",
		Version:      1,
		DisplayName:  "NewChan",
		Capabilities: []string{"publish：browser，就绪"},
	}, nil
}

func (f *fakeAnswerableGenerator) Test(context.Context, OwnerRef, string, int64) error {
	f.tested = true
	return nil
}

func (f *fakeAnswerableGenerator) Publish(context.Context, OwnerRef, string, int64) error {
	f.published = true
	return nil
}

func TestOnboardingCollectsPackQuestionsBeforeDrafting(t *testing.T) {
	svc := newTestService(t)
	svc.Packs = onboardingFakePacks()
	gen := &fakeAnswerableGenerator{}
	o := NewOnboarding(svc)
	o.Generator = gen
	owner := OwnerRef{"user", 1}

	step(t, o, 60, owner, 42, "接入新渠道 MyChan", "主域名")
	step(t, o, 60, owner, 42, "example.com", "选择器")
	step(t, o, 60, owner, 42, ".post", "能力矩阵")
	require.Equal(t, map[string]string{"host": "example.com", "selector": ".post"}, gen.answersSeen)

	step(t, o, 60, owner, 42, "继续", "测试账号")
	step(t, o, 60, owner, 42, "跑", "已发布")
	require.True(t, gen.tested)
	require.True(t, gen.published)
}
