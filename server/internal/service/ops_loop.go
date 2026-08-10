// Package service: the platform-agnostic 运营闭环 (ops-loop) capability.
//
// Every运营 scene (公众号 / 内容营销 / 社媒 / 未来小红书·抖音…) runs the SAME
// closed loop — only the platform differs:
//
//	① 资料/近期热点收集与分析 → ② 选题·关键词 → ③ 内容生产（撰稿·排版·发布前审校门禁）
//	→ ④ 发布 → ⑤ 效果追溯·分析，且 ⑤ 的洞察【回流】成下一轮 ① 的输入（闭环、可周期驱动）。
//
// At runtime the loop is orchestrated as an executable Epic:
//
//	Epic 父issue（运营活动）
//	 ├─ A 采集·选题子issue（周期，cron 起）  = ①+②  → 产『选题清单』→ fan-out B
//	 ├─ B 内容子issue（每选题一个，可并行）   = ③④⑤ 作为其生命周期看板列
//	 └─ C 复盘·回流子issue（周期，cron 起）   = 汇总 B 的 ⑤ → 提炼 → 回灌 ①
//
// This file is the SINGLE SOURCE of that loop. Both the loop-blueprint skeleton
// (opsLoopColumns, snapshotted per platform) and the runtime orchestration
// convention (opsLoopOrchestrationPrompt) are generated from OpsLoopParams, so a
// new运营 scene reuses one implementation with a few platform knobs instead of
// copy-pasting the whole board +编排纪律. It touches no kernel primitive — it only
// composes existing ones: kanban columns + phase prompts + gate specs + the
// batch_create_issues(parent)/start_workspace/advance_issue + scheduler(cron) tools.
package service

import (
	"fmt"
	"strings"
)

// OpsLoopParams parameterizes the platform-agnostic运营闭环 for one platform.
// The loop semantics are identical across platforms; only these knobs — the
// platform's name, its publish action, its data metrics, and the curated MCP the
// scene wires — change. Keep every field platform-specific: no loop mechanics
// belong here, they live in the shared templates below.
type OpsLoopParams struct {
	// PlatformKey is the stable slug fragment (e.g. "wechat-mp"); used to build
	// the blueprint slug and orchestration prompt id.
	PlatformKey string
	// PlatformName is the display name woven into prompts (e.g. "微信公众号").
	PlatformName string
	// SceneSlug / SceneName is the运营 scene the loop blueprint snapshots so a new
	// project created from it comes with the platform's curated MCP + gate attached.
	SceneSlug string
	SceneName string
	// BlueprintName is the loop blueprint's user-facing name (its slug is derived
	// from PlatformKey as "<key>-ops").
	BlueprintName string
	// TopicFocus / ProduceFocus / LayoutFocus / PublishFocus / MetricsFocus are the
	// platform-specific phrasings woven into the ②③④⑤ lane phase prompts.
	TopicFocus   string
	ProduceFocus string
	LayoutFocus  string
	PublishFocus string
	MetricsFocus string
	// ReviewNeedles are extra发布前审校 compliance checks unique to the platform
	// (e.g. 公众号《广告法》/诱导分享). The shared事实可溯源/版权 checks are always added.
	ReviewNeedles []string
	// DataSinkHint names where ⑤ 回流 writes distilled insight back for the next
	// round's ① (e.g. a knowledge-base MCP, or the project's artifacts). Optional.
	DataSinkHint string
}

// opsLoopBlueprintSlug is the stable slug for a platform's loop blueprint.
func opsLoopBlueprintSlug(p OpsLoopParams) string { return p.PlatformKey + "-ops" }

