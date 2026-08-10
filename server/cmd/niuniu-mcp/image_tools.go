package main

// MCP tool for the read-accel image fast path + optional OCR (issue #283).
//
// read_image is the single entry point the Read PreToolUse hook (readhook.go)
// reroutes large images to. It has two behaviors, decided by probing whether
// the optional Tesseract OCR engine is installed (the same dependency the
// Settings -> System Dependencies page probes via /api/system-deps):
//
//   - Tesseract INSTALLED  -> OCR the image to plain text and return the text.
//     This is the only "see an image" path for agents without native vision
//     (e.g. Codex), and it keeps screenshots/scans searchable as text.
//   - Tesseract MISSING    -> DEGRADE to model vision: downsample the image
//     (via internal/imageopt so a big screenshot doesn't flood the context as
//     raw base64) and hand it back as an image content block for the model to
//     read directly, plus a note pointing at the install guide.
//
// A missing OCR engine NEVER errors and NEVER fails silently: the degrade path
// always returns a usable image plus an explicit, link-bearing explanation.
// OCR that runs but yields no text (blank scan, diagram, unsupported codec)
// also degrades to vision rather than returning an empty answer.

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/niuniu-dev/niuniu/internal/imageopt"
)

// ocrGuideURL is the canonical Tesseract install guide (issue #284). Must stay
// in sync with the same constant in internal/service/system_deps.go and the
// SPA's toolLabels map.
const ocrGuideURL = "https://www.niu6ai.com/docs/install/ocr-tesseract"

// ocrTimeout bounds a single tesseract invocation so a pathological image
// can't wedge the tool. OCR on a large page is seconds, not minutes.
const ocrTimeout = 60 * time.Second

// ocrMaxLongEdgePx caps the resolution actually handed to Tesseract. OCR
// accuracy plateaus well below the native resolution of a phone screenshot or
// scan, but Tesseract runtime scales with pixel count: a 3000-4000px page can
// take minutes, while the same page at ~2200px reads in seconds with no loss
// for body text. Above this edge read_image OCRs a downsampled in-memory copy
// instead of the original file — this is the dominant speed lever (issue #490:
// "图片读取太慢"). The downsampled copy is re-encoded at high quality
// (imageopt.EncodeForOCR) so small text edges survive.
const ocrMaxLongEdgePx = 2200

// Test seams. Defaults call the real OS; tests swap them to simulate
// installed/missing Tesseract without a real binary on PATH.
var (
	ocrLookPath = exec.LookPath
	ocrRun      = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, bin, args...).Output()
	}
)

// imageExtMIME maps a lower-case extension to its MIME type. Used both to gate
// read_image to image inputs and to label the vision content block.
var imageExtMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".tif":  "image/tiff",
	".tiff": "image/tiff",
}

