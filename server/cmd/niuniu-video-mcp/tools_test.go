package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- helpers ---

func testApp(ws string, reg *Registry, deps Deps) *App {
	return &App{
		wsDir:      ws,
		dataDir:    filepath.Join(ws, ".data"),
		deps:       deps,
		reg:        reg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// noFFmpegDeps simulates a machine without ffmpeg (the degrade path).
func noFFmpegDeps() Deps {
	return Deps{ResolveFFmpeg: func() (string, error) {
		return "", fmt.Errorf("未找到 ffmpeg（PATH 中不存在）：媒体合成不可用。请安装 ffmpeg 并加入 PATH。")
	}}
}

func decodeResult(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("工具结果不是 JSON: %v\n%s", err, text)
	}
	return m
}

// quoteFor creates a real quote through the tool and returns its id.
func quoteFor(t *testing.T, app *App, capability string, quantity float64) string {
	t.Helper()
	res, err := app.handleQuoteEstimate(context.Background(), callArgs(map[string]any{
		"items": []any{map[string]any{"capability": capability, "quantity": quantity}},
	}))
	if err != nil {
		t.Fatalf("quote_estimate 返回 error: %v", err)
	}
	if res.IsError {
		t.Fatalf("quote_estimate 失败: %s", resultText(t, res))
	}
	id := jsonField(t, resultText(t, res), "quote_id")
	if id == "" {
		t.Fatal("quote_estimate 未返回 quote_id")
	}
	return id
}

// --- quote_estimate ---

func TestQuoteEstimateTool(t *testing.T) {
	ws := testWS(t)
	app := testApp(ws, pricedRegistry(), noFFmpegDeps())

	res, err := app.handleQuoteEstimate(context.Background(), callArgs(map[string]any{
		"items": []any{
			map[string]any{"capability": "tts", "quantity": 2000.0, "label": "旁白"},
			map[string]any{"capability": "video", "quantity": 5.0, "unit_price": 1.0},
		},
		"quality_tier": "草稿",
	}))
	if err != nil || res.IsError {
		t.Fatalf("res=%v err=%v", res, err)
	}
	out := decodeResult(t, resultText(t, res))
	if out["quote_id"] == "" || out["path"] == "" {
		t.Fatalf("结果缺少 quote_id/path: %v", out)
	}
	if total, _ := out["total"].(float64); total != 5.04 {
		t.Errorf("total = %v, want 5.04（0.04 + 5）", out["total"])
	}
	quoteID, _ := out["quote_id"].(string)
	qpath, err := quotePath(ws, quoteID)
	if err != nil {
		t.Fatalf("quotePath: %v", err)
	}
	if _, err := os.Stat(qpath); err != nil {
		t.Errorf("报价单未落盘: %v", err)
	}

	t.Run("items 缺失", func(t *testing.T) {
		res, _ := app.handleQuoteEstimate(context.Background(), callArgs(map[string]any{}))
		if !res.IsError || !strings.Contains(resultText(t, res), "items 必填") {
			t.Errorf("实得: %s", resultText(t, res))
		}
	})

	t.Run("item 数量非法", func(t *testing.T) {
		res, _ := app.handleQuoteEstimate(context.Background(), callArgs(map[string]any{
			"items": []any{map[string]any{"capability": "image", "quantity": 0.0}},
		}))
		if !res.IsError || !strings.Contains(resultText(t, res), "quantity 必须为正数") {
			t.Errorf("实得: %s", resultText(t, res))
		}
	})

	t.Run("未配置的能力", func(t *testing.T) {
		bare := testApp(ws, &Registry{Problems: map[string]string{}}, noFFmpegDeps())
		res, _ := bare.handleQuoteEstimate(context.Background(), callArgs(map[string]any{
			"items": []any{map[string]any{"capability": "video", "quantity": 5.0}},
		}))
		if !res.IsError || !strings.Contains(resultText(t, res), "设置→能力配置") {
			t.Errorf("实得: %s", resultText(t, res))
		}
	})
}

// --- tts_generate ---

func TestTTSGenerateUnconfigured(t *testing.T) {
	app := testApp(testWS(t), &Registry{Problems: map[string]string{}}, noFFmpegDeps())
	res, _ := app.handleTTSGenerate(context.Background(), callArgs(map[string]any{"text": "你好"}))
	if !res.IsError || !strings.Contains(resultText(t, res), "设置→能力配置") {
		t.Errorf("未配置时应给出可操作中文提示，实得: %s", resultText(t, res))
	}
}

func TestTTSGenerateWritesNumbersAndDegradesDuration(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3-audio-payload"))
	}))
	defer srv.Close()

	ws := testWS(t)
	reg := &Registry{Problems: map[string]string{}, TTS: &TTSBinding{
		Name: BackendOpenAICompat, Model: "tts-1", DefaultVoice: "nova",
		Backend: &OpenAICompatTTS{baseURL: srv.URL, apiKey: "k", model: "tts-1", client: srv.Client(), timeout: 5 * time.Second},
		Price:   PriceMeta{Unit: "元/千字符", UnitPrice: 0.02, Per: 1000, Configured: true},
	}}
	app := testApp(ws, reg, noFFmpegDeps())

	for i := 1; i <= 2; i++ {
		res, err := app.handleTTSGenerate(context.Background(), callArgs(map[string]any{"text": "第一段旁白"}))
		if err != nil || res.IsError {
			t.Fatalf("tts_generate #%d: err=%v text=%s", i, err, resultText(t, res))
		}
		out := decodeResult(t, resultText(t, res))
		want := fmt.Sprintf("video-project/assets/tts-%d.mp3", i)
		if out["file"] != want {
			t.Errorf("file = %v, want %v", out["file"], want)
		}
		if _, ok := out["duration_sec"]; ok {
			t.Error("无 ffmpeg 时不应给出 duration_sec")
		}
		if note, _ := out["duration_note"].(string); !strings.Contains(note, "未探测时长") {
			t.Errorf("应给出 duration_note 说明原因，实得: %v", out["duration_note"])
		}
		if out["voice"] != "nova" {
			t.Errorf("voice = %v, want 默认音色 nova", out["voice"])
		}
		raw, err := os.ReadFile(projectPath(ws, "assets", fmt.Sprintf("tts-%d.mp3", i)))
		if err != nil || string(raw) != "ID3-audio-payload" {
			t.Errorf("音频内容不符: %q, %v", raw, err)
		}
	}
	if hits.Load() != 2 {
		t.Errorf("后端调用次数 = %d, want 2", hits.Load())
	}
}

