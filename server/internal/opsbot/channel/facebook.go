package channel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// GraphDefaultBaseURL is the pinned Graph API version. Pinning keeps a platform
// release from silently changing field names the metrics map depends on.
const GraphDefaultBaseURL = "https://graph.facebook.com/v21.0"

// Facebook content kinds accepted by FacebookPublishPayload.
const (
	FacebookKindPost  = "post"
	FacebookKindPhoto = "photo"
	FacebookKindReel  = "reel"
)

// Permissions EnsureSession requires on the Page token.
const (
	FacebookPermissionManagePosts    = "pages_manage_posts"
	FacebookPermissionReadEngagement = "pages_read_engagement"
)

// MetricFollowers is the unified audience-size metric. Facebook reports it as a
// gauge (page_fans), so the adapter does not expose it as follower_delta.
const MetricFollowers = "followers"

// Limits mirrored from the builtin facebook pack's content_schema.json.
const (
	facebookMaxPostChars        = 63206
	facebookMaxDescriptionChars = 2200
	facebookMaxReelSec          = 90
	facebookReelRatio           = "9:16"
)

// Graph API error codes the adapter maps onto the shared error contract.
const (
	facebookErrCodeInvalidToken   = 190
	facebookErrCodeRateLimit      = 4
	facebookErrCodeUserRateLimit  = 17
	facebookErrCodePageRateLimit  = 32
	facebookErrCodeRateLimitExtra = 613
	facebookErrCodeInvalidParam   = 100
	facebookErrCodePermission     = 200
	facebookErrCodeSystem         = 1
	facebookErrCodeSystemTemp     = 2
)

var facebookMetricAliases = map[string]string{
	"post_impressions":             MetricViews,
	"post_reactions_by_type_total": MetricLikes,
	"post_comments":                MetricComments,
	"post_shares":                  MetricShares,
	"page_fans":                    MetricFollowers,
}

// facebookMetricOrder makes the alias resolution deterministic where two
// native fields could map to the same unified key.
var facebookMetricOrder = []string{
	"post_impressions",
	"post_reactions_by_type_total",
	"post_comments",
	"post_shares",
	"page_fans",
}

var facebookUnifiedMetrics = []string{
	MetricViews, MetricLikes, MetricComments, MetricShares, MetricFollowers,
}

// FacebookAPIError is the Graph API error envelope. Code is the stable field
// callers switch on; Subcode refines it for a few token failures.
type FacebookAPIError struct {
	Code       int
	Subcode    int
	Type       string
	Message    string
	FBTraceID  string
	HTTPStatus int
}

