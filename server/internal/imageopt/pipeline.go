package imageopt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"

	"github.com/disintegration/imaging"
)

var errEncodeBigger = errors.New("imageopt: encoded result not smaller than source")

// runPipeline runs the resize × quality iteration described in spec §5.2.
// Returns the final encoded buffer, long edge actually used, quality used,
// and output format ("jpeg" per Task 1 spike — WebP reserved for future).
//
// Strictly enforces TargetMaxBytes: iterates edges (descending) × qualities
// (descending), going past Options.MinLongEdgePx / Options.MinQuality down to
// 128px and q=10 if needed. The first combination whose encoded size is
// ≤ TargetMaxBytes (and smaller than source) wins.
//
// Returns errEncodeBigger if either:
//   - the first ≤budget hit is not smaller than the source (compression
//     made the file grow), or
//   - even the most aggressive combination (128px × q=10) exceeds budget.
//
// In practice 128×128 JPEG at q=10 is a few KB for any real image, so the
// second case only fires for pathological inputs; callers treat it as
// SkipEncodeBigger and keep the original on disk.
//
// ctx is checked between iterations; on cancellation returns ctx.Err()
// (修复 review I4).
func runPipeline(ctx context.Context, srcPath string, opts Options) (*bytes.Buffer, int, int, string, error) {
	d, err := detect(srcPath)
	if err != nil {
		return nil, 0, 0, "", err
	}
	src, hasAlpha, err := decodeImage(srcPath, d.Format)
	if err != nil {
		return nil, 0, 0, "", err
	}

	// 1. 先按内容选格式，在目标长边上编码一次。
	capped := resizeIfBigger(src, opts.TargetLongEdgePx)
	format := pickFormat(capped, hasAlpha)
	if format == "webp" {
		if buf, err := encodeFor(capped, "webp", 80, hasAlpha); err == nil {
			if int64(buf.Len()) <= opts.TargetMaxBytes && int64(buf.Len()) < d.SizeBytes {
				return buf, longEdge(capped), 80, "webp", nil
			}
		} else if errors.Is(err, errWebPUnavailable) {
			format = "jpeg" // 降级：无 cgo 时照片走 JPEG
		}
	} else { // png8
		if buf, err := encodeFor(capped, "png8", 0, hasAlpha); err == nil {
			if int64(buf.Len()) <= opts.TargetMaxBytes && int64(buf.Len()) < d.SizeBytes {
				return buf, longEdge(capped), 0, "png8", nil
			}
		}
	}

	// 2. 兜底迭代：选定格式上降质×降边。PNG8 走有损路径（转 WebP，无 cgo 则 JPEG）。
	iterFormat := format
	if iterFormat == "png8" {
		iterFormat = "webp"
		if _, err := encodeFor(capped, "webp", 80, hasAlpha); errors.Is(err, errWebPUnavailable) {
			iterFormat = "jpeg"
		}
	}

	edges := []int{opts.TargetLongEdgePx, 1024, 768, opts.MinLongEdgePx, 384, 256, 192, 128}
	qualities := []int{80, 70, 60, 50, opts.MinQuality, 30, 20, 10}

	for _, edge := range edges {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, "", err
		}
		resized := resizeIfBigger(src, edge)
		for _, q := range qualities {
			if err := ctx.Err(); err != nil {
				return nil, 0, 0, "", err
			}
			buf, err := encodeFor(resized, iterFormat, q, hasAlpha)
			if err != nil {
				continue
			}
			if int64(buf.Len()) <= opts.TargetMaxBytes {
				if int64(buf.Len()) >= d.SizeBytes {
					return nil, 0, 0, "", errEncodeBigger
				}
				return buf, edge, q, iterFormat, nil
			}
		}
	}

	// Even the most aggressive combination (128×q=10) failed to fit budget.
	// This is essentially unreachable for real images. Surface as
	// errEncodeBigger so the caller keeps the original — accepting a
	// >budget output would violate the strict 80KB contract.
	return nil, 0, 0, "", errEncodeBigger
}

func resizeIfBigger(src image.Image, maxLongEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	long := w
	if h > w {
		long = h
	}
	if long <= maxLongEdge {
		return src
	}
	if w >= h {
		return imaging.Resize(src, maxLongEdge, 0, imaging.Lanczos)
	}
	return imaging.Resize(src, 0, maxLongEdge, imaging.Lanczos)
}

// encodeFor dispatches encoding by output format.
//   - "jpeg": baseline JPEG; hasAlpha 时 flatten 白底
//   - "webp": cgo libwebp 有损（非 cgo 构建返回 errWebPUnavailable）
//   - "png8": 纯 Go 调色板量化（无损索引；q 忽略）
func encodeFor(img image.Image, format string, q int, hasAlpha bool) (*bytes.Buffer, error) {
	switch format {
	case "webp":
		b, err := encodeWebP(img, q, hasAlpha)
		if err != nil {
			return nil, err
		}
		return bytes.NewBuffer(b), nil
	case "png8":
		// PNG8 走兜底/上传场景时允许抖动（更平滑）；OCR 路径单独调 encodePNG8(dither=false)。
		b, err := encodePNG8(img, 256, hasAlpha, true)
		if err != nil {
			return nil, err
		}
		return bytes.NewBuffer(b), nil
	case "jpeg":
		if hasAlpha {
			return encodeJPEGFlattenAlpha(img, q, jpegBackground)
		}
		return encodeJPEG(img, q)
	default:
		return nil, fmt.Errorf("imageopt: unknown output format %q", format)
	}
}

// jpegBackground is the flatten color for transparent source pixels when
// encoding JPEG. White is the conventional fallback for screenshots.
var jpegBackground = nrgbaWhite()
