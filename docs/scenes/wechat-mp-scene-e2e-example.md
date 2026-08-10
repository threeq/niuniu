# 运营闭环通用能力 + 微信公众号首个落地 · 端到端示例

> 子任务D（Epic #625「牛牛 → Team OS」营销/运营 scene 系列，用户 2026-07-23 定调升级）。
> **首要交付物 = 平台无关的通用「运营闭环」能力**（所有运营场景可复用）；微信公众号 =
> 验证它的**首个平台落地实例**。全程复用牛牛现有原语（scene + blueprint + kanban 列 +
> phase_prompt + gate + batch_create_issues/start_workspace/advance_issue + scheduler cron +
> notify/imbot），**无内核改造**。

## 一、通用「运营闭环」能力（平台无关，首要交付物）

任意运营（公众号 / 内容营销 / 社媒 / 未来小红书·抖音…）跑**同一个闭环**，只有平台不同：

```
① 资料/近期热点收集与分析 → ② 选题·关键词 → ③ 内容生产（撰稿·排版·发布前审校门禁）
→ ④ 发布 → ⑤ 效果追溯·分析，且 ⑤ 洞察【回流】成下一轮 ① 输入（闭环、可周期驱动）
```

**单一实现（`server/internal/service/ops_loop.go`）**——不为每个平台各写一份：

| 构件 | 作用 | 复用点 |
|---|---|---|
| `OpsLoopParams` | 平台参数（平台名 / 发布动作 / 数据指标 / 选题·撰稿·排版·发布·合规话术） | 每平台一组参数 |
| `OpsLoopChildTemplate` + `opsLoopChildTemplates(p)` | **Epic 父 + 三类子issue** 一等结构：A 采集·选题(周期) / B 每内容(可并行,生命周期=`opsLoopColumns`) / C 复盘·回流(周期)，各带 phase_prompt、周期/并行标志、fan-out/回流语义 | 编排约定 + 蓝图 + 文档均从此派生（单源） |
| `opsLoopColumns(p)` | 生成 B 内容子issue 的 6 阶段生命周期看板（采集/选题→撰稿→排版→审校门禁→发布→数据复盘·回流） | ≥2 蓝图共用 |
| `opsLoopOrchestrationPrompt(p)` | 运行时 Epic父→A/B/C 编排约定，**三类子issue 段落由 `opsLoopChildTemplates` 逐条生成** | ≥2 场景共用（单源，测试断言不漂移） |
| `opsLoopBlueprintDef(p, desc)` | 由骨架 + 场景快照装配一套 builtin 闭环蓝图 | 每平台一行 |
| `opsLoopPlatforms()` | 复用该能力的运营平台注册表（公众号 + 内容营销） | **「≥2 场景复用一套能力」的证据** |

**运行时编排结构 = Epic 父issue → 三类子issue（参数化、平台无关）**：

```
Epic 父issue（运营活动，issue_type='epic'）
 ├─ A 采集·选题子issue（周期，cron 起）  = ①+② → 产『选题清单』→ fan-out B
 ├─ B 内容子issue（每选题一个，可并行）   = ③④⑤ 作为其生命周期看板列
 └─ C 复盘·回流子issue（周期，cron 起）   = 汇总各 B 的 ⑤ → 提炼 → 回灌 ①
```

**≥2 运营 scene 实际复用（通用性验证，非复制粘贴）**：

| 运营 scene | 闭环 blueprint（`<平台>-ops`，共享 `opsLoopColumns`） | 场景内 `ops-loop-orchestration` 约定（共享 `opsLoopOrchestrationPrompt`） |
|---|---|---|
| `wechat-mp`（微信公众号运营） | `wechat-mp-ops` | ✅ 接入 |
| `content-marketing`（内容营销全流程） | `content-marketing-ops`（与线性版 `content-marketing-flow` 互补） | ✅ 接入 |

- 蓝图播种：`builtinBlueprintDefs()` 首启 `SeedBuiltins`；升级库经 `BackfillOpsLoopBlueprints`
  一次性补种（幂等、migration ledger、`WHERE NOT EXISTS` 守卫、用户删除不复活）。
