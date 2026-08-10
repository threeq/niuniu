# WebP/JPEG 决策（Task 1 spike）
Date: 2026-05-11
Library: github.com/HugoSmits86/nativewebp@v1.3.0
Lossy support: no
Conclusion: 全 JPEG flatten alpha

## 详情

`nativewebp` v1.3.0 的 `Options` 结构体仅含：
- `UseExtendedFormat bool`
- `CompressionLevel  CompressionLevel`

无 `Quality`、`Lossy` 或任何有损编码字段/方法。该库仅支持 VP8L（无损）。

因此 Task 4 encoder.go 只实现 JPEG：
- 所有透明 PNG flatten 白底后输出 JPEG
- 不引入 nativewebp 依赖（已从 go.mod 移除）
