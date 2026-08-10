# Niuniu 品牌图标使用指南

## 三个版本

| 版本 | 文件后缀 | 文字内容 | 主要用途 |
|---|---|---|---|
| **EN（主图标）** | `-en` 或无后缀 | 牛头 + `N \| N` | 默认/海外用户/dock/任务栏/desktop/mobile/web app |
| **CN** | `-cn` | 牛头 + `牛 \| 牛` | 中文用户场景（按 locale 切换） |
| **双语（Bilingual）** | `-bilingual` | 牛头 + `Niu \| niu` 上 + `牛 \| 牛` 下 | Marketing / 官网 / 印刷物 / About 页 |

主色：**`#1E5FCC` 品牌蓝实色**（与 web 现有 `--brand` 主色一致）

## 文件位置

| 位置 | 文件 | 来源 |
|---|---|---|
| `server/web/public/favicon.svg` | EN | `niuniu-en.svg` |
| `server/web/public/icon.svg` | EN（master） | `niuniu-en.svg` |
| `server/web/public/icon-cn.svg` | CN | `niuniu-cn.svg` |
| `server/web/public/icon-bilingual.svg` | 双语 | `niuniu-bilingual.svg` |
| `server/web/public/icon-{192,512}.png` | EN PNG | rasterized |
| `server/web/public/icon-cn-{192,512}.png` | CN PNG | rasterized |
| `server/web/public/apple-touch-icon.png` | EN 180×180 | rasterized |
| `mobile/assets/icon.png` | EN 1024 | rasterized |
| `mobile/assets/icon-cn.png` | CN 1024 | 备用 |
| `mobile/assets/adaptive-icon.png` | EN 1024 | rasterized |
| `mobile/assets/splash-icon.png` | EN 1024 | rasterized |
| `mobile/assets/favicon.png` | EN 48 | rasterized |
| `desktop/cmd/personal/appicon.png` | EN 512 | Wails appicon |
| `desktop/cmd/personal/build/icon.{ico,icns}` | EN | 安装包打包 |
| `website/public/favicon.svg` | 双语 | marketing 品牌 |
| `website/public/favicon.ico` | 双语 | marketing 品牌 |

## 按语言切换 favicon（web）

在 SPA 入口（`server/web/index.html` 或 `main.tsx`）按浏览器/用户 locale 切换 favicon：

### 方案 A：HTML `<link>` 默认 + JS 动态替换

```html
<!-- index.html -->
<link rel="icon" type="image/svg+xml" href="/icon.svg" id="favicon">
<link rel="apple-touch-icon" href="/apple-touch-icon.png">
```

```ts
// main.tsx 或 i18n 初始化处
function setFaviconForLocale(locale: 'zh' | 'en' | string) {
  const link = document.getElementById('favicon') as HTMLLinkElement | null;
  if (!link) return;
  const href = locale.startsWith('zh') ? '/icon-cn.svg' : '/icon.svg';
  if (link.href.endsWith(href)) return;
  link.href = href;
}

// 启动时
setFaviconForLocale(navigator.language || 'en');

// 用户切换语言时
i18n.on('languageChanged', (lng) => setFaviconForLocale(lng));
```

### 方案 B：服务端渲染时按 `Accept-Language` 替换

后端检测 `Accept-Language: zh-*` → 在响应 HTML 中把 favicon 替换为 `/icon-cn.svg`。

我们目前 SPA 是客户端渲染（embedded `dist`），方案 A 更简单。

## desktop 端是否需要 i18n 切换

**结论：不需要**。

- macOS/Windows 应用图标在系统层（dock/Finder/任务栏）固定为打包时嵌入的资源
- 不能根据用户系统语言动态切换（操作系统不提供这个 API）
- 即使可以也不应该 —— app icon 是 visual identity，应保持一致
- 海外/中文用户都看到同一个 EN 主图标（`N \| N` 的 N 既是 Niuniu 也是抽象字母，全球可读）

## mobile 端是否需要 i18n 切换

**结论：技术可行，但不推荐**。

- iOS 14+ 支持 alternate app icon API（`UIApplication.setAlternateIconName`）
- React Native 通过 `react-native-change-icon` 可以切换
- 但需要在打包时预先注册所有备用图标，且切换需要用户授权弹窗
- 同 desktop 理由：保持品牌一致性更好

如果坚持要做：在 `mobile/app.json` 注册 `alternateIcons` 并在 i18n 切换时调用 native API。

## 重新生成

源 SVG：
- `icon-drafts/colors/niuniu-{cn,en,bi}-blue.svg`（主蓝）
- `icon-drafts/colors/niuniu-{cn,en,bi}-{indigo,orange,black}.svg`（备用色）

完整交付包：`icon-drafts/dist/`
- `master/niuniu-{cn,en,bilingual}.svg`
- `png/{cn,en,bilingual}/{16,24,32,48,64,96,128,192,256,384,512,1024}.png`
- `ico/niuniu-{cn,en,bilingual}.ico`（含 16/32/48/64/128/256 多尺寸）
- `icns/niuniu-{cn,en,bilingual}.icns`（含 16/32/64/128/256/512/1024 多尺寸）

重新生成命令：

```bash
cd icon-drafts
# PNG（需要 ImageMagick）
for lang in cn en bilingual; do
  for size in 16 24 32 48 64 96 128 192 256 384 512 1024; do
    magick -background none -density 300 dist/master/niuniu-${lang}.svg \
      -resize ${size}x${size} dist/png/${lang}/${size}.png
  done
done

# ICO
for lang in cn en bilingual; do
  magick dist/png/${lang}/16.png dist/png/${lang}/32.png dist/png/${lang}/48.png \
    dist/png/${lang}/64.png dist/png/${lang}/128.png dist/png/${lang}/256.png \
    dist/ico/niuniu-${lang}.ico
done

# ICNS（需要 npm install png2icons）
node build-icns.js
```

## 改色

主色 `#1E5FCC` 在 12 个 SVG 中硬编码。换主色：

```bash
cd icon-drafts/colors
sed -i 's/#1E5FCC/#NEW_COLOR/g' niuniu-{cn,en,bi}-blue.svg
```

然后重跑 PNG/ICO/ICNS 生成命令。

## 设计原则（来自项目 design-system.md）

- ✅ 实色（不用渐变）—— 蓝色主图标遵守
- ✅ 暖中性 + 蓝色品牌单色 —— 图标只用蓝 + 白
- ✅ 圆角 22%（`rx=56` on 256×256）—— iOS/Android squircle 兼容

## 牛角设计语义

```
∀ + I + 牛环 = AI + 牛
```

- **顶部 ∀**（粗base 锐tip 弯弧）= 倒置 A 字母 + 牛角剪影
- **角内 T**（小横+短竖）= 与下方 I 共享的字母骨架
- **长 I**（贯穿到底）= I 字母 + 鼻梁
- **牛环**（圆环）= 牛鼻环（最强"牛"文化符号）
- **左右文字**（牛\|牛 / N\|N）= 品牌名（被 I 分割）
- **底横线** = 字符基线 / 下颌