- 场景约定：`ops-loop-orchestration` prompt 在两个场景 YAML 里各带一份，其正文由
  `ops_loop.go` 单源生成，`scene_ops_loop_test.go` 断言两者**不漂移**。

## 二、首个平台落地：微信公众号官方 API MCP（`wechat-mp`）

场景声明一台「公众号运营 MCP」——官方 API 版，后端封装 `open.weixin.qq.com` 的
发布/草稿/素材/用户分析/消息（社区 `wenyan-mcp`/`weixin-mcp` 类已验证，用户自建部署）：

```yaml
mcp:
  - name: wechat-mp
    config:
      type: http
      url: https://your-wechat-mp-mcp.example.com/mcp   # 替换成你自部署/授权的端点
      headers:
        Authorization: Bearer ${cred:wechat-mp.token}   # credstore 注入，永不落场景/缓存
required_credentials:
  - alias: wechat-mp
    provider: wechat-mp
    optional: false
```

- **凭据主权**：牛牛注入的仅是访问**你自有 MCP 端点**的令牌（credstore 解密写入
  `.mcp.json`）；企业资质 / AppID / AppSecret 由用户自备并部署在其 MCP 后端——**牛牛不代持
  公众号凭据、不接触 AppSecret**。token 未绑定时该 MCP 整体丢弃（spec §4.2.4：不发半张鉴权头）。
- **appid/secret 形式**：若 MCP 采用 stdio + `env WECHAT_APP_ID/WECHAT_APP_SECRET`（wenyan-mcp
  形态），改成 `{command,args,env}` 并把 env 值写成 `${cred:wechat-mp.appid}` /
  `${cred:wechat-mp.secret}`，env 占位符同样由 credstore 注入。

## 三、端到端跑通一轮完整闭环（含子issue fan-out 与回流）

**目标**：一个自有公众号，周期化产出推文并让效果洞察回流下一轮。

0. **绑定凭据（一次性）**：设置 → 集成/凭据，为 provider `wechat-mp`、alias `wechat-mp` 录入
   访问你自有 MCP 端点的令牌（字段 `token`）。凭据入 credstore，不落场景。
1. **建运营 Epic（父issue）**：新建项目时选「微信公众号运营」项目模板（= `wechat-mp-ops` 闭环
   蓝图，6 列 + `wechat-mp` 场景已挂）。把承载「本公众号运营」的父 issue 设为
   `issue_type='epic'`（运营活动 Epic）。
2. **A 采集·选题子issue（周期，cron 起）**：
   - cron（设置→定时触发，如每周一上午）唤起 A 的 agent，执行「采集/选题」列 phase_prompt：
     收集近期资料/热点、定选题·角度，产『选题清单』。
   - fan-out：对清单里每个选题
     `batch_create_issues(tasks=[{title:"推文·<选题>", parent_issue_id=<运营Epic>,
     issue_type:"task", exec_wave:1}, …])` 建 B 内容子issue（同波可并行）。
3. **B 内容子issue（每选题一个，可并行）**：`start_workspace(issue_id=<B>)` 起工作空间；B 沿
   其生命周期列流转 —— 撰稿 → 排版（经公众号 MCP 排版/素材/草稿，记 `media_id`/`draft_id`）→
   **审校门禁**（未过不进发布，可绑 `wechat-review-gate` ai_judge gate）→ 发布/定时（官方
   发布接口，记 `publish_id`/`msg_id`；全员群发先经用户确认）→ 数据复盘（拉阅读/在看/涨粉…）。
   多个 B 并行推进，用 `advance_issue(B, <列>)` 自报流转。
4. **C 复盘·回流子issue（周期，cron 起）**：
   - cron（如每周五）唤起 C，汇总同 Epic 下各 B 的 ⑤ 数据 → 提炼「有效选题/标题/时段/涨粉归因」。
   - **回流**：把已验证结论写回选题知识沉淀（知识库 MCP 或项目 `.niuniu/artifacts.json`），
     作为**下一轮 A** 的输入起点；用 `batch_create_issues(parent=<运营Epic>)` 建「本期复盘 +
     下期选题建议」跟进 issue；配了 notify/imbot 则推送摘要。
