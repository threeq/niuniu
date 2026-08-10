---
name: site-audit
description: >-
  Use when the user wants a technical SEO / GEO audit of a web page — check
  title / meta description / canonical / robots (indexability) / headings /
  image alt / mobile viewport / Open Graph / JSON-LD structured data / page-speed
  signals — or wants to validate JSON-LD / schema.org markup locally. Produces a
  scored, P0–P3 prioritized audit checklist report (md + json). Runs on a
  zero-dependency stdlib Python engine; feed it a live URL or pre-fetched HTML.
  Trigger on: "站点审计" "页面审计" "SEO 体检" "技术SEO" "结构化数据校验"
  "JSON-LD 校验" "schema.org 验证" "检查 meta/canonical/robots" "页面能不能被收录"
  "site audit" "technical SEO audit" "validate structured data" "check JSON-LD"
  "is this page indexable" "audit my page" or any request to check a page's
  crawlability / structured data / on-page SEO health.
---

# Site Audit — 站点技术审计 + JSON-LD/结构化数据校验（纯本地）

对一个页面做**技术 SEO + GEO 审计**：逐项检查它能否被搜索引擎与生成式引擎正确
**抓取、理解、引用**，并校验其 JSON-LD 结构化数据是否合法。**零新依赖**——只用
Python 标准库；**不联网也能跑**（可把 HTML 喂给引擎，抓取交给 `fetch` MCP 或本地文件）。

## 分工（关键）

- **你（agent）负责模糊部分**：确认目标 URL/范围；判断某条 finding 是否「有意为之」
  （如 noindex 可能是刻意屏蔽）；把审计结论翻译成可执行的优化动作与优先级。
- **引擎 `scripts/site_audit.py` 负责确定性部分**：解析 HTML、逐项打「通过/告警/失败」、
  按 schema.org 规则校验 JSON-LD、算分、落报告、登记产物。**绝不要**手工数 meta、
  肉眼判断 JSON-LD 合不合法——一律交给引擎，保证可复现。

## 引擎能力

纯标准库 Python 3.8+（`html.parser` / `urllib` / `json`），三个子命令：

```bash
# 1) 全量审计：URL / 本地 HTML 文件 / '-'(stdin) 三选一
python scripts/site_audit.py audit <url|file|-> --out-dir . --ws-root <workspace_root>

# 2) 只校验 JSON-LD / schema.org：页面 URL、HTML 文件、独立 .json 片段、或 stdin
python scripts/site_audit.py validate-jsonld <url|file.html|snippet.json|->

# 3) 自检（不联网，验证检查项与校验逻辑）
python scripts/site_audit.py selftest
```

`audit` 覆盖的检查项（每项给 状态×严重度×证据×修复建议）：

- **可索引性**：robots meta / `X-Robots-Tag` 的 noindex；robots.txt 是否允许抓取（P0）
- **canonical**：`<link rel=canonical>` 是否声明（P1）
- **标题**：`<title>` 存在性与长度（~30–60 字符）
- **meta description**：存在性与长度（50–160 字符）
- **移动友好**：`viewport` meta（P1）；**html lang**；**charset**
- **标题结构**：唯一 H1、层级是否跳级
- **图片 alt** 覆盖率
- **Open Graph** 社交卡片
- **结构化数据 JSON-LD**：存在性 + 逐块合法性校验（P1）
- **AI 抽取友好度（GEO）**：首段是否直接作答、是否含 FAQ/Question 问答结构
- **加载速度信号**（仅 URL 抓取时）：HTML 首字节+下载耗时、HTML 体积

输出：`audit-report.json`（结构化，可接 cron 做时间序列）+ `audit-report.md`
（打分 + 清单表，按 状态/严重度 排序，最需修的在最上），`--ws-root` 时自动登记到
工作空间 `.niuniu/artifacts.json`。得分 = 100 − Σ(失败/告警按 P0=25/P1=12/P2=6/P3=2
扣分，告警减半)，映射 A–E。

`validate-jsonld` 逐块校验：JSON 语法、`@type` 存在、按类型的必填/建议属性
（Article→headline/author/datePublished/image；Product→name/image/offers；
FAQPage→mainEntity 内 Question+acceptedAnswer(Answer.text)；HowTo→name/step；
BreadcrumbList→itemListElement；Organization/WebSite/Event/LocalBusiness…），
支持 `@graph` 与 `@type` 数组。有硬错误时退出码非零，便于 gate。

## 工作流

### 1. 拿到页面 HTML

- **有 URL 且引擎能直连** → 直接 `audit https://example.com/page`（引擎用 urllib 抓，
  并顺带取 robots.txt 与速度信号）。
- **引擎连不上 / 需要走 `fetch` MCP / 页面需渲染** → 你先用 `fetch` MCP 抓页面，把
  HTML 存成本地文件（如 `page.html`）或通过管道喂给引擎：
  ```bash
  python scripts/site_audit.py audit page.html --ws-root <workspace_root>
  ```
  本地文件模式不联网，`robots.txt`/速度信号会自动跳过并在报告里标注。

### 2. 跑审计 / 校验

- 全量体检用 `audit`；只想校验一段即将嵌入的 JSON-LD 用 `validate-jsonld snippet.json`。
- 多页面就对每个 URL 各跑一次 `audit`，`--out-dir` 分目录。

### 3. 解读并给行动清单

读 `audit-report.json`，把 **失败(fail)** 项按 P0→P3 排优先级，逐条给：
1) 问题是什么、为什么影响 SEO/GEO；2) 具体怎么改（引擎已给 `fix` 起点，你结合页面细化）；
3) 复测方式（改完重跑 `audit`，看分数/该项是否转绿）。**告警(warn)** 作为次级优化项。

## 铁律

1. **确定性交给引擎**：meta/JSON-LD 是否合法、扣多少分，一律以引擎输出为准；
   不要凭印象说「结构化数据没问题」。
2. **抓不到就标注**：页面抓取失败、robots.txt 取不到、需 JS 渲染的内容——如实标注
   并说明，不臆断该页通过。
3. **noindex/屏蔽可能有意**：报告把 noindex 记为失败，但你要向用户确认是否刻意为之，
   别把有意的屏蔽当 bug。
4. **不注入不存在的信息**：生成/建议 JSON-LD 时只反映页面可见内容，不编造字段值，
   避免 spammy structured data 被引擎处罚。
5. **产物登记**：最终 `audit-report.md` 用 `--ws-root` 登记到工作空间
   `.niuniu/artifacts.json`；不登记中间的 `page.html` 等临时文件。