func registerImageTools(s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("read_image",
			mcp.WithDescription("Read an image/screenshot/scan. With Tesseract OCR installed, returns the image's text as plain text (good for screenshots, scans, table images); "+
				"without OCR it degrades to model vision — downsamples the image and hands it to the model directly, plus an install-guide link — never erroring or failing silently. "+
				"Prefer this over the built-in Read for large images (built-in Read dumps the whole image as base64 into context: slow and token-heavy)."),
			mcp.WithString("path", mcp.Description("Absolute path to the image"), mcp.Required()),
			mcp.WithString("mode", mcp.Description("auto (default) = OCR if installed, else vision; ocr = force OCR (degrades to vision if not installed); vision = skip OCR and hand the image to the model")),
			mcp.WithString("lang", mcp.Description("OCR language (tesseract -l), e.g. \"eng\", \"chi_sim+eng\". Omit = auto-select installed English/Chinese packs.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			path, errRes := requireString(args, "path")
			if errRes != nil {
				return errRes, nil
			}
			mode := "auto"
			if v, ok := args["mode"].(string); ok && strings.TrimSpace(v) != "" {
				mode = strings.ToLower(strings.TrimSpace(v))
			}
			langArg := ""
			if v, ok := args["lang"].(string); ok {
				langArg = strings.TrimSpace(v)
			}
			return readImage(ctx, path, mode, langArg), nil
		},
	)
}

// readImage implements the read_image tool body. It never returns a non-nil
// error result for the "OCR missing" case — that path degrades to vision.
// Genuine bad input (missing path, directory, non-image, unreadable) returns
// an MCP error because the built-in Read wouldn't do any better.
func readImage(ctx context.Context, path, mode, langArg string) *mcp.CallToolResult {
	ext := strings.ToLower(filepath.Ext(path))
	mime, isImage := imageExtMIME[ext]
	if !isImage {
		return mcp.NewToolResultError(fmt.Sprintf(
			"%q 不是受支持的图片类型（支持 png/jpg/jpeg/gif/webp/bmp/tif/tiff）。"+
				"若是 PDF/Office 文档请用 read_document；其它文件用内置 Read。", path))
	}
	st, err := os.Stat(path)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("无法读取图片 %q: %v", path, err))
	}
	if st.IsDir() {
		return mcp.NewToolResultError(fmt.Sprintf("%q 是目录，不是图片", path))
	}

	// OCR path: tried for mode=auto and mode=ocr, only when the engine is
	// present. mode=vision skips it entirely.
	if mode != "vision" {
		if bin, ok := tesseractBin(); ok {
			langs := langArg
			if langs == "" {
				langs = resolveOCRLangs(ctx, bin)
			}
			// Speed lever: OCR a downsampled copy of an oversized image instead
			// of the full-resolution original (issue #490). cleanup removes the
			// temp file; origEdge>0 iff a downsample was actually applied.
			ocrPath, cleanup, origEdge := ocrInputPath(ctx, path)
			defer cleanup()
			text, ocrErr := runOCR(ctx, bin, ocrPath, langs)
			if ocrErr == nil && strings.TrimSpace(text) != "" {
				downNote := ""
				if origEdge > 0 {
					downNote = fmt.Sprintf(" · 已降采样 %d→%dpx 加速", origEdge, ocrMaxLongEdgePx)
				}
				header := fmt.Sprintf("[OCR · tesseract · lang=%s%s · %s]\n"+
					"以下为图中识别出的文本（如需查看版式/图形而非文字，可用 mode=vision 重读）：\n\n",
					langs, downNote, path)
				return mcp.NewToolResultText(header + strings.TrimRight(text, "\n") + "\n")
			}
			// OCR ran but produced nothing usable (blank/diagram/unsupported
			// codec) or errored — fall through to vision rather than return
			// an empty or error result.
			return visionResult(ctx, path, mime, ocrDidNotYieldText(ocrErr, text))
		}
		if mode == "ocr" {
			// User explicitly asked for OCR but the engine isn't installed.
			// Degrade to vision (never error) and make the gap explicit.
			return visionResult(ctx, path, mime, ocrEngineMissingNote())
		}
	}

	// mode=vision, or mode=auto with no OCR engine: hand the image to the
	// model. For auto we still surface the install hint so the user learns OCR
	// would make this text-searchable.
	note := ""
	if mode != "vision" {
		note = ocrEngineMissingNote()
	}
	return visionResult(ctx, path, mime, note)
}

// visionResult builds the degrade-to-vision tool result: an optional textual
// note (OCR status + install guide) followed by the (possibly downsampled)
// image content block for the model to read directly. If the image cannot be
// read/encoded at all it falls back to an MCP error — there is nothing usable
// to hand back in that case.
func visionResult(ctx context.Context, path, mime, note string) *mcp.CallToolResult {
	b64, outMime, encErr := encodeImageForVision(ctx, path, mime)
	if encErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("无法读取图片用于视觉降级 %q: %v", path, encErr))
	}
	text := note
	if text == "" {
		text = fmt.Sprintf("[图片 · %s] 以下图片已交给模型直接识读：", path)
	} else {
		text += fmt.Sprintf("\n\n[图片 · %s] 已降采样后交给模型直接识读：", path)
	}
	return mcp.NewToolResultImage(text, b64, outMime)
}

// encodeImageForVision returns base64 image data (and its MIME) suitable for a
// vision content block. It downsamples through imageopt.DownsampleForVision —
// an in-memory, vision-tuned resize (long edge capped to ~1568px, re-encoded to
// a content-chosen format: WebP/PNG8/JPEG, with its MIME returned) — so the
// payload shrinks for a large-byte OR large-pixel image alike, without the
// long-edge cap being bypassed for highly-compressible originals.
// The original file is only read, never modified. If the image can't be decoded
// (unsupported format, corrupt, IO error) the best-effort fallback inlines the
// original bytes untouched, so a failed optimization never becomes a failed
// read.
func encodeImageForVision(ctx context.Context, path, mime string) (string, string, error) {
	if res, _ := imageopt.DownsampleForVision(ctx, path, imageopt.DefaultVisionOptions()); len(res.Encoded) > 0 {
		return base64.StdEncoding.EncodeToString(res.Encoded), res.OutMIME, nil
	}
	// Could not downsample (unsupported/corrupt/IO): inline the original so the
	// model still gets the image to look at.
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(raw), mime, nil
}

