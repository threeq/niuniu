package channel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func grantedFacebookAuth() FacebookPageAuth {
	return FacebookPageAuth{
		ID:   "page-1",
		Name: "Test Page",
		Permissions: map[string]string{
			FacebookPermissionManagePosts:    "granted",
			FacebookPermissionReadEngagement: "granted",
		},
	}
}

type fakeFacebookClient struct {
	auth      FacebookPageAuth
	verifyErr error
	verifyIDs []string

	postReceipt FacebookReceipt
	postErr     error
	posts       []FacebookPostRequest

	pageMetrics    map[string]float64
	pageMetricsErr error
	pageMetricsArg struct{ pageID, since, until string }

	postMetrics    map[string]float64
	postMetricsErr error
	postMetricsArg struct{ pageID, since, until string }

	comments       FacebookCommentPage
	commentsErr    error
	commentsPageID string
	commentsCursor string

	replyReceipt FacebookReceipt
	replyErr     error
	replyID      string
	replyText    string

	dmReceipt   FacebookReceipt
	dmErr       error
	dmPageID    string
	dmRecipient string
	dmText      string
}

func (f *fakeFacebookClient) VerifyPage(_ context.Context, pageID string) (FacebookPageAuth, error) {
	f.verifyIDs = append(f.verifyIDs, pageID)
	if f.verifyErr != nil {
		return FacebookPageAuth{}, f.verifyErr
	}
	return f.auth, nil
}

func (f *fakeFacebookClient) CreatePost(_ context.Context, req FacebookPostRequest) (FacebookReceipt, error) {
	f.posts = append(f.posts, req)
	if f.postErr != nil {
		return FacebookReceipt{}, f.postErr
	}
	return f.postReceipt, nil
}

func (f *fakeFacebookClient) PageInsights(_ context.Context, pageID, since, until string, _ []string) (map[string]float64, error) {
	f.pageMetricsArg = struct{ pageID, since, until string }{pageID, since, until}
	if f.pageMetricsErr != nil {
		return nil, f.pageMetricsErr
	}
	return f.pageMetrics, nil
}

func (f *fakeFacebookClient) PostInsights(_ context.Context, pageID, since, until string, _ []string) (map[string]float64, error) {
	f.postMetricsArg = struct{ pageID, since, until string }{pageID, since, until}
	if f.postMetricsErr != nil {
		return nil, f.postMetricsErr
	}
	return f.postMetrics, nil
}

func (f *fakeFacebookClient) ListComments(_ context.Context, pageID, cursor string) (FacebookCommentPage, error) {
	f.commentsPageID = pageID
	f.commentsCursor = cursor
	if f.commentsErr != nil {
		return FacebookCommentPage{}, f.commentsErr
	}
	return f.comments, nil
}

func (f *fakeFacebookClient) ReplyComment(_ context.Context, commentID, message string) (FacebookReceipt, error) {
	f.replyID = commentID
	f.replyText = message
	if f.replyErr != nil {
		return FacebookReceipt{}, f.replyErr
	}
	return f.replyReceipt, nil
}

func (f *fakeFacebookClient) SendMessage(_ context.Context, pageID, recipientID, message string) (FacebookReceipt, error) {
	f.dmPageID = pageID
	f.dmRecipient = recipientID
	f.dmText = message
	if f.dmErr != nil {
		return FacebookReceipt{}, f.dmErr
	}
	return f.dmReceipt, nil
}

func newFakeFacebookClient() *fakeFacebookClient {
	return &fakeFacebookClient{
		auth:        grantedFacebookAuth(),
		postReceipt: FacebookReceipt{ExternalID: "page-1_post-1", URL: "https://www.facebook.com/page-1_post-1"},
		pageMetrics: map[string]float64{"page_fans": 2500},
		postMetrics: map[string]float64{
			"post_impressions":             5000,
			"post_reactions_by_type_total": 120,
			"post_comments":                30,
			"post_shares":                  8,
		},
		comments: FacebookCommentPage{
			Comments:   []FacebookComment{{ID: "c-1", Author: "Alice", Text: "Great post"}},
			NextCursor: "cursor-2",
		},
		replyReceipt: FacebookReceipt{ExternalID: "reply-1", URL: "https://www.facebook.com/reply-1"},
		dmReceipt:    FacebookReceipt{ExternalID: "m_1"},
	}
}

