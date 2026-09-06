# 新版本检测多源化 + 显示版本优化点 — 设计

日期：2026-09-01
状态：已确认（用户批准）

## 背景与目标

当前更新检测链路：SPA → 本机 server `/api/app-update/latest` → 代理抓
`www.niu6ai.com/changelog` HTML 并正则解析。两个问题：

1. 数据源只有 niu6ai changelog 一处，页面改版即坏；
2. 用户可见「新版本 v0.8.6 可用」，但看不到**这个版本改了什么**（优化点）。

目标：检测源改为 `github.com/threeq/niuniu`（GitHub Releases，作为候选源与
niu6ai 并存），并让新版本提示**显示该版本的优化点清单**。

## 数据源设计（已确认：GitHub 优先，niu6ai 兜底）

`go-shared/releasecheck` 的 `FetchLatest` 改为依次尝试，第一个成功即返回：

1. **GitHub Releases API**
   `GET https://api.github.com/repos/threeq/niuniu/releases/latest`
   - 响应的 `tag_name` / `published_at` / `assets[]` 与现有 `Release` 结构
     天然对齐（`assets[].name` / `browser_download_url` 同形）；
   - **`body` 字段即优化点**（「## What's Changed」+ PR 链接清单），
     原样透传为新字段 `notes`；
   - server-to-server 请求，带 `User-Agent`（GitHub API 必需），走本机
     代理环境变量，规避大陆浏览器直连 403 问题。
2. **兜底 `https://www.niu6ai.com/changelog`**：现有 HTML 解析逻辑原样保留；
   GitHub 不可达（网络失败 / 非 200 / 限流）时自动降级。优化点尽力而为：
   从 changelog 最新版本小节提取纯文本，取不到则 `notes` 为空。

## 接口形状

`Release` 新增一个字段（向后兼容——老前端不读新字段，新前端对空值容错）：

```go
type Release struct {
    TagName     string  `json:"tag_name"`
    HTMLURL     string  `json:"html_url"`
    PublishedAt string  `json:"published_at"`
    Notes       string  `json:"notes,omitempty"` // 优化点（markdown 文本）
    Assets      []Asset `json:"assets"`
}
```

`HTMLURL` 语义按源而定：GitHub 源填 release 页 URL（用户可直接看 release
notes），niu6ai 源填 changelog 页 URL（保持现状）。

## 后端实现

- `releasecheck.go`：
  - `DefaultAPIURL = "https://api.github.com/repos/threeq/niuniu/releases/latest"`
  - 新增 `fetchFromGitHub(ctx, client)`：请求 API → 解码 GitHub JSON →
    映射为 `Release`（`notes = body`，`published_at` 截取日期部分保持
    `yyyy-mm-dd` 形态）。
  - `FetchLatest`：先 GitHub，失败（error 即降级，含 403/404/超时）再走
    原 changelog 逻辑；`ParseLatest` 签名不变。
  - 优化点提取（niu6ai 兜底路径）：从最新 tag 锚点之后截取该版本小节，
    剥离 HTML 标签取纯文本，按行拆分为列表文本；失败不影响主流程
    （`notes` 置空）。
- `api/app_update.go`：不动（缓存/端点/透传全部复用）。

## 前端实现

- `lib/version-check.ts`：`LatestRelease` / `UpdateCheckResult` 增加
  `notes?: string` 透传；`checkForUpdates` 带出。
- `pages/settings/about-settings.tsx`：新版本蓝色卡片内，版本行下方渲染
  `notes`（markdown → 逐行列表；`## What's Changed` 标题行去掉；行内
  `#123` PR 链接转成可点链接；纯文本兜底）。空 `notes` 不渲染该区块。
- `lib/update-checker.ts`：toast description 从固定文案改为优化点摘要
  （前 3 行非空文本 + 「…」），其余（轮询/dismiss/下载按钮）不动。
- i18n：`about.update.notesTitle`（「本次更新内容」）等新增 key，三语言。

## 不变的部分

- server 代理模式、30min TTL 缓存（含失败缓存）、`/api/app-update/latest`
  端点路径与响应外层形状；
- 前端 `findPlatformAsset` 平台匹配（资产命名不变）、6h 轮询、
  dismissed-version 记忆、personalMode 门控。

## 测试

- Go（`releasecheck_test.go`）：
  - GitHub API JSON（含 body、assets）→ `Release` 映射正确，`notes` 透传；
  - GitHub 失败（httptest 返回 403）→ 降级 changelog 解析仍成功；
  - changelog 兜底路径 `notes` 提取：有小节文本 → 提取为纯文本行；
    无 → 空；
  - `published_at` 归一为 `yyyy-mm-dd`。
- 前端（vitest，如现有测试布局允许）：`checkForUpdates` 透传 `notes`；
  about 页 notes 渲染/空值不渲染。

## 明确不做

- 不做自动下载/静默升级；
- 不改下载资产命名与平台匹配规则；
- 不新增设置项（源顺序固定，不走配置）。
