package imageopt

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// solidWithStripes 生成少色图：白底 + 几条黑线（模拟截图/文字）。
func solidWithStripes(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if x%20 == 0 {
				c = color.RGBA{0, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func TestEncodePNG8_OpaqueDecodesAndShrinks(t *testing.T) {
	src := solidWithStripes(400, 300)
	buf, err := encodePNG8(src, 256, false, false)
	if err != nil {
		t.Fatalf("encodePNG8: %v", err)
	}
	// 必须能作为合法 PNG 解回，且尺寸不变。
	got, err := png.Decode(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("decode png8: %v", err)
	}
	if got.Bounds().Dx() != 400 || got.Bounds().Dy() != 300 {
		t.Fatalf("dims = %v, want 400x300", got.Bounds())
	}
}

func TestEncodePNG8_AlphaPreserved(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	// 左半透明、右不透明。
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			a := uint8(255)
			if x < 32 {
				a = 0
			}
			src.Set(x, y, color.RGBA{255, 0, 0, a})
		}
	}
	buf, err := encodePNG8(src, 256, true, false)
	if err != nil {
		t.Fatalf("encodePNG8: %v", err)
	}
	got, err := png.Decode(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, _, _, a := got.At(0, 0).RGBA()
	if a != 0 {
		t.Fatalf("expected transparent top-left, got alpha=%d", a)
	}
}