func TestTTSGenerateEmptyText(t *testing.T) {
	reg := &Registry{Problems: map[string]string{}, TTS: &TTSBinding{Name: BackendOpenAICompat}}
	app := testApp(testWS(t), reg, noFFmpegDeps())
	res, _ := app.handleTTSGenerate(context.Background(), callArgs(map[string]any{"text": "   "}))
	if !res.IsError || !strings.Contains(resultText(t, res), "text 必填") {
		t.Errorf("实得: %s", resultText(t, res))
	}
}

// --- image_generate ---

func TestImageGenerateRequiresQuote(t *testing.T) {
	reg := &Registry{Problems: map[string]string{}, Image: &ImageBinding{Name: BackendOpenAICompat, Model: "gpt-image-1"}}
	app := testApp(testWS(t), reg, noFFmpegDeps())

	res, _ := app.handleImageGenerate(context.Background(), callArgs(map[string]any{"prompt": "一只猫"}))
	if !res.IsError || !strings.Contains(resultText(t, res), "quote_estimate") {
		t.Errorf("缺 quote_id 应指引先出报价，实得: %s", resultText(t, res))
	}

	res, _ = app.handleImageGenerate(context.Background(), callArgs(map[string]any{"prompt": "一只猫", "quote_id": "q-20260927-153045-0000"}))
	if !res.IsError || !strings.Contains(resultText(t, res), "不存在") {
		t.Errorf("不存在的报价单应报错，实得: %s", resultText(t, res))
	}

	res, _ = app.handleImageGenerate(context.Background(), callArgs(map[string]any{"prompt": "一只猫", "quote_id": "../../escape"}))
	if !res.IsError || !strings.Contains(resultText(t, res), "格式不合法") {
		t.Errorf("非法格式的 quote_id 应被拒，实得: %s", resultText(t, res))
	}
}

