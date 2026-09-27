package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testMP4 is a stand-in for a provider-produced video.
var testMP4 = []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}

func TestSeedanceLifecycle(t *testing.T) {
	var base string
	var polls int32
	var submitBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/contents/generations/tasks":
			if r.Header.Get("Authorization") != "Bearer ark-key" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &submitBody)
			_, _ = w.Write([]byte(`{"id":"task-123","model":"m"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/contents/generations/tasks/task-123":
			n := atomic.AddInt32(&polls, 1)
			if n < 3 {
				_, _ = w.Write([]byte(`{"id":"task-123","status":"running"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"task-123","status":"succeeded","content":{"video_url":"` + base + `/v/task-123.mp4"}}`))
		case r.URL.Path == "/v/task-123.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(testMP4)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	base = srv.URL

	b := &SeedanceBackend{baseURL: srv.URL + "/", apiKey: "ark-key", model: defaultSeedanceModel, client: srv.Client(), timeout: 5 * time.Second}
	handle, err := b.Submit(context.Background(), VideoRequest{
		FirstFrame: testPNG, FirstFrameName: "frame.png",
		Prompt: "镜头缓慢推近", DurationSec: 5, AspectRatio: "16:9",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if handle.TaskID != "task-123" || handle.Backend != BackendSeedance {
		t.Errorf("handle = %+v", handle)
	}
	if len(handle.Raw) == 0 {
		t.Error("handle.Raw 应保留提交响应原文")
	}
	// prompt 携带 CLI 风格参数；首帧以 data URL 传入。
	content, _ := submitBody["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content 段数 = %d, want 2", len(content))
	}
	text0, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text0, "镜头缓慢推近") || !strings.Contains(text0, "--dur 5") || !strings.Contains(text0, "--ratio 16:9") {
		t.Errorf("text 段 = %q", text0)
	}
	img1, _ := content[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(img1, "data:image/png;base64,") {
		t.Errorf("首帧 data URL = %.40q", img1)
	}

	// 轮询：running → running → succeeded
	if st, err := b.Poll(context.Background(), handle); err != nil || st.State != TaskRunning {
		t.Fatalf("poll#1 = %+v, %v", st, err)
	}
	if st, err := b.Poll(context.Background(), handle); err != nil || st.State != TaskRunning {
		t.Fatalf("poll#2 = %+v, %v", st, err)
	}
	st, err := b.Poll(context.Background(), handle)
	if err != nil || st.State != TaskSucceeded {
		t.Fatalf("poll#3 = %+v, %v", st, err)
	}
	if st.RawStatus != "succeeded" {
		t.Errorf("RawStatus = %q（应保留供应商原词）", st.RawStatus)
	}
	cands, err := b.Fetch(context.Background(), handle)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(cands) != 1 || string(cands[0].Data) != string(testMP4) || cands[0].Ext != "mp4" {
		t.Errorf("候选不符: len=%d ext=%q", len(cands[0].Data), cands[0].Ext)
	}
}

func TestSeedanceFailureKeepsProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"task-x"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"task-x","status":"failed","error":{"code":"QuotaExceeded","message":"余额不足"}}`))
	}))
	defer srv.Close()

	b := &SeedanceBackend{baseURL: srv.URL, model: defaultSeedanceModel, client: srv.Client(), timeout: 5 * time.Second}
	handle, err := b.Submit(context.Background(), VideoRequest{FirstFrame: testPNG, Prompt: "p", DurationSec: 5})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	st, err := b.Poll(context.Background(), handle)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if st.State != TaskFailed {
		t.Fatalf("state = %v, want failed", st.State)
	}
	if !strings.Contains(st.Err, "余额不足") || !strings.Contains(st.Err, "QuotaExceeded") {
		t.Errorf("错误原文被改写: %q", st.Err)
	}
	if _, err := b.Fetch(context.Background(), handle); err == nil || !strings.Contains(err.Error(), "尚未成功") {
		t.Errorf("未成功取片应报错，实得：%v", err)
	}
}

