# Archify skill 评估：牛牛该不该收编、以及怎么收编

> 状态：技术选型评估结论（issue #697）→ **已落地**（2026-09-06）｜类型：选型评估 + 实施记录
> 评估对象：[`tt-a1i/archify`](https://github.com/tt-a1i/archify)（MIT，49.8k star，v2.17.0-dev.1）
>
> **落地结果**：archify 已 vendor 进 `docs/scenes/skills/archify/`（切断联网更新检查，见
> `VENDOR.md`），并**接入 `viz-architecture` 场景**——同时让该场景从"无仓库专用"扩展为
> 支持有仓库工作空间。§5.3 的初评建议（新增独立 `code-architecture` 场景）已被覆盖，
> 定案与理由见该节。顺带修复了 `info-radar` 的 mirror drift 并加了守卫测试（§5.2）。
>
> **结论（TL;DR）：值得收编，但理由不是"再多一个画图 skill"。**
> - 牛牛已有三个画图 skill（`fireworks-tech-graph` / `drawio-skill` / `excalidraw-skill`），它们全部挂在 `viz-architecture` 场景下，而该场景的匹配规则是 **`workspace.has_repo_count max: 0`——明确面向无仓库工作空间**。所有 code 场景（`generic-code` / `go-dev` / `ts-react-dev`）**一个画图 skill 都没声明**。
> - Archify 的差异化能力恰好落在牛牛的主场而现有三个 skill 全都没有：**图能钉在某个 commit 上（SRC 证据）**、**两版架构可机器对比（Architecture Delta）**、**交付前过校验闸并吐机器可读 diagnostics**。
> - 继承机制上**完全可行且有现成通道**：走 `drawio-skill` 同一条 vendored builtin skill 路（`docs/scenes/skills/` → `make builtin-skills-sync` → `go:embed` → 场景 `skills:` 声明）。MIT 许可、Node ≥18、**零运行时依赖**。
> - 但有 **三个必须先解决的落地问题**：① SKILL.md 要求 agent 主动跑联网更新检查，与牛牛 local-first 承诺冲突；② 产物预览 iframe 沙箱会拦掉 Export 下载；③ payload 裁剪后约 2.2 MB，会让内嵌 skill 体积翻倍。

---

## 1. Archify 是什么（事实盘点）

| 维度 | 事实 |
|------|------|
| 许可 | MIT（`archify/LICENSE`，可干净再分发） |
| 运行时 | Node ≥18，**`dependencies` 为空**，只有 devDependencies（ajv/parse5/saxes/simple-icons 用于构建期）→ 运行零依赖 |
| 形态 | 标准 Agent Skill：`archify/SKILL.md`（16 KB）+ 自带 CLI `bin/archify.mjs` |
| 图型 | architecture / workflow / sequence / dataflow / lifecycle 五种 |
| 输入 | 自然语言描述、Mermaid（flowchart / sequenceDiagram / stateDiagram）、仓库源码证据 |
| 中间表示 | 有 schema 的 typed JSON IR（`schemas/`，7 个 schema） |
| 输出 | **单文件自包含 HTML**（约 700 KB/张）+ PNG / SVG / WebM / 1200×630 分享卡 |
| CLI 子命令 | `doctor` `guide` `validate` `preview` `deliver` `compare` `brands` `demo` |
| 交付纪律 | `deliver` 先渲染候选件、全部检查通过才原子替换目标文件；失败保留上一个 last-good |

payload 体积分布（`archify/` 目录，git tree 实测）：

| 子目录 | 体积 | 是否必需 |
|--------|-----:|:--------:|
| `examples/` | 3566 KB | **部分**——19 个文件里 5 个是渲染好的 HTML（约 3.5 MB），JSON 源只有约 55 KB，而 SKILL.md 只要求读 JSON |
| `test/` | 1339 KB | ✗ 可裁 |
| `renderers/` | 1087 KB | ✓ |
| `assets/` | 662 KB | ✓（viewer 运行时） |
| `bin/` | 129 KB | ✓ |
| `scripts/` | 102 KB | ✓（含 `check-update.mjs`，见 §5） |
| `delta/` `schemas/` `recipes/` `references/` `brand-marks/` `migrations/` + 根文件 | 228 KB | ✓ |
| **合计 / 裁剪后** | **7.1 MB / ≈2.2 MB** | |

---

## 2. 牛牛现状：三个画图 skill，差口在哪

| 维度 | fireworks-tech-graph | drawio-skill | excalidraw-skill | **archify** |
|------|---|---|---|---|
| 产物 | 只读 SVG/PNG | `.drawio` XML + 导出 | `.excalidraw` JSON + 导出 | **交互式单文件 HTML** + 静态导出 |
| 可再编辑 | ✗ | ✓（draw.io 打开） | ✓（excalidraw 打开） | ✓（改 JSON IR 重新 deliver） |
| 交付前校验 | 人工/vision 回看 | `validate.py` 查 XML | 无 | **9 项检查的机器闸，失败给 `subject`/`evidence`/`supportedFixes`** |
| 与源码绑定 | ✗ | 有 import 提取脚本（`goimports`/`jsimports`…），但图不锚定 commit | ✗ | **SRC 证据节点，钉死到某个 commit 的文件+行区间** |
| 两版对比 | ✗ | ✗ | ✗ | **Architecture Delta：Before/Delta/After + 机器回执** |
| 外部依赖 | cairosvg/rsvg/puppeteer（可降级） | draw.io desktop CLI（导出时） | 可选 | Node ≥18（生成+校验+交付全程） |
| 现挂场景 | viz-architecture | viz-architecture | viz-architecture | — |

**关键判读：现有三个是"一次性出图工具"，archify 是"可验证、可复查、可 diff 的架构事实载体"。** 前者的价值在无仓库的画图工作舱（这也正是 `viz-architecture` 场景 `has_repo_count max: 0` 的匹配意图），后者的价值在**有仓库、有看板、有 review 列**的工作空间——而牛牛所有 code 场景现在这块是空白。

---

## 3. 对牛牛的真实价值（三条，都咬合已有机制）

1. **仓库架构图锚定 commit** —— 牛牛每个 workspace 就是一个 `git worktree`，天然有确定的 commit。archify 的 SRC 证据节点直接落在这个心智上：出的图不是"某人画的印象"，而是"`<commit>` 这一版代码的架构"。这是现有三个 skill 结构上做不到的。

2. **Architecture Delta 咬合「审查」列** —— 看板的 `审查`（`implement-review`）列做的就是"改动值不值得严格 review"。`compare architecture base.json head.json --json` 产出 added/removed/changed/moved/rerouted 的精确事实 + 机器回执，正好是架构级 review 的输入。注意其**边界**：它只陈述作者声明过的拓扑差异，**不推断影响面、风险或合并安全性**——这个"不越界"的克制反而和牛牛 harness gate 的定位一致。

3. **校验闸的心智与 harness 同构** —— `validate --json` / `deliver --json` 的失败回执是稳定 rule code + subject + evidence + supportedFixes，和 `internal/harness/` 的 typed spec + checker 是同一种设计取向：**机器可读的失败，而不是让 agent 猜着重试**。

**同时要划清不适合的边界**（避免收编后被误用）：
- 不是 Mermaid 主题引擎，也不做 WYSIWYG 编辑——需要"交给他人继续手改"仍应走 `drawio-skill`。
- 单张 HTML 约 700 KB，**不适合入库**；应落工作空间目录并登记 `.niuniu/artifacts.json`，与 `viz-architecture` 现有产物纪律一致。
- 上游明确不做自动 Mermaid 解析、通用 auto-layout、托管分享。

---

## 4. 能不能继承：三条通道，只有一条该走

| 通道 | 机制 | 可行性 |
|------|------|--------|
| **A. vendored builtin skill（推荐）** | 放 `docs/scenes/skills/archify/` → `make builtin-skills-sync` 镜像到 `server/internal/service/builtin_skills/` → `//go:embed`（`scene_skills.go:29`）→ 场景 `skills:` 声明 → 启用场景时纯本地文件拷贝物化到 `<wsDir>/.<cli>/skills/archify/` | ✅ **完全可行**，与 `drawio-skill` 完全同构（`VENDOR.md` 格式、许可、裁剪、升级脚本都有现成先例） |
| B. marketplace 插件 | `SkillManager` 的 marketplace 源 | ❌ **不通**。`skillPluginMarketplaces` 只白名单了 `anthropic-agent-skills`（`skill_manager.go:97`），archify 不在其中，且它不是 plugin 形态 |
| C. 用户自行安装 | `npx skills add tt-a1i/archify -g`，落到 `~/.claude/skills/`，`SkillManager` 以 `source: "user"` 识别，`SkillGlobalEnabled` 会让场景跳过本地重复拷贝（`scene_skills.go:88`） | ✅ **今天就能用，零开发成本**——但不是产品能力，用户得自己知道有这东西、自己联网装 |

> 通道 C 值得单独说明：**牛牛现在其实已经"能"用 archify 了**，用户全局装一份即可，`SkillManager` 的 global-first 去重逻辑已经正确处理了这种情况。所以本次评估的真正问题不是"能不能"，而是**"要不要把它变成开箱即用的产品能力"**。我的判断是要——因为 §3 那三条价值全都需要场景 prompt 和 quick_action 去引导，用户自己装一个 skill 是拿不到这些的。

---

## 5. 落地方案（通道 A 的具体步骤）

### 5.1 Vendor 裁剪清单

保留：`SKILL.md` `LICENSE` `THIRD_PARTY_NOTICES.md` `package.json` `bin/` `renderers/` `schemas/` `references/` `recipes/` `brand-marks/` `assets/` `delta/` `migrations/` `scripts/` `examples/*.json`
排除：`test/`（1.3 MB）、`examples/*.html`（3.5 MB，5 个渲染样例）、`package-lock.json`（无运行时依赖，无意义）

### 5.2 改造 `make builtin-skills-sync`

现状（`Makefile:330`）是**硬编码 skill 名列表 + 只排除 `*.png`**：

```make
@cd docs/scenes/skills && find fireworks-tech-graph drawio-skill excalidraw-skill \
    geo-citation-audit site-audit imbot-onboarding -type f ! -name '*.png' ...
```

接入 archify 需要改这个 target。**注意初评给的排除写法 `! -name '*.html'` 是错的**——
`archify/assets/template.html`（678 KB）是渲染器模板而非样例，被 blanket `*.html` 规则删掉后
镜像里的 skill 是坏的（源 2.4 MB / 镜像 1.7 MB，72 → 71 文件）。定案做法：

- **skill 列表从源码树枚举，不再硬编码**（`find . -mindepth 1 -maxdepth 1 -type d`）。这条
  target 第一步是 `rm -rf`，硬编码列表意味着"漏写 = 静默删除"——info-radar 的 drift 正是这么来的。
- **只保留 `*.png` 一条排除**。per-skill 的裁剪（上游测试套件、预渲染样例）一律在
  **vendor 时**于源码树完成，并记进该 skill 的 `VENDOR.md`，而不是在 Makefile 里写后缀规则。
- 新增守卫测试 `TestBuiltinSkillsMirrorMatchesSource`：断言 `builtin_skills/` 的目录集合
  **恰好等于** `docs/scenes/skills/` 的目录集合，双向都查（源多了 = 没同步进二进制；镜像多了 = 下次 sync 会删）。

> ✅ **顺带发现的既有 drift（本次已修）**：`server/internal/service/builtin_skills/info-radar/`
> 存在于镜像目录，但**既不在 `docs/scenes/skills/` 里、也不在 Makefile 的 find 列表里**；
> 由于该 target 第一步是 `rm -rf`，**任何人执行一次 `make builtin-skills-sync` 都会静默删掉它**。
> 复查后发现比初评记录的更严重：`builtin_scenes/info-radar.yaml` 同样只存在于镜像，且它
> **确实声明了 `skills: - name: info-radar`**（初评"无场景引用"的判断有误），
> `TestBuiltinScenes_InfoRadarShape` 与 `TestBuiltinSkills_InfoRadarPresentAndValid` 都在跑。
> 两个 sync target 的危险性并不对称：`builtin-scenes-sync` 只 `cp`（additive，drift 无害），
> `builtin-skills-sync` 先 `rm -rf`（破坏性）。修法：把两份文件回填进 `docs/` 源码树，
> 再叠加上面的枚举改造 + 守卫测试，使这类 drift 不可能再无声发生。

### 5.3 场景接入：扩展 `viz-architecture`（已定案，覆盖初评建议）

> **决策变更**：初评建议新增 `code-architecture` 场景。经与需求方确认后**改为扩展现有
> `viz-architecture`**，并同步让该场景支持有仓库工作空间。下面记录定案方案与理由；被否决
> 的初评方案保留在末尾备查。

`viz-architecture` 原本的匹配规则明确偏好无仓库工作空间（`has_repo_count max: 0`, weight 16），
而 archify 的核心价值需要仓库——所以"接入"必须同时解决"这个场景能不能在有仓库时用"。定案改动：

1. **`skills:` 追加 `archify`**，场景从 3 个画图 skill 变 4 个。
2. **匹配规则改为双形态覆盖**：保留 `max: 0` weight 16（纯画图工作舱），新增 `min: 1`
   weight 6（有仓库也算命中）。仓库权重刻意压低，避免抢走 `go-dev`(40) / `ts-react-dev` /
   `generic-code`(25) 的常规代码工作空间。
3. **新增一条代码架构关键词规则**（weight 20），词表与原画图词表**刻意不重叠**（不含
   "架构图"/"architecture"），因此普通画图看板不会被重复计分；只有看板确实在谈"代码架构 +
   出图"时两条同时命中（0+6+18+20 = 44）才会压过 `go-dev`(40)——这正是应该让 archify 上场的场景。
4. **`disable_tool_groups` 去掉 `harness`**，只保留 `multi-agent`。既然该场景现在会跑在有
   仓库的工作空间里，就可能发生提交；继续禁用 harness 会让这些提交静默绕过仓库配置的
   pre-commit gate。
5. **选型 prompt 改为先判断"图的事实来源是代码还是用户口述"**，再判断"要不要可二次编辑"，
   避免 4 个 skill 挤在一起时选型含糊；并写明 `doctor` 不过就降级到 `fireworks-tech-graph`。
6. 新增两个 quick_actions：`map-repo-architecture`（按代码出带 SRC 证据的架构图）、
   `architecture-delta`（两个 ref 的架构差异）。

代价是初评担心的"一个场景 4 个画图 skill、选型 prompt 变复杂"确实发生了，用 §5.3-5 的
两级选型问题来抵消。换来的好处是用户不需要为"画图"和"按代码画图"记两个场景名，且
`viz-architecture` 从"无仓库专用"升级成通用出图舱——这本身就是需求方要的第二件事。

<details><summary>被否决的初评方案（备查）</summary>

- 新增 `code-architecture` 场景，匹配 `has_repo_count min: 1` + 架构/重构/review 类关键词，
  `skills: [archify]`。优点是"无仓库画图"与"有仓库映射架构"两个心智不互相污染；缺点是
  多一个场景名要用户记，且两个场景的画图选型知识会分叉维护。

</details>

### 5.4 写 `VENDOR.md`

按 `drawio-skill/VENDOR.md` 的既有格式记录：选型理由、upstream repo/commit/version、vendor 日期、许可、保留与排除清单、依赖清单、升级脚本。**注意一个惯例冲突**：drawio 的 VENDOR.md 声明"skill 本身是未修改的上游内容"，而 archify 因 §5.5① 很可能需要改动 SKILL.md，届时必须在 VENDOR.md 明确标注"非 unmodified upstream"并列出改动点。

### 5.5 三个必须先解决的问题

| # | 问题 | 事实依据 | 处理建议 |
|:-:|------|----------|----------|
| ① | **联网更新检查与 local-first 冲突** | SKILL.md 有 "Update awareness" 段，要求 agent 在首个候选件产出后**主动跑 `scripts/check-update.mjs`**；README 说明会 GET 固定 manifest（约 72h 一次，服务端只看到 IP+时间）。牛牛的承诺是"No data leaves your machine unless you connect an external source"，且 `scene_skills.go` 的整个设计前提就是"纯本地文件拷贝、不联网、不跑安装器" | 双保险：场景 `env_presets` 注入 `ARCHIFY_UPDATE_CHECK_DISABLED=1`（上游支持的官方开关，同时也禁掉 reminder 状态写盘）**并且**在 vendor 时删掉 SKILL.md 的 "Update awareness" 段——单靠 env 挡不住 agent 读到指令后的行为噪音 |
| ② | **产物预览沙箱会拦掉导出** — ✅ **本次已修** | `file-preview.tsx:108` 对 html 原为 `sandbox="allow-scripts"`。脚本能跑 → **交互式 viewer 可用**（搜索/focus/route/theme 都正常）；但缺 `allow-downloads` → **Export 菜单的 PNG/SVG/WebM 下载被浏览器静默拦截**（无报错、无提示，表现为点了没反应）；缺 `allow-same-origin` → clipboard 写入和 localStorage 主题记忆失效。**已用 Playwright 对真实 715 KB archify 产物 A/B 实测**：`allow-scripts` 单独 → PNG/SVG 导出均为 `null`；加 `allow-downloads` → 两者均成功落盘，导出 PNG 为 4320×2352 有效图像（此前担心的 opaque origin 下 `canvas.toBlob` 污染问题**未出现**） | 已改为 `sandbox="allow-scripts allow-downloads"`（commit `72b9802`）。**仍不给 `allow-same-origin`**——那才是真正有代价的一位，保持 opaque origin 让产物碰不到我们的 cookie/localStorage；代价（viewer 内剪贴板与主题记忆失效）可接受。另注：`file-preview.tsx` 约 724 行另有一处 `sandbox="allow-same-origin"` 供其他 renderer 使用，语义不同，未改动 |
| ③ | **内嵌体积翻倍** | 裁剪后实测 **2.4 MB**（初评估 2.2 MB），`builtin_skills/` 原 1.7 MB → 落地后 **3.4 MB / 8 skills / 176 文件**，内嵌 skill payload 翻倍有余（drawio 856 KB 是原最大者，archify 约为其 2.9 倍） | 已接受，在此显式记录。若要压：`assets/`(662 KB) 与 `renderers/`(1087 KB) 是硬需求不可裁，进一步压缩只能靠 gzip 存储 + 物化时解压（drawio 的 `shape-index.json.gz` 已有此先例） |

补充（非阻塞）：**Node 可用性**。archify 全程需要 Node ≥18。驱动 claude CLI 的用户几乎必然有 Node（Claude Code 本身是 Node 应用），但 codex / qwen / goose 等 agent 不保证。SKILL.md 自带 `doctor` 子命令，场景 prompt 里应写明：**`doctor` 不过时明确告知用户并降级到 `fireworks-tech-graph`**，而不是静默失败——这与 viz-architecture 场景对 cairosvg 缺失的"优雅降级，绝不报错"纪律保持一致。

---

## 6. 建议与优先级

| 优先级 | 事项 | 说明 |
|:---:|------|------|
| ~~**P1**~~ ✅ 已落地 | 按 §5.1–5.4 vendor archify + **扩展 `viz-architecture`（含支持有仓库）**，处理 §5.5① | 这是"收编"的最小完整闭环；①是硬前提，不能留着一个会联网的 skill 进 local-first 产品。①的处理方式：直接删掉 `scripts/check-update.mjs` + `scripts/update-contract.mjs` 并重写 SKILL.md 的 "Update awareness" 段（仅设 env 挡不住 agent 读到指令） |
| ~~**P2**~~ ✅ 已落地 | html 预览 iframe 加 `allow-downloads`（§5.5②） | 独立小改动，收益不止 archify——office-design 等场景的 HTML 产物同样受益。`docs/design-system.md` 的硬性裁决线是"禁止硬编码色值 / 间距字号圆角阴影走 token"，sandbox 属性不触及任何 token，故不触发该门禁。已用 Playwright 对真实 archify 产物 A/B 实测确认（详见 §5.5②修订） |
| ~~**P3**~~ ✅ 已落地 | 清理 §5.2 记录的 `info-radar` 镜像 drift | 实际比初评记录的严重（scene YAML 同样 drift 且被场景引用），已回填源码树并加守卫测试 |
| **不做** | 把 archify 做成牛牛内置的 Go 渲染服务 | 上游是活跃演进的 JS 项目（220 commits，v2.17 仍在 dev），重写即刻过时；vendored skill 是正确的耦合强度 |
| **不做** | 用 archify 替换现有三个画图 skill | 定位互补不重叠：要"可再编辑源文件"仍是 drawio/excalidraw，要"贴文档的静态成品图"仍是 fireworks |
| **不做** | 把 archify 做成牛牛内置的 Go 渲染服务 | 上游是活跃演进的 JS 项目（220 commits，v2.17 仍在 dev），重写即刻过时；vendored skill 是正确的耦合强度 |
| **不做** | 用 archify 替换现有三个画图 skill | 定位互补不重叠：要"可再编辑源文件"仍是 drawio/excalidraw，要"贴文档的静态成品图"仍是 fireworks |

**一句话结论**：archify 对牛牛有意义，且意义不在"画得好看"，而在于它把架构图从**一次性插图**变成**可验证、可锚定 commit、可 diff 的工程产物**——这正好补上牛牛所有 code 场景的可视化空白。继承路径清晰（与 drawio-skill 同构），主要成本不在集成本身，而在切断它的联网更新行为、以及给它一个不与"无仓库画图舱"混淆的场景归属。
