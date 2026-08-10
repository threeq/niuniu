package imageopt

import "image"

// countColorsUpTo 统计图中不同颜色数，命中 limit+1 即早停并返回 limit+1
// 作为「多于 limit」的哨兵。颜色按 8 位 RGBA 打包，O(像素) 且早停。
func countColorsUpTo(img image.Image, limit int) int {
	b := img.Bounds()
	seen := make(map[uint32]struct{}, limit+1)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			key := uint32(r>>8)<<24 | uint32(g>>8)<<16 | uint32(bl>>8)<<8 | uint32(a>>8)
			seen[key] = struct{}{}
			if len(seen) > limit {
				return limit + 1
			}
		}
	}
	return len(seen)
}

// pickFormat 选输出格式：少色（截图/图标/diff）或带透明且少色 → png8；
// 其余（照片/多色/渐变）→ webp。返回 "png8" | "webp"。
func pickFormat(img image.Image, hasAlpha bool) string {
	if countColorsUpTo(img, 256) <= 256 {
		return "png8"
	}
	return "webp"
}
