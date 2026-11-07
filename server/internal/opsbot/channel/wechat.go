package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Adapter error codes shared with the publish orchestrator and metrics sync.
const (
	CodeCredentialInvalid = "credential_invalid"
	CodeRateLimited       = "rate_limited"
	CodeUnsupported       = "unsupported"
	CodeInvalidPayload    = "invalid_payload"
	CodeSystemBusy        = "system_busy"
	CodeTransport         = "transport"
)

// WeChat metric keys are the platform's native names; the adapter folds them
// into the unified namespace so every channel reports the same keys.
const (
	MetricViews         = "views"
	MetricShares        = "shares"
	MetricLikes         = "likes"
	MetricFollowerDelta = "follower_delta"
)

// wechatMetricOrder lists the native keys by descending preference, so a
// response carrying both read_count and int_page_read_count resolves
// deterministically instead of depending on map iteration order.
var wechatMetricOrder = []string{
	"read_count",
	"int_page_read_count",
	"share_count",
	"share_user_count",
	"like_count",
	"net_fans",
	"net_fans_count",
}

var wechatMetricAliases = map[string]string{
	"read_count":          MetricViews,
	"int_page_read_count": MetricViews,
	"share_count":         MetricShares,
	"share_user_count":    MetricShares,
	"like_count":          MetricLikes,
	"net_fans":            MetricFollowerDelta,
	"net_fans_count":      MetricFollowerDelta,
}

var unifiedMetrics = []string{MetricViews, MetricShares, MetricLikes, MetricFollowerDelta}

// WeChatAPIError is the error shape returned by the WeChat MP MCP transport.
// It carries the platform errcode so adapters can distinguish an expired
// credential from a rate limit.
type WeChatAPIError struct {
	Code    int
	Message string
}

func (e *WeChatAPIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("wechat api error %d", e.Code)
	}
	return fmt.Sprintf("wechat api error %d: %s", e.Code, e.Message)
}

// DraftRequest is one article draft submitted to the WeChat MP draft API.
type DraftRequest struct {
	Kind             string
	Title            string
	Author           string
	Digest           string
	Content          string
	ContentSourceURL string
	ThumbMediaID     string
	ImageMediaIDs    []string
	VideoMediaID     string
}

// WeChatPublishPayload is the payload accepted by WeChatAdapter.Publish.
type WeChatPublishPayload struct {
	// Kind is article (default), image or video.
	Kind             string
	Title            string
	Author           string
	Digest           string
	Content          string
	ContentSourceURL string
	// ThumbMediaPath is the cover image uploaded before the draft is created.
	ThumbMediaPath string
	// ImagePaths are uploaded as permanent image material, in order.
	ImagePaths []string
	// VideoPath is uploaded as permanent video material.
	VideoPath string
	// DraftOnly creates the draft and returns its media_id without publishing.
	DraftOnly bool
	// PublishAt schedules the publish; nil publishes immediately.
	PublishAt *time.Time
}

// WeChatClient is the MCP transport surface the adapter drives. It is kept
// deliberately small so a fake can stand in for WeChat during tests.
type WeChatClient interface {
	DraftAdd(ctx context.Context, req DraftRequest) (string, error)
	UploadMaterial(ctx context.Context, path string) (string, error)
	Publish(ctx context.Context, mediaID string) (string, error)
	PublishScheduled(ctx context.Context, mediaID string, at time.Time) (string, error)
	ArticleStats(ctx context.Context, from, to string) (map[string]float64, error)
	UserSummary(ctx context.Context, from, to string) (map[string]float64, error)
}

// WeChatPinger is an optional lightweight probe. When the client implements it
// the adapter uses it for EnsureSession/HealthCheck instead of a data query.
type WeChatPinger interface {
	Ping(ctx context.Context) error
}

// WeChatAdapter publishes articles to and reads metrics from a WeChat Official
// Account. The MCP transport exposes no engagement or reply API, so those
// capabilities report unsupported.
type WeChatAdapter struct {
	client WeChatClient
}

var _ ChannelAdapter = (*WeChatAdapter)(nil)

func NewWeChatAdapter(client WeChatClient) *WeChatAdapter {
	return &WeChatAdapter{client: client}
}

func (a *WeChatAdapter) Capabilities() CapabilitySet {
	return CapabilitySet{CapPublish: true, CapMetrics: true}
}

