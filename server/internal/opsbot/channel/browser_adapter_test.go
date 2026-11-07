package channel

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/stretchr/testify/require"
)

func browserTestAdapter(worker BrowserWorker) *BrowserAdapter {
	recipes := RecipeSet{
		"login":       packs.Recipe{Name: "login"},
		"publish":     packs.Recipe{Name: "publish"},
		"metrics":     packs.Recipe{Name: "metrics"},
		"engagements": packs.Recipe{Name: "engagements"},
		"reply":       packs.Recipe{Name: "reply"},
	}
	return NewBrowserAdapter(worker, recipes,
		[]string{"publish", "metrics", "engagements", "reply"},
		BrowserLimits{MaxTitleChars: 100, MaxChars: 100000, MaxImages: 50, VideoSec: 900},
		map[string]string{"read_num": "views", "upvote_num": "likes", "share_num": "shares"})
}

func TestBrowserAdapterPublishesThroughPackRecipe(t *testing.T) {
	worker := &fakeBrowserWorker{byName: map[string]fakeBrowserResult{
		"publish": {result: browser.Result{Values: map[string]string{"url": "https://zhuanlan.zhihu.com/p/1"}}},
	}}
	adapter := browserTestAdapter(worker)
	ctx := context.Background()
	require.NoError(t, adapter.EnsureSession(ctx, "7"))

	receipt, err := adapter.Publish(ctx, BrowserPublishPayload{
		Title:  "知乎标题",
		Body:   "正文内容",
		Images: []string{"/img/a.jpg"},
		Tags:   []string{"#运营", "AI"},
	})
	require.NoError(t, err)
	require.Equal(t, "https://zhuanlan.zhihu.com/p/1", receipt.URL)

	task := worker.lastTask(t)
	require.Equal(t, int64(7), task.BotID)
	require.Equal(t, "publish", task.Recipe)
	require.Equal(t, "知乎标题", task.Input["title"])
	require.Equal(t, "正文内容", task.Input["body"])
	require.Equal(t, "/img/a.jpg", task.Input["images"])
	require.Equal(t, "运营,AI", task.Input["tags"])
}

func TestBrowserAdapterEnforcesTitleCap(t *testing.T) {
	adapter := NewBrowserAdapter(&fakeBrowserWorker{}, RecipeSet{"publish": {Name: "publish"}},
		[]string{"publish"}, BrowserLimits{MaxTitleChars: 3}, nil)
	require.NoError(t, adapter.EnsureSession(context.Background(), "7"))
	_, err := adapter.Publish(context.Background(), BrowserPublishPayload{Title: "四字以上", Body: "x"})
	require.Error(t, err)
	var adapterErr *AdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, CodeInvalidPayload, adapterErr.Code)
}

func TestBrowserAdapterMapsMetrics(t *testing.T) {
	worker := &fakeBrowserWorker{byName: map[string]fakeBrowserResult{
		"metrics": {result: browser.Result{Values: map[string]string{
			"read_num": "1,024", "upvote_num": "12", "share_num": "3",
		}}},
	}}
	adapter := browserTestAdapter(worker)
	require.NoError(t, adapter.EnsureSession(context.Background(), "7"))

	metrics, err := adapter.FetchMetrics(context.Background(), "2026-11-01", "2026-11-07")
	require.NoError(t, err)
	require.Equal(t, 1024.0, metrics["views"])
	require.Equal(t, 12.0, metrics["likes"])
	require.Equal(t, 3.0, metrics["shares"])
}

func TestBrowserAdapterMissingRecipeIsUnsupported(t *testing.T) {
	adapter := NewBrowserAdapter(&fakeBrowserWorker{}, RecipeSet{}, []string{"publish"}, BrowserLimits{}, nil)
	require.NoError(t, adapter.EnsureSession(context.Background(), "7"))
	_, err := adapter.Publish(context.Background(), BrowserPublishPayload{Title: "t", Body: "b"})
	var adapterErr *AdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, CodeUnsupported, adapterErr.Code)
}

func TestBrowserAdapterRequiresBoundAccount(t *testing.T) {
	adapter := browserTestAdapter(&fakeBrowserWorker{})
	_, err := adapter.Publish(context.Background(), BrowserPublishPayload{Title: "t", Body: "b"})
	var adapterErr *AdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, CodeCredentialInvalid, adapterErr.Code)
}
