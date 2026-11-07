package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultWeChatAPIBase is the WeChat Official Account API origin.
const DefaultWeChatAPIBase = "https://api.weixin.qq.com"

// WeChatAPIClient speaks the official WeChat Official Account API for one
// account. The AppID/AppSecret come from the encrypted credential store; the
// access token is cached in memory and never logged.
type WeChatAPIClient struct {
	AppID     string
	AppSecret string
	BaseURL   string
	HTTP      *http.Client

	Now func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

var _ WeChatClient = (*WeChatAPIClient)(nil)
var _ WeChatPinger = (*WeChatAPIClient)(nil)

// NewWeChatAPIClient builds a client for one account's AppID/AppSecret.
func NewWeChatAPIClient(appID, secret string) *WeChatAPIClient {
	return &WeChatAPIClient{
		AppID:     strings.TrimSpace(appID),
		AppSecret: strings.TrimSpace(secret),
		BaseURL:   DefaultWeChatAPIBase,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Ping forces a token refresh so an invalid AppSecret surfaces before a
// publish is attempted.
func (c *WeChatAPIClient) Ping(ctx context.Context) error {
	_, err := c.accessToken(ctx, true)
	return err
}

func (c *WeChatAPIClient) DraftAdd(ctx context.Context, req DraftRequest) (string, error) {
	if strings.TrimSpace(req.Title) == "" {
		return "", &AdapterError{Capability: CapPublish, Code: CodeInvalidPayload, Err: errors.New("wechat: draft title is required")}
	}
	article := map[string]any{
		"title":                 req.Title,
		"author":                req.Author,
		"digest":                req.Digest,
		"content_source_url":    req.ContentSourceURL,
		"need_open_comment":     0,
		"only_fans_can_comment": 0,
	}
	switch strings.ToLower(strings.TrimSpace(req.Kind)) {
	case "", "article":
		article["article_type"] = "news"
		article["content"] = req.Content
		article["thumb_media_id"] = req.ThumbMediaID
	case "image":
		if len(req.ImageMediaIDs) == 0 {
			return "", &AdapterError{Capability: CapPublish, Code: CodeInvalidPayload, Err: errors.New("wechat: image draft requires uploaded images")}
		}
		article["article_type"] = "newspic"
		article["content"] = req.Content
		images := make([]map[string]string, 0, len(req.ImageMediaIDs))
		for _, id := range req.ImageMediaIDs {
			images = append(images, map[string]string{"image_media_id": id})
		}
		article["image_info"] = map[string]any{"image_list": images}
	default:
		return "", &AdapterError{
			Capability: CapPublish,
			Code:       CodeUnsupported,
			Err:        fmt.Errorf("wechat: draft kind %q is not supported by the API", req.Kind),
		}
	}

	var out struct {
		MediaID string `json:"media_id"`
	}
	if err := c.post(ctx, "/cgi-bin/draft/add", map[string]any{"articles": []any{article}}, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.MediaID) == "" {
		return "", apiErr(-1, "wechat: draft/add returned no media_id")
	}
	return out.MediaID, nil
}

func (c *WeChatAPIClient) UploadMaterial(ctx context.Context, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", &AdapterError{Capability: CapPublish, Code: CodeInvalidPayload, Err: errors.New("wechat: upload path is empty")}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", &AdapterError{Capability: CapPublish, Code: CodeInvalidPayload,
			Err: fmt.Errorf("wechat: read material %s: %w", filepath.Base(path), err)}
	}
	materialType := "image"
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mp4", ".mov", ".m4v":
		materialType = "video"
	case ".mp3", ".m4a", ".wav":
		materialType = "voice"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("media", filepath.Base(path))
	if err != nil {
		return "", fmt.Errorf("wechat: build upload form: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("wechat: write upload form: %w", err)
	}
	if materialType == "video" {
		desc := fmt.Sprintf(`{"title":%q,"introduction":""}`, strings.TrimSuffix(filepath.Base(path), ext))
		if err := writer.WriteField("description", desc); err != nil {
			return "", fmt.Errorf("wechat: write video description: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("wechat: close upload form: %w", err)
	}

	token, err := c.accessToken(ctx, false)
	if err != nil {
		return "", err
	}
	endpoint := c.apiURL("/cgi-bin/material/add_material", map[string]string{
		"access_token": token,
		"type":         materialType,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	raw, err := c.do(req)
	if err != nil {
		return "", err
	}
	var out struct {
		MediaID string `json:"media_id"`
	}
	if err := decodeWeChat(raw, &out); err != nil {
		return "", err
	}
	if out.MediaID == "" {
		return "", apiErr(-1, "wechat: material upload returned no media_id")
	}
	return out.MediaID, nil
}

func (c *WeChatAPIClient) Publish(ctx context.Context, mediaID string) (string, error) {
	if strings.TrimSpace(mediaID) == "" {
		return "", &AdapterError{Capability: CapPublish, Code: CodeInvalidPayload, Err: errors.New("wechat: media_id is required")}
	}
	var out struct {
		PublishID string `json:"publish_id"`
	}
	if err := c.post(ctx, "/cgi-bin/freepublish/submit", map[string]any{"media_id": mediaID}, &out); err != nil {
		return "", err
	}
	if out.PublishID == "" {
		return "", apiErr(-1, "wechat: freepublish/submit returned no publish_id")
	}
	return out.PublishID, nil
}

// PublishScheduled is unsupported by the official API: scheduling happens at
// the ops Publisher (which fires the publish at the due instant). Returning a
// concrete unsupported error lets the caller degrade instead of silently
// publishing early.
func (c *WeChatAPIClient) PublishScheduled(_ context.Context, _ string, at time.Time) (string, error) {
	return "", &AdapterError{
		Capability: CapPublish,
		Code:       CodeUnsupported,
		Err:        fmt.Errorf("wechat: the official API cannot schedule publish (requested %s); schedule at the publisher instead", at.Format(time.RFC3339)),
	}
}

func (c *WeChatAPIClient) ArticleStats(ctx context.Context, from, to string) (map[string]float64, error) {
	var out struct {
		List []struct {
			Details []struct {
				ReadNum  float64 `json:"read_num"`
				ShareNum float64 `json:"share_num"`
				LikeNum  float64 `json:"like_num"`
			} `json:"details"`
		} `json:"list"`
	}
	if err := c.postDatacube(ctx, "/datacube/getarticletotal", from, to, &out); err != nil {
		return nil, err
	}
	stats := map[string]float64{}
	for _, article := range out.List {
		if len(article.Details) == 0 {
			continue
		}
		// The last detail row carries the article's cumulative totals.
		last := article.Details[len(article.Details)-1]
		stats[MetricViews] += last.ReadNum
		stats[MetricShares] += last.ShareNum
		stats[MetricLikes] += last.LikeNum
	}
	return stats, nil
}

func (c *WeChatAPIClient) UserSummary(ctx context.Context, from, to string) (map[string]float64, error) {
	var out struct {
		List []struct {
			NewUser    float64 `json:"new_user"`
			CancelUser float64 `json:"cancel_user"`
		} `json:"list"`
	}
	if err := c.postDatacube(ctx, "/datacube/getusersummary", from, to, &out); err != nil {
		return nil, err
	}
	var delta float64
	for _, row := range out.List {
		delta += row.NewUser - row.CancelUser
	}
	return map[string]float64{MetricFollowerDelta: delta}, nil
}

func (c *WeChatAPIClient) postDatacube(ctx context.Context, path, from, to string, out any) error {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" {
		return &AdapterError{Capability: CapMetrics, Code: CodeInvalidPayload, Err: errors.New("wechat: datacube needs a date range")}
	}
	return c.post(ctx, path, map[string]any{"begin_date": from, "end_date": to}, out)
}

func (c *WeChatAPIClient) post(ctx context.Context, path string, body, out any) error {
	token, err := c.accessToken(ctx, false)
	if err != nil {
		return err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("wechat: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiURL(path, map[string]string{"access_token": token}), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	raw, err := c.do(req)
	if err != nil {
		return err
	}
	return decodeWeChat(raw, out)
}

func (c *WeChatAPIClient) do(req *http.Request) ([]byte, error) {
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &AdapterError{Capability: capabilityFromPath(req.URL.Path), Code: CodeTransport, Retryable: true, Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, &AdapterError{Capability: capabilityFromPath(req.URL.Path), Code: CodeTransport, Retryable: true, Err: err}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &AdapterError{Capability: capabilityFromPath(req.URL.Path), Code: CodeRateLimited, Retryable: true,
			Err: fmt.Errorf("wechat: http %d", resp.StatusCode)}
	}
	if resp.StatusCode >= 500 {
		return nil, &AdapterError{Capability: capabilityFromPath(req.URL.Path), Code: CodeSystemBusy, Retryable: true,
			Err: fmt.Errorf("wechat: http %d", resp.StatusCode)}
	}
	if resp.StatusCode >= 400 {
		return nil, &AdapterError{Capability: capabilityFromPath(req.URL.Path), Code: CodeTransport,
			Err: fmt.Errorf("wechat: http %d", resp.StatusCode)}
	}
	return raw, nil
}

func (c *WeChatAPIClient) accessToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	if !force && c.token != "" && now.Before(c.expiresAt) {
		return c.token, nil
	}
	if c.AppID == "" || c.AppSecret == "" {
		return "", apiErr(40013, "wechat: app_id/app_secret are not configured")
	}
	endpoint := c.apiURL("/cgi-bin/token", map[string]string{
		"grant_type": "client_credential",
		"appid":      c.AppID,
		"secret":     c.AppSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	raw, err := c.do(req)
	if err != nil {
		return "", err
	}
	var out struct {
		AccessToken string  `json:"access_token"`
		ExpiresIn   float64 `json:"expires_in"`
	}
	if err := decodeWeChat(raw, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", apiErr(-1, "wechat: token response carried no access_token")
	}
	// Refresh one minute early so a token never expires mid-request.
	ttl := time.Duration(out.ExpiresIn)*time.Second - time.Minute
	if ttl <= 0 {
		ttl = time.Minute
	}
	c.token = out.AccessToken
	c.expiresAt = now.Add(ttl)
	return c.token, nil
}

func (c *WeChatAPIClient) apiURL(path string, query map[string]string) string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = DefaultWeChatAPIBase
	}
	values := url.Values{}
	for k, v := range query {
		if v != "" {
			values.Set(k, v)
		}
	}
	if encoded := values.Encode(); encoded != "" {
		return base + path + "?" + encoded
	}
	return base + path
}

type wechatAPIError struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func decodeWeChat(raw []byte, out any) error {
	var api wechatAPIError
	if err := json.Unmarshal(raw, &api); err == nil && api.ErrCode != 0 {
		return &WeChatAPIError{Code: api.ErrCode, Message: api.ErrMsg}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("wechat: decode response: %w", err)
	}
	return nil
}

func apiErr(code int, message string) *WeChatAPIError {
	return &WeChatAPIError{Code: code, Message: message}
}

func capabilityFromPath(path string) Capability {
	switch {
	case strings.Contains(path, "datacube"), strings.Contains(path, "metrics"):
		return CapMetrics
	case strings.Contains(path, "comment"), strings.Contains(path, "message"):
		return CapReply
	default:
		return CapPublish
	}
}