// TestImageGenerateRejectsOutsideReferenceImage proves the boundary check runs
// before the paid call (the binding has no Backend, so reaching the provider
// would panic).
func TestImageGenerateRejectsOutsideReferenceImage(t *testing.T) {
	reg := &Registry{Problems: map[string]string{}, Image: &ImageBinding{
		Name: BackendOpenAICompat, Model: "gpt-image-1",
		Backend: &OpenAICompatImage{baseURL: "http://127.0.0.1:1", model: "m", client: &http.Client{}, timeout: time.Second},
		Price:   PriceMeta{Unit: "元/张", UnitPrice: 0.5, Per: 1, Configured: true},
	}}
	app := testApp(testWS(t), reg, noFFmpegDeps())
	quoteID := quoteFor(t, app, "image", 1)

	for _, ref := range []string{"../../secret.png", `..\..\secret.png`} {
		res, _ := app.handleImageGenerate(context.Background(), callArgs(map[string]any{
			"prompt": "参考图越界", "quote_id": quoteID, "n": 1.0, "reference_image": ref,
		}))
		if !res.IsError || !strings.Contains(resultText(t, res), "路径越界") {
			t.Errorf("reference_image=%q 应被边界检查拒绝，实得: %s", ref, resultText(t, res))
		}
	}
}

func TestImageGenerateEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"b64_json": base64.StdEncoding.EncodeToString(testPNG)},
			map[string]any{"b64_json": base64.StdEncoding.EncodeToString(append(testPNG, 0x09))},
		}})
	}))
	defer srv.Close()

	ws := testWS(t)
	reg := &Registry{Problems: map[string]string{}, Image: &ImageBinding{
		Name: BackendOpenAICompat, Model: "gpt-image-1",
		Backend: &OpenAICompatImage{baseURL: srv.URL, model: "gpt-image-1", client: srv.Client(), timeout: 5 * time.Second},
		Price:   PriceMeta{Unit: "元/张", UnitPrice: 0.5, Per: 1, Configured: true},
	}}
	app := testApp(ws, reg, noFFmpegDeps())
	quoteID := quoteFor(t, app, "image", 2)

	res, err := app.handleImageGenerate(context.Background(), callArgs(map[string]any{
		"prompt": "一只戴帽子的猫", "quote_id": quoteID, "n": 2.0, "aspect_ratio": "16:9",
	}))
	if err != nil || res.IsError {
		t.Fatalf("image_generate: err=%v text=%s", err, resultText(t, res))
	}
	out := decodeResult(t, resultText(t, res))
	cands, _ := out["candidates"].([]any)
	if len(cands) != 2 {
		t.Fatalf("候选数 = %d, want 2（%v）", len(cands), out)
	}
	if out["size"] != "1536x1024" {
		t.Errorf("size = %v, want 16:9 → 1536x1024", out["size"])
	}
	for i, c := range cands {
		path := filepath.Join(ws, filepath.FromSlash(c.(string)))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("候选 %d 未落盘: %v", i+1, err)
		}
	}
	ledger, err := loadLedger(ws, quoteID)
	if err != nil || len(ledger.Calls) != 1 {
		t.Fatalf("调用留痕 = %+v, %v", ledger, err)
	}
	if ledger.Calls[0].Status != "succeeded" || ledger.Calls[0].Amount != 1.0 {
		t.Errorf("留痕终态不符: %+v", ledger.Calls[0])
	}
	if ledger.Calls[0].StartedAt == "" || ledger.Calls[0].FinishedAt == "" {
		t.Error("留痕应记录起止时间")
	}
}

func TestImageGenerateRecordsFailedCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad prompt"))
	}))
	defer srv.Close()

	ws := testWS(t)
	reg := &Registry{Problems: map[string]string{}, Image: &ImageBinding{
		Name:    BackendOpenAICompat,
		Backend: &OpenAICompatImage{baseURL: srv.URL, model: "m", client: srv.Client(), timeout: 5 * time.Second},
		Price:   PriceMeta{Unit: "元/张", UnitPrice: 0.5, Per: 1, Configured: true},
	}}
	app := testApp(ws, reg, noFFmpegDeps())
	quoteID := quoteFor(t, app, "image", 1)

	res, _ := app.handleImageGenerate(context.Background(), callArgs(map[string]any{"prompt": "x", "quote_id": quoteID, "n": 1.0}))
	if !res.IsError || !strings.Contains(resultText(t, res), "HTTP 400") {
		t.Fatalf("失败应报错并带状态码，实得: %s", resultText(t, res))
	}
	ledger, _ := loadLedger(ws, quoteID)
	if len(ledger.Calls) != 1 || ledger.Calls[0].Status != "failed" {
		t.Fatalf("失败调用应留痕为 failed: %+v", ledger.Calls)
	}
	if !strings.Contains(ledger.Calls[0].Error, "HTTP 400") {
		t.Errorf("留痕应含供应商错误: %+v", ledger.Calls[0])
	}
}

// --- video_generate ---

