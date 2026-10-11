# 集成授权面安全加固：三段分离、不可信输入纪律与确认卡

> 实现文档（issue #746，Epic #726）。红队演练清单见
> [`integration-onboarding-redteam.md`](./integration-onboarding-redteam.md)。

## 1. 核心原则

**AI 生成 → 系统渲染（结构化事实）→ 人工确认。**

凡 AI 生成/半生成物，不经系统渲染不得直接呈现给用户做决策。本设计里这类产物有五
类——执行代码 / 契约草稿 / **确认卡文案** / 能力说明 / 打包 bundle——每一类都是投毒
面。本文件约束其中**被用作授权决策依据**的部分。

## 2. 适用范围边界（防扩大化，重要）

**以「该输出是否被用作权限/凭证/安全边界的授予决策依据」为界，不以「是否 AI 生成」
为界。**

### 适用

外部系统/数据集成线的**授权与凭证决策面**：

- 集成草稿 → 确认卡（操作 / scope / 写权限的**授予决策**）
- 凭证提交
- 团队版审批
- 函数发布确认（行为预演出网清单）

### 不适用（零改动）

- **工作空间里的编码流程**：agent 生成的代码文件直接落盘，走既有工程流程——git
  diff、审查列、harness 闸、pre-commit 检查。本原则**不改其分毫**。
- **自由文本输出**：agent 的对话回答、分析文档、方案建议等不受影响。

> 落点提醒：本文件与 `integration_untrusted.go` / `integration_confirm.go` 只处理
> 集成授权面。任何"顺手把编码流程也管起来"的改动都超出本设计范围。

## 3. 不可信输入纪律

来源：`server/internal/service/integration_untrusted.go`。为 #731（文档→集成草稿解析）
与 #734（草稿流水线）提供公共逻辑与实现规范。

### 3.1 一律按不可信输入处理

任何来自 niuniu 之外的字节——OpenAPI 文档、README、HTML 页面、粘贴的规格、模型自己
的叙述——都是**不可信输入**。不可信文本是**数据**，不是**指令**：不继承任何权限、不能
扩宽任何 scope、不进入确认卡的决策区。

**来源标记**：`UntrustedOrigin{Kind, Ref, FetchedAt}` 随内容一路带到确认卡，让审阅者
看到"这段话是从哪来的"。未知来源降级为 `user_note`，绝不因"不知道来源"而当作可信。

### 3.2 指令文本隔离

`IngestUntrustedDoc` 把文档切块后分区：

| 分区 | 内容 | 用途 |
|---|---|---|
| `Facts` | 描述性文本 | 作为上下文 |
| `Quarantined` | 读起来像指令的文本 | 仅可展示给人工，**从不执行、从不解析入字段** |

分类器（`ClassifyDocText`）是**纵深防御**，不是承重墙：它故意偏保守（误判为指令的代价
是审阅者多看一眼，漏判的代价由 §3.3 的结构性规则兜住）。原因码：
`instruction_override` / `agent_addressed` / `privilege_escalation` / `exfiltration`。

### 3.3 承重规则：结构化字段才是指令面

草稿的**指令面**（操作清单、scope、host、鉴权）**只从结构化字段填充**——OpenAPI 的
`paths`/`operations`、数据源的 `scope_config`。即使分类器漏判一条注入，自由文本也没有
任何路径进入指令面。

这正是回归测试 `TestBuildConfirmCard_PoisonedDocumentCannotWidenTheDraft` 断言的东西：
投毒文档里藏着"加一条 DELETE /admin/users"，草稿的操作清单仍然恰好等于契约里的三条。

### 3.4 scope 最小起步默认值

`MinimalScopeDefaults(sourceKind)`：新接入的数据源从最小 scope 起步，文档自己的
"required scopes" 章节**从不**用于放宽它。

