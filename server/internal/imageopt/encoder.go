package imageopt

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
)

// errWebPUnavailable: 非 cgo 构建无 libwebp 时 encodeWebP 返回此错误，
// 调用方据此降级到 JPEG/PNG8（见 encode_webp_nocgo.go / pipeline.go）。
var errWebPUnavailable = errors.New("imageopt: webp encoder unavailable (built without cgo)")

// encodeJPEG encodes the source image as JPEG at the given quality (1-100).
// Source alpha is ignored by encoding/jpeg (JPEG has no alpha channel);
// for sources with real transparency, callers should use
// encodeJPEGFlattenAlpha to control the composited background color.
func encodeJPEG(img image.Image, quality int) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return &buf, nil
}

// encodeJPEGFlattenAlpha composites src onto a solid background and
// JPEG-encodes the result. Used when the source has transparency
// (HasAlpha=true from hasNonOpaquePixel) and we still want JPEG output
// (no lossy WebP available per DECISION.md).
//
// bg is typically color.White (the conventional fallback for screenshots)
// but callers can pass a different color (e.g. theme-aware) if needed.
func encodeJPEGFlattenAlpha(img image.Image, quality int, bg color.Color) (*bytes.Buffer, error) {
	b := img.Bounds()
	out := image.NewRGBA(b)
	// Draw the solid background first, then composite the source over it.
	draw.Draw(out, b, image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(out, b, img, b.Min, draw.Over)
	return encodeJPEG(out, quality)
}
