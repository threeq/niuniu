package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/harness/checkers"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Finding severities. A blocking finding stops the content before an approval
// card is created; a warning is surfaced on the approval card instead.
const (
	SeverityBlocking = "blocking"
	SeverityWarning  = "warning"
)

// The four review categories from design spec §7.2.
const (
	RuleFactCompliance = "fact_compliance"
	RuleBrand          = "brand_consistency"
	RulePlatform       = "platform_fit"
	RuleStructure      = "structure_completeness"
)

const (
	// IssueExecEventKindGate is the issue_exec_events.kind for gate outcomes.
	IssueExecEventKindGate = "gate"
	opsReviewSummary       = "ops_review"
	rubricRuleMarker       = "### RULE:"
)

// BotRef is the minimal bot identity the review needs: the pack decides which
// compliance rules and platform limits apply.
type BotRef struct {
	ID          int64
	PackID      string
	PackVersion int
}

// Artifact is one produced content version. Review is pure over it: a
// regenerated version is a new Artifact and is reviewed from scratch.
type Artifact struct {
	IssueID      int64
	WorkspaceID  int64
	Version      int64
	Format       string
	Title        string
	Body         string
	Tags         []string
	Images       []string
	Cover        string
	ImageRatio   string
	VideoRef     string
	DurationSec  int
	HasSubtitles bool
	CTA          string
	Sources      []string
}

// Finding is one review outcome.
type Finding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Location string `json:"location,omitempty"`
}

// ReviewResult is the gate outcome. Blocking is true when at least one
// blocking finding exists.
type ReviewResult struct {
	BotID       int64     `json:"bot_id"`
	IssueID     int64     `json:"issue_id"`
	WorkspaceID int64     `json:"workspace_id"`
	Version     int64     `json:"version"`
	Blocking    bool      `json:"blocking"`
	Findings    []Finding `json:"findings"`
	ReviewedAt  time.Time `json:"reviewed_at"`
}

// ReviewPreview is the review section rendered on the approval card. Warnings
// travel with the card; blocked content never produces one.
type ReviewPreview struct {
	Conclusion string    `json:"conclusion"` // blocked | warnings | passed
	Blocking   bool      `json:"blocking"`
	Findings   []Finding `json:"findings"`
}

// ApprovalPreview builds the approval-card payload.
func (r ReviewResult) ApprovalPreview() ReviewPreview {
	conclusion := "passed"
	switch {
	case r.Blocking:
		conclusion = "blocked"
	case len(r.Findings) > 0:
		conclusion = "warnings"
	}
	return ReviewPreview{
		Conclusion: conclusion,
		Blocking:   r.Blocking,
		Findings:   append([]Finding(nil), r.Findings...),
	}
}

// JudgeFunc performs the semantic part of the review. prompt carries the
// platform compliance rules and the four category rubrics; content carries the
// brief and the produced artifact.
type JudgeFunc func(ctx context.Context, prompt, content string) ([]Finding, error)

// ExecEvent is the append-only audit record written for every review.
type ExecEvent struct {
	IssueID     int64
	WorkspaceID int64
	Kind        string
	Summary     string
	DetailJSON  string
	CostUSD     float64
}

// EventRecorder appends one issue_exec_events row.
type EventRecorder func(ctx context.Context, ev ExecEvent) error

// NewSQLReviewRecorder adapts sqlc queries to EventRecorder.
func NewSQLReviewRecorder(q *store.Queries) EventRecorder {
	return func(ctx context.Context, ev ExecEvent) error {
		return q.InsertIssueExecEvent(ctx, store.InsertIssueExecEventParams{
			IssueID:     ev.IssueID,
			WorkspaceID: sql.NullInt64{Int64: ev.WorkspaceID, Valid: ev.WorkspaceID > 0},
			Kind:        ev.Kind,
			Summary:     ev.Summary,
			DetailJson:  sql.NullString{String: ev.DetailJSON, Valid: ev.DetailJSON != ""},
			CostUsd:     ev.CostUSD,
		})
	}
}

