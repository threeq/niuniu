package imageopt

// In-memory image down-sampling for model vision (issue #285).
//
// Unlike Optimize (which rewrites an attachment on disk under a strict byte
// budget), DownsampleForVision decodes an image, caps its long edge to a
// vision-friendly size, and returns the re-encoded JPEG bytes *in memory* so
// the niuniu-mcp read_image tool can hand them straight to the model. Built-in
// Read base64-inlines the full-resolution image into the vision context —
// slow and token-expensive for large screenshots; capping the long edge is
// where the token/latency saving comes from (cost scales with pixel count).
//
// It never writes to disk and never returns a non-nil error in normal use:
// every failure (unsupported format, corrupt/undecodable image, IO error,
// even a decoder panic) surfaces as a SkipReason so the caller can emit a
// {fallback:"read"} envelope instead of a hard error.

import (
	"context"
	"errors"
	"image"
	"log/slog"
)

// Vision down-sampling defaults. The long-edge cap is the dominant lever:
// Claude's vision pipeline already resizes images so the long edge is at most
// ~1568px, so sending anything larger is wasted tokens. Quality 80 keeps
// screenshot text legible while still shrinking 4K PNGs by an order of
// magnitude. Both are overridable via VisionOptions (issue #285: "阈值可配").
const (
	DefaultVisionMaxEdge = 1568
	DefaultVisionQuality = 80
)

// VisionOptions controls DownsampleForVision. The zero value is valid: unset
// fields fall back to the Default* constants.
type VisionOptions struct {
	MaxLongEdgePx int // cap the long edge to this; <=0 => DefaultVisionMaxEdge
	Quality       int // JPEG quality 1-100; <=0 => DefaultVisionQuality
}

// DefaultVisionOptions returns the spec defaults.
func DefaultVisionOptions() VisionOptions {
	return VisionOptions{MaxLongEdgePx: DefaultVisionMaxEdge, Quality: DefaultVisionQuality}
}

// VisionResult is the outcome of DownsampleForVision. Encoded is non-nil iff
// SkipReason == SkipNone.
type VisionResult struct {
	JPEG         []byte // 向后兼容：== Encoded（历史字段名）；nil when SkipReason != SkipNone
	Encoded      []byte // 编码后字节；nil when SkipReason != SkipNone
	OutFormat    string // 输出格式 "webp" | "png8" | "jpeg"
	OutMIME      string // 输出 MIME "image/webp" | "image/png" | "image/jpeg"
	Format       string // detected source format ("png"|"jpeg"|"gif"|"webp"|"svg"|"unknown")
	Width        int    // output width
	Height       int    // output height
	LongEdge     int    // output long edge
	OrigWidth    int    // source width (0 if decode never happened)
	OrigHeight   int    // source height
	OrigLongEdge int    // source long edge
	OrigSize     int64  // source file size in bytes
	NewSize      int64  // len(JPEG)
	// SkipReason is empty on success; otherwise one of:
	// unsupported_format | animated | decode_failed | io_failed | encode_failed.
	SkipReason SkipReason
}

