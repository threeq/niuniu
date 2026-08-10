package imageopt

import (
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"

	xwebp "golang.org/x/image/webp"
)

// DecodeDimensions reads only the image header (image.DecodeConfig) to
// return pixel dimensions without decoding pixels — a few-KB read, cheap
// enough to run on every image Read. Relies on the png/jpeg/gif/webp
// decoders this package already registers. Returns an error for an
// unsupported/corrupt header (e.g. bmp/tiff, which this package can't
// decode anyway); callers treat that as "unknown size" and fall back to a
// byte-based heuristic rather than blocking.
func DecodeDimensions(path string) (width, height int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// decodeImage decodes a file by known format string from detect().
// Returns the image and whether any pixel has alpha < 255.
// Uses a sampling pass for alpha check to bound cost on huge images.
func decodeImage(path, format string) (image.Image, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	var img image.Image
	switch format {
	case "png":
		img, err = png.Decode(f)
	case "jpeg":
		img, err = jpeg.Decode(f)
	case "gif":
		// Single-frame GIF; if Animated should have been filtered earlier.
		img, err = gif.Decode(f)
	case "webp":
		img, err = xwebp.Decode(f)
	default:
		return nil, false, fmt.Errorf("imageopt: cannot decode format %q", format)
	}
	if err != nil {
		return nil, false, err
	}

	return img, hasNonOpaquePixel(img), nil
}

// hasNonOpaquePixel returns true if any pixel has alpha < 255.
// Two-pass scan (fixes review I5: old step=8 missed tiny corner alpha):
//  1. coarse pass: step=8 stride scan, return on first hit.
//  2. anchors pass: 9 explicit points (4 corners + 4 mid-edges + center)
//     to catch "clipped-corner icon" style transparency that step=8 skips.
func hasNonOpaquePixel(img image.Image) bool {
	if img == nil {
		return false
	}
	b := img.Bounds()

	// pass 1: coarse stride scan
	for y := b.Min.Y; y < b.Max.Y; y += 8 {
		for x := b.Min.X; x < b.Max.X; x += 8 {
			_, _, _, a := img.At(x, y).RGBA()
			if a < 0xffff {
				return true
			}
		}
	}

	// pass 2: explicit anchors (corners + mid-edges + center)
	w, h := b.Dx(), b.Dy()
	if w < 2 || h < 2 {
		return false
	}
	x0, y0 := b.Min.X, b.Min.Y
	x1, y1 := b.Max.X-1, b.Max.Y-1
	xm, ym := x0+w/2, y0+h/2
	anchors := [9][2]int{
		{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1}, // 4 corners
		{xm, y0}, {xm, y1}, {x0, ym}, {x1, ym}, // 4 mid-edges
		{xm, ym},                                // center
	}
	for _, p := range anchors {
		_, _, _, a := img.At(p[0], p[1]).RGBA()
		if a < 0xffff {
			return true
		}
	}
	return false
}
