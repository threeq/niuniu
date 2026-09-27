package main

// openai-compat adapter — the protocol baseline (design §7.3): any vendor that
// speaks the OpenAI TTS / Images wire format works through this one adapter.
//
//	TTS:    POST {base}/v1/audio/speech   (JSON in, audio bytes out)
//	Image:  POST {base}/v1/images/generations  (JSON in, b64_json or url out)
//	        POST {base}/v1/images/edits        (multipart, when a reference
//	                                            image is supplied)
//
// The API key comes from NN_CAP_<CAP>_API_KEY and is only ever read here —
// it is never written to artifacts, quotes, task records or logs.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// OpenAICompatTTS implements TTSBackend against /v1/audio/speech.
type OpenAICompatTTS struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
	timeout time.Duration
}

// Synthesize calls the OpenAI-compatible speech endpoint and returns the audio
// bytes. The response is binary, so it goes through doRaw and the status is
// checked explicitly.
func (b *OpenAICompatTTS) Synthesize(ctx context.Context, req TTSRequest) (TTSResult, error) {
	if strings.TrimSpace(req.Text) == "" {
		return TTSResult{}, fmt.Errorf("TTS 文本为空：tts_generate 需要非空 text")
	}
	voice := strings.TrimSpace(req.Voice)
	if voice == "" {
		voice = "alloy"
	}
	payload := map[string]any{
		"model":           b.model,
		"input":           req.Text,
		"voice":           voice,
		"response_format": "mp3",
	}
	if req.Speed > 0 {
		payload["speed"] = req.Speed
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return TTSResult{}, fmt.Errorf("序列化 TTS 请求失败: %w", err)
	}
	url := joinURL(b.baseURL, "/v1/audio/speech")
	headers := bearer(b.apiKey)
	headers["Content-Type"] = "application/json"
	resp, data, err := doRaw(ctx, b.client, http.MethodPost, url, headers, bytes.NewReader(body), b.timeout)
	if err != nil {
		return TTSResult{}, fmt.Errorf("TTS（openai-compat）调用失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TTSResult{}, fmt.Errorf("TTS（openai-compat）%s 返回 HTTP %d：%s（请检查能力配置中的 API Key / 模型名 / 配额）",
			url, resp.StatusCode, previewBody(data))
	}
	if len(data) == 0 {
		return TTSResult{}, fmt.Errorf("TTS（openai-compat）%s 返回空音频（0 字节）", url)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.HasPrefix(ct, "application/json") {
		ct = "audio/mpeg"
	}
	return TTSResult{Audio: data, ContentType: ct}, nil
}

// OpenAICompatImage implements ImageBackend against /v1/images/generations and
// /v1/images/edits.
type OpenAICompatImage struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
	timeout time.Duration
}

// imageAPIResponse is the shared response shape of both image endpoints.
type imageAPIResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
		URL     string `json:"url"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Generate produces N image candidates. With a reference image it switches to
// the edits endpoint (multipart); otherwise it uses plain text-to-image.
func (b *OpenAICompatImage) Generate(ctx context.Context, req ImageRequest) ([]ImageCandidate, error) {
	n := req.N
	if n <= 0 {
		n = 1
	}
	if strings.TrimSpace(req.Prompt) == "" && len(req.ReferenceImage) == 0 {
		return nil, fmt.Errorf("图像 prompt 为空：image_generate 需要非空 prompt")
	}
	if len(req.ReferenceImage) > 0 {
		return b.generateWithReference(ctx, req, n)
	}
	payload := map[string]any{
		"model":           b.model,
		"prompt":          req.Prompt,
		"n":               n,
		"response_format": "b64_json",
	}
	if req.Size != "" {
		payload["size"] = req.Size
	}
	url := joinURL(b.baseURL, "/v1/images/generations")
	data, err := doJSON(ctx, b.client, http.MethodPost, url, bearer(b.apiKey), payload, b.timeout)
	if err != nil {
		return nil, fmt.Errorf("图像生成（openai-compat）失败: %w（请检查能力配置中的 API Key / 模型名 / 配额）", err)
	}
	return b.collect(ctx, url, data)
}

// generateWithReference posts the multipart edit request. Vendors that only
// implement /v1/images/generations will answer 404 here — that surfaces as a
// clear error (no silent downgrade, no auto-retry).
func (b *OpenAICompatImage) generateWithReference(ctx context.Context, req ImageRequest, n int) ([]ImageCandidate, error) {
	url := joinURL(b.baseURL, "/v1/images/edits")
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	fields := map[string]string{
		"model":  b.model,
		"prompt": req.Prompt,
		"n":      fmt.Sprintf("%d", n),
	}
	if req.Size != "" {
		fields["size"] = req.Size
	}
	// Deterministic field order keeps the request byte-stable for tests.
	for _, k := range []string{"model", "prompt", "n", "size"} {
		v, ok := fields[k]
		if !ok {
			continue
		}
		if err := w.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("构造多模态请求失败: %w", err)
		}
	}
	name := req.ReferenceImageName
	if name == "" {
		name = "reference.png"
	}
	part, err := w.CreateFormFile("image", name)
	if err != nil {
		return nil, fmt.Errorf("构造参考图表单失败: %w", err)
	}
	if _, err := part.Write(req.ReferenceImage); err != nil {
		return nil, fmt.Errorf("写入参考图失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("关闭多模态请求失败: %w", err)
	}
	headers := bearer(b.apiKey)
	headers["Content-Type"] = w.FormDataContentType()
	resp, data, err := doRaw(ctx, b.client, http.MethodPost, url, headers, bytes.NewReader(buf.Bytes()), b.timeout)
	if err != nil {
		return nil, fmt.Errorf("参考图生成（openai-compat）失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("参考图生成（openai-compat）%s 返回 HTTP %d：%s", url, resp.StatusCode, previewBody(data))
	}
	return b.collect(ctx, url, data)
}

// collect decodes the shared response shape into candidates, downloading any
// url-form entries (some vendors only return URLs).
func (b *OpenAICompatImage) collect(ctx context.Context, url string, data []byte) ([]ImageCandidate, error) {
	var parsed imageAPIResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("解析图像响应失败（%s）：%w；响应体：%s", url, err, previewBody(data))
	}
	if len(parsed.Data) == 0 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return nil, fmt.Errorf("图像后端返回错误：%s（%s）", parsed.Error.Message, parsed.Error.Type)
		}
		return nil, fmt.Errorf("图像后端 %s 未返回任何候选（data 为空）：%s", url, previewBody(data))
	}
	out := make([]ImageCandidate, 0, len(parsed.Data))
	for i, item := range parsed.Data {
		if item.B64JSON != "" {
			raw, err := base64.StdEncoding.DecodeString(item.B64JSON)
			if err != nil {
				return nil, fmt.Errorf("第 %d 个候选的 b64_json 解码失败: %w", i+1, err)
			}
			out = append(out, ImageCandidate{Data: raw, Ext: "png"})
			continue
		}
		if item.URL != "" {
			raw, ext, err := downloadURL(ctx, b.client, item.URL, b.timeout)
			if err != nil {
				return nil, fmt.Errorf("下载第 %d 个候选失败: %w", i+1, err)
			}
			out = append(out, ImageCandidate{Data: raw, Ext: ext, URL: item.URL})
			continue
		}
		return nil, fmt.Errorf("第 %d 个候选既无 b64_json 也无 url", i+1)
	}
	return out, nil
}