// Review runs the compliance and quality gate for one content version.
type Review struct {
	// Judge runs the semantic checks (harness ai_judge in production). Nil
	// keeps only the deterministic pack/structure checks.
	Judge JudgeFunc
	// Pack resolves the platform compliance rules and content limits. Nil means
	// no pack-specific rules.
	Pack func(ctx context.Context, packID string, version int) (packs.Manifest, error)
	// Recorder writes the gate outcome to issue_exec_events.
	Recorder EventRecorder
	// Now overrides the clock in tests.
	Now func() time.Time
}

// Run evaluates all four categories and records the outcome. It returns an
// error only when the review itself cannot run (pack load failure or judge
// failure); that is fail-closed because an unverified artifact must not reach
// approval.
func (r *Review) Run(ctx context.Context, bot BotRef, a Artifact, brief OpsContentBrief) (ReviewResult, error) {
	now := time.Now
	if r != nil && r.Now != nil {
		now = r.Now
	}
	res := ReviewResult{
		BotID:       bot.ID,
		IssueID:     a.IssueID,
		WorkspaceID: a.WorkspaceID,
		Version:     a.Version,
		Findings:    []Finding{},
		ReviewedAt:  now(),
	}

	manifest, err := r.manifest(ctx, bot)
	if err != nil {
		return ReviewResult{}, err
	}

	format := artifactFormat(a, brief)
	res.Findings = append(res.Findings, checkForbiddenTerms(a, manifest.Compliance)...)
	res.Findings = append(res.Findings, checkStructure(a, format)...)
	res.Findings = append(res.Findings, checkPlatform(a, format, manifest.ContentSchema)...)

	if r != nil && r.Judge != nil {
		findings, err := r.Judge(ctx, reviewRubric(manifest), reviewContent(a, brief))
		if err != nil {
			return res, fmt.Errorf("content: semantic review: %w", err)
		}
		res.Findings = append(res.Findings, normalizeFindings(findings)...)
	}

	res.Blocking = hasBlocking(res.Findings)
	if err := r.record(ctx, res); err != nil {
		return res, err
	}
	return res, nil
}

// Gate runs the review and only creates the approval when nothing blocks. A
// blocking result returns with createApproval never invoked.
func (r *Review) Gate(ctx context.Context, bot BotRef, a Artifact, brief OpsContentBrief, createApproval func(ReviewPreview) error) (ReviewResult, error) {
	res, err := r.Run(ctx, bot, a, brief)
	if err != nil {
		return res, err
	}
	if res.Blocking {
		return res, nil
	}
	if createApproval != nil {
		if err := createApproval(res.ApprovalPreview()); err != nil {
			return res, fmt.Errorf("content: create approval: %w", err)
		}
	}
	return res, nil
}

func (r *Review) manifest(ctx context.Context, bot BotRef) (packs.Manifest, error) {
	if r == nil || r.Pack == nil || bot.PackID == "" {
		return packs.Manifest{}, nil
	}
	manifest, err := r.Pack(ctx, bot.PackID, bot.PackVersion)
	if err != nil {
		return packs.Manifest{}, fmt.Errorf("content: load review pack: %w", err)
	}
	return manifest, nil
}

func (r *Review) record(ctx context.Context, res ReviewResult) error {
	if r == nil || r.Recorder == nil || res.IssueID == 0 {
		return nil
	}
	detail, err := json.Marshal(reviewEventDetail{
		BotID:           res.BotID,
		ArtifactVersion: res.Version,
		Blocking:        res.Blocking,
		Conclusion:      res.ApprovalPreview().Conclusion,
		Findings:        res.Findings,
	})
	if err != nil {
		return fmt.Errorf("content: marshal review event: %w", err)
	}
	if err := r.Recorder(ctx, ExecEvent{
		IssueID:     res.IssueID,
		WorkspaceID: res.WorkspaceID,
		Kind:        IssueExecEventKindGate,
		Summary:     opsReviewSummary,
		DetailJSON:  string(detail),
	}); err != nil {
		return fmt.Errorf("content: record review event: %w", err)
	}
	return nil
}