func (e *FacebookAPIError) Error() string {
	msg := fmt.Sprintf("facebook graph api error %d", e.Code)
	if e.Subcode != 0 {
		msg += fmt.Sprintf("/%d", e.Subcode)
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// FacebookPageAuth is the result of validating a Page token.
type FacebookPageAuth struct {
	ID          string
	Name        string
	Permissions map[string]string
}

// FacebookPostRequest is one Page write. Kind decides the Graph endpoint.
type FacebookPostRequest struct {
	PageID    string
	Kind      string
	Message   string
	Link      string
	ImageURL  string
	VideoURL  string
	PublishAt *time.Time
}

// FacebookReceipt is a Graph API write receipt.
type FacebookReceipt struct {
	ExternalID string
	URL        string
	Raw        string
}

// FacebookComment is one inbound Page comment.
type FacebookComment struct {
	ID        string
	Author    string
	Text      string
	CreatedAt string
}

// FacebookCommentPage is one page of comments plus the cursor for the next.
type FacebookCommentPage struct {
	Comments   []FacebookComment
	NextCursor string
}

// FacebookClient is the Graph API surface the adapter drives. The proxy-backed
// provider implementation is GraphHTTPClient; tests substitute the mock server
// or a fake.
type FacebookClient interface {
	VerifyPage(ctx context.Context, pageID string) (FacebookPageAuth, error)
	CreatePost(ctx context.Context, req FacebookPostRequest) (FacebookReceipt, error)
	PageInsights(ctx context.Context, pageID, since, until string, metrics []string) (map[string]float64, error)
	PostInsights(ctx context.Context, pageID, since, until string, metrics []string) (map[string]float64, error)
	ListComments(ctx context.Context, pageID, cursor string) (FacebookCommentPage, error)
	ReplyComment(ctx context.Context, commentID, message string) (FacebookReceipt, error)
	SendMessage(ctx context.Context, pageID, recipientID, message string) (FacebookReceipt, error)
}

// FacebookPublishPayload is the payload accepted by FacebookAdapter.Publish.
// Kind is post (default), photo or reel.
type FacebookPublishPayload struct {
	Kind    string
	Message string
	// Link turns a post into a link share.
	Link string
	// ImageURL posts a photo when set.
	ImageURL string
	// VideoURL is the reel video; a reel requires it.
	VideoURL string
	// PublishAt schedules the write through scheduled_publish_time.
	PublishAt *time.Time
	// DurationSec and AspectRatio are checked against the pack's reel limits.
	DurationSec int
	AspectRatio string
}

// FacebookAdapter implements ChannelAdapter over the Graph API. The Page token
// and app credentials live in the client; the adapter binds the Page from
// accountRef.
type FacebookAdapter struct {
	client FacebookClient

	mu     sync.RWMutex
	pageID string
}

var _ ChannelAdapter = (*FacebookAdapter)(nil)

func NewFacebookAdapter(client FacebookClient) *FacebookAdapter {
	return &FacebookAdapter{client: client}
}

func (a *FacebookAdapter) Capabilities() CapabilitySet {
	return CapabilitySet{
		CapPublish:     true,
		CapMetrics:     true,
		CapEngagements: true,
		CapReply:       true,
	}
}

// EnsureSession validates the Page token and the two permissions the adapter
// needs. A token that resolves to another Page is treated as a credential
// problem, not a transient failure.
func (a *FacebookAdapter) EnsureSession(ctx context.Context, accountRef string) error {
	pageID := strings.TrimSpace(accountRef)
	if pageID == "" {
		return &AdapterError{
			Capability: CapPublish,
			Code:       CodeCredentialInvalid,
			Err:        errors.New("facebook: empty page reference"),
		}
	}

	page, err := a.client.VerifyPage(ctx, pageID)
	if err != nil {
		return facebookError(CapPublish, err)
	}
	if missing := missingFacebookPermissions(page.Permissions); len(missing) > 0 {
		return &AdapterError{
			Capability: CapPublish,
			Code:       CodeCredentialInvalid,
			Err:        fmt.Errorf("facebook: page token is missing permissions %s", strings.Join(missing, ", ")),
		}
	}

	a.mu.Lock()
	a.pageID = pageID
	a.mu.Unlock()
	return nil
}

// HealthCheck re-validates the bound Page token.
func (a *FacebookAdapter) HealthCheck(ctx context.Context) error {
	pageID, ok := a.account()
	if !ok {
		return &AdapterError{
			Capability: CapMetrics,
			Code:       CodeCredentialInvalid,
			Err:        errors.New("facebook: no page bound; call EnsureSession first"),
		}
	}
	_, err := a.client.VerifyPage(ctx, pageID)
	return facebookError(CapMetrics, err)
}

// Publish validates the payload against the pack schema and writes a Page post,
// photo or reel. Scheduled writes pass scheduled_publish_time.
func (a *FacebookAdapter) Publish(ctx context.Context, payload any) (Receipt, error) {
	p, ok := facebookPayload(payload)
	if !ok {
		return Receipt{}, invalidPayload(CapPublish, fmt.Sprintf("facebook: unsupported payload type %T", payload))
	}
	pageID, bound := a.account()
	if !bound {
		return Receipt{}, &AdapterError{
			Capability: CapPublish,
			Code:       CodeCredentialInvalid,
			Err:        errors.New("facebook: no page bound; call EnsureSession first"),
		}
	}

	req, err := facebookPostRequest(pageID, p)
	if err != nil {
		return Receipt{}, err
	}
	receipt, err := a.client.CreatePost(ctx, req)
	if err != nil {
		return Receipt{}, facebookError(CapPublish, err)
	}
	return Receipt{ExternalID: receipt.ExternalID, URL: receipt.URL, Raw: receipt.Raw}, nil
}

func facebookPayload(payload any) (FacebookPublishPayload, bool) {
	switch p := payload.(type) {
	case FacebookPublishPayload:
		return p, true
	case *FacebookPublishPayload:
		if p == nil {
			return FacebookPublishPayload{}, false
		}
		return *p, true
	default:
		return FacebookPublishPayload{}, false
	}
}

func facebookPostRequest(pageID string, p FacebookPublishPayload) (FacebookPostRequest, error) {
	kind := strings.ToLower(strings.TrimSpace(p.Kind))
	switch kind {
	case "":
		kind = FacebookKindPost
	case "text", "link":
		kind = FacebookKindPost
	case "image":
		kind = FacebookKindPhoto
	}

	message := strings.TrimSpace(p.Message)
	link := strings.TrimSpace(p.Link)
	image := strings.TrimSpace(p.ImageURL)
	video := strings.TrimSpace(p.VideoURL)

	switch kind {
	case FacebookKindPost:
		if message == "" && link == "" && image == "" {
			return FacebookPostRequest{}, invalidPayload(CapPublish, "facebook: a post needs a message, link or image")
		}
		if utf8.RuneCountInString(message) > facebookMaxPostChars {
			return FacebookPostRequest{}, invalidPayload(CapPublish,
				fmt.Sprintf("facebook: post message exceeds %d characters", facebookMaxPostChars))
		}
		if image != "" {
			kind = FacebookKindPhoto
		}
	case FacebookKindPhoto:
		if image == "" {
			return FacebookPostRequest{}, invalidPayload(CapPublish, "facebook: a photo post requires an image URL")
		}
		if utf8.RuneCountInString(message) > facebookMaxPostChars {
			return FacebookPostRequest{}, invalidPayload(CapPublish,
				fmt.Sprintf("facebook: photo caption exceeds %d characters", facebookMaxPostChars))
		}
	case FacebookKindReel:
		if video == "" {
			return FacebookPostRequest{}, invalidPayload(CapPublish, "facebook: a reel requires a video URL")
		}
		if utf8.RuneCountInString(message) > facebookMaxDescriptionChars {
			return FacebookPostRequest{}, invalidPayload(CapPublish,
				fmt.Sprintf("facebook: reel description exceeds %d characters", facebookMaxDescriptionChars))
		}
		if p.DurationSec < 0 || p.DurationSec > facebookMaxReelSec {
			return FacebookPostRequest{}, invalidPayload(CapPublish,
				fmt.Sprintf("facebook: reel duration %ds exceeds %ds", p.DurationSec, facebookMaxReelSec))
		}
		if ratio := strings.TrimSpace(p.AspectRatio); ratio != "" && ratio != facebookReelRatio {
			return FacebookPostRequest{}, invalidPayload(CapPublish,
				fmt.Sprintf("facebook: reel aspect ratio %s, want %s", ratio, facebookReelRatio))
		}
	default:
		return FacebookPostRequest{}, &AdapterError{
			Capability: CapPublish,
			Code:       CodeUnsupported,
			Err:        fmt.Errorf("facebook: content kind %q is not supported", p.Kind),
		}
	}

	if p.PublishAt != nil && !p.PublishAt.After(time.Now()) {
		return FacebookPostRequest{}, invalidPayload(CapPublish,
			fmt.Sprintf("facebook: publish_at %s is not in the future", p.PublishAt.Format(time.RFC3339)))
	}

	return FacebookPostRequest{
		PageID:    pageID,
		Kind:      kind,
		Message:   message,
		Link:      link,
		ImageURL:  image,
		VideoURL:  video,
		PublishAt: p.PublishAt,
	}, nil
}

// FetchMetrics merges Page-level gauges (followers) with aggregated post
// insights and folds the native keys into the unified namespace.
func (a *FacebookAdapter) FetchMetrics(ctx context.Context, from, to string) (map[string]float64, error) {
	pageID, ok := a.account()
	if !ok {
		return nil, &AdapterError{
			Capability: CapMetrics,
			Code:       CodeCredentialInvalid,
			Err:        errors.New("facebook: no page bound; call EnsureSession first"),
		}
	}
	since, until, err := facebookDateRange(from, to)
	if err != nil {
		return nil, err
	}

	pageMetrics, err := a.client.PageInsights(ctx, pageID, since, until, []string{"page_fans"})
	if err != nil {
		return nil, facebookError(CapMetrics, err)
	}
	postMetrics, err := a.client.PostInsights(ctx, pageID, since, until, []string{
		"post_impressions", "post_reactions_by_type_total", "post_comments", "post_shares",
	})
	if err != nil {
		return nil, facebookError(CapMetrics, err)
	}

	out := make(map[string]float64, len(facebookUnifiedMetrics))
	mergeFacebookMetrics(out, pageMetrics)
	mergeFacebookMetrics(out, postMetrics)
	for _, key := range facebookUnifiedMetrics {
		if _, ok := out[key]; !ok {
			out[key] = 0
		}
	}
	return out, nil
}

func facebookDateRange(from, to string) (string, string, error) {
	start, err := time.Parse("2006-01-02", strings.TrimSpace(from))
	if err != nil {
		return "", "", invalidPayload(CapMetrics, fmt.Sprintf("facebook: invalid metrics start %q", from))
	}
	end, err := time.Parse("2006-01-02", strings.TrimSpace(to))
	if err != nil {
		return "", "", invalidPayload(CapMetrics, fmt.Sprintf("facebook: invalid metrics end %q", to))
	}
	if end.Before(start) {
		return "", "", invalidPayload(CapMetrics, fmt.Sprintf("facebook: metrics range %q..%q is reversed", from, to))
	}
	since := strconv.FormatInt(start.UTC().Unix(), 10)
	until := strconv.FormatInt(end.Add(24*time.Hour-time.Second).UTC().Unix(), 10)
	return since, until, nil
}

func mergeFacebookMetrics(dst, src map[string]float64) {
	for k, v := range src {
		if _, aliased := facebookMetricAliases[k]; !aliased {
			dst[k] = v
		}
	}
	for _, native := range facebookMetricOrder {
		unified := facebookMetricAliases[native]
		if _, set := dst[unified]; set {
			continue
		}
		if v, ok := src[native]; ok {
			dst[unified] = v
		}
	}
}

// ListEngagements pulls Page comments. cursor is forwarded as the Graph "after"
// cursor verbatim.
func (a *FacebookAdapter) ListEngagements(ctx context.Context, cursor string) ([]Engagement, error) {
	pageID, ok := a.account()
	if !ok {
		return nil, &AdapterError{
			Capability: CapEngagements,
			Code:       CodeCredentialInvalid,
			Err:        errors.New("facebook: no page bound; call EnsureSession first"),
		}
	}
	page, err := a.client.ListComments(ctx, pageID, strings.TrimSpace(cursor))
	if err != nil {
		return nil, facebookError(CapEngagements, err)
	}

	out := make([]Engagement, 0, len(page.Comments))
	for _, comment := range page.Comments {
		if strings.TrimSpace(comment.ID) == "" {
			continue
		}
		out = append(out, Engagement{
			ExtID:  comment.ID,
			Kind:   "comment",
			Author: comment.Author,
			Text:   comment.Text,
		})
	}
	return out, nil
}

// Reply sends an operator-approved reply. "dm:<psid>" addresses a Messenger
// recipient; anything else is a comment reply.
func (a *FacebookAdapter) Reply(ctx context.Context, engagementID, text string) (Receipt, error) {
	id := strings.TrimSpace(engagementID)
	body := strings.TrimSpace(text)
	if id == "" {
		return Receipt{}, invalidPayload(CapReply, "facebook: engagement id is required")
	}
	if body == "" {
		return Receipt{}, invalidPayload(CapReply, "facebook: reply text is required")
	}

	if recipient := strings.TrimSpace(strings.TrimPrefix(id, "dm:")); strings.HasPrefix(id, "dm:") {
		if recipient == "" {
			return Receipt{}, invalidPayload(CapReply, "facebook: direct message recipient is required")
		}
		pageID, ok := a.account()
		if !ok {
			return Receipt{}, &AdapterError{
				Capability: CapReply,
				Code:       CodeCredentialInvalid,
				Err:        errors.New("facebook: no page bound; call EnsureSession first"),
			}
		}
		receipt, err := a.client.SendMessage(ctx, pageID, recipient, body)
		if err != nil {
			return Receipt{}, facebookError(CapReply, err)
		}
		return Receipt{ExternalID: receipt.ExternalID, URL: receipt.URL, Raw: receipt.Raw}, nil
	}

	id = strings.TrimPrefix(id, "comment:")
	receipt, err := a.client.ReplyComment(ctx, id, body)
	if err != nil {
		return Receipt{}, facebookError(CapReply, err)
	}
	return Receipt{ExternalID: receipt.ExternalID, URL: receipt.URL, Raw: receipt.Raw}, nil
}

func (a *FacebookAdapter) account() (string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.pageID, a.pageID != ""
}

func missingFacebookPermissions(got map[string]string) []string {
	var missing []string
	for _, permission := range []string{FacebookPermissionManagePosts, FacebookPermissionReadEngagement} {
		if !strings.EqualFold(strings.TrimSpace(got[permission]), "granted") {
			missing = append(missing, permission)
		}
	}
	return missing
}

// facebookError maps Graph failures onto the adapter contract. An invalid token
// is never retried; Graph's several rate-limit codes are all retryable.
func facebookError(capability Capability, err error) error {
	if err == nil {
		return nil
	}

	var adapterErr *AdapterError
	if errors.As(err, &adapterErr) {
		return adapterErr
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &AdapterError{Capability: capability, Code: CodeTransport, Err: err}
	}

	var apiErr *FacebookAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case facebookErrCodeInvalidToken, facebookErrCodePermission:
			return &AdapterError{Capability: capability, Code: CodeCredentialInvalid, Err: err}
		case facebookErrCodeRateLimit, facebookErrCodeUserRateLimit,
			facebookErrCodePageRateLimit, facebookErrCodeRateLimitExtra:
			return &AdapterError{Capability: capability, Code: CodeRateLimited, Retryable: true, Err: err}
		case facebookErrCodeInvalidParam:
			return &AdapterError{Capability: capability, Code: CodeInvalidPayload, Err: err}
		case facebookErrCodeSystem, facebookErrCodeSystemTemp:
			return &AdapterError{Capability: capability, Code: CodeSystemBusy, Retryable: true, Err: err}
		default:
			return &AdapterError{
				Capability: capability,
				Code:       fmt.Sprintf("facebook_%d", apiErr.Code),
				Err:        err,
			}
		}
	}

	return &AdapterError{Capability: capability, Code: CodeTransport, Retryable: true, Err: err}
}

