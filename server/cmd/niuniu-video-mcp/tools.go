package main

// The five frozen tools (plan §1.5):
//
//	quote_estimate  — 付费调用清单 → 报价单（分项+总额，落 quotes/<quote_id>.json）
//	tts_generate    — 文本+voice → assets/tts-<n>.mp3（含时长元数据）
//	image_generate  — prompt(+参考图)+画幅+n → assets/img-<n>-<k>.png 候选
//	video_generate  — 首帧图+运动 prompt+时长 → 异步任务（submit / poll 两相）
//	media_compose   — approved storyboard → shots/ 逐镜 + output/final.mp4
//
// Guardrails live here, once, in the tool shell (design §7.3): paid calls
// require a quote_id produced by quote_estimate (L2 留痕 / L3 前置), failures
// are reported verbatim and never retried, and every unconfigured capability
// degrades to an actionable Chinese hint instead of a panic or a silent no-op.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// App carries the process-wide module state into every handler.
type App struct {
	wsDir      string
	dataDir    string
	deps       Deps
	reg        *Registry
	httpClient *http.Client
}

// assetSizeForAspect maps a storyboard aspect ratio onto an OpenAI-compatible
// image size. Vendors that only accept other sizes can override with the
// explicit `size` parameter.
func assetSizeForAspect(ratio string) (string, bool) {
	switch strings.TrimSpace(ratio) {
	case "16:9", "4:3":
		return "1536x1024", true
	case "9:16", "3:4":
		return "1024x1536", true
	case "1:1":
		return "1024x1024", true
	default:
		return "1024x1024", false
	}
}

// registerTools wires the five tools onto the MCP server.
func registerTools(s *server.MCPServer, app *App) {
	s.AddTool(quoteEstimateTool(), app.handleQuoteEstimate)
	s.AddTool(ttsGenerateTool(), app.handleTTSGenerate)
	s.AddTool(imageGenerateTool(), app.handleImageGenerate)
	s.AddTool(videoGenerateTool(), app.handleVideoGenerate)
	s.AddTool(mediaComposeTool(), app.handleMediaCompose)
}

// --- tool schemas ---

func quoteEstimateTool() mcp.Tool {
	return mcp.NewTool("quote_estimate",
		mcp.WithDescription("生成付费调用报价单（分项+总额），落 <workspace>/video-project/quotes/<quote_id>.json。"+
			"入参 items 是待执行的付费调用清单：capability=tts|image|video，quantity 含义随能力（tts=字符数 · image=张数 · video=秒数）；"+
			"unit_price 省略时取该能力后端的价格元数据（可在能力配置里用 price 设置）。"+
			"image_generate / video_generate 的 quote_id 必须来自本工具；L3（视频）派发前需把报价交给用户确认。"),
		mcp.WithArray("items", mcp.Required(), mcp.Description(
			"付费调用清单。每项：{capability(必填 tts|image|video), quantity(必填>0), unit_price(可选，省略取价格元数据), "+
				"unit(可选，如 '元/千字符'), backend(可选，默认当前配置的后端), model(可选), label(可选，如 '第3镜 首帧')}"),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"capability": map[string]any{"type": "string", "description": "tts | image | video"},
					"quantity":   map[string]any{"type": "number", "description": "tts=字符数 · image=张数 · video=秒数"},
					"unit_price": map[string]any{"type": "number", "description": "单价；省略则取适配器价格元数据"},
					"unit":       map[string]any{"type": "string", "description": "计价单位描述"},
					"backend":    map[string]any{"type": "string", "description": "后端实现名；省略=当前配置"},
					"model":      map[string]any{"type": "string"},
					"label":      map[string]any{"type": "string", "description": "自由标注（如 第3镜 首帧）"},
				},
				"required": []any{"capability", "quantity"},
			})),
		mcp.WithString("quality_tier", mcp.Description("质量档位（草稿/标准/精制）——候选与修复预算的来源，写入报价单备查")),
		mcp.WithString("note", mcp.Description("报价单备注（如 供应商价目来源、汇率、有效期）")),
	)
}

