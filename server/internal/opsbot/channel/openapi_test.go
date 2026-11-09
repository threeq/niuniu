package channel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

type fakeOpsCaller struct {
	calls []OperationCall
	reply func(call OperationCall) OperationResult
}

func (f *fakeOpsCaller) CallOperation(_ context.Context, call OperationCall) (OperationResult, error) {
	f.calls = append(f.calls, call)
	if f.reply != nil {
		return f.reply(call), nil
	}
	return OperationResult{Status: 200, Body: map[string]any{}}, nil
}

func TestOpenAPIAdapterPublishSingleStep(t *testing.T) {
	caller := &fakeOpsCaller{reply: func(OperationCall) OperationResult {
		return OperationResult{Status: 201, Body: map[string]any{"publish_id": "p1", "article_url": "https://example/post"}}
	}}
	adapter := NewOpenAPIAdapter(caller, "wechat-mp", map[Capability]packs.Binding{
		CapPublish: {Transport: "openapi", Provider: "wechat-mp", Operation: "publish",
			Outputs: map[string]string{"id": "publish_id", "url": "article_url"}},
	}, nil)

	receipt, err := adapter.Publish(context.Background(), OpenAPIPublishPayload{Fields: map[string]any{"title": "标题"}})
	require.NoError(t, err)
	require.Equal(t, "p1", receipt.ExternalID)
	require.Equal(t, "https://example/post", receipt.URL)
	require.Len(t, caller.calls, 1)
	require.Equal(t, "标题", caller.calls[0].Params["title"])
}

func TestOpenAPIAdapterPublishMultiStepTemplates(t *testing.T) {
	caller := &fakeOpsCaller{reply: func(call OperationCall) OperationResult {
		switch call.Operation {
		case "upload":
			return OperationResult{Status: 200, Body: map[string]any{"media_id": "cover-media"}}
		case "draft":
			return OperationResult{Status: 200, Body: map[string]any{"media_id": "draft-media"}}
		default:
			return OperationResult{Status: 200, Body: map[string]any{"publish_id": "pub-9"}}
		}
	}}
	adapter := NewOpenAPIAdapter(caller, "wechat-mp", map[Capability]packs.Binding{
		CapPublish: {
			Transport:  "openapi",
			Provider:   "wechat-mp",
			Operations: map[string]string{"upload": "upload", "draft": "draft", "publish": "publish"},
			StepFields: map[string][]string{"upload": {"type"}, "draft": {"title", "content"}},
			Templates: map[string]any{
				"draft":   map[string]any{"articles": []any{map[string]any{"title": "{{title}}", "thumb_media_id": "{{media_id}}"}}},
				"publish": map[string]any{"media_id": "{{media_id}}"},
			},
			Files:   map[string]map[string]string{"upload": {"media": "cover_path"}},
			Outputs: map[string]string{"id": "publish_id"},
		},
	}, nil)

	receipt, err := adapter.Publish(context.Background(), OpenAPIPublishPayload{
		Fields: map[string]any{"type": "thumb", "title": "标题", "content": "正文"},
		Files:  map[string]string{"cover_path": "/tmp/cover.png"},
	})
	require.NoError(t, err)
	require.Equal(t, "pub-9", receipt.ExternalID)
	require.Len(t, caller.calls, 3)

	// Step 1: upload carries only the declared field and the file.
	require.Equal(t, "thumb", caller.calls[0].Params["type"])
	require.Equal(t, "/tmp/cover.png", caller.calls[0].Files["media"])
	require.NotContains(t, caller.calls[0].Params, "title")

	// Step 2: the draft template renders the uploaded media id.
	articles, ok := caller.calls[1].Body["articles"].([]any)
	require.True(t, ok)
	article := articles[0].(map[string]any)
	require.Equal(t, "标题", article["title"])
	require.Equal(t, "cover-media", article["thumb_media_id"])

	// Step 3: publish reuses the draft's media id.
	require.Equal(t, "draft-media", caller.calls[2].Body["media_id"])
}

func TestOpenAPIAdapterMetricsAndEngagements(t *testing.T) {
	caller := &fakeOpsCaller{reply: func(call OperationCall) OperationResult {
		switch call.Operation {
		case "metrics":
			return OperationResult{Status: 200, Body: map[string]any{"read_count": 12.0, "net_fans": 3.0}}
		default:
			return OperationResult{Status: 200, Body: map[string]any{"comment": []any{
				map[string]any{"user_comment_id": 7.0, "openid": "o1", "content": "不错"},
			}}}
		}
	}}
	adapter := NewOpenAPIAdapter(caller, "wechat-mp", map[Capability]packs.Binding{
		CapMetrics:     {Operation: "metrics", Params: map[string]string{"from": "begin", "to": "end"}},
		CapEngagements: {Operation: "comments", Items: "comment", Fields: map[string]string{"id": "user_comment_id", "author": "openid", "text": "content"}},
	}, map[string]string{"read_count": MetricViews, "net_fans": MetricFollowerDelta})

	metrics, err := adapter.FetchMetrics(context.Background(), "2026-11-01", "2026-11-02")
	require.NoError(t, err)
	require.Equal(t, 12.0, metrics[MetricViews])
	require.Equal(t, 3.0, metrics[MetricFollowerDelta])

	engagements, err := adapter.ListEngagements(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, engagements, 1)
	require.Equal(t, "7", engagements[0].ExtID)
	require.Equal(t, "o1", engagements[0].Author)
}

func TestOpenAPIAdapterNestedEngagementsAndReply(t *testing.T) {
	caller := &fakeOpsCaller{reply: func(call OperationCall) OperationResult {
		switch call.Operation {
		case "articles":
			return OperationResult{Status: 200, Body: map[string]any{"item": []any{
				map[string]any{"article_id": "art-1"},
			}}}
		case "comments":
			return OperationResult{Status: 200, Body: map[string]any{"comment": []any{
				map[string]any{"user_comment_id": 5.0, "openid": "o2", "content": "求链接"},
			}}}
		default:
			return OperationResult{Status: 200, Body: map[string]any{"errcode": 0.0}}
		}
	}}
	adapter := NewOpenAPIAdapter(caller, "wechat-mp", map[Capability]packs.Binding{
		CapEngagements: {Operations: map[string]string{"list": "articles", "comments": "comments"},
			Items: "item", SubItems: "comment",
			Fields: map[string]string{"parent_id": "article_id", "id": "user_comment_id", "author": "openid", "text": "content"}},
		CapReply: {Operation: "reply", Params: map[string]string{"parent": "msg_data_id", "id": "user_comment_id", "text": "content"}},
	}, nil)

	engagements, err := adapter.ListEngagements(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, engagements, 1)
	require.Equal(t, "art-1:5", engagements[0].ExtID)

	_, err = adapter.Reply(context.Background(), "art-1:5", "在这里")
	require.NoError(t, err)
	reply := caller.calls[len(caller.calls)-1]
	require.Equal(t, "art-1", reply.Params["msg_data_id"])
	require.Equal(t, "5", reply.Params["user_comment_id"])
	require.Equal(t, "在这里", reply.Params["content"])
}
