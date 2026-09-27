package main

// Video backends — the asynchronous half of the adapter layer (design §7.3:
// `video_generate` is an async task: submit -> poll -> fetch). Two recipes ship
// in wave 1, both image-to-video:
//
//	seedance — 火山方舟 Ark 内容生成任务
//	   POST {base}/api/v3/contents/generations/tasks
//	   GET  {base}/api/v3/contents/generations/tasks/{id}
//	   status: queued|running|succeeded|failed, 结果取 content.video_url
//
//	kling — 可灵（快手）图生视频
//	   POST {base}/v1/videos/image2video          (HS256 JWT, ak/sk 签名)
//	   GET  {base}/v1/videos/image2video/{task_id}
//	   status: submitted|processing|succeed|failed, 结果取 data.task_result.videos[].url
//
// Both shapes follow the vendors' published API as of 2025-09; provider APIs
// drift month to month (design risk #4), which is exactly why each vendor is a
// self-contained adapter and a new recipe is one file + one registry line.
// Failures are reported verbatim and never retried (design §7.4).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Default models. Real deployments should pin the model via the capability
// config's `model` extra key — provider model ids roll over regularly.
const (
	defaultSeedanceModel = "doubao-seedance-1-0-pro-250528"
	defaultKlingModel    = "kling-v1"
)

// --- 火山方舟 Seedance ---

// SeedanceBackend implements VideoBackend for Volcengine Ark.
type SeedanceBackend struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
	timeout time.Duration
}

func (b *SeedanceBackend) tasksURL() string {
	return joinURL(b.baseURL, "/api/v3/contents/generations/tasks")
}

// seedanceTask mirrors the Ark task object (submit response carries just id).
type seedanceTask struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Status  string `json:"status"`
	Content struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Submit creates one Ark generation task. The prompt carries the CLI-style
// parameters Ark expects (--ratio/--dur); the first frame goes in as a data URL.
func (b *SeedanceBackend) Submit(ctx context.Context, req VideoRequest) (TaskHandle, error) {
	if len(req.FirstFrame) == 0 {
		return TaskHandle{}, fmt.Errorf("seedance 需要首帧图（image 参数）")
	}
	model := req.Model
	if model == "" {
		model = b.model
	}
	text := strings.TrimSpace(req.Prompt)
	if req.DurationSec > 0 {
		text += fmt.Sprintf(" --dur %s", trimFloat(req.DurationSec))
	}
	if req.AspectRatio != "" {
		text += " --ratio " + req.AspectRatio
	}
	payload := map[string]any{
		"model": model,
		"content": []map[string]any{
			{"type": "text", "text": text},
			{"type": "image_url", "image_url": map[string]any{
				"url": fmt.Sprintf("data:%s;base64,%s", imageMIME(req.FirstFrameName), base64.StdEncoding.EncodeToString(req.FirstFrame)),
			}},
		},
	}
	data, err := doJSON(ctx, b.client, http.MethodPost, b.tasksURL(), bearer(b.apiKey), payload, b.timeout)
	if err != nil {
		return TaskHandle{}, fmt.Errorf("seedance 提交任务失败: %w（请检查能力配置中的 API Key / 模型名）", err)
	}
	var task seedanceTask
	if err := json.Unmarshal(data, &task); err != nil {
		return TaskHandle{}, fmt.Errorf("解析 seedance 提交响应失败: %w；响应体：%s", err, previewBody(data))
	}
	if task.ID == "" {
		return TaskHandle{}, fmt.Errorf("seedance 提交响应缺少任务 id：%s", previewBody(data))
	}
	return TaskHandle{TaskID: task.ID, Backend: BackendSeedance, Model: model, Raw: data}, nil
}

// Poll reads the current task state.
func (b *SeedanceBackend) Poll(ctx context.Context, h TaskHandle) (TaskStatus, error) {
	data, err := doJSON(ctx, b.client, http.MethodGet, b.tasksURL()+"/"+url.PathEscape(h.TaskID), bearer(b.apiKey), nil, b.timeout)
	if err != nil {
		return TaskStatus{}, fmt.Errorf("seedance 轮询任务 %s 失败: %w", h.TaskID, err)
	}
	var task seedanceTask
	if err := json.Unmarshal(data, &task); err != nil {
		return TaskStatus{}, fmt.Errorf("解析 seedance 轮询响应失败: %w；响应体：%s", err, previewBody(data))
	}
	st := TaskStatus{RawStatus: task.Status, Raw: data}
	switch strings.ToLower(task.Status) {
	case "queued", "pending", "":
		st.State = TaskPending
	case "running", "processing":
		st.State = TaskRunning
	case "succeeded", "success":
		st.State = TaskSucceeded
	case "failed", "cancelled", "canceled", "expired":
		st.State = TaskFailed
		if task.Error != nil {
			if task.Error.Message != "" {
				st.Err = task.Error.Message
			}
			if task.Error.Code != "" {
				st.Err = strings.TrimSpace(st.Err + " (code=" + task.Error.Code + ")")
			}
		}
		if st.Err == "" {
			st.Err = "任务失败，供应商未给出错误详情"
		}
	default:
		st.State = TaskRunning // unknown state: keep polling rather than lying
	}
	return st, nil
}

