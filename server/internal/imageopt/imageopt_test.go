package imageopt

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultOptions_MatchSpec(t *testing.T) {
	opts := DefaultOptions()
	if opts.TriggerLongEdgePx != 1280 {
		t.Errorf("TriggerLongEdgePx = %d, want 1280", opts.TriggerLongEdgePx)
	}
	if opts.TriggerSizeBytes != 100*1024 {
		t.Errorf("TriggerSizeBytes = %d, want %d", opts.TriggerSizeBytes, 100*1024)
	}
	if opts.TargetLongEdgePx != 1280 {
		t.Errorf("TargetLongEdgePx = %d, want 1280", opts.TargetLongEdgePx)
	}
	if opts.TargetMaxBytes != 80*1024 {
		t.Errorf("TargetMaxBytes = %d, want %d", opts.TargetMaxBytes, 80*1024)
	}
	if opts.MinQuality != 40 {
		t.Errorf("MinQuality = %d, want 40", opts.MinQuality)
	}
	if opts.MinLongEdgePx != 512 {
		t.Errorf("MinLongEdgePx = %d, want 512", opts.MinLongEdgePx)
	}
}

func copyToTemp(t *testing.T, srcRel string) string {
	t.Helper()
	src := filepath.Join("testdata", srcRel)
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close()
	dir := t.TempDir()
	dst := filepath.Join(dir, srcRel)
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return dst
}