// ocrInputPath returns a filesystem path to hand to Tesseract for `path`. For
// an oversized image it writes a downsampled, content-tuned copy (PNG8 for
// low-color screenshots, high-quality WebP/JPEG for photos; long edge capped to
// ocrMaxLongEdgePx) to a temp file and returns it plus a cleanup func and the
// original long edge — OCR cost scales with pixel count, so this is the main
// speedup (issue #490). For an already-small image, or on ANY downsample/IO
// failure, it returns the original path, a no-op cleanup, and origEdge=0, so
// OCR still runs on something — this helper never errors and never blocks the
// OCR from happening.
func ocrInputPath(ctx context.Context, path string) (inPath string, cleanup func(), origEdge int) {
	noop := func() {}
	data, ext, edge, err := imageopt.EncodeForOCR(ctx, path, ocrMaxLongEdgePx)
	// Only write a temp file when encoding succeeded AND the source was actually
	// oversized (so a downsample happened) — re-encoding an already-small image
	// buys no speed and only risks softening its text. On ANY failure fall back
	// to the original path so OCR still runs.
	if err != nil || edge <= ocrMaxLongEdgePx || len(data) == 0 {
		return path, noop, 0
	}
	tmp, err := os.CreateTemp("", "niuniu-ocr-*"+ext)
	if err != nil {
		return path, noop, 0
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return path, noop, 0
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return path, noop, 0
	}
	return name, func() { os.Remove(name) }, edge
}

// tesseractBin probes for the Tesseract binary on PATH (the OCR equivalent of
// the system-deps probe). Returns the resolved path and whether it exists.
func tesseractBin() (string, bool) {
	p, err := ocrLookPath("tesseract")
	if err != nil {
		return "", false
	}
	return p, true
}

// runOCR invokes `tesseract <image> stdout -l <langs>` and returns the
// recognized text. An empty langs uses tesseract's default (eng).
func runOCR(ctx context.Context, bin, path, langs string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, ocrTimeout)
	defer cancel()
	args := []string{path, "stdout"}
	if strings.TrimSpace(langs) != "" {
		args = append(args, "-l", langs)
	}
	out, err := ocrRun(cctx, bin, args...)
	return string(out), err
}

// langOnce caches the resolved OCR language string for the process lifetime —
// the installed language packs don't change mid-session and `--list-langs`
// spawns a process we'd rather not repeat per image.
var (
	langOnce   sync.Once
	cachedLang string
)

// resolveOCRLangs picks the best available language string: prefer Simplified
// + Traditional Chinese alongside English when those packs are installed,
// otherwise fall back to English (or whatever single pack exists). Always
// returns a non-empty string so OCR has a deterministic -l value.
func resolveOCRLangs(ctx context.Context, bin string) string {
	langOnce.Do(func() {
		cachedLang = computeOCRLangs(ctx, bin)
	})
	return cachedLang
}

func computeOCRLangs(ctx context.Context, bin string) string {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := ocrRun(cctx, bin, "--list-langs")
	if err != nil {
		return "eng"
	}
	avail := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		l := strings.TrimSpace(line)
		// Skip the "List of available languages (N):" header line, and "osd"
		// (orientation/script-detection data, not a real OCR language — passing
		// `-l osd` makes tesseract error out with empty output).
		if l == "" || l == "osd" || strings.HasSuffix(l, ":") || strings.Contains(l, " ") {
			continue
		}
		avail[l] = true
	}
	var picked []string
	for _, want := range []string{"eng", "chi_sim", "chi_tra"} {
		if avail[want] {
			picked = append(picked, want)
		}
	}
	if len(picked) > 0 {
		return strings.Join(picked, "+")
	}
	// No preferred pack: use any single installed language, else eng.
	for l := range avail {
		return l
	}
	return "eng"
}

// ocrEngineMissingNote is the user-facing explanation + install link shown when
// Tesseract is not installed. This is the "弹出官网引导链接" surface for the
// agent/chat path (the Settings page links the same guide for the GUI path).
func ocrEngineMissingNote() string {
	return "未检测到 OCR 引擎（Tesseract）：已自动降级为「模型视觉」——直接把图交给模型识读，未报错。\n" +
		"若想让截图/扫描件被识别成可检索的纯文本（对不做视觉的 Codex 尤为关键），请安装 Tesseract：\n" +
		"  " + ocrGuideURL + "\n" +
		"装好后牛牛会自动探测到，无需重启；之后 read_image 默认走 OCR 出文本。"
}

// ocrDidNotYieldText explains why a present OCR engine still degraded to
// vision: either it errored, or it returned no usable text.
func ocrDidNotYieldText(ocrErr error, text string) string {
	if ocrErr != nil {
		return fmt.Sprintf("OCR（Tesseract）执行失败：%v —— 已降级为模型视觉直接识读，未报错。"+
			"该图可能是不支持的编码或损坏；如需排查可手动运行 tesseract。", ocrErr)
	}
	if strings.TrimSpace(text) == "" {
		return "OCR（Tesseract）未识别出文本（可能是无文字的图形/图表或空白扫描件）——已降级为模型视觉直接识读。"
	}
	return ""
}