func newBoundFacebookAdapter(t *testing.T, client FacebookClient) *FacebookAdapter {
	t.Helper()
	adapter := NewFacebookAdapter(client)
	if err := adapter.EnsureSession(context.Background(), "page-1"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	return adapter
}

func TestFacebookCapabilities(t *testing.T) {
	adapter := NewFacebookAdapter(newFakeFacebookClient())
	caps := adapter.Capabilities()
	for _, capability := range []Capability{CapPublish, CapMetrics, CapEngagements, CapReply} {
		if !caps.Supports(capability) {
			t.Fatalf("facebook must support %q: %v", capability, caps)
		}
	}
}

func TestFacebookEnsureSessionValidatesPermissions(t *testing.T) {
	client := newFakeFacebookClient()
	adapter := NewFacebookAdapter(client)

	if err := adapter.EnsureSession(context.Background(), " page-1 "); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	if len(client.verifyIDs) != 1 || client.verifyIDs[0] != "page-1" {
		t.Fatalf("verified pages = %v, want [page-1]", client.verifyIDs)
	}

	if err := adapter.EnsureSession(context.Background(), ""); asAdapterError(t, err).Code != CodeCredentialInvalid {
		t.Fatalf("empty page ref code = %v", err)
	}

	client.auth = FacebookPageAuth{
		ID:          "page-1",
		Permissions: map[string]string{FacebookPermissionManagePosts: "granted"},
	}
	err := adapter.EnsureSession(context.Background(), "page-1")
	adapterErr := asAdapterError(t, err)
	if adapterErr.Code != CodeCredentialInvalid || adapterErr.Retryable {
		t.Fatalf("missing permission = %+v, want credential_invalid non-retryable", adapterErr)
	}
}

func TestFacebookEnsureSessionMapsInvalidToken(t *testing.T) {
	client := newFakeFacebookClient()
	client.verifyErr = &FacebookAPIError{Code: facebookErrCodeInvalidToken, Message: "Invalid OAuth access token."}
	adapter := NewFacebookAdapter(client)

	adapterErr := asAdapterError(t, adapter.EnsureSession(context.Background(), "page-1"))
	if adapterErr.Code != CodeCredentialInvalid || adapterErr.Retryable {
		t.Fatalf("invalid token = %+v, want credential_invalid non-retryable", adapterErr)
	}
}

func TestFacebookPublishKinds(t *testing.T) {
	client := newFakeFacebookClient()
	adapter := newBoundFacebookAdapter(t, client)

	if _, err := adapter.Publish(context.Background(), FacebookPublishPayload{Message: "Hello Page"}); err != nil {
		t.Fatalf("publish post: %v", err)
	}
	if _, err := adapter.Publish(context.Background(), FacebookPublishPayload{Message: "Photo caption", ImageURL: "https://cdn.example.com/a.jpg"}); err != nil {
		t.Fatalf("publish photo: %v", err)
	}
	if _, err := adapter.Publish(context.Background(), FacebookPublishPayload{
		Kind:        FacebookKindReel,
		Message:     "Reel caption",
		VideoURL:    "https://cdn.example.com/a.mp4",
		DurationSec: 60,
		AspectRatio: "9:16",
	}); err != nil {
		t.Fatalf("publish reel: %v", err)
	}

	if len(client.posts) != 3 {
		t.Fatalf("posts = %d, want 3", len(client.posts))
	}
	if client.posts[0].Kind != FacebookKindPost || client.posts[0].Message != "Hello Page" {
		t.Errorf("post request = %+v", client.posts[0])
	}
	if client.posts[1].Kind != FacebookKindPhoto || client.posts[1].ImageURL == "" {
		t.Errorf("photo request = %+v", client.posts[1])
	}
	if client.posts[2].Kind != FacebookKindReel || client.posts[2].VideoURL == "" {
		t.Errorf("reel request = %+v", client.posts[2])
	}
}

func TestFacebookPublishScheduled(t *testing.T) {
	client := newFakeFacebookClient()
	adapter := newBoundFacebookAdapter(t, client)

	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if _, err := adapter.Publish(context.Background(), FacebookPublishPayload{
		Message:   "Later",
		PublishAt: &at,
	}); err != nil {
		t.Fatalf("scheduled publish: %v", err)
	}
	if client.posts[0].PublishAt == nil || !client.posts[0].PublishAt.Equal(at) {
		t.Fatalf("publish_at = %v, want %v", client.posts[0].PublishAt, at)
	}

	past := time.Now().Add(-time.Minute)
	_, err := adapter.Publish(context.Background(), FacebookPublishPayload{Message: "Past", PublishAt: &past})
	if asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("past publish_at code = %v, want invalid_payload", err)
	}
}

