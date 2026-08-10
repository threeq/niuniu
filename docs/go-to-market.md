# 牛牛 AI（Niuniu）推广方案 / Go-to-Market

> 本文回答 issue #428「如何推广牛牛产品」，并重点回答其子问题
> **「可以在哪些渠道确定发布」**——见 [§3 发布渠道清单](#3-发布渠道清单可以哪些确定中发布)。
> 品牌释义见 [`brand-naming.md`](./brand-naming.md)，发布流程见
> [`ai-context/release-workflow.md`](./ai-context/release-workflow.md)。

## 1. 产品定位与目标人群

**一句话**：牛牛 AI 是一个**本地优先的通用 AI 工作站**，可并行编排多个 Claude
Code / AI agent，跨项目、跨仓库持续协作。

| 维度 | 内容 |
|------|------|
| 核心价值 | 一个人/小团队同时驱动多个 AI agent 干活：看板分配任务 → 每个 workspace 独立 worktree → agent 自动推进 |
| 差异点 | ① 本地优先、数据落 SQLite 不出本机；② 多 agent 并行编排（不是单聊天框）；③ 看板 + worktree + 工程规范流水线一体化 |
| 两个版本 | **个人版**（`niuniu-desktop` 桌面应用，Win/macOS/Linux）；**团队版**（云端 `self.niu6ai.com`，多租户） |
| 目标人群 | 优先级从高到低：① 已在用 Claude Code / Codex 的重度开发者；② AI 编程尝鲜的独立开发者 / 小团队 Tech Lead；③ 关注「AI agent 编排 / 多智能体」的技术内容受众 |

**定位标语建议（可 A/B）**：
- 中文：「让一群 AI 帮你并行干活的本地工作站」
- English: "A local-first workstation to run a fleet of AI coding agents in parallel."

## 2. 推广策略：三层漏斗

1. **认知层（让人知道）**——技术内容 + 开发者社区曝光。低成本、可持续，是 dev tool 唯一靠谱的获客起点。
2. **试用层（让人下载/注册）**——个人版桌面包零门槛下载；团队版提供 demo 环境（`demo.niu6ai.com`）。
3. **留存层（让人留下）**——上手 5 分钟跑通第一个 agent 任务的「Aha 时刻」+ 官网文档/视频。

**关键原则**：开发者工具靠**真实演示 + 可复现的上手路径**传播，而不是广告投放。第一优先级是把「下载即能跑通一个多 agent 任务」的体验打磨到位，再放大渠道。

## 3. 发布渠道清单（"可以哪些确定中发布"）

按**就绪度/确定性**排序。✅ = 基础设施已具备，可立即发布；🟡 = 需少量准备；⚪ = 需立项建设。

### ✅ 已就绪，可立即确定发布

| 渠道 | 现状依据 | 发布内容 |
|------|----------|----------|
| **GitHub Releases**（`github.com/threeq/niuniu-public`） | 公开仓库已存在；`release-sync.yml` 已能从 `vX.Y.Z` tag 自动构建 Win/macOS(.dmg)/Linux 三平台并发布 Release | 个人版桌面安装包（主分发入口） |
| **官网 niu6ai.com / www.niu6ai.com** | 官网源码（Astro）已在 `niuniu-public/website`；安装指南页 `/docs/install/personal` 已规划 | 下载入口、按平台安装矩阵、文档、定位介绍 |
| **团队版云服务 self.niu6ai.com** | 生产环境已部署（Aliyun ECS + Caddy + PG），`deploy.sh` 可发布 | 在线注册试用团队版 |
| **Demo 环境 demo.niu6ai.com** | 由 `dev` 分支部署，已存在 | 无需安装的在线体验入口（放官网首屏 CTA） |

> 这四个是「确定可发布」的**自有渠道**——基础设施已经在仓库里跑通，发布动作即可执行。

### 🟡 低成本即可铺开（建议本轮纳入）

| 渠道 | 准备项 |
|------|--------|
| 技术社区文章 | 掘金 / 知乎 / 少数派（中文）、Dev.to / Hacker News "Show HN" / Reddit r/ChatGPTCoding(英文)：发「我如何用牛牛同时跑 N 个 Claude Code agent」实战贴 |
| 演示视频 | 录 60–90s 屏录：看板建 issue → 起 workspace → agent 自动改代码 → 看 diff。投 B 站 / YouTube / 嵌官网首屏 |
| README 即落地页 | `niuniu-public/README` 加截图 + GIF + 一键下载链接（开发者第一触点） |
| 微信/技术群 + X/Twitter | 发布即同步，配演示 GIF |

### ⚪ 需立项建设（后续规划，不在本轮"确定发布"范围）

- 应用商店 / 包管理器分发：Homebrew Cask、Scoop/winget、Microsoft Store、Snap/Flatpak（需签名与上架流程）。
- 桌面应用目前**未签名**（见 release-workflow 文档），上架商店前需补代码签名/公证。
- 付费投放、KOL 合作、线下/线上 meetup——建议有了留存数据再投入。

**本轮"可以确定发布"的结论**：✅ 的 4 个自有渠道（GitHub Releases、官网、团队版云、Demo）即可立刻发布；🟡 的社区/内容渠道建议同步启动，成本低、对 dev tool 转化最直接。

## 4. 落地动作（建议的最小可执行清单）

| # | 动作 | 渠道 | 前置 |
|---|------|------|------|
| 1 | 打 `vX.Y.Z` tag，产出三平台个人版包 | GitHub Releases | release-sync 已就绪 |
| 2 | 官网首屏加「在线体验(demo) + 下载(个人版) + 注册(团队版)」三 CTA | 官网 | demo/team 已在线 |
| 3 | README 配截图/GIF + 下载徽章 | niuniu-public | — |
| 4 | 录 1 支 90s 演示视频 | B站/YouTube/官网 | 产品可跑通 |
| 5 | 写 1 篇实战长文（多 agent 并行场景），中英各一 | 掘金/Dev.to/Show HN | 视频/截图 |
| 6 | 打磨「下载到跑通第一个 agent 任务 ≤5 分钟」上手路径 | 产品+文档 | 最高优先级 |

## 5. 衡量指标

- **认知**：官网 UV、文章阅读/点赞、GitHub Star & Release 下载数。
- **试用**：个人版下载量、团队版注册数、demo 进入率。
- **留存**：注册后 7 日内成功跑通 ≥1 个 agent 任务的比例（北极星指标）。

---

### 待用户确认的事项

issue 原描述「1. 可以哪些确定中发布」语义较简，本文按「**可以在哪些渠道确定发布**」
理解作答。若原意是「**哪些产品功能可以确定在下一版本中发布**」（即版本 feature
排期），则需另立 issue 并补充候选功能清单——这属于产品规划而非推广范畴。

---

# 附录：三块深化方案

## A. 应用商店 / 包管理器分发

### A.0 关键约束（决定哪些渠道适合）

牛牛个人版需要 **① 拉起子进程**（git、claude/codex CL、PTY 终端）、**② 较宽的
本机文件系统访问**（管理多个 workspace / worktree）。因此：

- ❌ **沙箱化打包不适配**：Flatpak、Snap、以及 Microsoft Store 的 **MSIX/PWA**
  路径会切断子进程与文件系统访问，硬塞进去会破坏核心功能或需大量豁免声明。
  **默认不投这些打包形态**，除非专门做沙箱兼容改造。
- ✅ **微软商店走「EXE 或 MSI 应用」路径例外可投**：该路径是非打包 Win32，无 MSIX
  容器限制，子进程/全盘访问照常工作——上架现有 `.exe`/安装包即可（仍需代码签名 +
  过微软审核；不接 Store 自动更新，沿用自带 updater）。
- ✅ **非沙箱、面向开发者的包管理器适配**：Scoop / Homebrew Cask / winget /
  AppImage / .deb·.rpm——直接分发二进制，不破坏运行模型。

### A.1 前置依赖：代码签名（最高优先，解锁后续一切）

桌面包目前**未签名**（见 [`release-workflow.md`](./ai-context/release-workflow.md)）。
未签名会触发 macOS Gatekeeper / Windows SmartScreen 拦截，直接劝退新用户，也是上
任何包管理器前的硬门槛。

| 平台 | 需要 | 成本/年 | 产出 |
|------|------|---------|------|
| macOS | Apple Developer 账号 + `codesign` + `notarytool` 公证 | $99 | 公证后的 .dmg，双击即开 |
| Windows | OV/EV 代码签名证书（EV 可立刻消除 SmartScreen 累积期） | $200–600 | 签名 .exe |

> 在 `release-sync.yml` CI 里加签名/公证步骤，证书走 GitHub Secrets。**这一步不做，
> 包管理器分发的体验都是打折的。**

### A.2 渠道优先级（投入产出比，面向 dev 受众）

| 优先 | 渠道 | 平台 | 工作量 | 说明 |
|------|------|------|--------|------|
| P0 | **Scoop**（自建 bucket 或 extras） | Win | 极小 | 一个 JSON manifest 指向 GitHub Release 资产，无需签名也能用；开发者高频使用，最划算的第一枪 |
| P0 | **Homebrew Cask** | macOS | 小 | 一个 Ruby cask 指向 .dmg；**依赖 A.1 公证**否则仍弹警告；dev 标配 |
| P1 | **winget** | Win | 小-中 | 向 `microsoft/winget-pkgs` 提 manifest PR；覆盖面广，签名后体验佳 |
| P1 | **AppImage + .deb/.rpm** | Linux | 小 | 直接挂 GitHub Release；AppImage 免安装，.deb/.rpm 给包管理用户。注意附带 WebKitGTK 依赖声明 |
| P2 | Microsoft Store（**EXE/MSI 路径**） | Win | 中 | 选「EXE 或 MSI 应用」而非 MSIX/PWA，绕开沙箱；需签名 + 微软审核；有签名后可考虑 |
| P3 | Snap / Flatpak / Store(MSIX) | 全 | 大 | 沙箱冲突（见 A.0），需专门改造，**暂缓** |

### A.3 落地顺序

1. 先做 **A.1 签名/公证**并接入 CI。
2. 同步上 **Scoop + Homebrew Cask**（两个 manifest 仓库，改动小，覆盖核心 dev 受众）。
3. 再补 **winget + Linux 包**。
4. 商店类（P2）等有留存数据再评估是否值得为沙箱改造投入。

---

## B. 最小落地清单 + 北极星指标

### B.1 北极星指标（North Star）

> **新用户在注册/安装后 7 日内，成功跑通 ≥1 个 AI agent 任务（agent 真正改了代码
> 并产生可见 diff）的人数。**

选它的理由：它同时编码了「下载到」（获客）+「跑通了」（激活）+「看到价值」（diff
= Aha 时刻），是 dev tool 真正的价值兑现点，比单纯下载量/注册数更抗虚荣。

### B.2 漏斗指标分层

| 漏斗 | 指标 | 定义 | 埋点位置 |
|------|------|------|----------|
| 认知 | 官网 UV / Star / Release 下载数 | 触达规模 | 官网分析 + GitHub API |
| 激活 | 安装后首次起 workspace 率 | 装了是否动手 | 客户端遥测（见 [`telemetry-privacy.md`](./telemetry-privacy.md)，须脱敏/可关） |
| **价值（北极星）** | 7 日内跑通 ≥1 agent 任务率 | Aha 时刻 | agent 任务完成事件 |
| 留存 | 次周/次月活跃 | 是否回来 | 会话/任务活跃 |
| 口碑 | Star 增速、社区提及、自然外链 | 自传播 | 人工 + GitHub |

> 埋点务必遵守 `telemetry-privacy.md`：默认脱敏、可一键关闭，本地优先产品的隐私承
> 诺是信任基础，别为指标砸了招牌。

### B.3 最小可执行清单（按依赖排序）

| # | 动作 | 渠道 | 前置 | 衡量 |
|---|------|------|------|------|
| 0 | **打磨「安装→跑通首个 agent 任务 ≤5 分钟」路径**（含示例 issue 模板） | 产品+文档 | — | 北极星 |
| 1 | 桌面包签名/公证接入 CI | release-sync | — | 安装成功率 |
| 2 | 打 `vX.Y.Z` tag 出三平台包 | GitHub Releases | #1 | 下载数 |
| 3 | 官网首屏三 CTA：在线体验(demo)/下载(个人版)/注册(团队版) | 官网 | demo·team 已在线 | UV→点击率 |
| 4 | README 加截图 + GIF + 下载徽章 | niuniu-public | #2 | 仓库转化 |
| 5 | 录 90s 演示视频（建 issue→起 workspace→agent 改码→看 diff） | B站/YouTube/官网 | #0 | 播放/完播 |
| 6 | 实战长文（多 agent 并行场景）中英各一 | 掘金/Dev.to/Show HN | #5 素材 | 阅读→下载 |

排序原则：**#0 最高优先**——渠道把人引来，若 5 分钟跑不通价值，全是漏桶。先把激活
到北极星这段打通，再放大流量。

---

## C. 低成本可同步铺开（内容 + 社区）

成本几乎为零、对 dev tool 转化最直接，建议与发布同步启动。核心打法：**一份核心素材
（演示 GIF/视频）一鱼多吃，按平台改写。**

### C.1 渠道与打法

| 渠道 | 内容形态 | 节奏 | 要点 |
|------|----------|------|------|
| 掘金 / 知乎 / 少数派 | 实战长文 + GIF | 首发 1 篇，之后每 2 周 1 篇 | 标题落到具体场景：「我如何用牛牛同时跑 5 个 Claude Code agent 重构老项目」 |
| Dev.to / Hacker News(Show HN) / Reddit r/ChatGPTCoding | 英文实战 + 链接 | Show HN 单发（择周二/周三上午 PT） | Show HN 标题朴素直给：`Show HN: Niuniu – run a fleet of AI coding agents in parallel, local-first` |
| B站 / YouTube | 90s 演示 + 进阶教程 | 演示先行，后续教程 | 完播率优先，前 3 秒出 diff 结果 |
| X/Twitter + 微信技术群 | 演示 GIF + 一句话 | 每次发布即同步 | 配可点击的下载/demo 链接 |
| GitHub README | 截图 + GIF + 徽章 | 随版本更新 | 开发者第一触点，等同落地页 |

### C.2 内容选题池（围绕差异点）

1. 多 agent 并行：一人同时推进多个 issue 的真实录屏。
2. 本地优先/隐私：数据落本机 SQLite、不出网，对比云端方案。
3. 看板 + worktree 工作流：从建 issue 到 agent 自动产出 diff 的闭环。
4. 工程规范流水线（spec/plan/impl/test 自动化检查）实操。
5. 团队版多租户协作场景。

### C.3 低成本原则

- **不投广告**：先靠真实演示 + 可复现上手路径自然传播，有留存数据再谈付费/KOL。
- **一鱼多吃**：1 支演示视频 → 切 GIF（README/X）、截帧（文章）、长视频（B站/YT）。
- **可复现**：每篇内容都附「3 步上手」链接，把读者直接导到北极星路径（见 §B）。
