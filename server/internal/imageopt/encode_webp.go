//go:build cgo

package imageopt

import (
	"bytes"
	"image"

	"github.com/chai2010/webp"
)

// encodeWebP 有损编码为 WebP。quality 1-100。chai2010/webp 自带 alpha 通道，
// 无需 flatten；hasAlpha 仅用于将来策略，当前实现透明信息由库直接保留。
func encodeWebP(img image.Image, quality int, hasAlpha bool) ([]byte, error) {
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, &webp.Options{
		Lossless: false,
		Quality:  float32(quality),
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
