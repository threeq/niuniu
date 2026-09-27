package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testPNG is a tiny binary blob standing in for a generated image.
var testPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}

func TestOpenAICompatTTSSuccess(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3fake-mp3-bytes"))
	}))
	defer srv.Close()

	adapter := &OpenAICompatTTS{baseURL: srv.URL + "/", apiKey: "sk-tts", model: "tts-1", client: srv.Client(), timeout: 5 * time.Second}
	res, err := adapter.Synthesize(context.Background(), TTSRequest{Text: "你好世界", Voice: "nova", Speed: 1.25})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if gotPath != "/v1/audio/speech" {
		t.Errorf("path = %q, want /v1/audio/speech", gotPath)
	}
	if gotAuth != "Bearer sk-tts" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["input"] != "你好世界" || gotBody["voice"] != "nova" || gotBody["model"] != "tts-1" {
		t.Errorf("请求体不符: %v", gotBody)
	}
	if gotBody["response_format"] != "mp3" {
		t.Errorf("response_format = %v, want mp3", gotBody["response_format"])
	}
	if gotBody["speed"] != 1.25 {
		t.Errorf("speed = %v, want 1.25", gotBody["speed"])
	}
	if string(res.Audio) != "ID3fake-mp3-bytes" || res.ContentType != "audio/mpeg" {
		t.Errorf("结果不符: %q / %q", res.Audio, res.ContentType)
	}
}

func TestOpenAICompatTTSDefaultsVoice(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	adapter := &OpenAICompatTTS{baseURL: srv.URL, model: "tts-1", client: srv.Client(), timeout: 5 * time.Second}
	if _, err := adapter.Synthesize(context.Background(), TTSRequest{Text: "hi"}); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if gotBody["voice"] != "alloy" {
		t.Errorf("voice = %v, want alloy（后端默认）", gotBody["voice"])
	}
	if _, ok := gotBody["speed"]; ok {
		t.Error("speed <= 0 时不应出现在请求体里")
	}
}

func TestOpenAICompatTTSErrorPaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// baseURL 会与端点路径拼接（srv.URL + "/fail" → /fail/v1/audio/speech），
		// 所以按前缀分流。
		if strings.HasPrefix(r.URL.Path, "/fail") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		_, _ = w.Write(nil) // 200 但空响应体
	}))
	defer srv.Close()

	t.Run("非 2xx 带状态码与响应体预览", func(t *testing.T) {
		adapter := &OpenAICompatTTS{baseURL: srv.URL + "/fail", model: "m", client: srv.Client(), timeout: 5 * time.Second}
		_, err := adapter.Synthesize(context.Background(), TTSRequest{Text: "hi"})
		if err == nil {
			t.Fatal("401 应报错")
		}
		if !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "invalid api key") {
			t.Errorf("错误信息应含状态码与响应体，实得：%v", err)
		}
	})

	t.Run("空文本", func(t *testing.T) {
		adapter := &OpenAICompatTTS{baseURL: srv.URL, model: "m", client: srv.Client(), timeout: 5 * time.Second}
		if _, err := adapter.Synthesize(context.Background(), TTSRequest{Text: "  "}); err == nil {
			t.Fatal("空文本应报错")
		}
	})

	t.Run("空音频响应", func(t *testing.T) {
		adapter := &OpenAICompatTTS{baseURL: srv.URL + "/empty", model: "m", client: srv.Client(), timeout: 5 * time.Second}
		_, err := adapter.Synthesize(context.Background(), TTSRequest{Text: "hi"})
		if err == nil || !strings.Contains(err.Error(), "空音频") {
			t.Errorf("空响应体应报错，实得：%v", err)
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
		adapter := &OpenAICompatTTS{baseURL: slow.URL, model: "m", client: slow.Client(), timeout: 50 * time.Millisecond}
		_, err := adapter.Synthesize(context.Background(), TTSRequest{Text: "hi"})
		if err == nil || !strings.Contains(err.Error(), "超时") {
			t.Errorf("超时应报「超时」，实得：%v", err)
		}
	})
}

