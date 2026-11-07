package channel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeWeChatClient struct {
	uploadIDs map[string]string
	uploads   []string
	uploadErr error

	draftID  string
	drafts   []DraftRequest
	draftErr error

	publishID  string
	publishes  []string
	publishErr error

	scheduledID  string
	scheduled    []scheduledCall
	scheduledErr error

	articleStats map[string]float64
	articleErr   error

	userSummary map[string]float64
	userErr     error

	pingErr   error
	pingCalls int
}

type scheduledCall struct {
	mediaID string
	at      time.Time
}

func (f *fakeWeChatClient) UploadMaterial(_ context.Context, path string) (string, error) {
	if f.uploadErr != nil {
		return "", f.uploadErr
	}
	f.uploads = append(f.uploads, path)
	return f.uploadIDs[path], nil
}

func (f *fakeWeChatClient) DraftAdd(_ context.Context, req DraftRequest) (string, error) {
	f.drafts = append(f.drafts, req)
	if f.draftErr != nil {
		return "", f.draftErr
	}
	return f.draftID, nil
}

func (f *fakeWeChatClient) Publish(_ context.Context, mediaID string) (string, error) {
	f.publishes = append(f.publishes, mediaID)
	if f.publishErr != nil {
		return "", f.publishErr
	}
	return f.publishID, nil
}

func (f *fakeWeChatClient) PublishScheduled(_ context.Context, mediaID string, at time.Time) (string, error) {
	f.scheduled = append(f.scheduled, scheduledCall{mediaID: mediaID, at: at})
	if f.scheduledErr != nil {
		return "", f.scheduledErr
	}
	return f.scheduledID, nil
}

func (f *fakeWeChatClient) ArticleStats(context.Context, string, string) (map[string]float64, error) {
	return f.articleStats, f.articleErr
}

func (f *fakeWeChatClient) UserSummary(context.Context, string, string) (map[string]float64, error) {
	return f.userSummary, f.userErr
}

func (f *fakeWeChatClient) Ping(context.Context) error {
	f.pingCalls++
	return f.pingErr
}

func newFakeWeChatClient() *fakeWeChatClient {
	return &fakeWeChatClient{
		uploadIDs:   map[string]string{},
		draftID:     "draft-media-id",
		publishID:   "publish-id",
		scheduledID: "scheduled-id",
	}
}

func asAdapterError(t *testing.T, err error) *AdapterError {
	t.Helper()
	var adapterErr *AdapterError
	if !errors.As(err, &adapterErr) {
		t.Fatalf("want *AdapterError, got %T (%v)", err, err)
	}
	return adapterErr
}

func TestWeChatCapabilities(t *testing.T) {
	a := NewWeChatAdapter(newFakeWeChatClient())
	caps := a.Capabilities()
	if !caps.Supports(CapPublish) || !caps.Supports(CapMetrics) {
		t.Fatalf("wechat adapter must publish and collect metrics: %v", caps)
	}
	if caps.Supports(CapEngagements) || caps.Supports(CapReply) {
		t.Fatalf("wechat transport has no engagement API: %v", caps)
	}
}