// Fetch downloads the finished video(s). Ark returns exactly one video per
// task; multiple candidates come from multiple submitted tasks.
func (b *SeedanceBackend) Fetch(ctx context.Context, h TaskHandle) ([]VideoCandidate, error) {
	data, err := doJSON(ctx, b.client, http.MethodGet, b.tasksURL()+"/"+url.PathEscape(h.TaskID), bearer(b.apiKey), nil, b.timeout)
	if err != nil {
		return nil, fmt.Errorf("seedance 取片失败（任务 %s）: %w", h.TaskID, err)
	}
	var task seedanceTask
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, fmt.Errorf("解析 seedance 取片响应失败: %w；响应体：%s", err, previewBody(data))
	}
	if st := strings.ToLower(task.Status); st != "succeeded" && st != "success" {
		return nil, fmt.Errorf("seedance 任务 %s 尚未成功（当前状态 %q），无法取片", h.TaskID, task.Status)
	}
	if task.Content.VideoURL == "" {
		return nil, fmt.Errorf("seedance 任务 %s 已完成但未返回 video_url：%s", h.TaskID, previewBody(data))
	}
	raw, ext, err := downloadURL(ctx, b.client, task.Content.VideoURL, b.timeout)
	if err != nil {
		return nil, fmt.Errorf("下载 seedance 产物失败（任务 %s）: %w", h.TaskID, err)
	}
	return []VideoCandidate{{Data: raw, Ext: ext, URL: task.Content.VideoURL}}, nil
}

// --- 可灵 Kling ---

// KlingBackend implements VideoBackend for Kuaishou Kling.
type KlingBackend struct {
	baseURL   string
	accessKey string
	secretKey string
	model     string
	client    *http.Client
	timeout   time.Duration
}

func (b *KlingBackend) tasksURL() string { return joinURL(b.baseURL, "/v1/videos/image2video") }

// klingEnvelope is the shared Kling response envelope.
type klingEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		TaskID        string `json:"task_id"`
		TaskStatus    string `json:"task_status"`
		TaskStatusMsg string `json:"task_status_msg"`
		TaskResult    struct {
			Videos []struct {
				ID       string `json:"id"`
				URL      string `json:"url"`
				Duration string `json:"duration"`
			} `json:"videos"`
		} `json:"task_result"`
	} `json:"data"`
}

// klingJWT builds the HS256 token Kling requires: iss=AccessKey, signed with
// SecretKey; 30-minute window with a small clock-skew allowance.
func klingJWT(accessKey, secretKey string, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"iss": accessKey,
		"exp": now.Add(30 * time.Minute).Unix(),
		"nbf": now.Add(-5 * time.Second).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("构造可灵 JWT 载荷失败: %w", err)
	}
	signing := header + "." + enc.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(signing))
	return signing + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

func (b *KlingBackend) authHeaders() (map[string]string, error) {
	token, err := klingJWT(b.accessKey, b.secretKey, time.Now())
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "Bearer " + token}, nil
}