// seedanceVideoApp wires an App onto a simulated 火山方舟 tasks API.
//   - every POST mints a unique task id (t-1, t-2, …)
//   - every poll of an id reports "queued" on the first hit and "succeeded"
//     afterwards; mode="fail" reports a policy failure instead
//
// The returned counter reports how many poll requests actually reached the
// provider (used to prove a failed task is never re-polled/retried).
func seedanceVideoApp(t *testing.T, ws, mode string) (*App, *atomic.Int32) {
	t.Helper()
	var base string
	var polls, submits atomic.Int32
	var mu sync.Mutex
	pollCount := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/contents/generations/tasks":
			id := fmt.Sprintf("t-%d", submits.Add(1))
			_, _ = w.Write([]byte(`{"id":"` + id + `"}`))
		case strings.HasSuffix(r.URL.Path, ".mp4"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(testMP4)
		default: // poll
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			mu.Lock()
			pollCount[id]++
			seen := pollCount[id]
			mu.Unlock()
			polls.Add(1)
			if mode == "fail" {
				_, _ = w.Write([]byte(`{"id":"` + id + `","status":"failed","error":{"code":"PolicyViolation","message":"内容审核未通过"}}`))
				return
			}
			if seen < 2 { // 每个任务先报 queued，再报 succeeded
				_, _ = w.Write([]byte(`{"id":"` + id + `","status":"queued"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"` + id + `","status":"succeeded","content":{"video_url":"` + base + `/v.mp4"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	base = srv.URL

	reg := &Registry{Problems: map[string]string{}, Video: &VideoBinding{
		Name: BackendSeedance, Model: defaultSeedanceModel,
		Backend: &SeedanceBackend{baseURL: srv.URL, apiKey: "k", model: defaultSeedanceModel, client: srv.Client(), timeout: 5 * time.Second},
		Price:   PriceMeta{Unit: "元/秒", UnitPrice: 0.8, Per: 1, Configured: true},
	}}
	app := testApp(ws, reg, noFFmpegDeps())
	return app, &polls
}

func videoTestWS(t *testing.T) string {
	t.Helper()
	ws := testWS(t)
	if err := ensureDirs(ws); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath(ws, "assets", "img-01-1.png"), testPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestVideoGenerateSubmitThenPoll(t *testing.T) {
	ws := videoTestWS(t)
	app, _ := seedanceVideoApp(t, ws, "ok")
	quoteID := quoteFor(t, app, "video", 5)

	// 提交：立即返回句柄，任务记录落盘。
	res, err := app.handleVideoGenerate(context.Background(), callArgs(map[string]any{
		"image": assetPath("img-01-1.png"), "prompt": "镜头缓慢推近", "duration_sec": 5.0, "n": 1.0,
		"aspect_ratio": "16:9", "quote_id": quoteID,
	}))
	if err != nil || res.IsError {
		t.Fatalf("提交失败: err=%v text=%s", err, resultText(t, res))
	}
	out := decodeResult(t, resultText(t, res))
	group, _ := out["task_group"].(string)
	if group == "" {
		t.Fatalf("缺少 task_group: %v", out)
	}
	if out["status"] != "running" {
		t.Errorf("提交后状态 = %v, want running", out["status"])
	}
	if _, err := os.Stat(filepath.Join(ws, "video-project", "shots", group+".task.json")); err != nil {
		t.Fatalf("任务记录未落盘: %v", err)
	}
	if got := jsonField(t, resultText(t, res), "record"); got != "video-project/shots/"+group+".task.json" {
		t.Errorf("record = %q", got)
	}
	ledger, _ := loadLedger(ws, quoteID)
	if len(ledger.Calls) != 1 || ledger.Calls[0].Status != "submitted" {
		t.Errorf("提交后留痕应为 submitted（等待轮询收尾）: %+v", ledger.Calls)
	}

	// 轮询两次：queued → succeeded + 取片。
	res, err = app.handleVideoGenerate(context.Background(), callArgs(map[string]any{"task_group": group}))
	if err != nil || res.IsError {
		t.Fatalf("轮询#1 失败: %v / %s", err, resultText(t, res))
	}
	if s := jsonField(t, resultText(t, res), "status"); s != "running" {
		t.Fatalf("轮询#1 状态 = %q, want running", s)
	}
	res, err = app.handleVideoGenerate(context.Background(), callArgs(map[string]any{"task_group": group}))
	if err != nil || res.IsError {
		t.Fatalf("轮询#2 失败: %v / %s", err, resultText(t, res))
	}
	out = decodeResult(t, resultText(t, res))
	if out["status"] != "succeeded" {
		t.Fatalf("终态 = %v, want succeeded（%v）", out["status"], out)
	}
	cands, _ := out["candidates"].([]any)
	if len(cands) != 1 {
		t.Fatalf("候选数 = %d, want 1", len(cands))
	}
	if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(cands[0].(string)))); err != nil {
		t.Errorf("候选未落盘: %v", err)
	}
	ledger, _ = loadLedger(ws, quoteID)
	if ledger.Calls[0].Status != "succeeded" {
		t.Errorf("成功后留痕应为 succeeded: %+v", ledger.Calls[0])
	}
	rec, err := loadTaskRecord(ws, group)
	if err != nil {
		t.Fatalf("loadTaskRecord: %v", err)
	}
	if !rec.allTerminal() || !rec.Settled {
		t.Errorf("任务记录应已终态并结算: %+v", rec)
	}
	if len(rec.Candidates) != 1 || rec.Candidates[0] == "" {
		t.Errorf("任务记录候选不符: %+v", rec.Candidates)
	}
	if err := loadTaskRecordInto(ws, rec); err != nil {
		t.Errorf("loadTaskRecordInto: %v", err)
	}
}

func TestVideoGenerateClampsCandidateCount(t *testing.T) {
	ws := videoTestWS(t)
	app, _ := seedanceVideoApp(t, ws, "ok")
	quoteID := quoteFor(t, app, "video", 15)

	res, err := app.handleVideoGenerate(context.Background(), callArgs(map[string]any{
		"image": assetPath("img-01-1.png"), "prompt": "p", "duration_sec": 5.0, "n": 9.0, "quote_id": quoteID,
	}))
	if err != nil || res.IsError {
		t.Fatalf("提交失败: %v / %s", err, resultText(t, res))
	}
	out := decodeResult(t, resultText(t, res))
	if out["status"] != "running" {
		t.Errorf("status = %v", out["status"])
	}
	tasks, _ := out["tasks"].([]any)
	if len(tasks) != 3 {
		t.Errorf("n=9 应被钳到 3，实得 %d 个任务", len(tasks))
	}
	notes, _ := out["notes"].([]any)
	found := false
	for _, n := range notes {
		if s, _ := n.(string); strings.Contains(s, "已按 3 处理") {
			found = true
		}
	}
	if !found {
		t.Errorf("应提示 n 被钳制，实得 notes=%v", notes)
	}
}

func TestVideoGenerateFailureIsVerbatimAndNotRetried(t *testing.T) {
	ws := videoTestWS(t)
	app, polls := seedanceVideoApp(t, ws, "fail")
	quoteID := quoteFor(t, app, "video", 5)

	res, err := app.handleVideoGenerate(context.Background(), callArgs(map[string]any{
		"image": assetPath("img-01-1.png"), "prompt": "p", "duration_sec": 5.0, "n": 1.0, "quote_id": quoteID,
	}))
	if err != nil || res.IsError {
		t.Fatalf("提交失败: %v / %s", err, resultText(t, res))
	}
	group := jsonField(t, resultText(t, res), "task_group")

	res, _ = app.handleVideoGenerate(context.Background(), callArgs(map[string]any{"task_group": group}))
	out := decodeResult(t, resultText(t, res))
	if out["status"] != "failed" {
		t.Fatalf("status = %v, want failed", out["status"])
	}
	tasks, _ := out["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %v", tasks)
	}
	task := tasks[0].(map[string]any)
	errText, _ := task["error"].(string)
	if !strings.Contains(errText, "内容审核未通过") {
		t.Errorf("失败必须回传供应商错误原文，实得: %q", errText)
	}
	if cands, _ := out["candidates"].([]any); len(cands) != 0 {
		t.Errorf("失败不应有候选: %v", cands)
	}
	// 再轮询一次：失败任务终态，不再向供应商轮询（不自动重试）。
	before := polls.Load()
	if before == 0 {
		t.Fatal("失败任务应至少轮询过一次供应商")
	}
	if _, _ = app.handleVideoGenerate(context.Background(), callArgs(map[string]any{"task_group": group})); polls.Load() != before {
		t.Errorf("失败任务不应再轮询（polls %d → %d）", before, polls.Load())
	}
}

func TestVideoGenerateUnknownGroup(t *testing.T) {
	app, _ := seedanceVideoApp(t, testWS(t), "ok")
	res, _ := app.handleVideoGenerate(context.Background(), callArgs(map[string]any{"task_group": "vg-does-not-exist"}))
	if !res.IsError || !strings.Contains(resultText(t, res), "未找到任务组记录") {
		t.Errorf("实得: %s", resultText(t, res))
	}
}

func TestVideoGenerateRejectsOutsideFirstFrame(t *testing.T) {
	ws := videoTestWS(t)
	app, _ := seedanceVideoApp(t, ws, "ok")
	quoteID := quoteFor(t, app, "video", 5)

	for _, ref := range []string{"../../secret.png", `..\..\secret.png`} {
		res, _ := app.handleVideoGenerate(context.Background(), callArgs(map[string]any{
			"image": ref, "prompt": "p", "duration_sec": 5.0, "n": 1.0, "quote_id": quoteID,
		}))
		if !res.IsError || !strings.Contains(resultText(t, res), "路径越界") {
			t.Errorf("image=%q 应被边界检查拒绝，实得: %s", ref, resultText(t, res))
		}
	}
}

func TestVideoGenerateUnconfigured(t *testing.T) {
	app := testApp(testWS(t), &Registry{Problems: map[string]string{}}, noFFmpegDeps())
	res, _ := app.handleVideoGenerate(context.Background(), callArgs(map[string]any{
		"image": "assets/x.png", "prompt": "p", "quote_id": "q-1",
	}))
	if !res.IsError || !strings.Contains(resultText(t, res), "设置→能力配置") {
		t.Errorf("实得: %s", resultText(t, res))
	}
}

// --- media_compose preconditions (no ffmpeg required) ---

func TestMediaComposeRequiresApprovedStoryboard(t *testing.T) {
	t.Run("缺少 storyboard", func(t *testing.T) {
		app := testApp(testWS(t), &Registry{Problems: map[string]string{}}, noFFmpegDeps())
		res, _ := app.handleMediaCompose(context.Background(), callArgs(map[string]any{}))
		if !res.IsError || !strings.Contains(resultText(t, res), "未找到分镜文件") {
			t.Errorf("实得: %s", resultText(t, res))
		}
	})

	t.Run("draft 被硬前置拒绝", func(t *testing.T) {
		ws := testWS(t)
		writeStoryboard(t, ws, storyboardJSON("draft", []testShot{{ID: "1", Dur: 1, Asset: assetPath("a.png")}}))
		app := testApp(ws, &Registry{Problems: map[string]string{}}, noFFmpegDeps())
		res, _ := app.handleMediaCompose(context.Background(), callArgs(map[string]any{}))
		text := resultText(t, res)
		if !res.IsError || !strings.Contains(text, "approved") || !strings.Contains(text, "draft") {
			t.Errorf("未过审的分镜必须被拒，实得: %s", text)
		}
	})

	t.Run("in-review 同样被拒", func(t *testing.T) {
		ws := testWS(t)
		writeStoryboard(t, ws, storyboardJSON("in-review", []testShot{{ID: "1", Dur: 1, Asset: assetPath("a.png")}}))
		app := testApp(ws, &Registry{Problems: map[string]string{}}, noFFmpegDeps())
		res, _ := app.handleMediaCompose(context.Background(), callArgs(map[string]any{}))
		if !res.IsError || !strings.Contains(resultText(t, res), "approved") {
			t.Errorf("实得: %s", resultText(t, res))
		}
	})

	t.Run("非法分镜先报校验问题", func(t *testing.T) {
		ws := testWS(t)
		writeStoryboard(t, ws, `{"title":"t","review_status":"approved","shots":[]}`)
		app := testApp(ws, &Registry{Problems: map[string]string{}}, noFFmpegDeps())
		res, _ := app.handleMediaCompose(context.Background(), callArgs(map[string]any{}))
		if !res.IsError || !strings.Contains(resultText(t, res), "校验失败") {
			t.Errorf("实得: %s", resultText(t, res))
		}
	})

	t.Run("素材路径越界被拒（不触碰工作空间外文件）", func(t *testing.T) {
		ws := testWS(t)
		writeStoryboard(t, ws, storyboardJSON("approved", []testShot{
			{ID: "1", Dur: 1, Asset: "../../../etc/passwd"},
		}))
		// 越界在 stage 1 素材校验处被拒，不会真的调用 ffmpeg。
		deps := Deps{ResolveFFmpeg: func() (string, error) { return "ffmpeg-not-needed", nil }}
		app := testApp(ws, &Registry{Problems: map[string]string{}}, deps)
		res, _ := app.handleMediaCompose(context.Background(), callArgs(map[string]any{}))
		text := resultText(t, res)
		if !res.IsError || !strings.Contains(text, "路径越界") || !strings.Contains(text, "visual.asset") {
			t.Errorf("实得: %s", text)
		}
		if _, err := os.Stat(projectPath(ws, "output", "final.mp4")); err == nil {
			t.Error("有越界素材时不得产出 final.mp4")
		}
	})

	t.Run("approved 但缺 ffmpeg 时明确降级", func(t *testing.T) {
		ws := testWS(t)
		writeStoryboard(t, ws, storyboardJSON("approved", []testShot{{ID: "1", Dur: 1, Asset: assetPath("a.png")}}))
		app := testApp(ws, &Registry{Problems: map[string]string{}}, noFFmpegDeps())
		res, _ := app.handleMediaCompose(context.Background(), callArgs(map[string]any{}))
		text := resultText(t, res)
		if !res.IsError || !strings.Contains(text, "未找到 ffmpeg") || !strings.Contains(text, "降级") {
			t.Errorf("缺 ffmpeg 应明确降级，实得: %s", text)
		}
	})
}