// EnsureSession validates the account reference and, when the client supports
// a probe, that the AppID/AppSecret or MCP token is still accepted.
func (a *WeChatAdapter) EnsureSession(ctx context.Context, accountRef string) error {
	if strings.TrimSpace(accountRef) == "" {
		return &AdapterError{
			Capability: CapPublish,
			Code:       CodeCredentialInvalid,
			Err:        errors.New("wechat: empty account reference"),
		}
	}
	pinger, ok := a.client.(WeChatPinger)
	if !ok {
		return nil
	}
	return wechatError(CapPublish, pinger.Ping(ctx))
}

// HealthCheck probes the account with the lightweight ping when available and
// otherwise falls back to a one-day user summary query.
func (a *WeChatAdapter) HealthCheck(ctx context.Context) error {
	if pinger, ok := a.client.(WeChatPinger); ok {
		return wechatError(CapPublish, pinger.Ping(ctx))
	}
	today := time.Now().Format("2006-01-02")
	_, err := a.client.UserSummary(ctx, today, today)
	return wechatError(CapPublish, err)
}

// Publish creates the draft, uploads any material it needs, then publishes
// immediately or schedules the publish. With DraftOnly the draft media_id is
// returned instead of a publish receipt.
func (a *WeChatAdapter) Publish(ctx context.Context, payload any) (Receipt, error) {
	p, ok := wechatPayload(payload)
	if !ok {
		return Receipt{}, &AdapterError{
			Capability: CapPublish,
			Code:       CodeInvalidPayload,
			Err:        fmt.Errorf("wechat: unsupported payload type %T", payload),
		}
	}

	if p.PublishAt != nil && !p.DraftOnly && !p.PublishAt.After(time.Now()) {
		return Receipt{}, invalidPayload(CapPublish, fmt.Sprintf("wechat: publish_at %s is not in the future", p.PublishAt.Format(time.RFC3339)))
	}

	req, err := a.buildDraft(ctx, p)
	if err != nil {
		return Receipt{}, err
	}

	mediaID, err := a.client.DraftAdd(ctx, req)
	if err != nil {
		return Receipt{}, wechatError(CapPublish, err)
	}
	if p.DraftOnly {
		return Receipt{ExternalID: mediaID}, nil
	}

	var ref string
	if p.PublishAt != nil {
		ref, err = a.client.PublishScheduled(ctx, mediaID, *p.PublishAt)
	} else {
		ref, err = a.client.Publish(ctx, mediaID)
	}
	if err != nil {
		return Receipt{}, wechatError(CapPublish, err)
	}
	return receiptFromRef(ref), nil
}

func (a *WeChatAdapter) buildDraft(ctx context.Context, p WeChatPublishPayload) (DraftRequest, error) {
	kind := strings.ToLower(strings.TrimSpace(p.Kind))
	if kind == "" {
		kind = "article"
	}
	req := DraftRequest{
		Kind:             kind,
		Title:            p.Title,
		Author:           p.Author,
		Digest:           p.Digest,
		Content:          p.Content,
		ContentSourceURL: p.ContentSourceURL,
	}

	switch kind {
	case "article":
		if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" {
			return DraftRequest{}, invalidPayload(CapPublish, "wechat: article requires title and content")
		}
	case "image":
		if len(p.ImagePaths) == 0 {
			return DraftRequest{}, invalidPayload(CapPublish, "wechat: image payload requires image paths")
		}
	case "video":
		if strings.TrimSpace(p.VideoPath) == "" {
			return DraftRequest{}, invalidPayload(CapPublish, "wechat: video payload requires a video path")
		}
	default:
		return DraftRequest{}, &AdapterError{
			Capability: CapPublish,
			Code:       CodeUnsupported,
			Err:        fmt.Errorf("wechat: content kind %q is not supported", p.Kind),
		}
	}

	if p.ThumbMediaPath != "" {
		id, err := a.client.UploadMaterial(ctx, p.ThumbMediaPath)
		if err != nil {
			return DraftRequest{}, wechatError(CapPublish, err)
		}
		req.ThumbMediaID = id
	}
	for _, path := range p.ImagePaths {
		id, err := a.client.UploadMaterial(ctx, path)
		if err != nil {
			return DraftRequest{}, wechatError(CapPublish, err)
		}
		req.ImageMediaIDs = append(req.ImageMediaIDs, id)
	}
	if p.VideoPath != "" {
		id, err := a.client.UploadMaterial(ctx, p.VideoPath)
		if err != nil {
			return DraftRequest{}, wechatError(CapPublish, err)
		}
		req.VideoMediaID = id
	}
	return req, nil
}