func ttsGenerateTool() mcp.Tool {
	return mcp.NewTool("tts_generate",
		mcp.WithDescription("文本转语音（OpenAI 兼容 /v1/audio/speech）：写 <workspace>/video-project/assets/tts-<n>.mp3，"+
			"并返回时长元数据（有 ffmpeg 时探测，否则省略并注明）。"+
			"L2 直接放行；给了 quote_id 会把本次调用留痕到 quotes/<quote_id>.calls.json。"),
		mcp.WithString("text", mcp.Required(), mcp.Description("要合成的文本（建议 = 该镜 narration）")),
		mcp.WithString("voice", mcp.Description("音色名（如 alloy）；省略用能力配置的 voice，再省略用后端默认")),
		mcp.WithNumber("speed", mcp.Description("语速倍率（如 1.1）；省略用后端默认")),
		mcp.WithString("quote_id", mcp.Description("可选：报价单 id，用于调用留痕")),
	)
}

func imageGenerateTool() mcp.Tool {
	return mcp.NewTool("image_generate",
		mcp.WithDescription("文生图（OpenAI 兼容 /v1/images/generations；给 reference_image 时走 /v1/images/edits 参考图生成）："+
			"产出 n 个候选 png 写 <workspace>/video-project/assets/，返回候选路径列表。"+
			"付费调用前会写报价留痕，因此 quote_id 必填（先调 quote_estimate）。失败不自动重试。"),
		mcp.WithString("prompt", mcp.Required(), mcp.Description("图像 prompt（应包含角色/场景/画风模板段）")),
		mcp.WithString("quote_id", mcp.Required(), mcp.Description("报价单 id（quote_estimate 产出）")),
		mcp.WithString("aspect_ratio", mcp.Description("画幅：16:9 / 9:16 / 1:1（默认 16:9），映射为后端支持的 size")),
		mcp.WithNumber("n", mcp.Description("候选数（默认 2，最大 4；质量档位越高候选越多）")),
		mcp.WithString("size", mcp.Description("显式尺寸（如 1536x1024）；省略由 aspect_ratio 推导")),
		mcp.WithString("reference_image", mcp.Description("参考图路径（角色/场景参考图，workspace 相对或绝对）；给了就走 edits 端点")),
		mcp.WithString("label", mcp.Description("自由标注（如 'c-hero 角色定义图'）")),
	)
}

func videoGenerateTool() mcp.Tool {
	return mcp.NewTool("video_generate",
		mcp.WithDescription("图生视频（异步任务：seedance=火山方舟 · kling=可灵）。两相用法——\n"+
			"【提交】给 image+prompt+duration_sec+n+quote_id：提交 n 个任务，返回 task_group 与任务记录路径 "+
			"(<workspace>/video-project/shots/<task_group>.task.json)，并留痕 quotes/。\n"+
			"【轮询】给 task_group（可带 wait_sec）：轮询任务；完成的取片落 shots/，失败的原样返回任务号+供应商错误原文，"+
			"绝不自动重试。wait_sec>0 时在调用内等待至多该秒数（每 poll_interval_sec 轮询一次）。\n"+
			"付费调用前必须先用 quote_estimate 出报价单；L3 派发前需经用户确认。"),
		mcp.WithString("task_group", mcp.Description("轮询模式：提交时返回的任务组 id（给这个参数就是轮询，不再提交新任务）")),
		mcp.WithString("image", mcp.Description("提交模式：首帧图路径（workspace 相对或绝对）")),
		mcp.WithString("prompt", mcp.Description("提交模式：运动/镜头 prompt")),
		mcp.WithString("quote_id", mcp.Description("提交模式：报价单 id（必填）")),
		mcp.WithNumber("duration_sec", mcp.Description("提交模式：时长秒（默认 5；可灵按 5/10 取整）")),
		mcp.WithNumber("n", mcp.Description("提交模式：候选数（默认 2，最大 3；每候选=一个独立任务）")),
		mcp.WithString("aspect_ratio", mcp.Description("提交模式：画幅（如 16:9 / 9:16）")),
		mcp.WithNumber("wait_sec", mcp.Description("提交后可选的等待秒数（默认 0=立即返回句柄；最大 600）")),
		mcp.WithNumber("poll_interval_sec", mcp.Description("轮询间隔秒（默认 5，最小 2）")),
		mcp.WithString("label", mcp.Description("自由标注（如 '第1镜 候选'）")),
	)
}