// opsLoopColumns builds the platform-agnostic运营闭环 board — the lifecycle a
// content card (B 子issue) flows through, with ① 采集 folded into the first lane's
// intake and ⑤ 回流 into the last. Shared by EVERY运营 loop blueprint; only
// OpsLoopParams differ, so two platforms reuse one skeleton rather than each
// hand-rolling six lanes. Contract kept stable for callers/tests:
//   - exactly 6 lanes; lane[0].OpPrimitive == "none"; last lane == "complete";
//   - lane[3] is the 审校 (pre-publish) gate lane and its Instruction contains
//     both "门禁" and "合规" (the review discipline needle).
func opsLoopColumns(p OpsLoopParams) []ColumnSeed {
	reviewNeedles := append([]string{
		"事实与可溯源（数据/引用/案例逐条标来源，无法溯源的关键断言降级或删除）",
		"版权与原创（转载标注来源与授权、图片自有或授权、无洗稿）",
	}, p.ReviewNeedles...)
	sink := p.DataSinkHint
	if strings.TrimSpace(sink) == "" {
		sink = "本项目的选题知识沉淀（写回知识库 MCP 或登记到 .niuniu/artifacts.json）"
	}
	return []ColumnSeed{
		{
			Name: "采集/选题", Position: 0, Lifecycle: "created", OpPrimitive: "none",
			WhenToUse: fmt.Sprintf("对齐%s账号定位与本期目标，收集近期资料/热点并定选题·角度时（闭环①②）", p.PlatformName),
			Instruction: fmt.Sprintf("闭环①资料·热点收集 + ②选题·关键词：%s。产『选题清单』——每个选题含读者痛点、差异化角度、内容形态、合规注意点，按优先级排序。若本轮由 cron 周期触发（A 采集·选题子issue），产出清单后用 batch_create_issues(parent_issue_id=运营Epic) 为每个选题 fan-out 一个内容子issue（B，可并行）。数据以实际了解为准，拿不准就标注、不臆造。", p.TopicFocus),
		},
		{
			Name: "撰稿", Position: 1, Lifecycle: "implement", OpPrimitive: "instruct",
			WhenToUse: "选题与角度已定、需要产出稿件时（闭环③生产）",
			Instruction: fmt.Sprintf("闭环③内容生产：%s。产出结构完整、事实可溯源的初稿 + 标题备选 + 引导语。不做标题党/不夸大、不编造数据与功效声明，缺一手素材就标占位并索取。", p.ProduceFocus),
		},
		{
			Name: "排版", Position: 2, Lifecycle: "implement", OpPrimitive: "instruct",
			WhenToUse: "初稿定稿、需要排版并生成可发布件时（闭环③生产）",
			Instruction: fmt.Sprintf("闭环③排版成件：%s。素材经平台官方素材接口管理、不外链盗图；产出可发布件/草稿并记录其标识。", p.LayoutFocus),
		},
		{
			Name: "审校", Position: 3, Lifecycle: "implement-review", OpPrimitive: "instruct",
			WhenToUse: "排版件完成、需要发布前把关时（闭环③门禁）",
			Instruction: "闭环③发布前审校门禁：逐项核查 " + strings.Join(reviewNeedles, "、") + "、平台合规（不夸大/不违规）、账号一致、可读性，列出「必须改」为硬性阻塞项。这是内容级门禁——存在无法溯源的关键断言或明显合规/版权风险即不进入发布；需硬闸时在本列绑定场景提供的「发布前审校门禁」gate。",
		},
		{
			Name: "发布", Position: 4, Lifecycle: "implement-review", OpPrimitive: "instruct",
			WhenToUse: "审校通过、准备发布或排定时时（闭环④）",
			Instruction: fmt.Sprintf("闭环④发布：%s。记录可核对的发布标识与状态；面向全部受众的群发属高影响外发，必须先经用户确认目标/受众/时间，遇平台频次或人工复核限制如实回报、不绕过。", p.PublishFocus),
		},
		{
			Name: "数据复盘", Position: 5, Lifecycle: "completed", OpPrimitive: "complete",
			WhenToUse: fmt.Sprintf("内容已发布、拉%s做效果复盘并把洞察回流为下一轮输入后移入（闭环⑤+回流，可挂 cron 周期跑 C 复盘·回流子issue）", p.MetricsFocus),
			Instruction: fmt.Sprintf("闭环⑤效果追溯·分析 + 回流：拉%s做环比，提炼有效选题/标题/时段；把洞察回流到%s，作为下一轮①的输入。数字以官方接口/后台导出为准，拉不到标「待补」、不臆造。作为周期 C 子issue 运行时，汇总同 Epic 下各 B 子issue 的⑤数据后再回流。", p.MetricsFocus, sink),
		},
	}
}

// opsLoopBlueprintDef assembles a builtin loop blueprint for one platform from
// the shared skeleton: the 6-lane opsLoopColumns board + a snapshot of the
// platform's运营 scene. This is the reuse seam — every运营 platform gets its loop
// blueprint from here, so adding one is a few OpsLoopParams, not a fresh board.
func opsLoopBlueprintDef(p OpsLoopParams, description string) builtinBlueprintDef {
	return builtinBlueprintDef{
		slug:        opsLoopBlueprintSlug(p),
		name:        p.BlueprintName,
		description: description,
		columns:     opsLoopColumns(p),
		scenes:      []BlueprintScene{{Slug: p.SceneSlug, DisplayName: p.SceneName, Source: "builtin"}},
	}
}