// DownsampleForVision decodes path, caps its long edge per opts, and returns
// the JPEG-re-encoded result in memory. See the file-level comment for the
// failure contract (always SkipReason, never a hard error in normal use).
func DownsampleForVision(ctx context.Context, path string, opts VisionOptions) (res VisionResult, _ error) {
	maxEdge := opts.MaxLongEdgePx
	if maxEdge <= 0 {
		maxEdge = DefaultVisionMaxEdge
	}
	quality := opts.Quality
	if quality <= 0 {
		quality = DefaultVisionQuality
	}

	// A malformed image can panic some decoders; turn that into a clean
	// decode_failed skip so read_image can fall back to Read rather than
	// crashing the MCP process.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("imageopt: DownsampleForVision panic", "path", path, "recovered", r)
			res = VisionResult{Format: res.Format, OrigSize: res.OrigSize, SkipReason: SkipDecodeFailed}
		}
	}()

	d, err := detect(path)
	if err != nil {
		// IO error (missing/unreadable). Caller stats first for missing
		// files; this covers the racy/unreadable case.
		return VisionResult{SkipReason: SkipIOFailed}, nil
	}
	res.Format = d.Format
	res.OrigSize = d.SizeBytes

	switch d.Format {
	case "svg", "unknown":
		res.SkipReason = SkipUnsupportedFormat
		return res, nil
	}

	// Animated GIF/WebP: decodeImage returns the first frame (gif.Decode /
	// webp.Decode are single-image), which is exactly what we want for a
	// still vision payload — no special-casing needed.
	src, hasAlpha, err := decodeImage(path, d.Format)
	if err != nil {
		res.SkipReason = SkipDecodeFailed
		return res, nil
	}
	res.OrigWidth, res.OrigHeight = src.Bounds().Dx(), src.Bounds().Dy()
	res.OrigLongEdge = longEdge(src)

	if err := ctx.Err(); err != nil {
		res.SkipReason = SkipCanceled
		return res, nil
	}

	resized := resizeIfBigger(src, maxEdge)
	format := pickFormat(resized, hasAlpha)
	buf, err := encodeFor(resized, format, quality, hasAlpha)
	if err != nil {
		if errors.Is(err, errWebPUnavailable) {
			format = "jpeg" // 无 cgo 降级
			buf, err = encodeFor(resized, "jpeg", quality, hasAlpha)
		}
		if err != nil {
			slog.Error("imageopt: vision encode failed", "path", path, "err", err)
			res.SkipReason = SkipEncodeFailed
			return res, nil
		}
	}

	res.Encoded = buf.Bytes()
	res.JPEG = res.Encoded
	res.OutFormat = format
	res.OutMIME = mimeForFormat(format)
	res.NewSize = int64(buf.Len())
	res.Width, res.Height = resized.Bounds().Dx(), resized.Bounds().Dy()
	res.LongEdge = longEdge(resized)
	res.SkipReason = SkipNone
	return res, nil
}

// errUnsupportedForOCR：EncodeForOCR 遇到无法解码的格式（svg/unknown）时返回。
var errUnsupportedForOCR = errors.New("imageopt: unsupported format for OCR")

// EncodeForOCR 为 OCR 准备图：长边封顶 maxEdge（仅缩小），按内容选格式——
// 少色用 PNG8（不抖动，保文字边缘）；多色用高质量 WebP（无 cgo 时 JPEG q92）。
// 不追 80KB（OCR 输出是文本，对字节无收益，降质只伤识别率）。
// 返回 (字节, 扩展名, 原图长边, error)。origEdge>maxEdge 表示发生了降采样。
//
// 与 DownsampleForVision 一样，恶意/损坏图可能让底层解码器 panic；这里用
// recover 把 panic 收敛成普通 error，调用方（ocrInputPath）据此回退到原图，
// 绝不让 panic 冒泡崩掉 MCP 进程。
func EncodeForOCR(ctx context.Context, path string, maxEdge int) (data []byte, ext string, origLongEdge int, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("imageopt: EncodeForOCR panic", "path", path, "recovered", r)
			data, ext, origLongEdge, err = nil, "", 0, errUnsupportedForOCR
		}
	}()
	d, err := detect(path)
	if err != nil {
		return nil, "", 0, err
	}
	switch d.Format {
	case "svg", "unknown":
		return nil, "", 0, errUnsupportedForOCR
	}
	src, hasAlpha, err := decodeImage(path, d.Format)
	if err != nil {
		return nil, "", 0, err
	}
	origEdge := longEdge(src)
	resized := resizeIfBigger(src, maxEdge)

	if pickFormat(resized, hasAlpha) == "png8" {
		b, err := encodePNG8(resized, 256, hasAlpha, false) // 不抖动
		if err != nil {
			return nil, "", origEdge, err
		}
		return b, ".png", origEdge, nil
	}
	// 照片类：高质量 WebP；无 cgo 退 JPEG q92。
	if b, err := encodeWebP(resized, 92, hasAlpha); err == nil {
		return b, ".webp", origEdge, nil
	} else if !errors.Is(err, errWebPUnavailable) {
		return nil, "", origEdge, err
	}
	jb, err := encodeJPEG(resized, 92)
	if err != nil {
		return nil, "", origEdge, err
	}
	return jb.Bytes(), ".jpg", origEdge, nil
}

// mimeForFormat 把内部格式串映射到 content block 的 MIME。
func mimeForFormat(format string) string {
	switch format {
	case "webp":
		return "image/webp"
	case "png8":
		return "image/png"
	default:
		return "image/jpeg"
	}
}

// longEdge returns the larger of an image's width and height.
func longEdge(img image.Image) int {
	b := img.Bounds()
	if b.Dx() >= b.Dy() {
		return b.Dx()
	}
	return b.Dy()
}