func TestFacebookPublishRejectsInvalidPayloads(t *testing.T) {
	client := newFakeFacebookClient()
	adapter := newBoundFacebookAdapter(t, client)

	cases := []struct {
		name    string
		payload any
		code    string
	}{
		{"unsupported type", struct{}{}, CodeInvalidPayload},
		{"empty post", FacebookPublishPayload{}, CodeInvalidPayload},
		{"photo without image", FacebookPublishPayload{Kind: FacebookKindPhoto, Message: "x"}, CodeInvalidPayload},
		{"reel without video", FacebookPublishPayload{Kind: FacebookKindReel, Message: "x"}, CodeInvalidPayload},
		{"reel too long", FacebookPublishPayload{Kind: FacebookKindReel, VideoURL: "v", DurationSec: 91}, CodeInvalidPayload},
		{"reel ratio", FacebookPublishPayload{Kind: FacebookKindReel, VideoURL: "v", AspectRatio: "16:9"}, CodeInvalidPayload},
		{"unknown kind", FacebookPublishPayload{Kind: "story", Message: "x"}, CodeUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapter.Publish(context.Background(), tc.payload)
			if got := asAdapterError(t, err).Code; got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}

	unbound := NewFacebookAdapter(client)
	_, err := unbound.Publish(context.Background(), FacebookPublishPayload{Message: "x"})
	if asAdapterError(t, err).Code != CodeCredentialInvalid {
		t.Fatalf("unbound publish code = %v", err)
	}
}

func TestFacebookFetchMetricsMapsUnifiedKeys(t *testing.T) {
	client := newFakeFacebookClient()
	adapter := newBoundFacebookAdapter(t, client)

	metrics, err := adapter.FetchMetrics(context.Background(), "2026-11-01", "2026-11-07")
	if err != nil {
		t.Fatalf("fetch metrics: %v", err)
	}
	want := map[string]float64{
		MetricViews:     5000,
		MetricLikes:     120,
		MetricComments:  30,
		MetricShares:    8,
		MetricFollowers: 2500,
	}
	for key, value := range want {
		if got, ok := metrics[key]; !ok || got != value {
			t.Errorf("metric %s = %v (present %v), want %v", key, got, ok, value)
		}
	}
	if client.pageMetricsArg.pageID != "page-1" || client.postMetricsArg.pageID != "page-1" {
		t.Errorf("metrics page ids = %+v / %+v", client.pageMetricsArg, client.postMetricsArg)
	}
	if client.pageMetricsArg.since == "" || client.pageMetricsArg.until == "" {
		t.Errorf("metrics range was not converted to timestamps: %+v", client.pageMetricsArg)
	}

	if _, err := adapter.FetchMetrics(context.Background(), "2026-11-07", "2026-11-01"); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("reversed range code = %v", err)
	}
	if _, err := adapter.FetchMetrics(context.Background(), "not-a-date", "2026-11-07"); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("bad date code = %v", err)
	}
}

func TestFacebookMetricsErrorMapping(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		code      string
		retryable bool
	}{
		{"invalid token", &FacebookAPIError{Code: 190}, CodeCredentialInvalid, false},
		{"app rate limit", &FacebookAPIError{Code: 4}, CodeRateLimited, true},
		{"user rate limit", &FacebookAPIError{Code: 17}, CodeRateLimited, true},
		{"page rate limit", &FacebookAPIError{Code: 32}, CodeRateLimited, true},
		{"invalid parameter", &FacebookAPIError{Code: 100}, CodeInvalidPayload, false},
		{"unknown graph code", &FacebookAPIError{Code: 999}, "facebook_999", false},
		{"transport", errors.New("connection reset"), CodeTransport, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeFacebookClient()
			client.pageMetricsErr = tc.err
			adapter := newBoundFacebookAdapter(t, client)

			_, err := adapter.FetchMetrics(context.Background(), "2026-11-01", "2026-11-07")
			adapterErr := asAdapterError(t, err)
			if adapterErr.Code != tc.code || adapterErr.Retryable != tc.retryable {
				t.Fatalf("error = %+v, want code %q retryable %v", adapterErr, tc.code, tc.retryable)
			}
		})
	}
}

