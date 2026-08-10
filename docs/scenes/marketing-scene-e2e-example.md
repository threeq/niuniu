# 营销 scene 端到端示例（内容营销全流程 / 社媒运营周报）

> 子任务A·营销 scene 包（Epic #625 波次0）。演示如何用牛牛**现有原语**把一个跨职能营销
> 任务建模为 `issue → no_repo workspace → agent（带 curated 工具）→ 看板列/gate → 交付物`，
> 无需任何内核架构改造。

## 涉及的两套模板

| 场景（scene，curated MCP + 阶段动作 + gate 模板） | 配套项目模板（blueprint，阶段化看板列 + phase_prompt） |
|---|---|
| `content-marketing`（内容营销全流程） | `content-marketing-flow`：需求 → 选题/关键词 → 素材/文案 → 审校 → 发布 → 复盘 |
| `social-ops`（社媒运营周报） | `social-weekly-ops`：规划 → 排期 → 数据采集 → 周报撰写 → 复核 → 完成 |

- 场景定义：`docs/scenes/builtin/content-marketing.yaml`、`docs/scenes/builtin/social-ops.yaml`
  （改后 `make builtin-scenes-sync` 同步进二进制）。
- 项目模板（builtin blueprint）：`server/internal/service/project_blueprint.go` 的
  `builtinBlueprintDefs()`；首启 `SeedBuiltins` 播种，升级库经 `BackfillMarketingBlueprints`
  一次性补种。每个 blueprint 已按 slug 快照对应场景，新建项目套用后即自动挂上该场景。

## 示例一：内容营销全流程（cross-border 落地页选题→复盘）

**目标任务**：为一款跨境 DTC 产品的英文落地页做一轮内容营销——选题、写文案、审校、发布、复盘。

1. **建项目（选模板）**：新建项目时在「项目模板」下拉选「内容营销全流程」。项目按 blueprint
   生成 6 个看板列（需求/选题·关键词/素材·文案/审校/发布/复盘），每列带 `phase_prompt`
   指导该阶段该做什么；`content-marketing` 场景已作为项目默认场景挂好。
2. **建 issue + no_repo workspace**：在「需求」列建 issue（如「Product X 落地页内容一轮」），
   为它启动**无仓库工作空间**（`no_repo:true` / `CreateWorkspaceInput.NoRepo`，零 worktree）。
   工作空间自动物化该场景：`.mcp.json` 写入 `fetch`；`.claude/skills/site-audit/` 拷入站点
   审计技能；`CLAUDE.md` 注入场景指引与助手话术；若在「设置→集成」绑过 SerpAPI/GSC，
   `call_external_api` 即可取数（未绑则相关步骤自动跳过）。
3. **阶段推进（agent 带工具执行）**：
   - 「选题/关键词」：跑快捷动作 **选题 + 关键词研究**、**竞品内容差距分析** → 产出选题清单。
   - 「素材/文案」：**生成文案初稿** → 产出 `draft-product-x.md` + meta + 社媒短文案。
   - 「审校」：**审校清单** 逐项把关（事实可溯源 / 合规 / 品牌 / SEO-GEO / 可读性）。
     这一步是**门禁**：可在该列绑定场景带来的「营销文案审校门禁」(`copy-review-gate`,
     ai_judge) 作为 `phase_exit` gate，未过不进「发布」（默认 warning，可调 error 硬闸）。
   - 「发布」：**发布清单** + `site-audit` 技术审计给「可发布 / 待修复」结论。
   - 「复盘」：**效果复盘报表** 拉排名/搜索表现/AI 引用做环比（达成后 `advance_issue` 到复盘=完成态）。
4. **交付物**：每步最终产物登记到 `<workspace>/.niuniu/artifacts.json`，右侧产物预览面板据此展示。

## 示例二：社媒运营周报（定时 + 通知）

**目标任务**：每周一自动产出上周社媒运营周报并推送给团队。

1. 用「社媒运营周报」项目模板建项目 → 6 列 + `social-ops` 场景。
2. 为周报 issue 启 no_repo workspace（`fetch` 就绪；Meta Ads/Google Ads/GSC 走用户自建
   provider，绑定后可取投放/搜索数据，未绑则引导用户导出）。
3. **定时（复用 scheduler）**：在工作空间「设置→定时触发」配 cron（每周一上午），触发消息
   「生成运营周报」。到点调度器唤起本工作空间 agent，自动跑 **运营数据采集 → 生成运营周报**。
4. **看板联动 + 通知（复用 notify/imbot）**：周报生成后 agent 用 `batch_create_issues` 建
   「本期周报 + 关键结论」issue；若配了站内通知或 imbot 渠道，周报摘要经其推送给相关人
   （未配则仅在看板留存）。整条链路（scheduler→agent→建 issue→notify/imbot）全是现有原语，无需新代码。

## 自动化验证（把「跑通」固化为测试）

端到端编排路径由单测覆盖，`cd server && go test ./internal/service/ -run 'MarketingScene|Blueprint|Scene'`：

- `TestMarketingScene_ContentMarketing_Projects` / `_SocialOps_Projects`：解析两个 builtin
  场景 YAML 并按 workspace-enable 的 `MergeFrom` 路径投影，断言 curated MCP（fetch）、
  site-audit 技能、`copy-review-gate` 审校门禁、可选跨境数据源凭证、定时/通知指引均就位。
- `TestProjectBlueprint_SeedsMarketingBlueprints`：两套营销 blueprint 播种出 6 阶段看板列、
  首列非动作/末列 complete、审校/复核列 phase_prompt 内建审校纪律、并各快照对应场景；
  套用后在新项目上重现全部列。
- `TestProjectBlueprint_BackfillMarketingBlueprints`：升级库一次性补种、幂等、用户删除不复活。
- `SceneSeeder` 的 embed 遍历用例自动覆盖两个新 YAML 的 parse/validate/seed。

## 设计系统说明

本子任务只新增**数据**（场景 YAML + builtin blueprint），无新增前端组件——`display_name` /
`description` / 列名 / phase_prompt 等字符串由既有的场景选择器、项目模板选择器、看板渲染，
本就遵循 `docs/design-system.md`（token / shadcn / `t()` / 暗色均等），故无新的设计系统改动面。