func TestSeedanceSubmitErrorPaths(t *testing.T) {
	t.Run("无首帧图", func(t *testing.T) {
		b := &SeedanceBackend{baseURL: "http://127.0.0.1:1", client: &http.Client{}, timeout: time.Second}
		if _, err := b.Submit(context.Background(), VideoRequest{Prompt: "p"}); err == nil {
			t.Fatal("缺首帧图应报错")
		}
	})

	t.Run("非 2xx 带响应体", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
		}))
		defer srv.Close()
		b := &SeedanceBackend{baseURL: srv.URL, model: "m", client: srv.Client(), timeout: 5 * time.Second}
		_, err := b.Submit(context.Background(), VideoRequest{FirstFrame: testPNG, Prompt: "p"})
		if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "invalid api key") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("响应缺 id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"model":"m"}`))
		}))
		defer srv.Close()
		b := &SeedanceBackend{baseURL: srv.URL, model: "m", client: srv.Client(), timeout: 5 * time.Second}
		_, err := b.Submit(context.Background(), VideoRequest{FirstFrame: testPNG, Prompt: "p"})
		if err == nil || !strings.Contains(err.Error(), "缺少任务 id") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("超时", func(t *testing.T) {
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		}))
		defer slow.Close()
		b := &SeedanceBackend{baseURL: slow.URL, model: "m", client: slow.Client(), timeout: 50 * time.Millisecond}
		_, err := b.Submit(context.Background(), VideoRequest{FirstFrame: testPNG, Prompt: "p"})
		if err == nil || !strings.Contains(err.Error(), "超时") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestKlingLifecycle(t *testing.T) {
	var base string
	var submitBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos/image2video":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &submitBody)
			_, _ = w.Write([]byte(`{"code":0,"message":"SUCCEED","data":{"task_id":"k-9","task_status":"submitted"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/image2video/k-9":
			_, _ = w.Write([]byte(`{"code":0,"data":{"task_id":"k-9","task_status":"succeed","task_result":{"videos":[{"id":"v1","url":"` + base + `/k-9.mp4","duration":"5"}]}}}`))
		case r.URL.Path == "/k-9.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(testMP4)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	base = srv.URL

	b := &KlingBackend{baseURL: srv.URL, accessKey: "ak", secretKey: "sk", model: defaultKlingModel, client: srv.Client(), timeout: 5 * time.Second}
	handle, err := b.Submit(context.Background(), VideoRequest{
		FirstFrame: testPNG, FirstFrameName: "f.jpg", Prompt: "轻微摇镜", DurationSec: 9, AspectRatio: "9:16",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if handle.TaskID != "k-9" || handle.Backend != BackendKling {
		t.Errorf("handle = %+v", handle)
	}
	if !strings.HasPrefix(gotAuth, "Bearer eyJ") {
		t.Errorf("Authorization 应为 JWT（Bearer eyJ…），实得 %q", gotAuth)
	}
	if submitBody["duration"] != "10" {
		t.Errorf("duration = %v, want 10（9 秒向上取到 10）", submitBody["duration"])
	}
	if submitBody["image"] != base64.StdEncoding.EncodeToString(testPNG) {
		t.Error("首帧图应为 base64")
	}
	if submitBody["aspect_ratio"] != "9:16" || submitBody["model_name"] != defaultKlingModel {
		t.Errorf("请求体不符: %v", submitBody)
	}

	st, err := b.Poll(context.Background(), handle)
	if err != nil || st.State != TaskSucceeded {
		t.Fatalf("Poll = %+v, %v", st, err)
	}
	cands, err := b.Fetch(context.Background(), handle)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(cands) != 1 || string(cands[0].Data) != string(testMP4) {
		t.Errorf("候选不符: %d 字节", len(cands[0].Data))
	}
}

func TestKlingDurationRoundingAndFailure(t *testing.T) {
	var submitBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &submitBody)
			_, _ = w.Write([]byte(`{"code":0,"data":{"task_id":"k-1","task_status":"submitted"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"task_id":"k-1","task_status":"failed","task_status_msg":"prompt 含违规内容"}}`))
	}))
	defer srv.Close()

	b := &KlingBackend{baseURL: srv.URL, accessKey: "ak", secretKey: "sk", model: "kling-v1", client: srv.Client(), timeout: 5 * time.Second}
	handle, err := b.Submit(context.Background(), VideoRequest{FirstFrame: testPNG, Prompt: "p", DurationSec: 3})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if submitBody["duration"] != "5" {
		t.Errorf("duration = %v, want 5（小于 7.5 取 5）", submitBody["duration"])
	}
	st, err := b.Poll(context.Background(), handle)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if st.State != TaskFailed || !strings.Contains(st.Err, "违规内容") {
		t.Errorf("失败状态/错误原文不符: %+v", st)
	}
}

func TestKlingRejectsBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":1001,"message":"authorization failed"}`))
	}))
	defer srv.Close()
	b := &KlingBackend{baseURL: srv.URL, accessKey: "ak", secretKey: "sk", model: "m", client: srv.Client(), timeout: 5 * time.Second}
	_, err := b.Submit(context.Background(), VideoRequest{FirstFrame: testPNG, Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "code=1001") || !strings.Contains(err.Error(), "authorization failed") {
		t.Errorf("err = %v", err)
	}
}

func TestKlingJWTShape(t *testing.T) {
	tok, err := klingJWT("my-ak", "my-sk", time.Now())
	if err != nil {
		t.Fatalf("klingJWT: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT 段数 = %d, want 3（%q）", len(parts), tok)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("payload 解码失败: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("claims 解析失败: %v", err)
	}
	if claims["iss"] != "my-ak" {
		t.Errorf("iss = %v, want my-ak", claims["iss"])
	}
	if _, ok := claims["exp"]; !ok {
		t.Error("claims 缺少 exp")
	}
	// 定时间戳签发是确定性的：同输入 → 同 token（无随机数）。
	at := time.Unix(1700000000, 0)
	tokA, _ := klingJWT("my-ak", "my-sk", at)
	tokB, _ := klingJWT("my-ak", "my-sk", at)
	if tokA != tokB {
		t.Error("同一时间戳签发的 JWT 应完全一致")
	}
	if tokA == tok {
		t.Error("不同时间戳签发的 JWT 不应相同（exp 参与签名）")
	}
}