func mediaComposeTool() mcp.Tool {
	return mcp.NewTool("media_compose",
		mcp.WithDescription("FFmpeg 最终装配器：读 <workspace>/video-project/storyboard.json（硬前置 review_status=approved），"+
			"逐镜合成到 shots/（每镜独立 mp4，合格缓存复用、坏镜只重做该镜），再 concat 拼接 + ass 字幕烧录 + BGM 混音 → "+
			"<workspace>/video-project/output/final.mp4（H.264/AAC/faststart，默认分辨率取 storyboard.resolution），"+
			"并写 qc/final-qc.json 技术 QC。缺 ffmpeg 时明确降级报错，不影响其它工具。"),
		mcp.WithString("bgm", mcp.Description("BGM 文件（workspace 相对或绝对）；省略则取 storyboard.bgm，都没有=仅人声")),
		mcp.WithNumber("bgm_volume", mcp.Description("BGM 音量 0~1（默认 0.2）")),
		mcp.WithString("font", mcp.Description("字幕字体名（默认按平台：Windows=Microsoft YaHei / macOS=PingFang SC / 其它=Noto Sans CJK SC）")),
		mcp.WithString("output", mcp.Description("成片输出路径（默认 video-project/output/final.mp4）")),
		mcp.WithBoolean("force", mcp.Description("true=忽略已缓存的 shots 全部重做（默认 false，只重做缺失/过期/不合格的镜头）")),
	)
}

// --- handlers ---

func (a *App) handleQuoteEstimate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	rawItems, ok := args["items"].([]any)
	if !ok || len(rawItems) == 0 {
		return mcp.NewToolResultError("items 必填：请给出至少一项付费调用 {capability, quantity}"), nil
	}
	items := make([]quoteRequestItem, 0, len(rawItems))
	for i, raw := range rawItems {
		obj, ok := raw.(map[string]any)
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("items[%d] 必须是对象 {capability, quantity, unit_price?}", i)), nil
		}
		capability, _ := obj["capability"].(string)
		if strings.TrimSpace(capability) == "" {
			return mcp.NewToolResultError(fmt.Sprintf("items[%d].capability 必填（tts / image / video）", i)), nil
		}
		quantity, ok := obj["quantity"].(float64)
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("items[%d].quantity 必填且必须是数字（tts=字符数 · image=张数 · video=秒数）", i)), nil
		}
		it := quoteRequestItem{Capability: strings.TrimSpace(capability), Quantity: quantity}
		it.Backend, _ = obj["backend"].(string)
		it.Model, _ = obj["model"].(string)
		it.Label, _ = obj["label"].(string)
		it.Unit, _ = obj["unit"].(string)
		if v, ok := obj["unit_price"].(float64); ok {
			if v < 0 {
				return mcp.NewToolResultError(fmt.Sprintf("items[%d].unit_price 不能为负", i)), nil
			}
			it.UnitPrice = &v
		}
		items = append(items, it)
	}
	tier, _ := args["quality_tier"].(string)
	note, _ := args["note"].(string)
	q, err := buildQuote(a.reg, items, tier, note)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	rel, err := writeQuote(a.wsDir, q)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	out := map[string]any{
		"quote_id":     q.QuoteID,
		"path":         rel,
		"currency":     q.Currency,
		"quality_tier": q.QualityTier,
		"items":        q.Items,
		"total":        q.Total,
		"warnings":     q.Warnings,
		"next":         "把 quote_id 传给 image_generate / video_generate；L3（视频）先请用户确认报价。",
	}
	return jsonResult(out)
}