type reviewEventDetail struct {
	BotID           int64     `json:"bot_id"`
	ArtifactVersion int64     `json:"artifact_version"`
	Blocking        bool      `json:"blocking"`
	Conclusion      string    `json:"conclusion"`
	Findings        []Finding `json:"findings"`
}

func artifactFormat(a Artifact, brief OpsContentBrief) string {
	if a.Format != "" {
		return a.Format
	}
	return brief.Format
}

func normalizeFindings(in []Finding) []Finding {
	out := make([]Finding, 0, len(in))
	for _, f := range in {
		f.Rule = strings.TrimSpace(f.Rule)
		f.Message = strings.TrimSpace(f.Message)
		f.Location = strings.TrimSpace(f.Location)
		switch f.Severity {
		case SeverityBlocking, SeverityWarning:
		default:
			f.Severity = SeverityWarning
		}
		out = append(out, f)
	}
	return out
}

func hasBlocking(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityBlocking {
			return true
		}
	}
	return false
}

// checkForbiddenTerms enforces the pack's absolute-language / red-line list.
// These are legal and platform violations, so they block.
func checkForbiddenTerms(a Artifact, c packs.Compliance) []Finding {
	if len(c.ForbiddenTerms) == 0 {
		return nil
	}
	fields := []struct {
		name string
		text string
	}{
		{"title", a.Title},
		{"body", a.Body},
		{"tags", strings.Join(a.Tags, " ")},
		{"cover", a.Cover},
	}
	seen := map[string]bool{}
	var out []Finding
	for _, term := range c.ForbiddenTerms {
		term = strings.TrimSpace(term)
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		needle := strings.ToLower(term)
		for _, f := range fields {
			if !strings.Contains(strings.ToLower(f.text), needle) {
				continue
			}
			out = append(out, Finding{
				Rule:     RuleFactCompliance,
				Severity: SeverityBlocking,
				Message:  fmt.Sprintf("含广告法/平台禁用的绝对化用语 %q", term),
				Location: f.name,
			})
			break
		}
	}
	return out
}

// checkStructure blocks content that is missing required publishable parts.
func checkStructure(a Artifact, format string) []Finding {
	if format == "" {
		return nil
	}
	var out []Finding
	if strings.TrimSpace(a.Title) == "" {
		out = append(out, Finding{Rule: RuleStructure, Severity: SeverityBlocking, Message: "缺少标题", Location: "title"})
	}
	if strings.TrimSpace(a.Body) == "" {
		out = append(out, Finding{Rule: RuleStructure, Severity: SeverityBlocking, Message: "缺少正文", Location: "body"})
	}

	switch format {
	case FormatArticle:
		if strings.TrimSpace(a.Cover) == "" {
			out = append(out, Finding{Rule: RuleStructure, Severity: SeverityBlocking, Message: "缺少封面", Location: "cover"})
		}
	case FormatNote:
		if len(a.Images) == 0 {
			out = append(out, Finding{Rule: RuleStructure, Severity: SeverityBlocking, Message: "缺少配图", Location: "images"})
		}
		if strings.TrimSpace(a.CTA) == "" {
			out = append(out, Finding{Rule: RuleStructure, Severity: SeverityWarning, Message: "缺少明确 CTA", Location: "cta"})
		}
	case FormatVideo:
		if strings.TrimSpace(a.VideoRef) == "" {
			out = append(out, Finding{Rule: RuleStructure, Severity: SeverityBlocking, Message: "缺少成片文件", Location: "video"})
		}
		if strings.TrimSpace(a.Cover) == "" {
			out = append(out, Finding{Rule: RuleStructure, Severity: SeverityBlocking, Message: "缺少封面", Location: "cover"})
		}
		if !a.HasSubtitles {
			out = append(out, Finding{Rule: RuleStructure, Severity: SeverityBlocking, Message: "缺少字幕", Location: "subtitles"})
		}
	}
	return out
}