// Submit creates one Kling image2video task.
func (b *KlingBackend) Submit(ctx context.Context, req VideoRequest) (TaskHandle, error) {
	if len(req.FirstFrame) == 0 {
		return TaskHandle{}, fmt.Errorf("kling 需要首帧图（image 参数）")
	}
	headers, err := b.authHeaders()
	if err != nil {
		return TaskHandle{}, err
	}
	model := req.Model
	if model == "" {
		model = b.model
	}
	// Kling takes the duration as a string enum ("5"/"10"); round to the
	// nearest supported value instead of failing on e.g. 3.5.
	dur := "5"
	if req.DurationSec >= 7.5 {
		dur = "10"
	}
	payload := map[string]any{
		"model_name": model,
		"image":      base64.StdEncoding.EncodeToString(req.FirstFrame),
		"prompt":     req.Prompt,
		"duration":   dur,
		"mode":       "std",
	}
	if req.AspectRatio != "" {
		payload["aspect_ratio"] = req.AspectRatio
	}
	data, err := doJSON(ctx, b.client, http.MethodPost, b.tasksURL(), headers, payload, b.timeout)
	if err != nil {
		return TaskHandle{}, fmt.Errorf("kling 提交任务失败: %w（请检查能力配置中的 Access/Secret Key）", err)
	}
	var env klingEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return TaskHandle{}, fmt.Errorf("解析 kling 提交响应失败: %w；响应体：%s", err, previewBody(data))
	}
	if env.Code != 0 {
		return TaskHandle{}, fmt.Errorf("kling 提交被拒绝（code=%d）：%s", env.Code, env.Message)
	}
	if env.Data.TaskID == "" {
		return TaskHandle{}, fmt.Errorf("kling 提交响应缺少 task_id：%s", previewBody(data))
	}
	return TaskHandle{TaskID: env.Data.TaskID, Backend: BackendKling, Model: model, Raw: data}, nil
}

// Poll reads the current task state.
func (b *KlingBackend) Poll(ctx context.Context, h TaskHandle) (TaskStatus, error) {
	headers, err := b.authHeaders()
	if err != nil {
		return TaskStatus{}, err
	}
	data, err := doJSON(ctx, b.client, http.MethodGet, b.tasksURL()+"/"+url.PathEscape(h.TaskID), headers, nil, b.timeout)
	if err != nil {
		return TaskStatus{}, fmt.Errorf("kling 轮询任务 %s 失败: %w", h.TaskID, err)
	}
	var env klingEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return TaskStatus{}, fmt.Errorf("解析 kling 轮询响应失败: %w；响应体：%s", err, previewBody(data))
	}
	if env.Code != 0 {
		return TaskStatus{}, fmt.Errorf("kling 轮询被拒绝（code=%d）：%s", env.Code, env.Message)
	}
	st := TaskStatus{RawStatus: env.Data.TaskStatus, Raw: data}
	switch strings.ToLower(env.Data.TaskStatus) {
	case "submitted", "queued", "pending", "":
		st.State = TaskPending
	case "processing", "running":
		st.State = TaskRunning
	case "succeed", "succeeded", "success":
		st.State = TaskSucceeded
	case "failed", "error":
		st.State = TaskFailed
		st.Err = strings.TrimSpace(env.Data.TaskStatusMsg)
		if st.Err == "" {
			st.Err = "任务失败，供应商未给出错误详情"
		}
	default:
		st.State = TaskRunning
	}
	return st, nil
}

// Fetch downloads the finished video(s).
func (b *KlingBackend) Fetch(ctx context.Context, h TaskHandle) ([]VideoCandidate, error) {
	headers, err := b.authHeaders()
	if err != nil {
		return nil, err
	}
	data, err := doJSON(ctx, b.client, http.MethodGet, b.tasksURL()+"/"+url.PathEscape(h.TaskID), headers, nil, b.timeout)
	if err != nil {
		return nil, fmt.Errorf("kling 取片失败（任务 %s）: %w", h.TaskID, err)
	}
	var env klingEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("解析 kling 取片响应失败: %w；响应体：%s", err, previewBody(data))
	}
	if st := strings.ToLower(env.Data.TaskStatus); st != "succeed" && st != "succeeded" && st != "success" {
		return nil, fmt.Errorf("kling 任务 %s 尚未成功（当前状态 %q），无法取片", h.TaskID, env.Data.TaskStatus)
	}
	videos := env.Data.TaskResult.Videos
	if len(videos) == 0 {
		return nil, fmt.Errorf("kling 任务 %s 已完成但未返回视频：%s", h.TaskID, previewBody(data))
	}
	out := make([]VideoCandidate, 0, len(videos))
	for i, v := range videos {
		if v.URL == "" {
			continue
		}
		raw, ext, err := downloadURL(ctx, b.client, v.URL, b.timeout)
		if err != nil {
			return nil, fmt.Errorf("下载 kling 第 %d 个产物失败（任务 %s）: %w", i+1, h.TaskID, err)
		}
		out = append(out, VideoCandidate{Data: raw, Ext: ext, URL: v.URL})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("kling 任务 %s 的结果没有可用 url", h.TaskID)
	}
	return out, nil
}

// trimFloat renders a float without a trailing ".0" (Ark CLI params read
// cleaner as integers, but fractional durations still survive).
func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