func (a *App) handleTTSGenerate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	if a.reg.TTS == nil {
		return mcp.NewToolResultError(a.reg.UnconfiguredMessage(CapTTS, "配音")), nil
	}
	text, ok := args["text"].(string)
	if !ok || strings.TrimSpace(text) == "" {
		return mcp.NewToolResultError("text 必填：要合成的文本（建议 = 该镜 narration）"), nil
	}
	voice, _ := args["voice"].(string)
	if strings.TrimSpace(voice) == "" {
		voice = a.reg.TTS.DefaultVoice
	}
	speed := parseFloatDefault(args["speed"], 0)
	quoteID, _ := args["quote_id"].(string)
	quoteID = strings.TrimSpace(quoteID)
	if quoteID != "" && !validQuoteID(quoteID) {
		return mcp.NewToolResultError(invalidQuoteIDError(quoteID).Error()), nil
	}

	binding := a.reg.TTS
	result, err := binding.Backend.Synthesize(ctx, TTSRequest{Text: text, Voice: voice, Speed: speed})
	if err != nil {
		if quoteID != "" {
			_ = appendCall(a.wsDir, quoteID, CallRecord{
				Capability: CapTTS, Backend: binding.Name, Model: binding.Model,
				Quantity: float64(len([]rune(text))), Unit: binding.Price.Unit, UnitPrice: binding.Price.UnitPrice,
				Amount: amountFor(binding.Price, float64(len([]rune(text)))), Status: "failed",
				Error: truncateForErr(err.Error(), 500), FinishedAt: time.Now().Format(time.RFC3339),
			})
		}
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := ensureDirs(a.wsDir); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	n := nextAssetIndex(projectPath(a.wsDir, "assets"), "tts-")
	file := projectPath(a.wsDir, "assets", fmt.Sprintf("tts-%d.mp3", n))
	if err := os.WriteFile(file, result.Audio, 0o644); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("写入音频文件失败: %v", err)), nil
	}

	out := map[string]any{
		"file":    relToWS(a.wsDir, file),
		"bytes":   len(result.Audio),
		"voice":   voice,
		"model":   binding.Model,
		"backend": binding.Name,
	}
	// Duration metadata: probed with ffmpeg when available, explicitly omitted
	// (with the reason) when not — never a silent zero.
	if ffmpegBin, ferr := a.deps.ffmpegPath(); ferr != nil {
		out["duration_note"] = "未探测时长：" + ferr.Error()
	} else if info, perr := probeMedia(ctx, ffmpegBin, file); perr != nil || !info.Valid {
		out["duration_note"] = "ffmpeg 未能解析音频时长，已省略 duration_sec。"
	} else {
		out["duration_sec"] = info.DurationSec
	}

	chars := float64(len([]rune(text)))
	if quoteID != "" {
		if err := appendCall(a.wsDir, quoteID, CallRecord{
			Capability: CapTTS, Backend: binding.Name, Model: binding.Model,
			Quantity: chars, Unit: binding.Price.Unit, UnitPrice: binding.Price.UnitPrice,
			Amount: amountFor(binding.Price, chars), Status: "succeeded",
			Files: relToWS(a.wsDir, file), FinishedAt: time.Now().Format(time.RFC3339),
		}); err != nil {
			out["ledger_warning"] = err.Error()
		}
		out["quote_id"] = quoteID
	}
	return jsonResult(out)
}

