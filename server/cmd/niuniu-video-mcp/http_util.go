package main

// Shared HTTP helpers for the backend adapters: JSON request/response plumbing,
// uniform (Chinese, actionable) error wrapping and media downloads.
//
// Every adapter error message carries: the adapter name, the endpoint, the HTTP
// status and a truncated response body — the agent must be able to tell an auth
// failure from a quota failure from a provider outage without guessing. Errors
// are returned verbatim-ish and NEVER swallowed: the tools surface the
// provider's own error text (failure without retry, design §7.4).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// bodyPreviewLimit caps how much of a provider response body we echo back into
// an error message (keeps tool results readable).
const bodyPreviewLimit = 600

// joinURL appends a path to a base URL, tolerating trailing slashes on either
// side (capability configs are hand-typed).
func joinURL(base, p string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(p, "/")
}

// previewBody renders a response body for an error message: single line,
// truncated, with binary bodies replaced by a byte count.
func previewBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "(空响应体)"
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' && r != '\r' {
			return fmt.Sprintf("(二进制响应体 %d 字节，已省略)", len(body))
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > bodyPreviewLimit {
		s = s[:bodyPreviewLimit] + "…"
	}
	return s
}

// doJSON performs a JSON request and returns the raw response body. Non-2xx
// responses become an error carrying status + body preview.
func doJSON(ctx context.Context, client *http.Client, method, url string, headers map[string]string, payload any, timeout time.Duration) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(payload); err != nil {
			return nil, fmt.Errorf("序列化请求体失败: %w", err)
		}
		body = buf
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("请求 %s 超时（%s）：%w", url, timeout, err)
		}
		return nil, fmt.Errorf("请求 %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 响应失败: %w", url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s 返回 HTTP %d：%s", url, resp.StatusCode, previewBody(respBody))
	}
	return respBody, nil
}

// doRaw performs a request without a JSON body (media downloads, multipart)
// and returns status + body; callers decide how to interpret it.
func doRaw(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body io.Reader, timeout time.Duration) (*http.Response, []byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, url, body)
	if err != nil {
		return nil, nil, fmt.Errorf("构造请求失败: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return nil, nil, fmt.Errorf("请求 %s 超时（%s）：%w", url, timeout, err)
		}
		return nil, nil, fmt.Errorf("请求 %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, fmt.Errorf("读取 %s 响应失败: %w", url, err)
	}
	return resp, data, nil
}

// bearer returns the Authorization header map for a Bearer token (empty map
// when the key is missing, so unauthenticated local endpoints still work).
func bearer(apiKey string) map[string]string {
	if strings.TrimSpace(apiKey) == "" {
		return map[string]string{}
	}
	return map[string]string{"Authorization": "Bearer " + apiKey}
}

// downloadURL fetches a remote media URL and returns its bytes plus a guessed
// extension (from Content-Type first, URL path second, "bin" fallback).
func downloadURL(ctx context.Context, client *http.Client, url string, timeout time.Duration) ([]byte, string, error) {
	resp, data, err := doRaw(ctx, client, http.MethodGet, url, nil, nil, timeout)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("下载产物 %s 返回 HTTP %d：%s", url, resp.StatusCode, previewBody(data))
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("下载产物 %s 得到空响应", url)
	}
	return data, extFromContentType(resp.Header.Get("Content-Type"), url), nil
}

// extFromContentType derives a bare file extension from a Content-Type header,
// falling back to the URL path suffix.
func extFromContentType(contentType, url string) string {
	if contentType != "" {
		if mt, _, err := mime.ParseMediaType(contentType); err == nil {
			switch mt {
			case "video/mp4", "application/mp4":
				return "mp4"
			case "image/png":
				return "png"
			case "image/jpeg":
				return "jpg"
			case "image/webp":
				return "webp"
			case "audio/mpeg", "audio/mp3":
				return "mp3"
			case "audio/wav", "audio/x-wav":
				return "wav"
			}
		}
	}
	if ext := strings.TrimPrefix(strings.ToLower(path.Ext(url)), "."); ext != "" && len(ext) <= 5 {
		return ext
	}
	return "bin"
}

// imageMIME returns the data-URL MIME type for a file name hint.
func imageMIME(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/png"
	}
}

// truncateForErr keeps an error string bounded for embedding in tool output.
func truncateForErr(s string, max int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
