package channel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newWeChatTestServer(t *testing.T, handler http.HandlerFunc) *WeChatAPIClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewWeChatAPIClient("app-id", "app-secret")
	c.BaseURL = srv.URL
	return c
}

func TestWeChatAPIClientDraftAndPublish(t *testing.T) {
	var tokenCalls atomic.Int32
	var draftBody map[string]any
	var publishBody map[string]any

	client := newWeChatTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/token":
			tokenCalls.Add(1)
			require.Equal(t, "client_credential", r.URL.Query().Get("grant_type"))
			require.Equal(t, "app-id", r.URL.Query().Get("appid"))
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-1", "expires_in": 7200})
		case "/cgi-bin/draft/add":
			require.Equal(t, "tok-1", r.URL.Query().Get("access_token"))
			require.NoError(t, json.NewDecoder(r.Body).Decode(&draftBody))
			_ = json.NewEncoder(w).Encode(map[string]any{"media_id": "media-1"})
		case "/cgi-bin/freepublish/submit":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&publishBody))
			_ = json.NewEncoder(w).Encode(map[string]any{"publish_id": "pub-1"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})

	ctx := context.Background()
	mediaID, err := client.DraftAdd(ctx, DraftRequest{
		Kind: "article", Title: "标题", Author: "运营", Content: "<p>正文</p>", Digest: "摘要",
	})
	require.NoError(t, err)
	require.Equal(t, "media-1", mediaID)

	articles := draftBody["articles"].([]any)
	article := articles[0].(map[string]any)
	require.Equal(t, "标题", article["title"])
	require.Equal(t, "<p>正文</p>", article["content"])
	require.Equal(t, "news", article["article_type"])

	publishID, err := client.Publish(ctx, mediaID)
	require.NoError(t, err)
	require.Equal(t, "pub-1", publishID)
	require.Equal(t, "media-1", publishBody["media_id"])

	// The access token is cached: a second call must not re-fetch it.
	_, err = client.DraftAdd(ctx, DraftRequest{Kind: "article", Title: "t", Content: "c"})
	require.NoError(t, err)
	require.Equal(t, int32(1), tokenCalls.Load())
}

func TestWeChatAPIClientMapsPlatformErrors(t *testing.T) {
	client := newWeChatTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 7200})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 40001, "errmsg": "invalid credential"})
	})

	_, err := client.DraftAdd(context.Background(), DraftRequest{Kind: "article", Title: "t", Content: "c"})
	var apiErr *WeChatAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 40001, apiErr.Code)

	adapter := NewWeChatAdapter(client)
	_, err = adapter.Publish(context.Background(), WeChatPublishPayload{Title: "t", Content: "c"})
	var adapterErr *AdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, CodeCredentialInvalid, adapterErr.Code)
	require.False(t, adapterErr.Retryable)
}

func TestWeChatAPIClientRequiresCredentials(t *testing.T) {
	client := &WeChatAPIClient{}
	err := client.Ping(context.Background())
	var apiErr *WeChatAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 40013, apiErr.Code)
}

func TestWeChatAPIClientUploadMaterial(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "cover.png")
	require.NoError(t, os.WriteFile(image, []byte("png-bytes"), 0o600))

	var gotType, gotToken string
	client := newWeChatTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 7200})
		case "/cgi-bin/material/add_material":
			gotType, gotToken = r.URL.Query().Get("type"), r.URL.Query().Get("access_token")
			require.Contains(t, r.Header.Get("Content-Type"), "multipart/form-data")
			_ = json.NewEncoder(w).Encode(map[string]any{"media_id": "img-1"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})

	id, err := client.UploadMaterial(context.Background(), image)
	require.NoError(t, err)
	require.Equal(t, "img-1", id)
	require.Equal(t, "image", gotType)
	require.Equal(t, "tok", gotToken)
}

func TestWeChatAPIClientMetrics(t *testing.T) {
	client := newWeChatTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 7200})
		case "/datacube/getarticletotal":
			_ = json.NewEncoder(w).Encode(map[string]any{"list": []any{
				map[string]any{"details": []any{
					map[string]any{"read_num": 10, "share_num": 1, "like_num": 2},
					map[string]any{"read_num": 30, "share_num": 4, "like_num": 5},
				}},
			}})
		case "/datacube/getusersummary":
			_ = json.NewEncoder(w).Encode(map[string]any{"list": []any{
				map[string]any{"new_user": 12, "cancel_user": 2},
			}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})

	apart, err := client.ArticleStats(context.Background(), "2026-11-01", "2026-11-07")
	require.NoError(t, err)
	require.Equal(t, 30.0, apart[MetricViews])
	require.Equal(t, 4.0, apart[MetricShares])
	require.Equal(t, 5.0, apart[MetricLikes])

	fans, err := client.UserSummary(context.Background(), "2026-11-01", "2026-11-07")
	require.NoError(t, err)
	require.Equal(t, 10.0, fans[MetricFollowerDelta])
}

func TestWeChatAPIClientScheduledPublishIsUnsupported(t *testing.T) {
	client := &WeChatAPIClient{}
	_, err := client.PublishScheduled(context.Background(), "media-1", time.Now().Add(time.Hour))
	var adapterErr *AdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, CodeUnsupported, adapterErr.Code)
	require.True(t, errors.Is(err, ErrCapabilityUnavailable))
}