// checkPlatform reports platform adaptation mismatches as warnings so the
// operator can adjust or approve on the card.
func checkPlatform(a Artifact, format string, schema packs.ContentSchema) []Finding {
	if format == "" {
		return nil
	}
	limits, ok := schema.Formats[format]
	if !ok {
		return nil
	}
	var out []Finding
	if limits.MaxTitleChars > 0 {
		if n := runeLen(a.Title); n > limits.MaxTitleChars {
			out = append(out, Finding{
				Rule:     RulePlatform,
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("标题 %d 字，超过平台上限 %d 字", n, limits.MaxTitleChars),
				Location: "title",
			})
		}
	}
	if limits.MaxChars > 0 {
		if n := runeLen(a.Body); n > limits.MaxChars {
			out = append(out, Finding{
				Rule:     RulePlatform,
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("正文 %d 字，超过平台上限 %d 字", n, limits.MaxChars),
				Location: "body",
			})
		}
	}
	if limits.MaxImages > 0 && len(a.Images) > limits.MaxImages {
		out = append(out, Finding{
			Rule:     RulePlatform,
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("图片 %d 张，超过平台上限 %d 张", len(a.Images), limits.MaxImages),
			Location: "images",
		})
	}
	if limits.VideoSec > 0 && a.DurationSec > limits.VideoSec {
		out = append(out, Finding{
			Rule:     RulePlatform,
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("视频时长 %d 秒，超过平台上限 %d 秒", a.DurationSec, limits.VideoSec),
			Location: "video",
		})
	}
	if a.ImageRatio != "" && limits.ImageRatio != "" && a.ImageRatio != limits.ImageRatio {
		out = append(out, Finding{
			Rule:     RulePlatform,
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("画面比例 %s，平台要求 %s", a.ImageRatio, limits.ImageRatio),
			Location: "cover",
		})
	}
	if format == FormatNote && (len(a.Tags) < 5 || len(a.Tags) > 10) {
		out = append(out, Finding{
			Rule:     RulePlatform,
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("标签 %d 个，平台要求 5-10 个", len(a.Tags)),
			Location: "tags",
		})
	}
	return out
}

func runeLen(s string) int { return len([]rune(s)) }

func reviewRubric(m packs.Manifest) string {
	sections := []struct {
		rule, severity, rubric string
	}{
		{
			RuleFactCompliance, SeverityBlocking,
			"事实/合规：检查无来源的关键断言、广告法绝对化用语、医疗/金融资质、平台红线、版权风险。" + complianceText(m.Compliance),
		},
		{
			RuleBrand, SeverityWarning,
			"品牌一致性：检查语气、术语、视觉规范是否与品牌人设和受众一致。",
		},
		{
			RulePlatform, SeverityWarning,
			"平台适配：检查尺寸、时长、标题长度、标签数量、图片数量是否符合该平台要求。",
		},
		{
			RuleStructure, SeverityWarning,
			"结构完整：检查必填字段、CTA、封面、字幕是否齐全。",
		},
	}

	var b strings.Builder
	b.WriteString("你是内容合规与质量审校员。逐项检查下面的内容，只报告确实存在的问题。\n")
	for _, s := range sections {
		fmt.Fprintf(&b, "\n%s%s severity=%s\n%s\n", rubricRuleMarker, s.rule, s.severity, s.rubric)
	}
	return b.String()
}

func complianceText(c packs.Compliance) string {
	var b strings.Builder
	if len(c.ForbiddenTerms) > 0 {
		b.WriteString(" 禁用词：")
		b.WriteString(strings.Join(c.ForbiddenTerms, "、"))
		b.WriteString("。")
	}
	if len(c.RequiredChecks) > 0 {
		b.WriteString(" 平台红线：")
		for _, check := range c.RequiredChecks {
			b.WriteString(check)
			b.WriteString("；")
		}
	}
	return b.String()
}