// opsLoopPlatforms is the registry of运营 platforms that reuse the closed-loop
// capability. Adding a platform here (plus its scene YAML) gives it the full loop
// blueprint + orchestration convention with no new mechanics — this list IS the
// "≥2 scenes reuse one capability" proof (公众号 + 内容营销 today).
func opsLoopPlatforms() []OpsLoopParams {
	return []OpsLoopParams{
		{
			PlatformKey: "wechat-mp", PlatformName: "微信公众号",
			SceneSlug: "wechat-mp", SceneName: "微信公众号运营",
			BlueprintName: "微信公众号运营",
			TopicFocus:    "结合账号历史爆款方向与近期热点定选题·角度，避免撞题",
			ProduceFocus:  "撰写公众号推文（主标题 + 备选、开头钩子、分节小标题、正文与配图位、结尾引导）",
			LayoutFocus:   "经公众号运营 MCP 的排版/素材/草稿能力排成图文并存为草稿（记录 media_id/draft_id）",
			PublishFocus:  "经官方发布接口提交立即或定时群发（记录 publish_id/msg_id）",
			MetricsFocus:  "阅读/在看/分享/收藏/涨掉粉/图文转化/菜单点击等官方数据",
			ReviewNeedles: []string{"平台合规（《广告法》绝对化用语、未证实功效/医疗/金融违规、诱导关注·分享、涉政涉黄涉赌等公众号红线、标题党）"},
		},
		{
			PlatformKey: "content-marketing", PlatformName: "内容营销",
			SceneSlug: "content-marketing", SceneName: "内容营销全流程",
			BlueprintName: "内容营销闭环",
			TopicFocus:    "做选题与关键词研究、竞品内容差距分析，定差异化机会（搜索量以接口/抓取为准）",
			ProduceFocus:  "撰写结构完整、事实可溯源的文案初稿，附 meta 与社媒分发短文案",
			LayoutFocus:   "定稿排版并补齐结构化数据/内外链/图片 alt，落地页可跑 site-audit 技术审计",
			PublishFocus:  "核对可发布性清单后发布到官网/博客/落地页并埋 utm 追踪",
			MetricsFocus:  "搜索点击/曝光/排名与 AI 引用等表现（GSC/SerpAPI，未绑则引导导出）",
			ReviewNeedles: []string{"营销合规（跨境广告合规、无夸大或未经证实的功效声明、无竞品贬损）"},
		},
	}
}

// OpsLoopChildKind enumerates the THREE child-issue types the运营闭环 spawns under
// an运营 Epic. They are first-class (not prose): the orchestration convention, the
// tests, and the E2E doc all derive from opsLoopChildTemplates so the "Epic 父 +
// A/B/C 三类子issue" structure has one parameterized source.
type OpsLoopChildKind string

const (
	OpsLoopCollect OpsLoopChildKind = "collect" // A 采集·选题（周期，cron 起，产选题清单 + fan-out B）
	OpsLoopContent OpsLoopChildKind = "content" // B 每内容（每选题一个，可并行；生命周期=生产·发布·效果）
	OpsLoopRecap   OpsLoopChildKind = "recap"   // C 复盘·回流（周期，cron 起，汇总 B 的⑤ → 提炼 → 回灌①）
)

// OpsLoopChildTemplate is one parameterized child-issue type under the运营 Epic.
// A/C are periodic (scheduler/cron drives a fresh round); B is on-demand fan-out
// (one per topic, run in parallel) and is the only type with a生命周期看板列 board
// (Lifecycle == the生产·发布·效果 columns a content card flows through). The agent
// creates each via batch_create_issues(parent_issue_id=运营Epic) using these
// templates, then start_workspace on the B children.
type OpsLoopChildTemplate struct {
	Kind        OpsLoopChildKind
	Label       string       // display label incl. periodicity, e.g. "A 采集·选题子issue（周期）"
	TitleHint   string       // batch_create_issues title pattern hint (e.g. "推文 · <选题>")
	Periodic    bool         // true for A/C (cron); false for B (fan-out on demand)
	Parallel    bool         // true for B (many topics run concurrently)
	PhasePrompt string       // what this child does — platform-parameterized, authored once here
	Lifecycle   []ColumnSeed // only B has a生产·发布·效果 lifecycle board; A/C are single-purpose (nil)
}

