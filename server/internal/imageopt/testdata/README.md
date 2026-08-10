# imageopt testdata

样本图用于 pipeline_test.go / imageopt_test.go。

生成的 PNG/JPEG（不 commit 到 git，本地按需生成）：
- `small.png` — 800×600 渐变，触发 `already_small` 跳过
- `screenshot-4k.png` — 3840×2160 噪声，主流压缩用例
- `screenshot-mobile.png` — 1290×2796 噪声，长截图
- `transparent-icon.png` — 1024×1024 带 alpha
- `photo.jpg` — 4032×3024 噪声
- `complex-ppt.png` — 1920×1080 最大熵噪声，触发软底线
- `tiny-random.png` — 64×64 最大熵噪声，触发 encode_bigger

重新生成：

```
cd server/internal/imageopt
go run -tags=testdata ./testdata/gen.go
```

提交到 git 的样本（小文件）：
- `icon.svg`
- `animated.gif`
- `corrupt.png`
