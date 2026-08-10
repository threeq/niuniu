package imageopt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetect_PNG(t *testing.T) {
	got, err := detect(filepath.Join("testdata", "small.png"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Format != "png" {
		t.Errorf("Format=%q, want png", got.Format)
	}
	if got.Animated {
		t.Errorf("png should not be animated")
	}
}

func TestDetect_SVG(t *testing.T) {
	got, err := detect(filepath.Join("testdata", "icon.svg"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Format != "svg" {
		t.Errorf("Format=%q, want svg", got.Format)
	}
}

func TestDetect_AnimatedGIF(t *testing.T) {
	got, err := detect(filepath.Join("testdata", "animated.gif"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Format != "gif" {
		t.Errorf("Format=%q, want gif", got.Format)
	}
	if !got.Animated {
		t.Errorf("gif with multiple frames should be Animated")
	}
}

func TestDetect_CorruptPNG_FormatStillPNG(t *testing.T) {
	got, err := detect(filepath.Join("testdata", "corrupt.png"))
	if err != nil {
		t.Fatalf("detect should not error on magic-only check: %v", err)
	}
	if got.Format != "png" {
		t.Errorf("Format=%q, want png (magic match)", got.Format)
	}
}

func TestDetect_JPEG(t *testing.T) {
	got, err := detect(filepath.Join("testdata", "photo.jpg"))
	if err != nil {
		// photo.jpg is generated locally and may be absent on a fresh checkout.
		if os.IsNotExist(err) {
			t.Skip("testdata/photo.jpg not generated; run: cd server/internal/imageopt && go run -tags=testdata ./testdata/gen.go")
		}
		t.Fatalf("err: %v", err)
	}
	if got.Format != "jpeg" {
		t.Errorf("Format=%q, want jpeg", got.Format)
	}
	if got.Animated {
		t.Errorf("jpeg should not be animated")
	}
}

func TestDetect_StaticWebPInline(t *testing.T) {
	// 30 bytes: RIFF(4) + size(4) + WEBP(4) + VP8X(4) + chunk_size(4) +
	// flags_byte(0x00 — no animation flag) + reserved(3) + canvas_w(3) + canvas_h(3)
	// This exercises the VP8X branch without needing a fixture file.
	dir := t.TempDir()
	path := filepath.Join(dir, "static.webp")
	data := []byte{
		'R', 'I', 'F', 'F',
		0x00, 0x00, 0x00, 0x00, // file size — unused by detect
		'W', 'E', 'B', 'P',
		'V', 'P', '8', 'X',
		0x0a, 0x00, 0x00, 0x00, // chunk size — unused by detect (was previously the dead assignment)
		0x00,                   // flags: animation bit (0x02) NOT set
		0x00, 0x00, 0x00,       // reserved
		0x00, 0x00, 0x00,       // canvas width minus 1
		0x00, 0x00, 0x00,       // canvas height minus 1
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := detect(path)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Format != "webp" {
		t.Errorf("Format=%q, want webp", got.Format)
	}
	if got.Animated {
		t.Errorf("static WebP should not be Animated")
	}
}

func TestDetect_AnimatedWebPInline(t *testing.T) {
	// Same layout as static, but with animation flag bit set (0x02).
	dir := t.TempDir()
	path := filepath.Join(dir, "anim.webp")
	data := []byte{
		'R', 'I', 'F', 'F',
		0x00, 0x00, 0x00, 0x00,
		'W', 'E', 'B', 'P',
		'V', 'P', '8', 'X',
		0x0a, 0x00, 0x00, 0x00,
		0x02,             // flags: animation bit set
		0x00, 0x00, 0x00,
		0x00, 0x00, 0x00,
		0x00, 0x00, 0x00,
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := detect(path)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Format != "webp" {
		t.Errorf("Format=%q, want webp", got.Format)
	}
	if !got.Animated {
		t.Errorf("animated WebP should be Animated=true")
	}
}