5. **闭环成立**：下一周期 A 起轮时读取 C 回流的结论，选题更准 → 新一轮 B → 新一轮 C…… 周而复始。

> 每步最终产物（选题清单、推文稿、草稿标识、审校报告、发布结果、复盘报表）登记到
> `<workspace>/.niuniu/artifacts.json`，右侧产物预览面板据此展示。

## 四、合规红线（必守）

1. 仅走公众号**官方 API** + 用户对**自有公众号**的显式授权；不引入浏览器自动化/爬取/模拟登录。
2. 授权链在用户侧：企业资质/AppID/AppSecret 由用户自备并部署在其自有 MCP 后端，牛牛不代持
   公众号凭据；token 永不落场景定义或投射缓存。
3. 发布属高影响外发：面向全部受众的群发/定时必须先经用户确认，遇平台频次/人工复核限制如实
   回报、不绕过。
4. 数据不外传：只通过公众号 MCP 读取用户自有数据用于本工作空间产物，不转发其它服务。

## 五、自动化验证（把「跑通」固化为测试）

`cd server && go test ./internal/service/ -run 'OpsLoop|WechatMPScene|Blueprint|Scene'`：

- `TestOpsLoop_SharedColumnsContract`：共享 `opsLoopColumns` 对每个平台都产出 6 阶段、首列非
  动作/末列 complete、审校列含「门禁+合规」、采集列教 `batch_create_issues` fan-out、复盘列教
  「回流」、平台话术真的织入——证明是参数化而非套壳。
- `TestOpsLoop_ChildTemplates`：三类子issue（A/B/C）是一等结构——恰好三种、A/C 周期(cron)、
  B 并行且唯一带生产·发布·效果生命周期看板、各 phase_prompt 织入平台并点名所驱动的原语。
- `TestOpsLoop_OrchestrationPromptBuiltFromChildren`：运行时编排约定由 `opsLoopChildTemplates`
  逐条生成——每个子issue 的 label 与 phase_prompt 都出现在编排正文里，三类子issue 与文案不漂移。
- `TestOpsLoop_ReusedByMultiplePlatforms`：注册表 ≥2 平台，各自映射到不同闭环蓝图 + 场景、
  同一 `opsLoopColumns`/`opsLoopBlueprintDef` 代码路径——「≥2 场景复用一套能力」的硬证据。
- `TestOpsLoop_SceneOrchestrationPromptNoDrift`：两个场景 YAML 的 `ops-loop-orchestration`
  约定与 `ops_loop.go` 单源一致（不漂移），平台名已参数化织入。
- `TestWechatMPScene_Projects` / `_CredentialInjection`：公众号场景投影出 curated 官方 API MCP
  （http + credstore 占位符鉴权、非明文）、发布前审校门禁、全阶段快捷动作、合规/定时/编排 prompt；
  绑定时占位符解析为真实令牌、缺失时整台 server 丢弃、绝不泄漏。
- `TestProjectBlueprint_SeedsWechatMPBlueprint` / `_SeedsContentMarketingLoop`：两套闭环蓝图播种出
  6 阶段列、审校列内建「门禁+合规」纪律、各快照对应场景；套用后在新项目上重现全部列。
- `TestProjectBlueprint_BackfillOpsLoopBlueprints`：升级库一次性补种两套闭环蓝图、幂等、用户删除不复活。

## 六、设计系统说明

本子任务只新增**数据/后端**（场景 YAML + 共享 Go 骨架 + builtin blueprint），无新增前端组件——
`display_name` / 列名 / phase_prompt / 快捷动作标签等字符串由既有的场景选择器、项目模板选择器、
看板渲染，本就遵循 `docs/design-system.md`（token/shadcn/`t()`/暗色均等）。录公众号凭据复用既有
通用凭据绑定流（「绑定凭据」卡片 + credstore），未新增专用向导，故无新的设计系统改动面。
