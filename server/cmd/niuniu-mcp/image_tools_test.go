package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg" // register JPEG decoder for image.DecodeConfig on OCR temp files
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// resetOCRSeams restores the package-level test seams + lang cache after a
// test mutates them, so tests stay independent regardless of order.
func resetOCRSeams(t *testing.T) {
	t.Helper()
	origLook, origRun := ocrLookPath, ocrRun
	t.Cleanup(func() {
		ocrLookPath = origLook
		ocrRun = origRun
		langOnce = sync.Once{}
		cachedLang = ""
	})
	langOnce = sync.Once{}
	cachedLang = ""
}

// writeTinyPNG writes a small valid PNG and returns its path.
func writeTinyPNG(t *testing.T, dir string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 60), G: uint8(y * 60), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	p := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return p
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func hasImage(res *mcp.CallToolResult) bool {
	for _, c := range res.Content {
		if _, ok := c.(mcp.ImageContent); ok {
			return true
		}
	}
	return false
}

func TestReadImage_OCRInstalled_ReturnsText(t *testing.T) {
	resetOCRSeams(t)
	dir := t.TempDir()
	img := writeTinyPNG(t, dir)

	ocrLookPath = func(name string) (string, error) {
		if name == "tesseract" {
			return "/usr/bin/tesseract", nil
		}
		return "", errors.New("not found")
	}
	ocrRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "--list-langs" {
			return []byte("List of available languages (2):\neng\nchi_sim\n"), nil
		}
		return []byte("Hello 牛牛 OCR\n"), nil
	}

	res := readImage(context.Background(), img, "auto", "")
	if res.IsError {
		t.Fatalf("unexpected error result: %s", textOf(t, res))
	}
	out := textOf(t, res)
	if !strings.Contains(out, "Hello 牛牛 OCR") {
		t.Fatalf("OCR text missing: %q", out)
	}
	if !strings.Contains(out, "[OCR") || !strings.Contains(out, "eng+chi_sim") {
		t.Fatalf("OCR header/lang missing: %q", out)
	}
	if hasImage(res) {
		t.Fatalf("OCR-success result should be text-only, got an image block")
	}
}

// writeWidePNG writes a PNG whose long edge exceeds ocrMaxLongEdgePx so the
// OCR path must downsample it before invoking tesseract.
func writeWidePNG(t *testing.T, dir string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	p := filepath.Join(dir, "wide.png")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return p
}

func TestReadImage_OCR_DownsamplesLargeImageBeforeTesseract(t *testing.T) {
	resetOCRSeams(t)
	dir := t.TempDir()
	orig := writeWidePNG(t, dir, 3300, 200) // long edge 3300 > 2200 cap

	ocrLookPath = func(string) (string, error) { return "/usr/bin/tesseract", nil }
	var gotImagePath string
	var gotWidth int
	// Inspect the OCR input *inside* the stub: readImage's defer cleanup()
	// deletes the temp file as soon as it returns, so we must decode it here
	// while it still exists.
	ocrRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "--list-langs" {
			return []byte("eng\nchi_sim\n"), nil
		}
		if len(args) > 0 {
			gotImagePath = args[0] // first positional arg is the image path
			if f, err := os.Open(gotImagePath); err == nil {
				if cfg, _, derr := image.DecodeConfig(f); derr == nil {
					gotWidth = cfg.Width
				}
				f.Close()
			}
		}
		return []byte("downsampled text\n"), nil
	}

	res := readImage(context.Background(), orig, "auto", "")
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	if gotImagePath == "" || gotImagePath == orig {
		t.Fatalf("OCR should run on a downsampled temp file, not the original; got %q", gotImagePath)
	}
	if gotWidth == 0 || gotWidth > ocrMaxLongEdgePx {
		t.Fatalf("downsampled long edge = %d, want in (0, %d]", gotWidth, ocrMaxLongEdgePx)
	}
	if !strings.Contains(textOf(t, res), "已降采样") {
		t.Fatalf("header should note the downsample: %q", textOf(t, res))
	}
}

func TestReadImage_OCRMissing_DegradesToVision(t *testing.T) {
	resetOCRSeams(t)
	dir := t.TempDir()
	img := writeTinyPNG(t, dir)

	ocrLookPath = func(string) (string, error) { return "", errors.New("not found") }
	ocrRun = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ocrRun must not be called when tesseract is missing")
		return nil, nil
	}

	res := readImage(context.Background(), img, "auto", "")
	if res.IsError {
		t.Fatalf("missing OCR must NOT error: %s", textOf(t, res))
	}
	if !hasImage(res) {
		t.Fatalf("degrade path must include an image content block")
	}
	out := textOf(t, res)
	if !strings.Contains(out, ocrGuideURL) {
		t.Fatalf("degrade note must carry the install guide link: %q", out)
	}
}

