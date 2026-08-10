//go:build testdata

// To regenerate testdata fixtures:
//
//	cd server/internal/imageopt
//	go run -tags=testdata ./testdata/gen.go
package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
)

func main() {
	dir, _ := filepath.Abs("testdata")
	must(os.MkdirAll(dir, 0o755))

	// small.png — 200KB equivalent, 800×600, simple gradient
	writePNG(filepath.Join(dir, "small.png"), gradient(800, 600, false))

	// screenshot-4k.png — 3840×2160, complex noise to avoid trivial compression
	writePNG(filepath.Join(dir, "screenshot-4k.png"), noise(3840, 2160, false))

	// screenshot-mobile.png — 1290×2796, noise
	writePNG(filepath.Join(dir, "screenshot-mobile.png"), noise(1290, 2796, false))

	// transparent-icon.png — 1024×1024, transparent corners
	writePNG(filepath.Join(dir, "transparent-icon.png"), noise(1024, 1024, true))

	// photo.jpg — 4032×3024, noise, JPEG q=90
	writeJPEG(filepath.Join(dir, "photo.jpg"), noise(4032, 3024, false), 90)

	// complex-ppt.png — 1920×1080, max-entropy noise (won't compress under 80KB at floor)
	writePNG(filepath.Join(dir, "complex-ppt.png"), maxEntropy(1920, 1080))

	// tiny-random.png — 64×64 random pixels; used for encode_bigger test
	writePNG(filepath.Join(dir, "tiny-random.png"), maxEntropy(64, 64))
}

func gradient(w, h int, alpha bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if alpha && (x < w/4 || y < h/4) {
				a = 0
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, a})
		}
	}
	return img
}

func noise(w, h int, alpha bool) *image.NRGBA {
	r := rand.New(rand.NewSource(42))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if alpha && (x < w/4 && y < h/4) {
				a = 0
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), a})
		}
	}
	return img
}

func maxEntropy(w, h int) *image.NRGBA {
	r := rand.New(rand.NewSource(7))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255})
		}
	}
	return img
}

func writePNG(p string, img image.Image) {
	f, err := os.Create(p)
	must(err)
	defer f.Close()
	must(png.Encode(f, img))
}

func writeJPEG(p string, img image.Image, q int) {
	var buf bytes.Buffer
	must(jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}))
	must(os.WriteFile(p, buf.Bytes(), 0o644))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
