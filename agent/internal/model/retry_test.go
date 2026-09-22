package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 429/5xx 指数退避重试：前两次 503，第三次成功——调用方看到成功且
// server 恰好被请求 3 次。
func TestRetryRetries5xxThenSucceeds(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.Header().Set("retry-after", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"overloaded"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer ts.Close()

	m := NewAnthropic(Config{BaseURL: ts.URL, APIKey: "k", Model: "m", Retry: RetryPolicy{Max: 3, Base: time.Millisecond, MaxWait: 2 * time.Millisecond}})
	resp, err := m.Complete(context.Background(), Request{
		Messages:  []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
		MaxTokens: 64,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Message.Text() != "ok" {
		t.Fatalf("resp = %+v", resp)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("server calls = %d, want 3", got)
	}
}

// 4xx（除 429）不重试——语义错误重试无意义。
func TestRetryDoesNotRetry4xx(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer ts.Close()

	m := NewAnthropic(Config{BaseURL: ts.URL, APIKey: "k", Model: "m", Retry: RetryPolicy{Max: 3, Base: time.Millisecond}})
	_, err := m.Complete(context.Background(), Request{
		Messages:  []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
		MaxTokens: 64,
	})
	if err == nil {
		t.Fatal("want error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("4xx retried: %d calls, want 1", got)
	}
}

// 尊重 Retry-After 头（0 值立即可重试；此处只验证不 panic 且重试发生）。
func TestRetryRespectsRetryAfter(t *testing.T) {
	var calls int32
	var sawRetryAfter int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("retry-after", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		atomic.StoreInt32(&sawRetryAfter, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]string{"type": "text", "text": "ok"}}, "stop_reason": "end_turn", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer ts.Close()

	m := NewAnthropic(Config{BaseURL: ts.URL, APIKey: "k", Model: "m", Retry: RetryPolicy{Max: 2, Base: time.Millisecond}})
	if _, err := m.Complete(context.Background(), Request{
		Messages:  []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "hi"}}}},
		MaxTokens: 64,
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if atomic.LoadInt32(&sawRetryAfter) != 1 {
		t.Error("retry after 429 did not happen")
	}
}