func TestReadImage_ModeOCR_MissingEngine_DegradesWithGuide(t *testing.T) {
	resetOCRSeams(t)
	dir := t.TempDir()
	img := writeTinyPNG(t, dir)
	ocrLookPath = func(string) (string, error) { return "", errors.New("not found") }

	res := readImage(context.Background(), img, "ocr", "")
	if res.IsError {
		t.Fatalf("mode=ocr with missing engine must degrade, not error: %s", textOf(t, res))
	}
	if !hasImage(res) || !strings.Contains(textOf(t, res), ocrGuideURL) {
		t.Fatalf("mode=ocr degrade must include image + guide link: %q", textOf(t, res))
	}
}

func TestReadImage_ModeVision_SkipsOCR(t *testing.T) {
	resetOCRSeams(t)
	dir := t.TempDir()
	img := writeTinyPNG(t, dir)

	// Even with a "present" engine, mode=vision must not invoke OCR.
	ocrLookPath = func(string) (string, error) { return "/usr/bin/tesseract", nil }
	ocrRun = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("mode=vision must not run OCR")
		return nil, nil
	}

	res := readImage(context.Background(), img, "vision", "")
	if res.IsError || !hasImage(res) {
		t.Fatalf("mode=vision should return an image, got err=%v text=%q", res.IsError, textOf(t, res))
	}
	// vision mode does not nag about OCR install.
	if strings.Contains(textOf(t, res), ocrGuideURL) {
		t.Fatalf("mode=vision should not append the OCR install note")
	}
}

func TestReadImage_OCREmpty_DegradesToVision(t *testing.T) {
	resetOCRSeams(t)
	dir := t.TempDir()
	img := writeTinyPNG(t, dir)

	ocrLookPath = func(string) (string, error) { return "/usr/bin/tesseract", nil }
	ocrRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "--list-langs" {
			return []byte("eng\n"), nil
		}
		return []byte("   \n"), nil // whitespace only -> no usable text
	}

	res := readImage(context.Background(), img, "auto", "")
	if res.IsError {
		t.Fatalf("empty OCR must degrade, not error: %s", textOf(t, res))
	}
	if !hasImage(res) {
		t.Fatalf("empty OCR must degrade to a vision image block")
	}
	if !strings.Contains(textOf(t, res), "未识别出文本") {
		t.Fatalf("empty-OCR note missing: %q", textOf(t, res))
	}
}

func TestReadImage_NonImage_Errors(t *testing.T) {
	resetOCRSeams(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(p, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := readImage(context.Background(), p, "auto", "")
	if !res.IsError {
		t.Fatalf("non-image input should be an MCP error result")
	}
}

func TestReadImage_MissingFile_Errors(t *testing.T) {
	resetOCRSeams(t)
	res := readImage(context.Background(), filepath.Join(t.TempDir(), "nope.png"), "auto", "")
	if !res.IsError {
		t.Fatalf("missing file should be an MCP error result")
	}
}

func TestComputeOCRLangs_PrefersChineseAndEnglish(t *testing.T) {
	resetOCRSeams(t)
	ocrRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte("List of available languages (3):\nosd\nchi_tra\neng\nchi_sim\n"), nil
	}
	got := computeOCRLangs(context.Background(), "/usr/bin/tesseract")
	if got != "eng+chi_sim+chi_tra" {
		t.Fatalf("lang pick: got %q want eng+chi_sim+chi_tra", got)
	}
}

func TestComputeOCRLangs_OsdOnly_DoesNotSelectOsd(t *testing.T) {
	resetOCRSeams(t)
	ocrRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		// Only the orientation/script-detection data is present — not a real
		// OCR language. Picking it would make `tesseract -l osd` error out.
		return []byte("List of available languages (1):\nosd\n"), nil
	}
	got := computeOCRLangs(context.Background(), "/usr/bin/tesseract")
	if got == "osd" {
		t.Fatalf("computeOCRLangs must never select osd as a language, got %q", got)
	}
	if got != "eng" {
		t.Fatalf("osd-only install should fall back to eng, got %q", got)
	}
}

func TestComputeOCRLangs_ListFails_FallsBackToEng(t *testing.T) {
	resetOCRSeams(t)
	ocrRun = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("boom")
	}
	if got := computeOCRLangs(context.Background(), "/usr/bin/tesseract"); got != "eng" {
		t.Fatalf("fallback lang: got %q want eng", got)
	}
}

// writePNGFile writes a low-color (black/white checkerboard) PNG so the vision
// path picks PNG8 and the content block reports image/png rather than the old
// hardcoded image/jpeg.
func writePNGFile(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if (x/10+y/10)%2 == 0 {
				c = color.RGBA{0, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
}

func TestEncodeImageForVision_LowColorReturnsPNG(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.png")
	writePNGFile(t, p, 2000, 1500) // low-color big image -> png8

	_, mime, err := encodeImageForVision(context.Background(), p, "image/png")
	if err != nil {
		t.Fatalf("encodeImageForVision: %v", err)
	}
	if mime != "image/png" {
		t.Fatalf("mime=%q want image/png", mime)
	}
}