func TestWeChatPublishArticleCreatesDraftThenPublishes(t *testing.T) {
	client := newFakeWeChatClient()
	client.uploadIDs["/tmp/cover.png"] = "thumb-media-id"
	a := NewWeChatAdapter(client)

	receipt, err := a.Publish(context.Background(), WeChatPublishPayload{
		Kind:             "article",
		Title:            "秋季护理指南",
		Author:           "运营团队",
		Digest:           "一篇摘要",
		Content:          "<p>正文</p>",
		ContentSourceURL: "https://example.com/source",
		ThumbMediaPath:   "/tmp/cover.png",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	if !reflect.DeepEqual(client.uploads, []string{"/tmp/cover.png"}) {
		t.Fatalf("uploads = %v, want cover only", client.uploads)
	}
	if len(client.drafts) != 1 {
		t.Fatalf("drafts = %d, want 1", len(client.drafts))
	}
	got := client.drafts[0]
	want := DraftRequest{
		Kind:             "article",
		Title:            "秋季护理指南",
		Author:           "运营团队",
		Digest:           "一篇摘要",
		Content:          "<p>正文</p>",
		ContentSourceURL: "https://example.com/source",
		ThumbMediaID:     "thumb-media-id",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("draft = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(client.publishes, []string{"draft-media-id"}) {
		t.Fatalf("publishes = %v, want draft media id", client.publishes)
	}
	if receipt.ExternalID != "publish-id" {
		t.Fatalf("receipt ExternalID = %q, want publish-id", receipt.ExternalID)
	}
}

func TestWeChatPublishAcceptsPointerPayload(t *testing.T) {
	client := newFakeWeChatClient()
	a := NewWeChatAdapter(client)

	_, err := a.Publish(context.Background(), &WeChatPublishPayload{Title: "t", Content: "c"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(client.drafts) != 1 || client.drafts[0].Kind != "article" {
		t.Fatalf("empty kind must default to article: %+v", client.drafts)
	}
}

func TestWeChatPublishScheduledUsesScheduler(t *testing.T) {
	client := newFakeWeChatClient()
	a := NewWeChatAdapter(client)
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)

	receipt, err := a.Publish(context.Background(), WeChatPublishPayload{
		Title:     "定时文章",
		Content:   "<p>正文</p>",
		PublishAt: &at,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(client.publishes) != 0 {
		t.Fatalf("scheduled publish must not publish immediately: %v", client.publishes)
	}
	if len(client.scheduled) != 1 {
		t.Fatalf("scheduled calls = %d, want 1", len(client.scheduled))
	}
	if client.scheduled[0].mediaID != "draft-media-id" || !client.scheduled[0].at.Equal(at) {
		t.Fatalf("scheduled call = %+v, want media id + %s", client.scheduled[0], at)
	}
	if receipt.ExternalID != "scheduled-id" {
		t.Fatalf("receipt ExternalID = %q, want scheduled-id", receipt.ExternalID)
	}
}

func TestWeChatPublishDraftOnlySkipsPublish(t *testing.T) {
	client := newFakeWeChatClient()
	a := NewWeChatAdapter(client)

	receipt, err := a.Publish(context.Background(), WeChatPublishPayload{
		Title:     "仅草稿",
		Content:   "<p>正文</p>",
		DraftOnly: true,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(client.publishes) != 0 || len(client.scheduled) != 0 {
		t.Fatal("draft-only must not publish or schedule")
	}
	if receipt.ExternalID != "draft-media-id" {
		t.Fatalf("receipt ExternalID = %q, want draft media id", receipt.ExternalID)
	}
}

func TestWeChatPublishImageUploadsMaterialsFirst(t *testing.T) {
	client := newFakeWeChatClient()
	client.uploadIDs["/tmp/a.png"] = "img-a"
	client.uploadIDs["/tmp/b.png"] = "img-b"
	a := NewWeChatAdapter(client)

	if _, err := a.Publish(context.Background(), WeChatPublishPayload{
		Kind:       "image",
		ImagePaths: []string{"/tmp/a.png", "/tmp/b.png"},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !reflect.DeepEqual(client.uploads, []string{"/tmp/a.png", "/tmp/b.png"}) {
		t.Fatalf("uploads = %v, want both images in order", client.uploads)
	}
	if !reflect.DeepEqual(client.drafts[0].ImageMediaIDs, []string{"img-a", "img-b"}) {
		t.Fatalf("image media ids = %v", client.drafts[0].ImageMediaIDs)
	}
}

func TestWeChatPublishVideoUploadsMaterial(t *testing.T) {
	client := newFakeWeChatClient()
	client.uploadIDs["/tmp/v.mp4"] = "video-media-id"
	a := NewWeChatAdapter(client)

	if _, err := a.Publish(context.Background(), WeChatPublishPayload{
		Kind:      "video",
		VideoPath: "/tmp/v.mp4",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !reflect.DeepEqual(client.uploads, []string{"/tmp/v.mp4"}) {
		t.Fatalf("uploads = %v, want video", client.uploads)
	}
	if client.drafts[0].VideoMediaID != "video-media-id" {
		t.Fatalf("video media id = %q", client.drafts[0].VideoMediaID)
	}
}

func TestWeChatPublishRejectsBadPayloads(t *testing.T) {
	a := NewWeChatAdapter(newFakeWeChatClient())

	if _, err := a.Publish(context.Background(), "not-a-payload"); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("unknown payload code = %v", err)
	}
	if _, err := a.Publish(context.Background(), WeChatPublishPayload{Kind: "audio"}); asAdapterError(t, err).Code != CodeUnsupported {
		t.Fatalf("unknown kind code = %v", err)
	}
	if _, err := a.Publish(context.Background(), WeChatPublishPayload{Title: "no body"}); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("article without content code = %v", err)
	}
	if _, err := a.Publish(context.Background(), WeChatPublishPayload{Kind: "image"}); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("image without paths code = %v", err)
	}
	if _, err := a.Publish(context.Background(), WeChatPublishPayload{Kind: "video"}); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("video without path code = %v", err)
	}
}

func TestWeChatPublishRejectsPastScheduleBeforeAnySideEffect(t *testing.T) {
	client := newFakeWeChatClient()
	a := NewWeChatAdapter(client)
	past := time.Now().Add(-time.Minute)

	_, err := a.Publish(context.Background(), WeChatPublishPayload{
		Title:     "过期",
		Content:   "<p>正文</p>",
		PublishAt: &past,
	})
	if asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("past schedule code = %v", err)
	}
	if len(client.drafts) != 0 || len(client.uploads) != 0 {
		t.Fatal("validation failure must not create a draft or upload material")
	}
}

func TestWeChatFetchMetricsMergesAndFillsZero(t *testing.T) {
	client := newFakeWeChatClient()
	client.articleStats = map[string]float64{
		"read_count":          100,
		"int_page_read_count": 120, // lower priority, must not win
		"share_count":         7,
		"like_count":          5,
	}
	client.userSummary = map[string]float64{
		"net_fans":    3,
		"new_fans":    9,
		"cancel_fans": 2,
	}
	a := NewWeChatAdapter(client)

	got, err := a.FetchMetrics(context.Background(), "2026-11-01", "2026-11-06")
	if err != nil {
		t.Fatalf("fetch metrics: %v", err)
	}
	want := map[string]float64{
		"views":          100,
		"shares":         7,
		"likes":          5,
		"follower_delta": 3,
		"new_fans":       9,
		"cancel_fans":    2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metrics = %v, want %v", got, want)
	}
}

func TestWeChatFetchMetricsFillsAbsentKeysWithZero(t *testing.T) {
	client := newFakeWeChatClient()
	client.articleStats = map[string]float64{"read_count": 12}
	client.userSummary = map[string]float64{}
	a := NewWeChatAdapter(client)

	got, err := a.FetchMetrics(context.Background(), "2026-11-01", "2026-11-06")
	if err != nil {
		t.Fatalf("fetch metrics: %v", err)
	}
	for _, key := range unifiedMetrics {
		if _, ok := got[key]; !ok {
			t.Fatalf("metric %q missing from %v", key, got)
		}
	}
	if got[MetricViews] != 12 || got[MetricShares] != 0 || got[MetricLikes] != 0 || got[MetricFollowerDelta] != 0 {
		t.Fatalf("metrics = %v", got)
	}
}

func TestWeChatFetchMetricsRejectsInvalidRange(t *testing.T) {
	a := NewWeChatAdapter(newFakeWeChatClient())
	if _, err := a.FetchMetrics(context.Background(), "", "2026-11-06"); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("empty range code = %v", err)
	}
	if _, err := a.FetchMetrics(context.Background(), "2026-11-07", "2026-11-01"); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("reversed range code = %v", err)
	}
}

func TestWeChatMapsCredentialFailure(t *testing.T) {
	client := newFakeWeChatClient()
	client.articleErr = &WeChatAPIError{Code: 40001, Message: "invalid credential"}
	a := NewWeChatAdapter(client)

	_, err := a.FetchMetrics(context.Background(), "2026-11-01", "2026-11-06")
	adapterErr := asAdapterError(t, err)
	if adapterErr.Code != CodeCredentialInvalid || adapterErr.Retryable {
		t.Fatalf("40001 mapped to %+v, want non-retryable credential_invalid", adapterErr)
	}
	if !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("credential failure must gate degradation: %v", err)
	}
}

func TestWeChatMapsRateLimit(t *testing.T) {
	client := newFakeWeChatClient()
	client.publishErr = &WeChatAPIError{Code: 45009, Message: "api freq out of limit"}
	a := NewWeChatAdapter(client)

	_, err := a.Publish(context.Background(), WeChatPublishPayload{Title: "t", Content: "c"})
	adapterErr := asAdapterError(t, err)
	if adapterErr.Code != CodeRateLimited || !adapterErr.Retryable {
		t.Fatalf("45009 mapped to %+v, want retryable rate_limited", adapterErr)
	}
}

func TestWeChatMapsUploadAndDraftFailures(t *testing.T) {
	client := newFakeWeChatClient()
	client.uploadErr = &WeChatAPIError{Code: 40001}
	a := NewWeChatAdapter(client)
	if _, err := a.Publish(context.Background(), WeChatPublishPayload{
		Title: "t", Content: "c", ThumbMediaPath: "/tmp/cover.png",
	}); asAdapterError(t, err).Code != CodeCredentialInvalid {
		t.Fatalf("upload error code = %v", err)
	}

	client = newFakeWeChatClient()
	client.draftErr = &WeChatAPIError{Code: 45009}
	a = NewWeChatAdapter(client)
	if _, err := a.Publish(context.Background(), WeChatPublishPayload{Title: "t", Content: "c"}); asAdapterError(t, err).Code != CodeRateLimited {
		t.Fatalf("draft error code = %v", err)
	}
}

func TestWeChatEnsureSessionValidatesCredential(t *testing.T) {
	a := NewWeChatAdapter(newFakeWeChatClient())

	err := a.EnsureSession(context.Background(), "  ")
	adapterErr := asAdapterError(t, err)
	if adapterErr.Code != CodeCredentialInvalid || adapterErr.Retryable {
		t.Fatalf("empty ref mapped to %+v", adapterErr)
	}

	client := newFakeWeChatClient()
	client.pingErr = &WeChatAPIError{Code: 40001}
	a = NewWeChatAdapter(client)
	if err := a.EnsureSession(context.Background(), "app-id"); asAdapterError(t, err).Code != CodeCredentialInvalid {
		t.Fatalf("ping failure code = %v", err)
	}

	client.pingErr = nil
	if err := a.EnsureSession(context.Background(), "app-id"); err != nil {
		t.Fatalf("healthy session: %v", err)
	}
	if client.pingCalls != 2 {
		t.Fatalf("ping calls = %d, want 2 (one failure, one success)", client.pingCalls)
	}
}

func TestWeChatHealthCheckProbesClient(t *testing.T) {
	client := newFakeWeChatClient()
	a := NewWeChatAdapter(client)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check: %v", err)
	}
	if client.pingCalls != 1 {
		t.Fatalf("ping calls = %d, want 1", client.pingCalls)
	}

	client.pingErr = &WeChatAPIError{Code: 45009}
	err := a.HealthCheck(context.Background())
	if asAdapterError(t, err).Code != CodeRateLimited {
		t.Fatalf("health check error = %v", err)
	}
}

func TestWeChatUnsupportedEngagements(t *testing.T) {
	a := NewWeChatAdapter(newFakeWeChatClient())

	if _, err := a.ListEngagements(context.Background(), ""); asAdapterError(t, err).Code != CodeUnsupported {
		t.Fatalf("list engagements code = %v", err)
	}
	if _, err := a.Reply(context.Background(), "1", "hi"); asAdapterError(t, err).Code != CodeUnsupported {
		t.Fatalf("reply code = %v", err)
	}
}

func TestWeChatAPIErrorMessage(t *testing.T) {
	if got := (&WeChatAPIError{Code: 40001}).Error(); !strings.Contains(got, "40001") {
		t.Fatalf("message = %q", got)
	}
	if got := (&WeChatAPIError{Code: 40001, Message: "invalid credential"}).Error(); !strings.Contains(got, "invalid credential") {
		t.Fatalf("message = %q", got)
	}
}