func TestFacebookListEngagementsAndReply(t *testing.T) {
	client := newFakeFacebookClient()
	adapter := newBoundFacebookAdapter(t, client)

	engagements, err := adapter.ListEngagements(context.Background(), "cursor-1")
	if err != nil {
		t.Fatalf("list engagements: %v", err)
	}
	if len(engagements) != 1 || engagements[0].ExtID != "c-1" || engagements[0].Kind != "comment" || engagements[0].Author != "Alice" {
		t.Fatalf("engagements = %+v", engagements)
	}
	if client.commentsPageID != "page-1" || client.commentsCursor != "cursor-1" {
		t.Fatalf("comments args = %s / %s", client.commentsPageID, client.commentsCursor)
	}

	receipt, err := adapter.Reply(context.Background(), "c-1", " Thanks! ")
	if err != nil {
		t.Fatalf("reply comment: %v", err)
	}
	if receipt.ExternalID != "reply-1" || client.replyID != "c-1" || client.replyText != "Thanks!" {
		t.Fatalf("comment reply = %+v / %s / %q", receipt, client.replyID, client.replyText)
	}

	dm, err := adapter.Reply(context.Background(), "dm:psid-9", "Hello in DM")
	if err != nil {
		t.Fatalf("reply dm: %v", err)
	}
	if dm.ExternalID != "m_1" || client.dmRecipient != "psid-9" || client.dmPageID != "page-1" {
		t.Fatalf("dm reply = %+v / %s / %s", dm, client.dmPageID, client.dmRecipient)
	}

	if _, err := adapter.Reply(context.Background(), "", "x"); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("empty id code = %v", err)
	}
	if _, err := adapter.Reply(context.Background(), "dm:", "x"); asAdapterError(t, err).Code != CodeInvalidPayload {
		t.Fatalf("empty dm recipient code = %v", err)
	}
}

// mockGraphAPI is a minimal Graph API stand-in. It is a real HTTP server so the
// E2E test exercises GraphHTTPClient's request building, auth injection and
// error parsing, not just the adapter.
type mockGraphAPI struct {
	mu       sync.Mutex
	requests []mockGraphRequest
	errs     map[string]mockGraphError
}

type mockGraphRequest struct {
	Method string
	Path   string
	Query  url.Values
	Form   url.Values
	Auth   string
}

type mockGraphError struct {
	StatusCode int
	Code       int
	Message    string
}

func newMockGraphAPI() *mockGraphAPI {
	return &mockGraphAPI{errs: make(map[string]mockGraphError)}
}

func (m *mockGraphAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.requests = append(m.requests, mockGraphRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Form:   r.PostForm,
		Auth:   r.Header.Get("Authorization"),
	})
	m.mu.Unlock()

	key := r.Method + " " + r.URL.Path
	m.mu.Lock()
	mockErr, hasErr := m.errs[key]
	m.mu.Unlock()
	if hasErr {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(mockErr.StatusCode)
		fmt.Fprintf(w, `{"error":{"message":%q,"type":"OAuthException","code":%d}}`, mockErr.Message, mockErr.Code)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	switch key {
	case "GET /v21.0/me":
		fmt.Fprint(w, `{"id":"page-1","name":"Test Page"}`)
	case "GET /v21.0/me/permissions":
		fmt.Fprint(w, `{"data":[
			{"permission":"pages_manage_posts","status":"granted"},
			{"permission":"pages_read_engagement","status":"granted"}]}`)
	case "POST /v21.0/page-1/feed":
		fmt.Fprint(w, `{"id":"page-1_post-1"}`)
	case "POST /v21.0/page-1/photos":
		fmt.Fprint(w, `{"id":"page-1_photo-1"}`)
	case "POST /v21.0/page-1/video_reels":
		fmt.Fprint(w, `{"video_id":"video-1","permalink_url":"https://www.facebook.com/reel/video-1"}`)
	case "GET /v21.0/page-1/insights":
		fmt.Fprint(w, `{"data":[{"name":"page_fans","period":"day","values":[{"value":2400},{"value":2500}]}]}`)
	case "GET /v21.0/page-1/posts":
		fmt.Fprint(w, `{"data":[{"id":"post-1"},{"id":"post-2"}]}`)
	case "GET /v21.0/post-1/insights":
		fmt.Fprint(w, `{"data":[
			{"name":"post_impressions","values":[{"value":3000}]},
			{"name":"post_reactions_by_type_total","values":[{"value":{"like":80,"love":10,"wow":5}}]},
			{"name":"post_comments","values":[{"value":20}]},
			{"name":"post_shares","values":[{"value":5}]}]}`)
	case "GET /v21.0/post-2/insights":
		fmt.Fprint(w, `{"data":[
			{"name":"post_impressions","values":[{"value":2000}]},
			{"name":"post_reactions_by_type_total","values":[{"value":{"like":20,"haha":5}}]},
			{"name":"post_comments","values":[{"value":10}]},
			{"name":"post_shares","values":[{"value":3}]}]}`)
	case "GET /v21.0/page-1/comments":
		fmt.Fprint(w, `{"data":[{"id":"c-1","from":{"id":"u-1","name":"Alice"},"message":"Great post"}],
			"paging":{"cursors":{"after":"cursor-2"},"next":"https://graph.facebook.com/v21.0/page-1/comments?after=cursor-2"}}`)
	case "POST /v21.0/c-1/comments":
		fmt.Fprint(w, `{"id":"reply-1"}`)
	case "POST /v21.0/page-1/messages":
		fmt.Fprint(w, `{"message_id":"m_1"}`)
	default:
		http.NotFound(w, r)
	}
}