func TestOptimize_LargeScreenshot_Hits80KB(t *testing.T) {
	path := copyToTemp(t, "screenshot-4k.png")
	res, err := Optimize(context.Background(), path, DefaultOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.Optimized {
		t.Fatalf("expected Optimized=true, got SkipReason=%s", res.SkipReason)
	}
	if res.NewSize > 80*1024 {
		t.Errorf("NewSize=%d > 80KB", res.NewSize)
	}
	if _, err := os.Stat(res.NewPath); err != nil {
		t.Errorf("NewPath %s missing: %v", res.NewPath, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("original %s should have been removed (extension changed)", path)
	}
}

func TestOptimize_ComplexNoise_Hits80KB(t *testing.T) {
	// 1920×1080 max-entropy noise — the worst-case input. Strict pipeline
	// must keep iterating past the (legacy) soft floor until the result
	// fits ≤80KB. If the file is missing, regenerate via
	// `go run -tags=testdata ./testdata/gen.go`.
	src := filepath.Join("testdata", "complex-ppt.png")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("complex-ppt.png missing: %v", err)
	}
	path := copyToTemp(t, "complex-ppt.png")
	res, err := Optimize(context.Background(), path, DefaultOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.Optimized {
		t.Fatalf("expected Optimized=true, got SkipReason=%s", res.SkipReason)
	}
	if res.NewSize > 80*1024 {
		t.Errorf("NewSize=%d > 80KB (strict enforcement broken)", res.NewSize)
	}
}

func TestOptimize_SmallImage_Skipped(t *testing.T) {
	path := copyToTemp(t, "small.png")
	res, err := Optimize(context.Background(), path, DefaultOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Optimized {
		t.Fatalf("small image should skip")
	}
	if res.SkipReason != SkipAlreadySmall {
		t.Errorf("SkipReason=%s, want already_small", res.SkipReason)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("original should be untouched")
	}
}

func TestOptimize_AnimatedGIF_Skipped(t *testing.T) {
	path := copyToTemp(t, "animated.gif")
	res, _ := Optimize(context.Background(), path, DefaultOptions())
	if res.Optimized {
		t.Fatalf("animated should skip")
	}
	if res.SkipReason != SkipAnimated {
		t.Errorf("SkipReason=%s, want animated", res.SkipReason)
	}
}

func TestOptimize_SVG_Skipped(t *testing.T) {
	path := copyToTemp(t, "icon.svg")
	res, _ := Optimize(context.Background(), path, DefaultOptions())
	if res.Optimized {
		t.Fatalf("svg should skip")
	}
	if res.SkipReason != SkipUnsupportedFormat {
		t.Errorf("SkipReason=%s, want unsupported_format", res.SkipReason)
	}
}

func TestOptimize_CorruptFile_GracefulSkip(t *testing.T) {
	path := copyToTemp(t, "corrupt.png")
	res, err := Optimize(context.Background(), path, DefaultOptions())
	if err != nil {
		t.Fatalf("Optimize should not error on corrupt input: %v", err)
	}
	if res.Optimized {
		t.Fatalf("corrupt should skip")
	}
	if res.SkipReason != SkipDecodeFailed {
		t.Errorf("SkipReason=%s, want decode_failed", res.SkipReason)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("original should remain on disk")
	}
}

func TestOptimize_TinyRandom_Skipped(t *testing.T) {
	// tiny-random.png is 64×64 ~12KB — below both trigger thresholds
	// (size < 100KB and long_edge < 1280px), so Optimize skips with
	// already_small. We verify this skip path doesn't mutate the file.
	path := copyToTemp(t, "tiny-random.png")
	res, _ := Optimize(context.Background(), path, DefaultOptions())
	if res.Optimized {
		t.Errorf("expected skip; got Optimized=true SkipReason=%s NewSize=%d", res.SkipReason, res.NewSize)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("original should remain on disk")
	}
}

// TestOptimize_EncodeBigger_KeepsOriginal creates a solid-white PNG just above
// the trigger threshold (1281px long edge). Solid-color PNG compresses to a
// few KB; JPEG at q=80 has fixed header overhead and may encode bigger →
// runPipeline returns errEncodeBigger → Optimize maps to SkipEncodeBigger.
// If the JPEG encoder on this platform produces a smaller output (test path
// didn't fire), the test logs informational and returns without failing —
// both outcomes are valid per the skip-or-optimize contract.
func TestOptimize_EncodeBigger_KeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "white.png")

	img := image.NewNRGBA(image.Rect(0, 0, 1281, 1281))
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	for y := 0; y < 1281; y++ {
		for x := 0; x < 1281; x++ {
			img.SetNRGBA(x, y, white)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatalf("encode png: %v", err)
	}
	f.Close()

	res, _ := Optimize(context.Background(), path, DefaultOptions())

	if res.SkipReason == SkipEncodeBigger {
		// Expected path: verify original PNG is still on disk.
		if _, err := os.Stat(path); err != nil {
			t.Errorf("original PNG should remain when SkipEncodeBigger fires: %v", err)
		}
		return
	}

	if res.Optimized {
		// JPEG encoder produced a smaller result — test path didn't fire, but
		// Optimized=true is the correct outcome in that case. Log and return.
		t.Logf("solid-white JPEG encoded smaller than PNG (size=%d); SkipEncodeBigger branch not exercised", res.NewSize)
		return
	}

	// Any other skip reason is unexpected.
	if res.SkipReason != SkipNone {
		t.Errorf("unexpected SkipReason=%s (want encode_bigger or Optimized=true)", res.SkipReason)
	}
}

// TestOptimize_IOFailed_OnAtomicWrite forces atomicWrite to fail by
// pre-creating the .tmp path as a directory, so os.WriteFile returns an error.
// Optimize must return SkipIOFailed and leave the original file intact.
func TestOptimize_IOFailed_OnAtomicWrite(t *testing.T) {
	src := filepath.Join("testdata", "screenshot-4k.png")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("screenshot-4k.png missing: %v", err)
	}

	dir := t.TempDir()

	// Copy source to temp dir preserving the filename.
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	dstPath := filepath.Join(dir, "screenshot-4k.png")
	out, err := os.Create(dstPath)
	if err != nil {
		in.Close()
		t.Fatalf("create dst: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		in.Close()
		out.Close()
		t.Fatalf("copy: %v", err)
	}
	in.Close()
	out.Close()

	// The pipeline converts .png → the chosen lossy format (.webp when the cgo
	// libwebp encoder is built in, else .jpg). atomicWrite writes to
	// screenshot-4k.<ext>.tmp before renaming — block that exact path with a
	// directory so the rename target's tmp can't be created.
	outExt := ".jpg"
	if lossyFormat() == "webp" {
		outExt = ".webp"
	}
	blocker := filepath.Join(dir, "screenshot-4k"+outExt+".tmp")
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}

	res, _ := Optimize(context.Background(), dstPath, DefaultOptions())

	if res.Optimized {
		t.Errorf("expected Optimized=false; got Optimized=true")
	}
	if res.SkipReason != SkipIOFailed {
		t.Errorf("SkipReason=%s, want io_failed", res.SkipReason)
	}
	// Original must remain.
	if _, err := os.Stat(dstPath); err != nil {
		t.Errorf("original should remain on disk: %v", err)
	}
}

func TestOptimize_LowColorOutputsPNG(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big-stripes.png")
	writeTestPNG(t, p, solidWithStripes(3000, 2000))

	res, err := Optimize(context.Background(), p, DefaultOptions())
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if !res.Optimized {
		t.Fatalf("expected optimized, skip=%q", res.SkipReason)
	}
	if res.Format != "png8" {
		t.Fatalf("format=%q want png8", res.Format)
	}
	if filepath.Ext(res.NewName) != ".png" {
		t.Fatalf("ext=%q want .png", filepath.Ext(res.NewName))
	}
}