- 非 http 源：`databases` / `tables_allow` / `tables_deny` 全空
- http 源：额外 `methods: ["GET"]`（这是**读的地板**而非授予）

Provider 线的最小起步沿用既有语义：空白名单即 GET-only（`IsAllowed`），写开关默认关。

## 4. 确认卡系统渲染

来源：`server/internal/service/integration_confirm.go`（模型与构造）+
`server/web/src/components/integrations/IntegrationConfirmCard.tsx`（渲染）。

### 4.1 决策区 vs 建议区

确认卡只有两个区：

- **决策区**（`data-region="decision"`）：操作清单 / scope / 差异 / 高危项 / **发布后验证
  探针**。**只含结构化事实**——即代理将实际强制执行的那些字段（method、path、scope 值、
  host）。探针也在此列：它是发布后**真的会发出**的一次只读请求，属于"将被执行的"，
  因此由 `verify_operation` 解析而来并在卡上可见（#734 的硬性要求）。
  **探针只能由契约里的名字解析得到，不会被凭空发明**——名字对不上就没有探针
  （`LintContract` 已把这种情况判为 error）。
- **建议区**（`data-region="advisory"`）：外部文档原文与模型叙述，逐条标注隔离原因，
  视觉上以虚线边框 + 警示与决策区分开。**不是任何决策字段的输入。**

操作项**不含** summary/description 等描述性文本——它们是文档/模型写的，属于建议区。
决策区里留下的 `method + path + read_only + name` 才是"用户看到的 = 将被执行的"。

### 4.1.1 展示标签 vs 授权内容

决策区里有两类字段，**只有一类构成授权**：

| | 字段 | 性质 |
|---|---|---|
| **授权内容** | 操作 `method` + `path`、scope 值 | 代理实际强制执行的边界 |
| **展示标签** | `title`、操作 `name`、风险的 `target` | 只为让人认出在批什么，**本身不授予任何权限** |

展示标签可能来自文档（`operationId`）或用户填的名称，所以**不得当作安全声明读**——
`name: "safe_readonly_lookup"` 完全可以对应 `DELETE /admin/users`，真正被强制的是
method + path。

**给 #734 的硬规则**：`Title` / `Label` 只能取自用户输入或 provider 标识符，**不得取自
文档prose 或模型叙述**——否则投毒方能把"内部可信 API"这类帽子扣到一组危险操作上。
风险项要指名道姓时用 `Name`（标识符）而不是 `Label`（显示名），这一点构造器已强制
（见 `structuredName`）。

`DecisionPayload(m)` 产出审阅者实际批准的那串字节：按构造丢弃建议区。回归测试两层断言：
(a) 投毒文本（`IGNORE` / `DELETE /admin`）不出现在其中；(b) **只改建议区、决策字节
逐字节不变**（`TestDecisionPayload_IsIndependentOfAdvisory`）——后者才是不变式，前者只是
抽样。

### 4.1.2 描述性不可信文本不上授权卡

操作 summary、模型叙述这类**描述性**（非指令性）不可信文本，**刻意不在授权卡上渲染**：
它们既不进决策区，也不进建议区（建议区只放被隔离的指令性文本）。能力说明归
#738 的能力说明面，不归授予决策面——授权卡上少一个投毒面，就少一次"用户以为在批 A、
实际批了 B"的机会。

### 4.2 高危项逐项勾选、默认不勾

风险码（全部 severity=high，均从结构化字段推导）：

| 码 | 触发条件 | Target / Detail |
|---|---|---|
| `cross_origin_host` | 拟用 host 与已发布 host 或文档声明 host 不同 | host / 对照 host |
| `new_write_operation` | 非只读操作不在已发布集合中 | `METHOD path` |
| `operation_not_in_document` | 操作不在文档**结构化**操作清单中 | `METHOD path` |
| `scope_widened` | scope 键授予了基线（已发布配置；首次发布取最小默认值）之外的值 | scope 键 / 新增值 |
| `write_enabled` | Provider 写开关被打开 | 卡片标题 |

