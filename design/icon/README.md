# Niuniu App Icon · 设计源文件

当前应用图标的设计源 + 历史探索归档 + 全套交付物。

## 核心设计

**方案**：v2 浅底反版 — NN 衬线字母 + 双牛角，深蓝（#1E3A8A）/白底
**理念**：「牛牛 = NN = 双牛」三重含义统一在一个 mark 里

- 中文品牌锚点：双 N 既是字母 N，又作为牛字头部「ノ ─」的双角抽象
- 国际化兼容：NN 字母在中英文环境下都可读，牛角是非语言符号
- 浅色底：与现代 iOS / macOS 默认应用图标语言一致

**画布利用率**：母版采用 tight 版本，NN+双角内容占画布 ~80%（由初版 ~50% 优化而来），平台再加圆角 mask 后视觉重心仍清晰，避免「图标里的图标」的双框感。

**圆角处理**：母版自带 20% 圆角（约 204px @ 1024 画布，iOS / macOS squircle 标准比例）。这样在 Windows 资源管理器、任务栏这些**不自动加圆角**的环境下也能保持现代 app icon 范，同时跟 Apple 系统的 mask 兼容（不会产生双圆角冲突）。

## 目录结构

```
design/icon/
├── README.md                    ← 本文件
├── master.png                   ← 1024×1024 浅色母版（直接编辑用）
├── source.pen                   ← Pencil 设计源文件（需手动保存，见下文）
├── exports/                     ← 17 个浅色交付物（已部署到各端）
│   ├── icon-{16,32,48,64,128,180,192,256,384,512,1024}.png
│   ├── icon.ico                 ← Windows 多分辨率图标
│   ├── icon.icns                ← macOS 多分辨率图标
│   ├── icon.svg                 ← Web 主图标（PNG-base64 包裹的 SVG）
│   ├── icon-cn.svg              ← 中文环境图标（同 NN 内容）
│   ├── icon-bilingual.svg       ← 双语图标
│   ├── favicon.svg              ← 浏览器收藏夹图标
│   └── dark/                    ← 深色变体套件（16 个文件，未部署）
│       ├── master-dark.png      ← 1024×1024 深色母版
│       ├── icon-{各尺寸}.png    ← 11 个尺寸的深色 PNG
│       ├── icon.{ico,icns}      ← 深色版多分辨率桌面图标
│       └── {icon,favicon}.svg   ← 深色版 SVG（PNG-base64 包裹）
└── exploration/                 ← 57 张设计探索历史
    ├── pencil-A*.png            ← A 方向（牛形剪影）变体
    ├── pencil-B*.png            ← B 方向（牛字书法）变体
    ├── pencil-C*.png            ← C 方向（牛形+牛字融合）变体
    ├── pencil-Cur*.png          ← 现有图标重塑变体
    ├── pencil-V2*.png           ← v2 派生（最终方向）变体
    ├── pencil-R*.png            ← 圆角化变体
    └── *-labeled.png            ← 横向对比图
```

## 部署清单

母版部署到全平台 22 个文件：

| 类别 | 路径 | 文件数 |
|---|---|---|
| 桌面（Wails personal） | `desktop/cmd/personal/{appicon.png, build/icon.{ico,icns}}` | 3 |
| Web/PWA | `server/web/public/{icon-{192,512}.png, icon-cn-{192,512}.png, *.svg, apple-touch-icon.png}` | 9 |
| 移动（Expo） | `mobile/assets/{icon.png, icon-cn.png, adaptive-icon.png, splash-icon.png, favicon.png}` | 5 |
| 官网（Astro） | `website/public/favicon.{ico,svg}` | 2 |

## 重要 caveat

⚠️ **SVG 是 PNG-base64 嵌入版**，不是真矢量。每个约 230KB（原版纯矢量约 1KB）。

**修复方案**：找设计师把 `master.png` 描成真矢量 SVG，替换 `exports/*.svg` 后重新部署。

## 深色变体（dark）

`exports/dark/` 是**程序化生成**的深色底版本（不是 AI 重新生成的）：

- 浅色母版亮度反转 → 暗的 NN+双角变成白色，白底变成深蓝渐变（顶部 #0A2A6B → 底部 #1E5FCC）
- 形状轮廓和原版完全一致
- 适合：dark mode 媒体查询切换、操作系统暗色主题适配

**当前状态**：已生成完整套件（16 个文件），**未部署到生产路径**。如需启用：
- Web/PWA: 在 `index.html` 加 `<link rel="icon" media="(prefers-color-scheme: dark)" href="/icon-dark.svg">`
- iOS: 在 Asset Catalog 提供 dark variant
- macOS: 单独打 dark .icns 替换或用 NSAppearance 切换

## 如何保存 Pencil 源文件 (`source.pen`)

由于 Pencil MCP 工具不支持把 .pen 文件保存到任意磁盘路径，需要手动操作：

1. 打开 Pencil 应用
2. 文件 → 打开（找到当前活动的 niuniu-icon 设计文档）
3. 文件 → 另存为
4. 保存路径：`<repo>/design/icon/source.pen`
5. 完成后，把 source.pen 一起 commit

Pencil 应该会同时把 `images/` 子目录（AI 生成的所有变体原图）一起保存到 `design/icon/`。

## 如何重新生成图标变体

如果要在 Pencil 里继续迭代：

1. 打开 `source.pen`（保存后）
2. 在主画布右侧空白处插入新 frame（256×256，cornerRadius 56）
3. 用 `G(frameId, "ai", prompt)` 生成新 AI 图像变体
4. 参考 `exploration/` 里已经探索过的 prompt 思路（避免重复方向）

成功的 prompt 模式（可参考）：
```
Minimalist iOS app icon, two letter N letterforms with classical refined serifs interlocked into NN monogram, with two bull horns rising from the tops, deep navy blue 1E3A8A on clean white background with subtle light blue gradient, rounded square 256px, premium classical NN+horns brand mark
```

## 设计探索时间线

参考 `exploration/` 中按时间命名的对比图：

1. `compare-all-labeled.png` — 第一轮 4 大方向（A 牛形 / B 牛字 / C 融合 / D 抽象）
2. `cur-row-labeled.png` — 现有图标的 4 个大胆重塑
3. `c2-row-labeled.png` — Cur2 双 N 即双角的 4 个深化
4. `v2r-labeled.png` — 最终 v2 + 圆头帽变体决战
5. `final-compare-labeled.png` — 跟参考图的最终对比

## 工程归属

- 设计工具：Pencil MCP + AI 图像生成
- 母版导出：1024×1024 PNG（4× scale，Lanczos 滤波）
- 多分辨率打包：ImageMagick（.ico）+ icnsutil（.icns）
- 设计时间：2026-05-08
