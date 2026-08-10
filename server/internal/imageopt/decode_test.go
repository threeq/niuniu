package imageopt

import (
	"path/filepath"
	"testing"
)

func TestDecodeImage_PNG_Opaque(t *testing.T) {
	img, hasAlpha, err := decodeImage(filepath.Join("testdata", "small.png"), "png")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if img == nil {
		t.Fatalf("nil image")
	}
	if hasAlpha {
		t.Errorf("small.png should be reported opaque")
	}
}

func TestDecodeImage_PNG_Transparent(t *testing.T) {
	img, hasAlpha, err := decodeImage(filepath.Join("testdata", "transparent-icon.png"), "png")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if img == nil {
		t.Fatalf("nil image")
	}
	if !hasAlpha {
		t.Errorf("transparent-icon.png should be reported as having alpha")
	}
}

func TestDecodeImage_Corrupt(t *testing.T) {
	_, _, err := decodeImage(filepath.Join("testdata", "corrupt.png"), "png")
	if err == nil {
		t.Errorf("expected error on corrupt png")
	}
}

func TestHasNonOpaquePixel_OpaqueImage(t *testing.T) {
	img, _, _ := decodeImage(filepath.Join("testdata", "small.png"), "png")
	if hasNonOpaquePixel(img) {
		t.Errorf("should be opaque")
	}
}

// TestDecodeImage_WebP_Coverage: WebP decode coverage relies on the
// golang.org/x/image/webp library's own tests plus the integration path in
// optimizeLocked (exercised via TestOptimize_* on WebP inputs when available).
// Constructing a valid VP8L bitstream by hand is fragile (LZ77 + Huffman
// entropy coding), and the project has no committed WebP fixture. The
// detect-side WebP paths (animated / static sniff) are covered by
// TestDetect_* tests. Skipped as M-C — document gap here rather than risk
// a flaky hand-crafted byte literal.
func TestDecodeImage_WebP_Deferred(t *testing.T) {
	t.Skip("M-C deferred: no committed WebP fixture and VP8L hand-crafting is fragile — see comment above")
}
