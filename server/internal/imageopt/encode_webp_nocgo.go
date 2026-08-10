//go:build !cgo

package imageopt

import "image"

// encodeWebP 存根：非 cgo 构建无 libwebp，返回 errWebPUnavailable，
// 调用方据此降级到 JPEG/PNG8（见 encoder.go / pipeline.go）。
func encodeWebP(img image.Image, quality int, hasAlpha bool) ([]byte, error) {
	return nil, errWebPUnavailable
}