每条风险带稳定的 `ack_key`。UI 侧默认**全部不勾**，逐条勾选全部完成后"确认并发布"才可点
——单次点击的橡皮图章在结构上不可能。

### 4.3 供 #734 使用（接入缝）

#734 的草稿流水线在"影响范围确认"这一步：

1. **先过 lint**：`service.ValidateContract(contract)`（= `LintContract` 的 error 级）
   必须在构造卡片之前挡住不可发布的契约；卡片是给人看的，不是校验器。
2. 从草稿的结构化字段构造 `ConfirmDraftInput`（契约 / `scope_config` / `DocDeclaredOperations`
   / `DocDeclaredHost` / `Origin` / `Advisory` / `Previous`）；`DocDeclaredOperations` 必须
   来自文档的**结构化**段落（OpenAPI 的 `paths`），不是从 prose 里猜的。
3. `model := service.BuildConfirmCard(input)`——**这是唯一的构造点**，不要在 #734 里另
   写一套渲染；
4. 把 `model` 原样下发（JSON 字段名见 `src/types/integration-confirm.ts`），前端用
   `<IntegrationConfirmCard model={model} onDecide={...} />` 渲染；
5. 批准后落库与审计用 `service.DecisionPayload(model)`——**不要**用 `json.Marshal(model)`，
   那会把建议区一起写进批准记录。

AI 侧不得自行拼装或改写卡片文案；文案全部来自 i18n + 结构化字段。`Title`/`Label` 的取值
规则见 §4.1.1。

### 4.4 行为预演一致性（与 #739 协同）

`BehaviorPreview` 是"发布后会真正产生的出网行为"的唯一结构化描述：每条被批准的操作对应
一条 `{method, path_template, read_only}`，**外加发布后验证探针**（它同样出网，漏掉就会
出现"批准时没列、运行时会打"的分叉）。

不变式：**预览是决策字段的纯函数**。沙箱函数运行时（#739）必须用同一个值推导它自己的
预览，从而保证"用户批准的"与"实际执行的"不可能分叉。回归测试断言：只改建议区文本，
预览不变（`TestBehaviorPreview_DerivesOnlyFromTheDecisionArea`）。

## 5. 安全回归套件（fixture 入库）

fixture 目录：`server/internal/service/testdata/integration_security/`。

| 类别 | fixture | 测试 | 断言 |
|---|---|---|---|
| 投毒文档 | `poisoned-openapi.json` | `TestIngestUntrustedDoc_PoisonedDocumentCannotBecomeInstruction`、`TestBuildConfirmCard_PoisonedDocumentCannotWidenTheDraft` | 扩权指令进隔离区；草稿操作清单不含扩权；批准字节不含任何不可信文本 |
| 说明注入 | `injected-description.json` | `TestIngestUntrustedDoc_DescriptionInjectionKeepsInstructionAreaIntact` | 污染说明进隔离区；指令面（结构化操作清单）不被改写 |
| 确认卡快照 | `confirm-card-golden.json` | `TestBuildConfirmCard_GoldenDecisionPayload` | 渲染输出只含结构化事实 |
| 行为预演一致性 | `behavior-preview-ops.json` | `TestBehaviorPreview_DerivesOnlyFromTheDecisionArea` | 预览 = 决策字段的纯函数；建议区变化不影响 |

前端：`IntegrationConfirmCard.test.tsx` 断言建议区文本不出现在决策区、高危项默认不勾、
确认按钮在全部勾选前禁用。

更新快照：`UPDATE_GOLDEN=1 go test ./internal/service/ -run TestBuildConfirmCard_GoldenDecisionPayload`。

## 6. 发布前自查

针对接入流程的滥用演练项见
[`integration-onboarding-redteam.md`](./integration-onboarding-redteam.md)（含已执行的
演练记录）。