func (a *App) handleImageGenerate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	if a.reg.Image == nil {
		return mcp.NewToolResultError(a.reg.UnconfiguredMessage(CapImage, "图像")), nil
	}
	prompt, ok := args["prompt"].(string)
	if !ok || strings.TrimSpace(prompt) == "" {
		return mcp.NewToolResultError("prompt 必填（应包含角色/场景/画风模板段）"), nil
	}
	quoteID, _ := args["quote_id"].(string)
	if err := requireQuote(a.wsDir, quoteID); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	binding := a.reg.Image

	n := int(parseFloatDefault(args["n"], 2))
	notes := []string{}
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
		notes = append(notes, "n 超过上限 4，已按 4 处理")
	}
	size, _ := args["size"].(string)
	if strings.TrimSpace(size) == "" {
		ratio, _ := args["aspect_ratio"].(string)
		if strings.TrimSpace(ratio) == "" {
			ratio = "16:9"
		}
		var mapped bool
		size, mapped = assetSizeForAspect(ratio)
		if !mapped {
			notes = append(notes, fmt.Sprintf("画幅 %q 未映射到尺寸，按 %s 处理（可用 size 参数显式指定）", ratio, size))
		}
	}

	imageReq := ImageRequest{Prompt: prompt, N: n, Size: size}
	if ref, _ := args["reference_image"].(string); strings.TrimSpace(ref) != "" {
		refPath, err := resolveCheckedAsset(a.wsDir, ref, "reference_image")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		data, err := os.ReadFile(refPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("读取参考图失败（%s）：%v", ref, err)), nil
		}
		imageReq.ReferenceImage = data
		imageReq.ReferenceImageName = refPath[strings.LastIndexAny(refPath, `/\`)+1:]
	}

	label, _ := args["label"].(string)
	call := CallRecord{
		Capability: CapImage, Backend: binding.Name, Model: binding.Model, Label: label,
		Quantity: float64(n), Unit: binding.Price.Unit, UnitPrice: binding.Price.UnitPrice,
		Amount: amountFor(binding.Price, float64(n)), Status: "submitted",
	}
	// 付费调用前先落留痕（报价纪律：调用即留痕，崩溃也有据可查）。
	if err := appendCall(a.wsDir, quoteID, call); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	callID := lastCallID(a.wsDir, quoteID)

	candidates, err := binding.Backend.Generate(ctx, imageReq)
	if err != nil {
		a.failCall(quoteID, callID, err)
		return mcp.NewToolResultError(fmt.Sprintf("图像生成失败（不自动重试）：%v", err)), nil
	}
	if len(candidates) == 0 {
		err := fmt.Errorf("图像后端返回 0 个候选")
		a.failCall(quoteID, callID, err)
		return mcp.NewToolResultError(err.Error()), nil
	}

	if err := ensureDirs(a.wsDir); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	idx := nextAssetIndex(projectPath(a.wsDir, "assets"), "img-")
	files := make([]string, 0, len(candidates))
	for k, c := range candidates {
		ext := c.Ext
		if ext == "" {
			ext = "png"
		}
		file := projectPath(a.wsDir, "assets", fmt.Sprintf("img-%02d-%d.%s", idx, k+1, ext))
		if err := os.WriteFile(file, c.Data, 0o644); err != nil {
			a.failCall(quoteID, callID, err)
			return mcp.NewToolResultError(fmt.Sprintf("写入候选图失败: %v", err)), nil
		}
		files = append(files, relToWS(a.wsDir, file))
	}
	a.finishCall(quoteID, callID, "succeeded", strings.Join(files, ", "), "")

	out := map[string]any{
		"candidates": files,
		"backend":    binding.Name,
		"model":      binding.Model,
		"size":       size,
		"count":      len(files),
		"quote_id":   quoteID,
		"amount":     amountFor(binding.Price, float64(n)),
		"next":       "AI 预审（G2/G4）通过后把人选/首选写回 storyboard 的 visual.candidates/selected。",
	}
	if len(notes) > 0 {
		out["notes"] = notes
	}
	return jsonResult(out)
}

func (a *App) handleVideoGenerate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	if a.reg.Video == nil {
		return mcp.NewToolResultError(a.reg.UnconfiguredMessage(CapVideo, "视频")), nil
	}
	groupID, _ := args["task_group"].(string)
	if strings.TrimSpace(groupID) != "" {
		return a.pollVideoTask(ctx, strings.TrimSpace(groupID), args)
	}
	return a.submitVideoTask(ctx, args)
}

// submitVideoTask handles the submit phase: N provider tasks + task record.
func (a *App) submitVideoTask(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
	binding := a.reg.Video
	imageRef, _ := args["image"].(string)
	if strings.TrimSpace(imageRef) == "" {
		return mcp.NewToolResultError("image 必填：首帧图路径（提交模式）；要轮询已有任务请传 task_group"), nil
	}
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return mcp.NewToolResultError("prompt 必填：运动/镜头描述"), nil
	}
	quoteID, _ := args["quote_id"].(string)
	if err := requireQuote(a.wsDir, quoteID); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	imagePath, err := resolveCheckedAsset(a.wsDir, imageRef, "image")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	frame, err := os.ReadFile(imagePath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("读取首帧图失败（%s）：%v", imageRef, err)), nil
	}

	duration := parseFloatDefault(args["duration_sec"], 5)
	if duration <= 0 {
		duration = 5
	}
	n := int(parseFloatDefault(args["n"], 2))
	notes := []string{}
	if n < 1 {
		n = 1
	}
	if n > 3 {
		n = 3
		notes = append(notes, "n 超过上限 3，已按 3 处理")
	}
	aspect, _ := args["aspect_ratio"].(string)
	label, _ := args["label"].(string)

	call := CallRecord{
		Capability: CapVideo, Backend: binding.Name, Model: binding.Model, Label: label,
		Quantity: duration * float64(n), Unit: binding.Price.Unit, UnitPrice: binding.Price.UnitPrice,
		Amount: amountFor(binding.Price, duration*float64(n)), Status: "submitted",
	}
	if err := appendCall(a.wsDir, quoteID, call); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	callID := lastCallID(a.wsDir, quoteID)

	rec := &videoTaskRecord{
		GroupID:     newID("vg"),
		Backend:     binding.Name,
		Model:       binding.Model,
		QuoteID:     quoteID,
		CallID:      callID,
		CreatedAt:   time.Now().Format(time.RFC3339),
		UpdatedAt:   time.Now().Format(time.RFC3339),
		Prompt:      prompt,
		DurationSec: duration,
		AspectRatio: aspect,
		SourceImage: relToWS(a.wsDir, imagePath),
		Requested:   n,
		Status:      "submitted",
	}
	for i := 0; i < n; i++ {
		handle, err := binding.Backend.Submit(ctx, VideoRequest{
			FirstFrame:     frame,
			FirstFrameName: imagePath,
			Prompt:         prompt,
			DurationSec:    duration,
			NCandidates:    1,
			AspectRatio:    aspect,
			Model:          binding.Model,
		})
		entry := videoTaskEntry{Index: i + 1}
		if err != nil {
			entry.State = string(TaskFailed)
			entry.SubmitError = truncateForErr(err.Error(), 800)
		} else {
			entry.TaskID = handle.TaskID
			entry.State = string(TaskPending)
			entry.SubmitRaw = handle.Raw
		}
		rec.Tasks = append(rec.Tasks, entry)
	}
	okCount := 0
	for _, t := range rec.Tasks {
		if t.TaskID != "" {
			okCount++
		}
	}
	if okCount == 0 {
		a.finishCall(quoteID, callID, "failed", "", "全部任务提交失败")
		rec.Settled = true
		rec.Status = rec.aggregateStatus()
		_ = saveTaskRecord(a.wsDir, rec)
		return mcp.NewToolResultError(fmt.Sprintf("视频任务提交全部失败（不自动重试）。任务组 %s 记录：%s\n首个错误：%s",
			rec.GroupID, relToWS(a.wsDir, taskRecordPath(a.wsDir, rec.GroupID)), rec.Tasks[0].SubmitError)), nil
	}
	rec.Status = rec.aggregateStatus()
	if err := saveTaskRecord(a.wsDir, rec); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	wait := parseFloatDefault(args["wait_sec"], 0)
	interval := parseFloatDefault(args["poll_interval_sec"], 5)
	if wait > 0 {
		if err := a.pollUntil(ctx, rec, wait, interval); err != nil {
			notes = append(notes, err.Error())
		}
	}

	out := map[string]any{
		"task_group": rec.GroupID,
		"record":     relToWS(a.wsDir, taskRecordPath(a.wsDir, rec.GroupID)),
		"status":     rec.Status,
		"tasks":      rec.Tasks,
		"candidates": rec.Candidates,
		"quote_id":   quoteID,
		"backend":    binding.Name,
		"model":      binding.Model,
		"amount":     amountFor(binding.Price, duration*float64(n)),
		"next":       "稍后用 video_generate(task_group=...) 轮询取片；失败任务不自动重试，需人决定。",
	}
	if rec.Status == string(TaskSucceeded) || rec.Status == "partial" {
		out["next"] = "候选已取回，写入 storyboard 的 visual.candidates 并走 G4 抽帧评审。"
	}
	if len(notes) > 0 {
		out["notes"] = notes
	}
	return jsonResult(out)
}

// pollVideoTask handles the poll phase: advance every non-terminal task,
// fetch finished ones, persist the record.
func (a *App) pollVideoTask(ctx context.Context, groupID string, args map[string]any) (*mcp.CallToolResult, error) {
	binding := a.reg.Video
	rec, err := loadTaskRecord(a.wsDir, groupID)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if rec.Backend != binding.Name {
		return mcp.NewToolResultError(fmt.Sprintf(
			"任务组 %s 由 %q 后端提交，当前配置的视频后端是 %q：无法继续轮询。请改回原后端配置，或重新提交任务。",
			groupID, rec.Backend, binding.Name)), nil
	}
	wait := parseFloatDefault(args["wait_sec"], 0)
	interval := parseFloatDefault(args["poll_interval_sec"], 5)
	notes := []string{}
	if wait > 0 {
		if err := a.pollUntil(ctx, rec, wait, interval); err != nil {
			notes = append(notes, err.Error())
		}
	} else {
		if err := a.advanceOnce(ctx, rec); err != nil {
			notes = append(notes, err.Error())
		}
	}
	out := map[string]any{
		"task_group": rec.GroupID,
		"record":     relToWS(a.wsDir, taskRecordPath(a.wsDir, rec.GroupID)),
		"status":     rec.Status,
		"tasks":      rec.Tasks,
		"candidates": rec.Candidates,
	}
	if rec.Status == string(TaskFailed) {
		out["next"] = "任务失败：供应商错误原文见 tasks[].error；按纪律不自动重试，请人工决定换模型/改 prompt/降档。"
	}
	if len(notes) > 0 {
		out["notes"] = notes
	}
	return jsonResult(out)
}

// pollUntil polls every non-terminal task until all are terminal or the wait
// budget runs out (never past the caller's context deadline).
func (a *App) pollUntil(ctx context.Context, rec *videoTaskRecord, waitSec, intervalSec float64) error {
	if waitSec > 600 {
		waitSec = 600
	}
	if intervalSec < 2 {
		intervalSec = 2
	}
	if intervalSec > 60 {
		intervalSec = 60
	}
	deadline := time.Now().Add(time.Duration(waitSec * float64(time.Second)))
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("等待被取消：%v", err)
		}
		if err := a.advanceOnce(ctx, rec); err != nil {
			return err
		}
		if rec.allTerminal() || time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待被取消：%v", ctx.Err())
		case <-time.After(time.Duration(intervalSec * float64(time.Second))):
		}
	}
}

// advanceOnce polls each non-terminal task and fetches the ones that finished.
func (a *App) advanceOnce(ctx context.Context, rec *videoTaskRecord) error {
	binding := a.reg.Video
	for i := range rec.Tasks {
		t := &rec.Tasks[i]
		if t.TaskID == "" || TaskState(t.State).Terminal() {
			continue
		}
		status, err := binding.Backend.Poll(ctx, TaskHandle{TaskID: t.TaskID, Backend: rec.Backend, Model: rec.Model})
		if err != nil {
			// A poll failure is transient by nature; record it but do not
			// convert it into a task failure (that would be a lie).
			t.LastPollError = truncateForErr(err.Error(), 500)
			continue
		}
		t.State = string(status.State)
		t.RawStatus = status.RawStatus
		t.LastPollError = ""
		if status.State == TaskFailed {
			t.Error = status.Err // 供应商错误原文，不改写
			continue
		}
		if status.State == TaskSucceeded {
			cands, err := binding.Backend.Fetch(ctx, TaskHandle{TaskID: t.TaskID, Backend: rec.Backend, Model: rec.Model})
			if err != nil {
				// 取片失败不是任务失败（任务已成功）：留痕，下次轮询重取（下载免费，不算重试付费调用）。
				t.State = string(TaskRunning)
				t.LastPollError = "取片失败（下次轮询重试）：" + truncateForErr(err.Error(), 500)
				continue
			}
			if len(cands) == 0 {
				t.State = string(TaskFailed)
				t.Error = "任务成功但取片结果为空（供应商未返回视频产物）"
				continue
			}
			if err := ensureDirs(a.wsDir); err != nil {
				return err
			}
			for _, c := range cands {
				ext := c.Ext
				if ext == "" {
					ext = "mp4"
				}
				file := projectPath(a.wsDir, "shots", fmt.Sprintf("%s-%d.%s", rec.GroupID, len(rec.Candidates)+1, ext))
				if err := os.WriteFile(file, c.Data, 0o644); err != nil {
					return fmt.Errorf("写入视频候选失败: %w", err)
				}
				rel := relToWS(a.wsDir, file)
				rec.Candidates = append(rec.Candidates, rel)
				t.Candidates = append(t.Candidates, rel)
			}
			t.State = string(TaskSucceeded)
		}
	}
	rec.Status = rec.aggregateStatus()
	if rec.allTerminal() && !rec.Settled {
		status, detail := "failed", ""
		if len(rec.Candidates) > 0 {
			status = "succeeded"
			detail = strings.Join(rec.Candidates, ", ")
		} else {
			errs := make([]string, 0, len(rec.Tasks))
			for _, t := range rec.Tasks {
				if t.SubmitError != "" {
					errs = append(errs, fmt.Sprintf("任务#%d 提交失败: %s", t.Index, t.SubmitError))
				} else if t.Error != "" {
					errs = append(errs, fmt.Sprintf("任务#%d(%s): %s", t.Index, t.TaskID, t.Error))
				}
			}
			detail = "全部任务失败：" + strings.Join(errs, "; ")
		}
		a.finishCall(rec.QuoteID, rec.CallID, status, detail, "")
		rec.Settled = true
	}
	return saveTaskRecord(a.wsDir, rec)
}

// handleMediaCompose runs the FFmpeg assembler. It needs no capability account
// (pure local tool) — only the storyboard and an ffmpeg binary.
func (a *App) handleMediaCompose(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	opts := composeOptions{
		BGMVolume: parseFloatDefault(args["bgm_volume"], 0),
	}
	opts.BGM, _ = args["bgm"].(string)
	opts.Font, _ = args["font"].(string)
	opts.OutputRel, _ = args["output"].(string)
	if v, ok := args["force"].(bool); ok {
		opts.Force = v
	}
	report, err := composeFilm(ctx, a.wsDir, a.deps, opts)
	if err != nil {
		// Return the per-shot diagnostics alongside the error: report is the
		// actionable part (which shot failed and why).
		payload := map[string]any{"error": err.Error()}
		if report != nil {
			payload["report"] = report
		}
		data, _ := json.MarshalIndent(payload, "", "  ")
		return mcp.NewToolResultError(string(data)), nil
	}
	out := map[string]any{
		"report": report,
		"next": "成片与 QC 已产出：请人工目检审核（G5 技术 QC 只覆盖时长/流/编码/时长一致，" +
			"画面质量与内容一致性由人工确认）。",
	}
	return jsonResult(out)
}

// --- small handler helpers ---

// jsonResult renders a JSON tool result.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("序列化结果失败: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

// failCall marks a ledger call failed (best effort — a ledger problem must not
// mask the real error).
func (a *App) failCall(quoteID, callID string, err error) {
	if quoteID == "" || callID == "" {
		return
	}
	_ = finishCall(a.wsDir, quoteID, callID, func(r *CallRecord) {
		r.Status = "failed"
		r.Error = truncateForErr(err.Error(), 800)
	})
}

// finishCall marks a ledger call succeeded with its output files (best effort).
func (a *App) finishCall(quoteID, callID, status, files, errMsg string) {
	if quoteID == "" || callID == "" {
		return
	}
	_ = finishCall(a.wsDir, quoteID, callID, func(r *CallRecord) {
		r.Status = status
		r.Files = files
		if errMsg != "" {
			r.Error = errMsg
		}
	})
}

// lastCallID returns the most recently appended call id of a quote (the one
// appendCall just wrote). Returns "" when the ledger cannot be read.
func lastCallID(wsDir, quoteID string) string {
	ledger, err := loadLedger(wsDir, quoteID)
	if err != nil || len(ledger.Calls) == 0 {
		return ""
	}
	return ledger.Calls[len(ledger.Calls)-1].CallID
}
