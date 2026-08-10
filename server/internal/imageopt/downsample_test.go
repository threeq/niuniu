package imageopt

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writeNoisePNG writes a w×h PNG of high-frequency pseudo-noise (so JPEG
// re-encoding is a real, non-trivial reduction) and returns its path.
func writeNoisePNG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(2166136261)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = (seed ^ uint32(x*31+y*17)) * 16777619
			img.SetNRGBA(x, y, color.NRGBA{uint8(seed), uint8(seed >> 8), uint8(seed >> 16), 255})
		}
	}
	p := filepath.Join(t.TempDir(), "noise.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDownsampleForVision_LargePNG_CapsLongEdge(t *testing.T) {
	path := writeNoisePNG(t, 3000, 2000)
	res, err := DownsampleForVision(context.Background(), path, DefaultVisionOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.SkipReason != SkipNone {
		t.Fatalf("expected a downsample, got SkipReason=%s", res.SkipReason)
	}
	if res.OrigLongEdge != 3000 {
		t.Errorf("OrigLongEdge=%d, want 3000", res.OrigLongEdge)
	}
	if res.LongEdge != DefaultVisionMaxEdge {
		t.Errorf("LongEdge=%d, want %d", res.LongEdge, DefaultVisionMaxEdge)
	}
	// 3000×2000 → long edge 1568 → width 1568, height 1045 (aspect preserved).
	if res.Width != DefaultVisionMaxEdge {
		t.Errorf("Width=%d, want %d", res.Width, DefaultVisionMaxEdge)
	}
	// Noise picks webp under cgo, jpeg under !cgo — decode generically
	// (the webp decoder is registered via decode.go's import) and assert
	// against the format the result actually reports rather than a hardcoded
	// jpeg, so this stays green on the production cgo build too.
	img, decFmt, err := image.Decode(bytes.NewReader(res.Encoded))
	if err != nil {
		t.Fatalf("output (%s) failed to decode: %v", res.OutFormat, err)
	}
	if res.OutFormat == "jpeg" && decFmt != "jpeg" {
		t.Errorf("OutFormat=jpeg but decoded format=%q", decFmt)
	}
	if img.Bounds().Dx() != res.Width || img.Bounds().Dy() != res.Height {
		t.Errorf("decoded dims %dx%d disagree with result %dx%d",
			img.Bounds().Dx(), img.Bounds().Dy(), res.Width, res.Height)
	}
	if res.NewSize >= res.OrigSize {
		t.Errorf("downsample should shrink bytes: new=%d orig=%d", res.NewSize, res.OrigSize)
	}
}

func TestDownsampleForVision_SmallImage_StillEncodesNoUpscale(t *testing.T) {
	// read_image always hands the model a viewable image; a small source is
	// re-encoded as-is (never upscaled) rather than skipped.
	path := writeNoisePNG(t, 100, 80)
	res, err := DownsampleForVision(context.Background(), path, DefaultVisionOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.SkipReason != SkipNone {
		t.Fatalf("small image should still encode, got SkipReason=%s", res.SkipReason)
	}
	if res.LongEdge != 100 || res.Width != 100 || res.Height != 80 {
		t.Errorf("small image must not be upscaled: got %dx%d (long %d)", res.Width, res.Height, res.LongEdge)
	}
	if len(res.JPEG) == 0 {
		t.Error("expected JPEG bytes for small image")
	}
}

func TestDownsampleForVision_RespectsMaxEdgeOption(t *testing.T) {
	path := writeNoisePNG(t, 2000, 1000)
	res, err := DownsampleForVision(context.Background(), path, VisionOptions{MaxLongEdgePx: 800})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.LongEdge != 800 || res.Width != 800 || res.Height != 400 {
		t.Errorf("got %dx%d (long %d), want 800x400", res.Width, res.Height, res.LongEdge)
	}
}

func TestDownsampleForVision_Corrupt_SkipsDecode(t *testing.T) {
	res, err := DownsampleForVision(context.Background(), copyToTemp(t, "corrupt.png"), DefaultVisionOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.SkipReason != SkipDecodeFailed {
		t.Fatalf("corrupt image: SkipReason=%s, want decode_failed", res.SkipReason)
	}
	if res.JPEG != nil {
		t.Error("corrupt image should yield no JPEG")
	}
}

func TestDownsampleForVision_SVG_Unsupported(t *testing.T) {
	res, err := DownsampleForVision(context.Background(), copyToTemp(t, "icon.svg"), DefaultVisionOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.SkipReason != SkipUnsupportedFormat {
		t.Fatalf("svg: SkipReason=%s, want unsupported_format", res.SkipReason)
	}
}

func TestDownsampleForVision_Missing_IOFailed(t *testing.T) {
	res, err := DownsampleForVision(context.Background(), filepath.Join(t.TempDir(), "nope.png"), DefaultVisionOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.SkipReason != SkipIOFailed {
		t.Fatalf("missing file: SkipReason=%s, want io_failed", res.SkipReason)
	}
}

func TestDownsampleForVision_AnimatedGIF_UsesFirstFrame(t *testing.T) {
	// Animation is meaningless for a still vision payload; we down-sample the
	// first frame rather than bailing to Read (which is what we're avoiding).
	res, err := DownsampleForVision(context.Background(), copyToTemp(t, "animated.gif"), DefaultVisionOptions())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.SkipReason != SkipNone {
		t.Fatalf("animated gif first frame should encode, got SkipReason=%s", res.SkipReason)
	}
	if len(res.JPEG) == 0 {
		t.Error("expected JPEG bytes from animated gif first frame")
	}
}

func TestDownsampleForVision_PicksFormatAndMIME(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "stripes.png")
	writeTestPNG(t, p, solidWithStripes(2000, 1500))

	res, _ := DownsampleForVision(context.Background(), p, DefaultVisionOptions())
	if res.SkipReason != SkipNone {
		t.Fatalf("skip=%q", res.SkipReason)
	}
	if res.OutFormat != "png8" || res.OutMIME != "image/png" {
		t.Fatalf("got format=%q mime=%q, want png8/image/png", res.OutFormat, res.OutMIME)
	}
	if res.LongEdge > DefaultVisionMaxEdge {
		t.Fatalf("long edge %d exceeds cap", res.LongEdge)
	}
	if len(res.Encoded) == 0 {
		t.Fatal("empty encoded output")
	}
}

func TestEncodeForOCR_LowColorIsPNGNoDither(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.png")
	writeTestPNG(t, p, solidWithStripes(3000, 1000)) // 长边 3000 > 2200

	data, ext, edge, err := EncodeForOCR(context.Background(), p, 2200)
	if err != nil {
		t.Fatalf("EncodeForOCR: %v", err)
	}
	if ext != ".png" {
		t.Fatalf("ext=%q want .png", ext)
	}
	if edge != 3000 {
		t.Fatalf("orig edge=%d want 3000", edge)
	}
	if len(data) == 0 {
		t.Fatal("empty")
	}
}

// TestEncodeForOCR_Corrupt_ReturnsErrorNoPanic guards the recover()/error
// contract: a corrupt image must surface as an error (so ocrInputPath falls
// back to the original path) and must never panic up into the MCP process.
func TestEncodeForOCR_Corrupt_ReturnsErrorNoPanic(t *testing.T) {
	data, _, _, err := EncodeForOCR(context.Background(), copyToTemp(t, "corrupt.png"), 2200)
	if err == nil {
		t.Fatal("corrupt image: want error, got nil")
	}
	if data != nil {
		t.Errorf("corrupt image should yield no data, got %d bytes", len(data))
	}
}

func TestDefaultVisionOptions_Defaults(t *testing.T) {
	o := DefaultVisionOptions()
	if o.MaxLongEdgePx != DefaultVisionMaxEdge {
		t.Errorf("MaxLongEdgePx=%d, want %d", o.MaxLongEdgePx, DefaultVisionMaxEdge)
	}
	if o.Quality != DefaultVisionQuality {
		t.Errorf("Quality=%d, want %d", o.Quality, DefaultVisionQuality)
	}
}