// FetchMetrics merges article statistics with the user summary, maps the native
// keys into the unified namespace and fills any absent unified key with zero so
// charts stay continuous.
func (a *WeChatAdapter) FetchMetrics(ctx context.Context, from, to string) (map[string]float64, error) {
	if from == "" || to == "" || from > to {
		return nil, invalidPayload(CapMetrics, fmt.Sprintf("wechat: invalid metrics range %q..%q", from, to))
	}

	articles, err := a.client.ArticleStats(ctx, from, to)
	if err != nil {
		return nil, wechatError(CapMetrics, err)
	}
	users, err := a.client.UserSummary(ctx, from, to)
	if err != nil {
		return nil, wechatError(CapMetrics, err)
	}

	out := make(map[string]float64, len(articles)+len(users))
	mergeWechatMetrics(out, articles)
	mergeWechatMetrics(out, users)
	for _, key := range unifiedMetrics {
		if _, ok := out[key]; !ok {
			out[key] = 0
		}
	}
	return out, nil
}

func mergeWechatMetrics(dst, src map[string]float64) {
	for k, v := range src {
		if _, aliased := wechatMetricAliases[k]; !aliased {
			dst[k] = v
		}
	}
	for _, native := range wechatMetricOrder {
		unified := wechatMetricAliases[native]
		if _, set := dst[unified]; set {
			continue
		}
		if v, ok := src[native]; ok {
			dst[unified] = v
		}
	}
}

// ListEngagements is not exposed by the WeChat MP transport.
func (a *WeChatAdapter) ListEngagements(context.Context, string) ([]Engagement, error) {
	return nil, &AdapterError{
		Capability: CapEngagements,
		Code:       CodeUnsupported,
		Err:        errors.New("wechat: engagement listing is not supported"),
	}
}

// Reply is not exposed by the WeChat MP transport.
func (a *WeChatAdapter) Reply(context.Context, string, string) (Receipt, error) {
	return Receipt{}, &AdapterError{
		Capability: CapReply,
		Code:       CodeUnsupported,
		Err:        errors.New("wechat: engagement replies are not supported"),
	}
}

func wechatPayload(payload any) (WeChatPublishPayload, bool) {
	switch p := payload.(type) {
	case WeChatPublishPayload:
		return p, true
	case *WeChatPublishPayload:
		if p == nil {
			return WeChatPublishPayload{}, false
		}
		return *p, true
	default:
		return WeChatPublishPayload{}, false
	}
}

func receiptFromRef(ref string) Receipt {
	receipt := Receipt{ExternalID: ref}
	if strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://") {
		receipt.URL = ref
	}
	return receipt
}

func invalidPayload(cap Capability, msg string) *AdapterError {
	return &AdapterError{Capability: cap, Code: CodeInvalidPayload, Err: errors.New(msg)}
}

// wechatError maps transport and WeChat errcode failures to AdapterError so the
// publish orchestrator can decide between retry, manual hand-off and alerting.
func wechatError(cap Capability, err error) error {
	if err == nil {
		return nil
	}

	var adapterErr *AdapterError
	if errors.As(err, &adapterErr) {
		return adapterErr
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &AdapterError{Capability: cap, Code: CodeTransport, Retryable: true, Err: err}
	}

	var apiErr *WeChatAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case 40001, 40013, 40014, 41002, 42001:
			return &AdapterError{Capability: cap, Code: CodeCredentialInvalid, Retryable: false, Err: err}
		case 45009:
			return &AdapterError{Capability: cap, Code: CodeRateLimited, Retryable: true, Err: err}
		case 48001:
			return &AdapterError{Capability: cap, Code: CodeUnsupported, Retryable: false, Err: err}
		case -1:
			return &AdapterError{Capability: cap, Code: CodeSystemBusy, Retryable: true, Err: err}
		default:
			return &AdapterError{
				Capability: cap,
				Code:       fmt.Sprintf("wechat_%d", apiErr.Code),
				Retryable:  false,
				Err:        err,
			}
		}
	}

	return &AdapterError{Capability: cap, Code: CodeTransport, Retryable: true, Err: err}
}
