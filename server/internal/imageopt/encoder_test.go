package imageopt

import (
	"image"
	"image/color"
	"testing"
)

func makeTestImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	return img
}

func TestEncodeJPEG_Q80(t *testing.T) {
	img := makeTestImage(200, 200)
	buf, err := encodeJPEG(img, 80)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatalf("empty output")
	}
	// JPEG magic: FF D8
	if buf.Bytes()[0] != 0xff || buf.Bytes()[1] != 0xd8 {
		t.Errorf("not a JPEG: %x %x", buf.Bytes()[0], buf.Bytes()[1])
	}
}

func TestEncodeJPEG_LowerQ_SmallerOutput(t *testing.T) {
	img := makeTestImage(800, 600)
	bufHigh, _ := encodeJPEG(img, 90)
	bufLow, _ := encodeJPEG(img, 30)
	if bufLow.Len() >= bufHigh.Len() {
		t.Errorf("q=30 (%d B) should be smaller than q=90 (%d B)", bufLow.Len(), bufHigh.Len())
	}
}

func TestEncodeJPEGFlattenAlpha_AlphaCompositesOnBackground(t *testing.T) {
	// Fully transparent source pixels should be composited onto the bg color
	// (white) — output is a valid JPEG and has no notion of alpha.
	img := image.NewNRGBA(image.Rect(0, 0, 50, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 50; x++ {
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 0}) // fully transparent black
		}
	}
	buf, err := encodeJPEGFlattenAlpha(img, 80, color.White)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatalf("empty output")
	}
	if buf.Bytes()[0] != 0xff || buf.Bytes()[1] != 0xd8 {
		t.Errorf("not a JPEG: %x %x", buf.Bytes()[0], buf.Bytes()[1])
	}
}

func TestEncodeFor_PNG8(t *testing.T) {
	buf, err := encodeFor(solidWithStripes(100, 80), "png8", 0, false)
	if err != nil {
		t.Fatalf("encodeFor png8: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("empty png8 output")
	}
}

func TestEncodeFor_UnknownFormatErrors(t *testing.T) {
	if _, err := encodeFor(gradient(10, 10), "bogus", 80, false); err == nil {
		t.Fatal("expected error for unknown format")
	}
}