func TestOpenAICompatImageB64AndURL(t *testing.T) {
	var base string // set right after the server starts (the handler needs its own URL)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/images/generations":
			var body map[string]any
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			if body["response_format"] != "b64_json" {
				t.Errorf("response_format = %v, want b64_json", body["response_format"])
			}
			if body["n"] != float64(2) {
				t.Errorf("n = %v, want 2", body["n"])
			}
			if body["size"] != "1536x1024" {
				t.Errorf("size = %v, want 1536x1024", body["size"])
			}
			resp := map[string]any{"data": []any{
				map[string]any{"b64_json": base64.StdEncoding.EncodeToString(testPNG)},
				map[string]any{"url": base + "/img/2.png"},
			}}
			_ = json.NewEncoder(w).Encode(resp)
		case "/img/2.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNG)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	base = srv.URL

	adapter := &OpenAICompatImage{baseURL: srv.URL, apiKey: "sk-img", model: "gpt-image-1", client: srv.Client(), timeout: 5 * time.Second}
	cands, err := adapter.Generate(context.Background(), ImageRequest{Prompt: "一只猫", N: 2, Size: "1536x1024"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("候选数 = %d, want 2", len(cands))
	}
	if string(cands[0].Data) != string(testPNG) || cands[0].Ext != "png" {
		t.Errorf("b64 候选不符: %v / %q", len(cands[0].Data), cands[0].Ext)
	}
	if string(cands[1].Data) != string(testPNG) || cands[1].URL == "" {
		t.Errorf("url 候选未下载成功: %v / %q", len(cands[1].Data), cands[1].URL)
	}
}

func TestOpenAICompatImageWithReferenceUsesEdits(t *testing.T) {
	var gotPath, gotCT, gotFilename string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Errorf("解析 multipart 失败: %v", err)
		}
		if f, hdr, err := r.FormFile("image"); err == nil {
			gotFilename = hdr.Filename
			data, _ := io.ReadAll(f)
			if string(data) != string(testPNG) {
				t.Errorf("参考图字节不符: %v", len(data))
			}
		} else {
			t.Errorf("缺少 image 表单字段: %v", err)
		}
		if r.FormValue("prompt") != "参考这只猫画一张新图" {
			t.Errorf("prompt = %q", r.FormValue("prompt"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"b64_json": base64.StdEncoding.EncodeToString(testPNG)},
		}})
	}))
	defer srv.Close()

	adapter := &OpenAICompatImage{baseURL: srv.URL, model: "gpt-image-1", client: srv.Client(), timeout: 5 * time.Second}
	cands, err := adapter.Generate(context.Background(), ImageRequest{
		Prompt: "参考这只猫画一张新图", N: 1,
		ReferenceImage: testPNG, ReferenceImageName: "ref.png",
	})
	if err != nil {
		t.Fatalf("Generate(参考图): %v", err)
	}
	if gotPath != "/v1/images/edits" {
		t.Errorf("path = %q, want /v1/images/edits", gotPath)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want multipart", gotCT)
	}
	if gotFilename != "ref.png" {
		t.Errorf("参考图文件名 = %q", gotFilename)
	}
	if len(cands) != 1 {
		t.Fatalf("候选数 = %d, want 1", len(cands))
	}
}

func TestOpenAICompatImageErrorPaths(t *testing.T) {
	t.Run("供应商错误对象", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "content policy violation", "type": "invalid_request_error"},
			})
		}))
		defer srv.Close()
		adapter := &OpenAICompatImage{baseURL: srv.URL, model: "m", client: srv.Client(), timeout: 5 * time.Second}
		_, err := adapter.Generate(context.Background(), ImageRequest{Prompt: "x", N: 1})
		if err == nil || !strings.Contains(err.Error(), "content policy violation") {
			t.Errorf("应转述供应商错误原文，实得：%v", err)
		}
	})

	t.Run("非 2xx", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
		}))
		defer srv.Close()
		adapter := &OpenAICompatImage{baseURL: srv.URL, model: "m", client: srv.Client(), timeout: 5 * time.Second}
		_, err := adapter.Generate(context.Background(), ImageRequest{Prompt: "x", N: 1})
		if err == nil || !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "rate limited") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("空 prompt", func(t *testing.T) {
		adapter := &OpenAICompatImage{baseURL: "http://127.0.0.1:1", model: "m", client: &http.Client{}, timeout: time.Second}
		if _, err := adapter.Generate(context.Background(), ImageRequest{Prompt: "  "}); err == nil {
			t.Fatal("空 prompt 应报错（不该发出请求）")
		}
	})
}
