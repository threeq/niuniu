package imageopt

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func writeTestPNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func TestRunPipeline_LowColorPicksPNG8(t *testing.T) {
	// 少色大图：应选 png8 且一次命中 80KB。
	dir := t.TempDir()
	p := filepath.Join(dir, "stripes.png")
	writeTestPNG(t, p, solidWithStripes(1000, 800))

	buf, _, _, format, err := runPipeline(context.Background(), p, DefaultOptions())
	if err != nil {
		t.Fatalf("runPipeline: %v", err)
	}
	if format != "png8" {
		t.Fatalf("format=%q want png8", format)
	}
	if int64(buf.Len()) > DefaultOptions().TargetMaxBytes {
		t.Fatalf("size %d over budget", buf.Len())
	}
}

func TestPipeline_Large4KScreenshot_Under80KB(t *testing.T) {
	src := filepath.Join("testdata", "screenshot-4k.png")
	out, edge, q, format, err := runPipeline(context.Background(), src, DefaultOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if want := lossyFormat(); format != want {
		t.Errorf("Format=%q, want %q", format, want)
	}
	if out.Len() > int(DefaultOptions().TargetMaxBytes) {
		t.Errorf("output %d bytes > target 80KB", out.Len())
	}
	if edge > 1280 {
		t.Errorf("edge %d > 1280", edge)
	}
	t.Logf("quality settled at %d, edge=%d, size=%d bytes (informational)", q, edge, out.Len())
}

func TestPipeline_ImpossibleBudget_ReturnsEncodeBigger(t *testing.T) {
	// Force every iteration (including the most aggressive 128×q=10) to
	// exceed budget by setting an impossibly small target. The strict
	// pipeline must surface errEncodeBigger rather than ship an oversized
	// result.
	opts := DefaultOptions()
	opts.TargetMaxBytes = 1 // no JPEG can ever encode below 1 byte

	src := filepath.Join("testdata", "screenshot-mobile.png")
	_, _, _, _, err := runPipeline(context.Background(), src, opts)
	if err == nil {
		t.Fatalf("expected errEncodeBigger when budget is unreachable, got nil")
	}
	if !errors.Is(err, errEncodeBigger) {
		t.Errorf("expected errEncodeBigger, got %v", err)
	}
}

func TestPipeline_ComplexNoise_StaysUnder80KB(t *testing.T) {
	// complex-ppt.png is 1920×1080 max-entropy noise — the worst case the
	// old soft-floor branch accepted as >80KB. The new strict pipeline
	// must keep iterating (smaller dims, lower quality) until ≤80KB.
	src := filepath.Join("testdata", "complex-ppt.png")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("complex-ppt.png missing: %v", err)
	}
	out, edge, q, format, err := runPipeline(context.Background(), src, DefaultOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// High-entropy noise is many-colored → pickFormat selects WebP; under a
	// non-cgo build WebP is unavailable and the pipeline degrades to JPEG.
	// The invariant under test is strict ≤80KB enforcement, not the format.
	if want := lossyFormat(); format != want {
		t.Errorf("Format=%q, want %q", format, want)
	}
	if out.Len() > int(DefaultOptions().TargetMaxBytes) {
		t.Errorf("output %d bytes > target 80KB (strict enforcement broken)", out.Len())
	}
	t.Logf("complex-ppt settled at edge=%d quality=%d size=%d (informational)", edge, q, out.Len())
}

func TestPipeline_TransparentPNG_JPEGFlatten(t *testing.T) {
	src := filepath.Join("testdata", "transparent-icon.png")
	_, _, _, format, err := runPipeline(context.Background(), src, DefaultOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Many-colored transparent source → WebP (preserves alpha) when the cgo
	// libwebp encoder is built in; otherwise the pipeline degrades to JPEG with
	// alpha flattened (the documented non-cgo fallback).
	if want := lossyFormat(); format != want {
		t.Errorf("Format=%q, want %q", format, want)
	}
}

// lossyFormat reports the lossy output format the pipeline will settle on for a
// many-colored image given the current build: "webp" when the cgo libwebp
// encoder is available, else "jpeg" (the non-cgo degrade path). Lets format
// assertions stay correct on both cgo and non-cgo builds.
func lossyFormat() string {
	if _, err := encodeWebP(image.NewRGBA(image.Rect(0, 0, 2, 2)), 80, false); err == nil {
		return "webp"
	}
	return "jpeg"
}

func TestPipeline_CtxCanceled_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	src := filepath.Join("testdata", "screenshot-4k.png")
	_, _, _, _, err := runPipeline(ctx, src, DefaultOptions())
	if err == nil {
		t.Errorf("expected ctx.Canceled error, got nil")
	}
}