func (m *mockGraphAPI) setErr(method, path string, statusCode, code int, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errs[method+" "+path] = mockGraphError{StatusCode: statusCode, Code: code, Message: message}
}

func (m *mockGraphAPI) clearErr(method, path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.errs, method+" "+path)
}

func (m *mockGraphAPI) request(method, path string) (mockGraphRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.requests) - 1; i >= 0; i-- {
		if m.requests[i].Method == method && m.requests[i].Path == path {
			return m.requests[i], true
		}
	}
	return mockGraphRequest{}, false
}

func (m *mockGraphAPI) snapshot() []mockGraphRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]mockGraphRequest, len(m.requests))
	copy(out, m.requests)
	return out
}

func TestFacebookE2EMockGraphAPI(t *testing.T) {
	mock := newMockGraphAPI()
	server := httptest.NewServer(mock)
	defer server.Close()

	client := &GraphHTTPClient{
		BaseURL:    server.URL + "/v21.0",
		PageToken:  "page-token",
		AppSecret:  "app-secret",
		HTTPClient: server.Client(),
	}
	adapter := NewFacebookAdapter(client)
	ctx := context.Background()

	// Session: token + permissions.
	if err := adapter.EnsureSession(ctx, "page-1"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}

	// Publish a Page post and receive the permalink.
	receipt, err := adapter.Publish(ctx, FacebookPublishPayload{Message: "Hello Facebook"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if receipt.ExternalID != "page-1_post-1" || receipt.URL != "https://www.facebook.com/page-1_post-1" {
		t.Fatalf("receipt = %+v", receipt)
	}

	// Metrics: page gauge + post totals fold into the unified keys.
	metrics, err := adapter.FetchMetrics(ctx, "2026-11-01", "2026-11-07")
	if err != nil {
		t.Fatalf("fetch metrics: %v", err)
	}
	want := map[string]float64{
		MetricViews:     5000,
		MetricLikes:     120,
		MetricComments:  30,
		MetricShares:    8,
		MetricFollowers: 2500,
	}
	for key, value := range want {
		if got := metrics[key]; got != value {
			t.Errorf("metric %s = %v, want %v", key, got, value)
		}
	}

	// Comments pull.
	engagements, err := adapter.ListEngagements(ctx, "")
	if err != nil {
		t.Fatalf("list engagements: %v", err)
	}
	if len(engagements) != 1 || engagements[0].ExtID != "c-1" || engagements[0].Text != "Great post" {
		t.Fatalf("engagements = %+v", engagements)
	}

	// Operator-approved comment reply.
	if reply, err := adapter.Reply(ctx, "c-1", "Thanks!"); err != nil {
		t.Fatalf("reply comment: %v", err)
	} else if reply.ExternalID != "reply-1" {
		t.Fatalf("comment reply receipt = %+v", reply)
	}

	// Operator-approved DM.
	if dm, err := adapter.Reply(ctx, "dm:psid-9", "Thanks for reaching out"); err != nil {
		t.Fatalf("reply dm: %v", err)
	} else if dm.ExternalID != "m_1" {
		t.Fatalf("dm receipt = %+v", dm)
	}

	// Auth never rides in a URL: GET carries the token in the Authorization
	// header, POST carries it in the form body. appsecret_proof accompanies it.
	for _, request := range mock.snapshot() {
		if request.Query.Get("access_token") != "" {
			t.Errorf("%s %s leaked access_token into the query string", request.Method, request.Path)
		}
		if request.Method == http.MethodGet {
			if request.Auth != "Bearer page-token" {
				t.Errorf("%s %s has Authorization %q", request.Method, request.Path, request.Auth)
			}
			if request.Query.Get("appsecret_proof") == "" {
				t.Errorf("%s %s has no appsecret_proof", request.Method, request.Path)
			}
			continue
		}
		if request.Form.Get("access_token") != "page-token" {
			t.Errorf("%s %s has form access_token %q", request.Method, request.Path, request.Form.Get("access_token"))
		}
		if request.Form.Get("appsecret_proof") == "" {
			t.Errorf("%s %s has no form appsecret_proof", request.Method, request.Path)
		}
	}

	feed, ok := mock.request(http.MethodPost, "/v21.0/page-1/feed")
	if !ok || feed.Form.Get("message") != "Hello Facebook" {
		t.Fatalf("feed form = %+v (found %v)", feed.Form, ok)
	}
	comment, ok := mock.request(http.MethodPost, "/v21.0/c-1/comments")
	if !ok || comment.Form.Get("message") != "Thanks!" {
		t.Fatalf("comment reply form = %+v (found %v)", comment.Form, ok)
	}
	dm, ok := mock.request(http.MethodPost, "/v21.0/page-1/messages")
	if !ok || !strings.Contains(dm.Form.Get("recipient"), "psid-9") || !strings.Contains(dm.Form.Get("message"), "Thanks for reaching out") {
		t.Fatalf("dm form = %+v (found %v)", dm.Form, ok)
	}
}

func TestFacebookGraphAPIClientScheduledPost(t *testing.T) {
	mock := newMockGraphAPI()
	server := httptest.NewServer(mock)
	defer server.Close()

	at := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	client := &GraphHTTPClient{BaseURL: server.URL + "/v21.0", PageToken: "page-token", HTTPClient: server.Client()}
	receipt, err := client.CreatePost(context.Background(), FacebookPostRequest{
		PageID:    "page-1",
		Kind:      FacebookKindPost,
		Message:   "Scheduled",
		PublishAt: &at,
	})
	if err != nil {
		t.Fatalf("create scheduled post: %v", err)
	}
	if receipt.ExternalID != "page-1_post-1" {
		t.Fatalf("receipt = %+v", receipt)
	}

	feed, ok := mock.request(http.MethodPost, "/v21.0/page-1/feed")
	if !ok {
		t.Fatal("feed request was not made")
	}
	if feed.Form.Get("published") != "false" {
		t.Errorf("published = %q, want false", feed.Form.Get("published"))
	}
	if got := feed.Form.Get("scheduled_publish_time"); got != fmt.Sprint(at.Unix()) {
		t.Errorf("scheduled_publish_time = %q, want %d", got, at.Unix())
	}
}

func TestFacebookGraphAPIClientErrorMapping(t *testing.T) {
	mock := newMockGraphAPI()
	server := httptest.NewServer(mock)
	defer server.Close()
	client := &GraphHTTPClient{BaseURL: server.URL + "/v21.0", PageToken: "bad-token", HTTPClient: server.Client()}

	mock.setErr(http.MethodGet, "/v21.0/me", http.StatusBadRequest, 190, "Invalid OAuth access token.")
	adapter := NewFacebookAdapter(client)
	err := adapter.EnsureSession(context.Background(), "page-1")
	adapterErr := asAdapterError(t, err)
	if adapterErr.Code != CodeCredentialInvalid || adapterErr.Retryable {
		t.Fatalf("invalid token = %+v", adapterErr)
	}

	mock.setErr(http.MethodGet, "/v21.0/page-1/insights", http.StatusBadRequest, 4, "Application request limit reached")
	mock.clearErr(http.MethodGet, "/v21.0/me")
	if err := adapter.EnsureSession(context.Background(), "page-1"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	_, err = adapter.FetchMetrics(context.Background(), "2026-11-01", "2026-11-07")
	adapterErr = asAdapterError(t, err)
	if adapterErr.Code != CodeRateLimited || !adapterErr.Retryable {
		t.Fatalf("rate limit = %+v", adapterErr)
	}
}