// opsLoopChildTemplates returns the three parameterized child-issue types (A/B/C)
// under the运营 Epic — the single source the orchestration convention + the loop
// board derive from. B's Lifecycle reuses opsLoopColumns (the shared skeleton), so
// there is exactly one place a platform's生产·发布·效果 board is defined.
func opsLoopChildTemplates(p OpsLoopParams) []OpsLoopChildTemplate {
	return []OpsLoopChildTemplate{
		{
			Kind: OpsLoopCollect, Label: "A 采集·选题子issue（周期）",
			TitleHint: fmt.Sprintf("%s采集·选题 · 第N轮", p.PlatformName), Periodic: true,
			PhasePrompt: fmt.Sprintf("①资料·热点收集 + ②选题·关键词：%s，产『选题清单』。随后为清单里每个选题用 batch_create_issues(parent_issue_id=运营Epic, issue_type=\"task\", exec_wave=同波) fan-out 一个 B 内容子issue，再 start_workspace(issue_id=B) 让各内容并行推进。cron 周期起一轮。", p.TopicFocus),
		},
		{
			Kind: OpsLoopContent, Label: "B 内容子issue（每选题一个，可并行）",
			TitleHint: "推文/内容 · <选题>", Parallel: true, Lifecycle: opsLoopColumns(p),
			PhasePrompt: "③生产·排版 → 发布前审校门禁 → ④发布 → ⑤效果 作为其生命周期看板列（采集/选题→撰稿→排版→审校→发布→数据复盘），用 advance_issue 在列间流转；审校未过不进发布。一份选题清单 fan-out 出的多个 B 可并行推进。",
		},
		{
			Kind: OpsLoopRecap, Label: "C 复盘·回流子issue（周期）",
			TitleHint: fmt.Sprintf("%s复盘·回流 · 第N轮", p.PlatformName), Periodic: true,
			PhasePrompt: fmt.Sprintf("汇总同 Epic 下各 B 子issue 的⑤%s → 提炼有效选题/标题/时段 → 回灌①（写回知识库 MCP 或项目沉淀），作为下一轮 A 的输入起点。cron 周期起一轮。", p.MetricsFocus),
		},
	}
}

// opsLoopOrchestrationPrompt returns the runtime Epic父→A/B/C 子issue 编排约定,
// platform-parameterized and authored ONCE here. Its "三类子issue" bullets are
// BUILT from opsLoopChildTemplates so the three child types are the single source
// (not hand-written prose). A运营 scene includes the fragment so its workspace
// agent knows how to drive the closed loop over existing primitives —
// batch_create_issues(parent_issue_id)/start_workspace/advance_issue +
// scheduler(cron) — without each scene re-deriving the编排逻辑.
//
// The scene YAML carries the静态 fragment (seeded via the YAML path, which cannot
// call Go); this helper is the canonical text used to author those fragments and
// is asserted against in tests so the two never drift. Its id is stable
// ("ops-loop-orchestration") so it dedups across layers.
func opsLoopOrchestrationPrompt(p OpsLoopParams) PromptFragment {
	var childBullets strings.Builder
	for _, c := range opsLoopChildTemplates(p) {
		fmt.Fprintf(&childBullets, "- **%s**：%s\n", c.Label, c.PhasePrompt)
	}
	return PromptFragment{
		ID:    "ops-loop-orchestration",
		Title: "运营闭环编排约定（Epic 父 → A 采集·选题 / B 内容 / C 复盘·回流 子issue）",
		Body: fmt.Sprintf(`本场景接入牛牛通用「运营闭环」能力——把%s运营建模为一个可周期驱动的闭环，
运行时用现有原语编排为「Epic 父issue → 三类子issue」，无需任何额外代码：

**闭环五阶段（平台无关）**：① 资料/热点收集 → ② 选题·关键词 → ③ 内容生产（撰稿·排版·
发布前审校门禁）→ ④ 发布 → ⑤ 效果追溯·分析；⑤ 的洞察【回流】为下一轮 ① 的输入。

**Epic 父issue（运营活动）→ 三类子issue（用 batch_create_issues(parent_issue_id=运营Epic) 建）**：
%s
**周期驱动（复用 scheduler/cron + notify/imbot）**：把 A、C 挂到工作空间「设置→定时触发」
的 cron（如每周一起 A 采集选题、每周五起 C 复盘回流）。到点调度器唤起对应子issue 的 agent
自动跑；产出后用 batch_create_issues 建跟进 issue，配了通知渠道则推送摘要。

纪律：涉及真实对外发布始终遵守「发布需用户确认」；数据必须有来源，无新数据不臆造增长；
回流写回的是【已验证的结论】而非原始噪声。`, p.PlatformName, strings.TrimRight(childBullets.String(), "\n")),
	}
}