func reviewContent(a Artifact, brief OpsContentBrief) string {
	var b strings.Builder
	b.WriteString("## 任务\n")
	fmt.Fprintf(&b, "渠道：%s\n账号：%s\n目标：%s\n受众：%s\n格式：%s\n选题：%s\n",
		brief.Channel, brief.AccountRef, brief.Goal, brief.Audience, brief.Format, brief.Topic)
	if len(brief.Acceptance) > 0 {
		b.WriteString("验收项：\n")
		for _, item := range brief.Acceptance {
			fmt.Fprintf(&b, "- %s\n", item)
		}
	}
	if len(brief.SourceRefs) > 0 || len(a.Sources) > 0 {
		b.WriteString("来源：\n")
		for _, ref := range append(append([]string{}, brief.SourceRefs...), a.Sources...) {
			fmt.Fprintf(&b, "- %s\n", ref)
		}
	}

	b.WriteString("\n## 内容版本 ")
	fmt.Fprintf(&b, "%d\n", a.Version)
	fmt.Fprintf(&b, "标题：%s\n", a.Title)
	fmt.Fprintf(&b, "正文：%s\n", a.Body)
	if len(a.Tags) > 0 {
		fmt.Fprintf(&b, "标签：%s\n", strings.Join(a.Tags, "、"))
	}
	if len(a.Images) > 0 {
		fmt.Fprintf(&b, "配图：%d 张\n", len(a.Images))
	}
	fmt.Fprintf(&b, "封面：%s\n", a.Cover)
	if a.ImageRatio != "" {
		fmt.Fprintf(&b, "画面比例：%s\n", a.ImageRatio)
	}
	if a.VideoRef != "" {
		fmt.Fprintf(&b, "成片：%s（%d 秒，字幕：%t）\n", a.VideoRef, a.DurationSec, a.HasSubtitles)
	}
	fmt.Fprintf(&b, "CTA：%s\n", a.CTA)
	return b.String()
}

type rubricSection struct {
	Rule     string
	Severity string
	Rubric   string
}

func splitRubric(rubric string) []rubricSection {
	parts := strings.Split(rubric, rubricRuleMarker)
	out := make([]rubricSection, 0, len(parts)-1)
	for _, part := range parts[1:] {
		header, body, ok := strings.Cut(part, "\n")
		if !ok {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) == 0 {
			continue
		}
		section := rubricSection{
			Rule:     fields[0],
			Severity: SeverityWarning,
			Rubric:   strings.TrimSpace(body),
		}
		for _, field := range fields[1:] {
			if value, ok := strings.CutPrefix(field, "severity="); ok {
				section.Severity = value
			}
		}
		out = append(out, section)
	}
	return out
}

// JudgeWithHarness returns the production JudgeFunc: each rubric section is
// evaluated by the harness ai_judge checker in a separate model call, so a
// failure maps back to its category and severity. Any judge error aborts the
// review (fail-closed).
func JudgeWithHarness(judge *checkers.AIJudge, model string, timeoutSec int) JudgeFunc {
	if judge == nil {
		judge = checkers.NewAIJudge()
	}
	return func(ctx context.Context, rubric, content string) ([]Finding, error) {
		sections := splitRubric(rubric)
		if len(sections) == 0 {
			return nil, errors.New("content: review rubric has no rule sections")
		}
		var out []Finding
		for _, section := range sections {
			res := judge.Run(ctx, harness.Spec{
				Kind:        harness.KindAIJudge,
				Target:      harness.TargetAgentOutput,
				JudgePrompt: section.Rubric,
				JudgeModel:  model,
				TimeoutSec:  timeoutSec,
			}, harness.CheckEnv{AgentOutput: content})

			switch res.Status {
			case "pass", "skip":
			case "fail":
				out = append(out, Finding{
					Rule:     section.Rule,
					Severity: section.Severity,
					Message:  res.Message,
				})
			default:
				return nil, fmt.Errorf("content: ai_judge %s: %s", section.Rule, res.Message)
			}
		}
		return out, nil
	}
}
