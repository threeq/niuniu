package imageopt

import (
	"image"
	"image/color"
	"testing"
)

func gradient(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), uint8((x + y) % 256), 255})
		}
	}
	return img
}

func TestCountColorsUpTo_EarlyStop(t *testing.T) {
	// 少色图：白底+黑线，远少于 256。
	if n := countColorsUpTo(solidWithStripes(200, 200), 256); n > 256 || n < 2 {
		t.Fatalf("stripes colors = %d, want small >=2", n)
	}
	// 多色渐变：命中上限早停，返回 257（>256 哨兵）。
	if n := countColorsUpTo(gradient(400, 400), 256); n <= 256 {
		t.Fatalf("gradient colors = %d, want >256 sentinel", n)
	}
}

func TestPickFormat(t *testing.T) {
	if f := pickFormat(solidWithStripes(200, 200), false); f != "png8" {
		t.Fatalf("stripes -> %q, want png8", f)
	}
	if f := pickFormat(gradient(400, 400), false); f != "webp" {
		t.Fatalf("gradient -> %q, want webp", f)
	}
	// 透明且少色 -> png8。
	icon := image.NewRGBA(image.Rect(0, 0, 32, 32))
	if f := pickFormat(icon, true); f != "png8" {
		t.Fatalf("transparent icon -> %q, want png8", f)
	}
}
