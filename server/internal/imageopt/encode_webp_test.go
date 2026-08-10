//go:build cgo

package imageopt

import (
	"bytes"
	"testing"

	xwebp "golang.org/x/image/webp"
)

func TestEncodeWebP_OpaqueDecodes(t *testing.T) {
	src := gradient(200, 150)
	buf, err := encodeWebP(src, 80, false)
	if err != nil {
		t.Fatalf("encodeWebP: %v", err)
	}
	img, err := xwebp.Decode(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("decode webp: %v", err)
	}
	if img.Bounds().Dx() != 200 || img.Bounds().Dy() != 150 {
		t.Fatalf("dims=%v want 200x150", img.Bounds())
	}
}

func TestEncodeWebP_AlphaNoError(t *testing.T) {
	src := solidWithStripes(64, 64) // 复用 quantize_test 的 helper（同包）
	if _, err := encodeWebP(src, 80, true); err != nil {
		t.Fatalf("encodeWebP alpha: %v", err)
	}
}
