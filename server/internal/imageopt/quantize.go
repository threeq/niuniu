package imageopt

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	"github.com/ericpauley/go-quantize/quantize"
)

// encodePNG8 将图量化为 ≤maxColors 色的调色板 PNG（8 位索引）。
// hasAlpha=true 时在调色板中保留透明项。dither=true 用 Floyd-Steinberg
// 误差扩散（照片/渐变更平滑）；OCR/文字场景传 false，避免抖动噪点伤识别。
// 输出用 png.BestCompression，重编码天然剥除原图 metadata。
func encodePNG8(src image.Image, maxColors int, hasAlpha, dither bool) ([]byte, error) {
	if maxColors < 2 {
		maxColors = 2
	}
	if maxColors > 256 {
		maxColors = 256
	}
	q := quantize.MedianCutQuantizer{AddTransparent: hasAlpha}
	pal := q.Quantize(make(color.Palette, 0, maxColors), src)

	paletted := image.NewPaletted(src.Bounds(), pal)
	if dither {
		draw.FloydSteinberg.Draw(paletted, src.Bounds(), src, image.Point{})
	} else {
		draw.Draw(paletted, src.Bounds(), src, image.Point{}, draw.Src)
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, paletted); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