// GraphHTTPClient is the Graph API transport. It is the provider-backed
// implementation of FacebookClient: BaseURL points at the graph host (or a mock
// in tests), the Page token and optional app secret are injected on every call.
type GraphHTTPClient struct {
	BaseURL   string
	PageToken string
	// AppSecret, when set, adds appsecret_proof so a request cannot replay a
	// stolen token without the app secret.
	AppSecret  string
	HTTPClient *http.Client
	// MaxPosts bounds the post-insight fan-out per metrics sync.
	MaxPosts int
}

var _ FacebookClient = (*GraphHTTPClient)(nil)

func NewGraphHTTPClient(pageToken string) *GraphHTTPClient {
	return &GraphHTTPClient{BaseURL: GraphDefaultBaseURL, PageToken: pageToken}
}

func (c *GraphHTTPClient) VerifyPage(ctx context.Context, pageID string) (FacebookPageAuth, error) {
	var me struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/me", url.Values{"fields": {"id,name"}}, &me); err != nil {
		return FacebookPageAuth{}, err
	}
	if me.ID != "" && pageID != "" && me.ID != pageID {
		return FacebookPageAuth{}, &FacebookAPIError{
			Code:    facebookErrCodeInvalidToken,
			Message: fmt.Sprintf("page token belongs to %s, not %s", me.ID, pageID),
		}
	}

	var permissions struct {
		Data []struct {
			Permission string `json:"permission"`
			Status     string `json:"status"`
		} `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/me/permissions", nil, &permissions); err != nil {
		return FacebookPageAuth{}, err
	}

	auth := FacebookPageAuth{ID: me.ID, Name: me.Name, Permissions: make(map[string]string, len(permissions.Data))}
	for _, permission := range permissions.Data {
		auth.Permissions[permission.Permission] = permission.Status
	}
	return auth, nil
}

func (c *GraphHTTPClient) CreatePost(ctx context.Context, req FacebookPostRequest) (FacebookReceipt, error) {
	pageID := strings.TrimSpace(req.PageID)
	if pageID == "" {
		return FacebookReceipt{}, errors.New("facebook: page id is required")
	}

	form := url.Values{}
	var path string
	switch req.Kind {
	case FacebookKindReel:
		path = url.PathEscape(pageID) + "/video_reels"
		form.Set("description", req.Message)
		form.Set("video_url", req.VideoURL)
	case FacebookKindPhoto:
		path = url.PathEscape(pageID) + "/photos"
		form.Set("url", req.ImageURL)
		if req.Message != "" {
			form.Set("caption", req.Message)
		}
	default:
		path = url.PathEscape(pageID) + "/feed"
		if req.Message != "" {
			form.Set("message", req.Message)
		}
		if req.Link != "" {
			form.Set("link", req.Link)
		}
	}
	if req.PublishAt != nil {
		form.Set("published", "false")
		form.Set("scheduled_publish_time", strconv.FormatInt(req.PublishAt.Unix(), 10))
	}

	var out graphWriteResponse
	raw, err := c.do(ctx, http.MethodPost, path, form, &out)
	if err != nil {
		return FacebookReceipt{}, err
	}
	id := out.firstID()
	if id == "" {
		return FacebookReceipt{}, fmt.Errorf("facebook: graph write returned no id: %s", truncateBody(raw))
	}
	receipt := FacebookReceipt{ExternalID: id, Raw: truncateBody(raw)}
	if out.PermalinkURL != "" {
		receipt.URL = out.PermalinkURL
	} else {
		receipt.URL = "https://www.facebook.com/" + id
	}
	return receipt, nil
}

func (c *GraphHTTPClient) PageInsights(ctx context.Context, pageID, since, until string, metrics []string) (map[string]float64, error) {
	if len(metrics) == 0 {
		return map[string]float64{}, nil
	}
	form := url.Values{
		"metric": {strings.Join(metrics, ",")},
		"period": {"day"},
	}
	if since != "" {
		form.Set("since", since)
	}
	if until != "" {
		form.Set("until", until)
	}

	var payload graphInsightResponse
	if _, err := c.do(ctx, http.MethodGet, url.PathEscape(pageID)+"/insights", form, &payload); err != nil {
		return nil, err
	}
	return payload.latestValues(), nil
}

// PostInsights aggregates the requested metrics across the Page's posts in the
// range. Post metrics are per-post totals, so they sum; a reaction breakdown
// object is summed into one likes number.
func (c *GraphHTTPClient) PostInsights(ctx context.Context, pageID, since, until string, metrics []string) (map[string]float64, error) {
	if len(metrics) == 0 {
		return map[string]float64{}, nil
	}
	posts, err := c.pagePostIDs(ctx, pageID, since, until)
	if err != nil {
		return nil, err
	}

	out := make(map[string]float64, len(metrics))
	for _, postID := range posts {
		values, err := c.postInsights(ctx, postID, metrics)
		if err != nil {
			return nil, err
		}
		for key, value := range values {
			out[key] += value
		}
	}
	return out, nil
}

func (c *GraphHTTPClient) pagePostIDs(ctx context.Context, pageID, since, until string) ([]string, error) {
	form := url.Values{
		"fields": {"id"},
		"limit":  {strconv.Itoa(c.maxPosts())},
	}
	if since != "" {
		form.Set("since", since)
	}
	if until != "" {
		form.Set("until", until)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, url.PathEscape(pageID)+"/posts", form, &payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Data))
	for _, post := range payload.Data {
		if id := strings.TrimSpace(post.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (c *GraphHTTPClient) postInsights(ctx context.Context, postID string, metrics []string) (map[string]float64, error) {
	form := url.Values{"metric": {strings.Join(metrics, ",")}}
	var payload graphInsightResponse
	if _, err := c.do(ctx, http.MethodGet, url.PathEscape(postID)+"/insights", form, &payload); err != nil {
		return nil, err
	}
	// Lifetime post insights carry one value per metric; summing the values is
	// the same as taking the total when the API returns a single bucket.
	return payload.totalValues(), nil
}

func (c *GraphHTTPClient) ListComments(ctx context.Context, pageID, cursor string) (FacebookCommentPage, error) {
	form := url.Values{
		"fields": {"id,from,message,created_time"},
		"limit":  {"50"},
	}
	if cursor != "" {
		form.Set("after", cursor)
	}

	var payload graphCommentResponse
	if _, err := c.do(ctx, http.MethodGet, url.PathEscape(pageID)+"/comments", form, &payload); err != nil {
		return FacebookCommentPage{}, err
	}

	page := FacebookCommentPage{Comments: make([]FacebookComment, 0, len(payload.Data))}
	for _, comment := range payload.Data {
		page.Comments = append(page.Comments, FacebookComment{
			ID:        comment.ID,
			Author:    comment.From.Name,
			Text:      comment.Message,
			CreatedAt: comment.CreatedTime,
		})
	}
	page.NextCursor = payload.Paging.Cursors.After
	if page.NextCursor == "" {
		page.NextCursor = cursorFromNext(payload.Paging.Next)
	}
	return page, nil
}

func (c *GraphHTTPClient) ReplyComment(ctx context.Context, commentID, message string) (FacebookReceipt, error) {
	id := strings.TrimSpace(commentID)
	if id == "" {
		return FacebookReceipt{}, errors.New("facebook: comment id is required")
	}
	form := url.Values{"message": {message}}
	var out graphWriteResponse
	raw, err := c.do(ctx, http.MethodPost, url.PathEscape(id)+"/comments", form, &out)
	if err != nil {
		return FacebookReceipt{}, err
	}
	writeID := out.firstID()
	if writeID == "" {
		return FacebookReceipt{}, fmt.Errorf("facebook: reply returned no id: %s", truncateBody(raw))
	}
	return FacebookReceipt{
		ExternalID: writeID,
		URL:        "https://www.facebook.com/" + writeID,
		Raw:        truncateBody(raw),
	}, nil
}

func (c *GraphHTTPClient) SendMessage(ctx context.Context, pageID, recipientID, message string) (FacebookReceipt, error) {
	id := strings.TrimSpace(pageID)
	if id == "" {
		return FacebookReceipt{}, errors.New("facebook: page id is required")
	}
	form := url.Values{
		"recipient":      {fmt.Sprintf(`{"id":%q}`, strings.TrimSpace(recipientID))},
		"message":        {fmt.Sprintf(`{"text":%q}`, message)},
		"messaging_type": {"RESPONSE"},
	}
	var out graphWriteResponse
	raw, err := c.do(ctx, http.MethodPost, url.PathEscape(id)+"/messages", form, &out)
	if err != nil {
		return FacebookReceipt{}, err
	}
	messageID := strings.TrimSpace(out.MessageID)
	if messageID == "" {
		messageID = out.firstID()
	}
	if messageID == "" {
		return FacebookReceipt{}, fmt.Errorf("facebook: message send returned no id: %s", truncateBody(raw))
	}
	return FacebookReceipt{ExternalID: messageID, Raw: truncateBody(raw)}, nil
}

func (c *GraphHTTPClient) maxPosts() int {
	if c.MaxPosts > 0 {
		return c.MaxPosts
	}
	return 25
}

func (c *GraphHTTPClient) baseURL() string {
	if strings.TrimSpace(c.BaseURL) == "" {
		return GraphDefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *GraphHTTPClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// do issues one Graph request. GET parameters ride in the query string and the
// access token travels in the Authorization header; write parameters (including
// the token) ride in the form body. A token is never placed in a URL, so it
// cannot leak through proxies or request logs.
func (c *GraphHTTPClient) do(ctx context.Context, method, path string, form url.Values, out any) ([]byte, error) {
	endpoint := c.baseURL() + "/" + strings.TrimLeft(path, "/")

	token := strings.TrimSpace(c.PageToken)
	proof := ""
	if token != "" {
		if secret := strings.TrimSpace(c.AppSecret); secret != "" {
			proof = graphAppSecretProof(token, secret)
		}
	}

	var body io.Reader
	if method == http.MethodGet {
		query := url.Values{}
		for k, vs := range form {
			query[k] = append([]string(nil), vs...)
		}
		if proof != "" {
			query.Set("appsecret_proof", proof)
		}
		if len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
	} else {
		bodyForm := url.Values{}
		for k, vs := range form {
			bodyForm[k] = append([]string(nil), vs...)
		}
		if token != "" {
			bodyForm.Set("access_token", token)
			if proof != "" {
				bodyForm.Set("appsecret_proof", proof)
			}
		}
		if len(bodyForm) > 0 {
			body = strings.NewReader(bodyForm.Encode())
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodGet && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return raw, parseGraphAPIError(resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return raw, fmt.Errorf("facebook: decode %s %s response: %w", method, path, err)
		}
	}
	return raw, nil
}

type graphWriteResponse struct {
	ID           string `json:"id"`
	PostID       string `json:"post_id"`
	VideoID      string `json:"video_id"`
	MessageID    string `json:"message_id"`
	PermalinkURL string `json:"permalink_url"`
}

func (r graphWriteResponse) firstID() string {
	for _, id := range []string{r.ID, r.PostID, r.VideoID, r.MessageID} {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type graphInsightResponse struct {
	Data []struct {
		Name   string `json:"name"`
		Period string `json:"period"`
		Values []struct {
			Value json.RawMessage `json:"value"`
		} `json:"values"`
	} `json:"data"`
}

// latestValues keeps the last bucket per metric, which is the current state of
// a gauge such as page_fans and the lifetime total of a post metric.
func (r graphInsightResponse) latestValues() map[string]float64 {
	out := make(map[string]float64, len(r.Data))
	for _, series := range r.Data {
		for i := len(series.Values) - 1; i >= 0; i-- {
			if value, ok := graphNumber(series.Values[i].Value); ok {
				out[series.Name] = value
				break
			}
		}
	}
	return out
}

// totalValues sums every bucket, which is what post-level totals need.
func (r graphInsightResponse) totalValues() map[string]float64 {
	out := make(map[string]float64, len(r.Data))
	for _, series := range r.Data {
		total := 0.0
		found := false
		for _, bucket := range series.Values {
			if value, ok := graphNumber(bucket.Value); ok {
				total += value
				found = true
			}
		}
		if found {
			out[series.Name] = total
		}
	}
	return out
}

// graphNumber accepts the three shapes Graph returns for an insight value: a
// number, a numeric string, or a reaction-type breakdown object.
func graphNumber(raw json.RawMessage) (float64, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, false
	}

	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if value, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
			return value, true
		}
		return 0, false
	}
	var breakdown map[string]float64
	if err := json.Unmarshal(raw, &breakdown); err == nil {
		total := 0.0
		for _, value := range breakdown {
			total += value
		}
		return total, true
	}
	return 0, false
}

type graphCommentResponse struct {
	Data []struct {
		ID          string `json:"id"`
		Message     string `json:"message"`
		CreatedTime string `json:"created_time"`
		From        struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"from"`
	} `json:"data"`
	Paging struct {
		Cursors struct {
			After string `json:"after"`
		} `json:"cursors"`
		Next string `json:"next"`
	} `json:"paging"`
}

func cursorFromNext(next string) string {
	if next == "" {
		return ""
	}
	parsed, err := url.Parse(next)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("after")
}

func parseGraphAPIError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Message   string `json:"message"`
			Type      string `json:"type"`
			Code      int    `json:"code"`
			Subcode   int    `json:"error_subcode"`
			FBTraceID string `json:"fbtrace_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && (payload.Error.Code != 0 || payload.Error.Message != "") {
		return &FacebookAPIError{
			Code:       payload.Error.Code,
			Subcode:    payload.Error.Subcode,
			Type:       payload.Error.Type,
			Message:    payload.Error.Message,
			FBTraceID:  payload.Error.FBTraceID,
			HTTPStatus: status,
		}
	}
	return fmt.Errorf("facebook: graph api http %d: %s", status, truncateBody(body))
}

func graphAppSecretProof(token, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func truncateBody(raw []byte) string {
	const limit = 512
	body := strings.TrimSpace(string(raw))
	if len(body) <= limit {
		return body
	}
	return body[:limit] + "..."
}
