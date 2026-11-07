package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// BotRef identifies the ops bot a content run belongs to. Task 14 owns this
// type for the content package; the opsbot package defines its own BotRef.
type BotRef struct {
	ID     int64
	PackID string
}

// Pipeline stages, in order. A failure names the stage it stopped at and the
// pipeline never advances past it.
const (
	StageTopic  = "topic"
	StageCopy   = "copy"
	StageVisual = "visual"
	StageLayout = "layout"
)

// Stage statuses recorded in artifacts.json.
const (
	StatusOK     = "ok"
	StatusFailed = "failed"
)

var (
	// ErrInvalidPipelineInput covers a run with no issue, pack or a format the
	// pack does not support.
	ErrInvalidPipelineInput = errors.New("content: invalid pipeline input")
	// ErrArtifactStoreRequired is returned when a run has nowhere to write its
	// versioned artifacts.
	ErrArtifactStoreRequired = errors.New("content: artifact store is required")
)

// StageError reports which pipeline stage failed and why.
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string { return e.Stage + " stage: " + e.Err.Error() }

func (e *StageError) Unwrap() error { return e.Err }

// TopicPlan is the output of the topic stage: the brief turned into one topic
// and an angle the copy stage writes against.
type TopicPlan struct {
	Topic    string   `json:"topic"`
	Angle    string   `json:"angle"`
	Audience string   `json:"audience,omitempty"`
	Goal     string   `json:"goal,omitempty"`
	Format   string   `json:"format"`
	Sources  []string `json:"sources,omitempty"`
}

// CopyWriter turns a topic plan and the pack's writing prompts into platform
// copy. The production implementation calls an LLM; tests inject a fake.
type CopyWriter interface {
	Write(ctx context.Context, req CopyRequest) (CopyResult, error)
}

// CopyRequest carries everything a copy provider needs.
type CopyRequest struct {
	PackID  string
	Format  string
	Brief   OpsContentBrief
	Topic   TopicPlan
	Schema  packs.ContentFormat
	Prompts []Prompt
}

// CopyResult is the generated copy. Articles use Digest/Body/CTA/Sources;
// notes use Hook/Body/Tags/CTA.
type CopyResult struct {
	Title    string   `json:"title"`
	Digest   string   `json:"digest,omitempty"`
	Hook     string   `json:"hook,omitempty"`
	Body     string   `json:"body"`
	CTA      string   `json:"cta,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Sources  []string `json:"sources,omitempty"`
	Provider string   `json:"provider"`
	CostUSD  float64  `json:"cost_usd"`
}

// LayoutRenderer pairs copy and images into a publishable draft: an HTML
// article for 公众号, an ordered image/copy pairing for 小红书.
type LayoutRenderer interface {
	Render(ctx context.Context, req LayoutRequest) (LayoutResult, error)
}

// LayoutRequest is handed to the layout renderer.
type LayoutRequest struct {
	PackID string
	Format string
	Brief  OpsContentBrief
	Topic  TopicPlan
	Copy   CopyResult
	Images []ImageAsset
	Schema packs.ContentFormat
	Prompt string
	OutDir string
}

// LayoutResult points at the rendered draft.
type LayoutResult struct {
	Path     string  `json:"path"`
	Provider string  `json:"provider"`
	CostUSD  float64 `json:"cost_usd"`
}

// ArtifactStore allocates a fresh version directory per run. Every modification
// of a piece of content produces a new version; approvals and publishing
// reference Version.
type ArtifactStore interface {
	AllocateVersion(ctx context.Context, issueID int64) (dir string, version int, err error)
}

// FileArtifactStore keeps artifacts under Root/<issueID>/v<version>/. Root is
// normally <workspace>/ops/content.
type FileArtifactStore struct {
	Root string
}

var _ ArtifactStore = (*FileArtifactStore)(nil)

// AllocateVersion creates the next free version directory. It relies on
// os.Mkdir's atomicity rather than a lock so concurrent runs never share a
// version.
func (s *FileArtifactStore) AllocateVersion(ctx context.Context, issueID int64) (string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return "", 0, ErrArtifactStoreRequired
	}
	if issueID <= 0 {
		return "", 0, fmt.Errorf("%w: issue id is required", ErrInvalidPipelineInput)
	}
	base := filepath.Join(s.Root, strconv.FormatInt(issueID, 10))
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", 0, err
	}
	version := 1
	for {
		dir := filepath.Join(base, "v"+strconv.Itoa(version))
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, version, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", 0, err
		}
		version++
	}
}

// StageRecord is one line of artifacts.json: the files a stage produced, which
// provider made them, how long it took and what it cost.
type StageRecord struct {
	Stage      string   `json:"stage"`
	Status     string   `json:"status"`
	Files      []string `json:"files,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	DurationMS int64    `json:"duration_ms"`
	CostUSD    float64  `json:"cost_usd"`
	Error      string   `json:"error,omitempty"`
}

// Artifact is the result of one pipeline run. Its JSON form is artifacts.json.
type Artifact struct {
	IssueID         int64         `json:"issue_id"`
	BotID           int64         `json:"bot_id,omitempty"`
	PackID          string        `json:"pack_id"`
	Format          string        `json:"format"`
	Version         int           `json:"version"`
	Dir             string        `json:"dir,omitempty"`
	TopicPath       string        `json:"topic_path,omitempty"`
	CopyPath        string        `json:"copy_path,omitempty"`
	Images          []ImageAsset  `json:"images,omitempty"`
	LayoutPath      string        `json:"layout_path,omitempty"`
	Stages          []StageRecord `json:"stages"`
	TotalCostUSD    float64       `json:"total_cost_usd"`
	TotalDurationMS int64         `json:"total_duration_ms"`
	CreatedAt       time.Time     `json:"created_at"`
}

// Pipeline runs topic → copy → visual → layout for one content issue.
type Pipeline struct {
	Copy   CopyWriter
	Images ImageGenerator
	// Layout defaults to TemplateLayout when nil.
	Layout LayoutRenderer
	Store  ArtifactStore
	// Packs defaults to the embedded built-in packs when nil.
	Packs PackResolver
	// Now overrides the clock in tests.
	Now func() time.Time
	// InlineImages overrides how many inline images the visual stage asks for.
	// Zero means the per-format default (article 1, note 2).
	InlineImages int
}

// Run produces one version of the content. A stage error stops the run at that
// stage and is returned as *StageError; no later stage runs.
func (p *Pipeline) Run(ctx context.Context, bot BotRef, brief OpsContentBrief, issueID int64) (Artifact, error) {
	if issueID <= 0 {
		return Artifact{}, fmt.Errorf("%w: issue id is required", ErrInvalidPipelineInput)
	}
	packID := strings.TrimSpace(bot.PackID)
	if packID == "" {
		packID = strings.TrimSpace(brief.Channel)
	}
	if packID == "" {
		return Artifact{}, fmt.Errorf("%w: pack id is required", ErrInvalidPipelineInput)
	}

	pack, err := p.packs().Resolve(ctx, packID)
	if err != nil {
		return Artifact{}, fmt.Errorf("pipeline: resolve pack %s: %w", packID, err)
	}

	format := strings.ToLower(strings.TrimSpace(brief.Format))
	if format == "" {
		format = defaultFormat(pack.Manifest)
	}
	schema, ok := pack.Manifest.ContentSchema.Formats[format]
	if !ok {
		return Artifact{}, fmt.Errorf("%w: pack %s does not support format %q", ErrInvalidPipelineInput, packID, format)
	}
	brief.Format = format
	brief.Channel = packID

	art := Artifact{
		IssueID:   issueID,
		BotID:     bot.ID,
		PackID:    packID,
		Format:    format,
		Version:   0,
		CreatedAt: p.now(),
	}
	if p.Store != nil {
		dir, version, err := p.Store.AllocateVersion(ctx, issueID)
		if err != nil {
			return art, fmt.Errorf("pipeline: allocate version: %w", err)
		}
		art.Dir = dir
		art.Version = version
	}

	// topic: turn the brief into one topic and an angle.
	started := time.Now()
	plan, topicFiles, err := p.topicStage(ctx, art.Dir, brief)
	topicRec := StageRecord{Stage: StageTopic, Status: StatusOK, Files: topicFiles, Provider: "builtin", DurationMS: elapsedMS(started)}
	if err != nil {
		return p.fail(&art, topicRec, err)
	}
	p.complete(&art, topicRec)
	if len(topicFiles) > 0 {
		art.TopicPath = filepath.Join(art.Dir, topicFiles[0])
	}

	// copy: platform article or note, using the pack's writing prompts.
	started = time.Now()
	copyResult, copyFiles, err := p.copyStage(ctx, art.Dir, pack, brief, plan, schema)
	copyRec := StageRecord{
		Stage:      StageCopy,
		Status:     StatusOK,
		Files:      copyFiles,
		Provider:   copyResult.Provider,
		DurationMS: elapsedMS(started),
		CostUSD:    copyResult.CostUSD,
	}
	if err != nil {
		return p.fail(&art, copyRec, err)
	}
	p.complete(&art, copyRec)
	if len(copyFiles) > 0 {
		art.CopyPath = filepath.Join(art.Dir, "copy.md")
	}

	// visual: cover plus inline images at the pack's dimensions.
	started = time.Now()
	visual, err := p.visualStage(ctx, art.Dir, pack, brief, plan, schema)
	visualRec := StageRecord{
		Stage:      StageVisual,
		Status:     StatusOK,
		Files:      visual.Files,
		Provider:   visual.Provider,
		DurationMS: elapsedMS(started),
		CostUSD:    visual.CostUSD,
	}
	if err != nil {
		return p.fail(&art, visualRec, err)
	}
	p.complete(&art, visualRec)
	art.Images = visual.Images

	// layout: pasteable 公众号 HTML draft or 小红书 ordered pairing.
	started = time.Now()
	layoutResult, layoutFiles, err := p.layoutStage(ctx, art.Dir, pack, brief, plan, copyResult, visual.Images, schema)
	layoutRec := StageRecord{
		Stage:      StageLayout,
		Status:     StatusOK,
		Files:      layoutFiles,
		Provider:   layoutResult.Provider,
		DurationMS: elapsedMS(started),
		CostUSD:    layoutResult.CostUSD,
	}
	if err != nil {
		return p.fail(&art, layoutRec, err)
	}
	p.complete(&art, layoutRec)
	art.LayoutPath = layoutResult.Path

	if err := writeArtifactManifest(art.Dir, art); err != nil {
		return art, fmt.Errorf("pipeline: write artifacts manifest: %w", err)
	}
	return art, nil
}

func (p *Pipeline) packs() PackResolver {
	if p.Packs != nil {
		return p.Packs
	}
	return BuiltinPacks{}
}

func (p *Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pipeline) complete(art *Artifact, rec StageRecord) {
	art.Stages = append(art.Stages, rec)
	art.TotalDurationMS += rec.DurationMS
	art.TotalCostUSD += rec.CostUSD
}

// fail records the failed stage (so artifacts.json still shows how far the run
// got) and returns the stage error without running anything else.
func (p *Pipeline) fail(art *Artifact, rec StageRecord, cause error) (Artifact, error) {
	rec.Status = StatusFailed
	rec.Error = cause.Error()
	art.Stages = append(art.Stages, rec)
	art.TotalDurationMS += rec.DurationMS
	art.TotalCostUSD += rec.CostUSD
	if art.Dir != "" {
		_ = writeArtifactManifest(art.Dir, *art)
	}
	return *art, &StageError{Stage: rec.Stage, Err: cause}
}

func (p *Pipeline) topicStage(ctx context.Context, dir string, brief OpsContentBrief) (TopicPlan, []string, error) {
	if err := ctx.Err(); err != nil {
		return TopicPlan{}, nil, err
	}
	plan := buildTopicPlan(brief)
	if dir == "" {
		return plan, nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return plan, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "topic.md"), []byte(renderTopicMarkdown(plan)), 0o644); err != nil {
		return plan, nil, err
	}
	return plan, []string{"topic.md"}, nil
}

func (p *Pipeline) copyStage(ctx context.Context, dir string, pack Pack, brief OpsContentBrief, plan TopicPlan, schema packs.ContentFormat) (CopyResult, []string, error) {
	if err := ctx.Err(); err != nil {
		return CopyResult{}, nil, err
	}
	if p.Copy == nil {
		return CopyResult{}, nil, errors.New("copy writer is not configured")
	}
	prompts, err := loadPrompts(pack.Files, copyPromptNames(brief.Format)...)
	if err != nil {
		return CopyResult{}, nil, err
	}
	result, err := p.Copy.Write(ctx, CopyRequest{
		PackID:  brief.Channel,
		Format:  brief.Format,
		Brief:   brief,
		Topic:   plan,
		Schema:  schema,
		Prompts: prompts,
	})
	if err != nil {
		return CopyResult{}, nil, err
	}
	if err := validateCopy(result, schema); err != nil {
		return CopyResult{}, nil, err
	}
	if result.Provider == "" {
		result.Provider = "unknown"
	}
	if dir == "" {
		return result, nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return CopyResult{}, nil, err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return CopyResult{}, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "copy.json"), append(data, '\n'), 0o644); err != nil {
		return CopyResult{}, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "copy.md"), []byte(renderCopyMarkdown(result)), 0o644); err != nil {
		return CopyResult{}, nil, err
	}
	return result, []string{"copy.json", "copy.md"}, nil
}

type visualStageResult struct {
	Images   []ImageAsset
	Provider string
	CostUSD  float64
	Files    []string
}

func (p *Pipeline) visualStage(ctx context.Context, dir string, pack Pack, brief OpsContentBrief, plan TopicPlan, schema packs.ContentFormat) (visualStageResult, error) {
	if err := ctx.Err(); err != nil {
		return visualStageResult{}, err
	}
	if dir == "" {
		return visualStageResult{}, ErrArtifactStoreRequired
	}
	if p.Images == nil {
		return visualStageResult{}, errors.New("image generator is not configured")
	}
	outDir := filepath.Join(dir, "images")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return visualStageResult{}, err
	}
	prompt, err := promptText(pack.Files, coverPromptName(brief.Format))
	if err != nil {
		return visualStageResult{}, err
	}
	specs := imageSpecs(brief.Format, schema, p.InlineImages)
	result, err := p.Images.Generate(ctx, ImageRequest{
		PackID: brief.Channel,
		Format: brief.Format,
		Topic:  plan.Topic,
		Prompt: prompt,
		Specs:  specs,
		OutDir: outDir,
	})
	if err != nil {
		return visualStageResult{}, err
	}
	if len(result.Images) == 0 {
		return visualStageResult{}, errors.New("image generator returned no images")
	}
	if !hasCoverImage(result.Images) {
		return visualStageResult{}, errors.New("image generator returned no cover image")
	}
	if result.Provider == "" {
		result.Provider = "image-generation"
	}
	out := visualStageResult{Images: result.Images, Provider: result.Provider, CostUSD: result.CostUSD}
	for _, img := range result.Images {
		if rel, err := filepath.Rel(dir, img.Path); err == nil && !strings.HasPrefix(rel, "..") {
			out.Files = append(out.Files, filepath.ToSlash(rel))
		}
	}
	return out, nil
}

func (p *Pipeline) layoutStage(ctx context.Context, dir string, pack Pack, brief OpsContentBrief, plan TopicPlan, copyResult CopyResult, images []ImageAsset, schema packs.ContentFormat) (LayoutResult, []string, error) {
	if err := ctx.Err(); err != nil {
		return LayoutResult{}, nil, err
	}
	if dir == "" {
		return LayoutResult{}, nil, ErrArtifactStoreRequired
	}
	renderer := p.Layout
	if renderer == nil {
		renderer = TemplateLayout{}
	}
	prompt, err := promptText(pack.Files, layoutPromptName(brief.Format))
	if err != nil {
		return LayoutResult{}, nil, err
	}
	result, err := renderer.Render(ctx, LayoutRequest{
		PackID: brief.Channel,
		Format: brief.Format,
		Brief:  brief,
		Topic:  plan,
		Copy:   copyResult,
		Images: images,
		Schema: schema,
		Prompt: prompt,
		OutDir: dir,
	})
	if err != nil {
		return LayoutResult{}, nil, err
	}
	if !filepath.IsAbs(result.Path) && result.Path != "" {
		result.Path = filepath.Join(dir, result.Path)
	}
	if strings.TrimSpace(result.Path) == "" {
		return LayoutResult{}, nil, errors.New("layout renderer returned no output path")
	}
	if _, err := os.Stat(result.Path); err != nil {
		return LayoutResult{}, nil, fmt.Errorf("layout output %s: %w", result.Path, err)
	}
	if result.Provider == "" {
		result.Provider = "builtin-template"
	}
	files := []string{filepath.Base(result.Path)}
	return result, files, nil
}

// defaultFormat picks the pack's primary format when the brief leaves it open.
func defaultFormat(m packs.Manifest) string {
	for _, format := range []string{FormatArticle, FormatNote, FormatVideo} {
		if _, ok := m.ContentSchema.Formats[format]; ok {
			return format
		}
	}
	return FormatArticle
}

func buildTopicPlan(brief OpsContentBrief) TopicPlan {
	audience := strings.TrimSpace(brief.Audience)
	goal := strings.TrimSpace(brief.Goal)
	topic := strings.TrimSpace(brief.Topic)
	if topic == "" {
		switch {
		case audience != "":
			topic = "给" + audience + "的实用指南"
		case goal != "":
			topic = "围绕「" + goal + "」的内容"
		default:
			topic = "未命名选题"
		}
	}
	angle := "「" + topic + "」"
	if audience != "" {
		angle = "面向" + audience + "，" + angle
	}
	if goal != "" {
		angle += "，服务于「" + goal + "」目标"
	}
	angle += "，以" + formatLabel(brief.Format) + "呈现"
	return TopicPlan{
		Topic:    topic,
		Angle:    angle,
		Audience: audience,
		Goal:     goal,
		Format:   brief.Format,
		Sources:  brief.SourceRefs,
	}
}

func formatLabel(format string) string {
	switch format {
	case FormatArticle:
		return "公众号长文"
	case FormatNote:
		return "小红书笔记"
	case FormatVideo:
		return "短视频"
	default:
		return format
	}
}

func renderTopicMarkdown(p TopicPlan) string {
	var b strings.Builder
	b.WriteString("# 选题\n\n")
	fmt.Fprintf(&b, "- 选题：%s\n", p.Topic)
	fmt.Fprintf(&b, "- 角度：%s\n", p.Angle)
	if p.Audience != "" {
		fmt.Fprintf(&b, "- 受众：%s\n", p.Audience)
	}
	if p.Goal != "" {
		fmt.Fprintf(&b, "- 目标：%s\n", p.Goal)
	}
	fmt.Fprintf(&b, "- 形式：%s\n", p.Format)
	for _, source := range p.Sources {
		fmt.Fprintf(&b, "- 来源：%s\n", source)
	}
	return b.String()
}

func renderCopyMarkdown(c CopyResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", c.Title)
	if c.Digest != "" {
		fmt.Fprintf(&b, "> %s\n\n", c.Digest)
	}
	if c.Hook != "" {
		fmt.Fprintf(&b, "**钩子**：%s\n\n", c.Hook)
	}
	b.WriteString(c.Body)
	b.WriteString("\n")
	if c.CTA != "" {
		fmt.Fprintf(&b, "\n**CTA**：%s\n", c.CTA)
	}
	if len(c.Tags) > 0 {
		fmt.Fprintf(&b, "\n**标签**：%s\n", strings.Join(c.Tags, " "))
	}
	if len(c.Sources) > 0 {
		b.WriteString("\n**来源**：\n")
		for _, source := range c.Sources {
			fmt.Fprintf(&b, "- %s\n", source)
		}
	}
	return b.String()
}

// validateCopy enforces the pack's platform limits at the copy stage so a run
// never advances to visuals with unpostable copy.
func validateCopy(c CopyResult, schema packs.ContentFormat) error {
	if strings.TrimSpace(c.Title) == "" {
		return errors.New("copy title is empty")
	}
	if strings.TrimSpace(c.Body) == "" {
		return errors.New("copy body is empty")
	}
	if schema.MaxTitleChars > 0 {
		if n := len([]rune(c.Title)); n > schema.MaxTitleChars {
			return fmt.Errorf("title is %d chars, pack allows %d", n, schema.MaxTitleChars)
		}
	}
	if schema.MaxChars > 0 {
		if n := len([]rune(c.Body)); n > schema.MaxChars {
			return fmt.Errorf("body is %d chars, pack allows %d", n, schema.MaxChars)
		}
	}
	return nil
}

func elapsedMS(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}

func writeArtifactManifest(dir string, art Artifact) error {
	if dir == "" {
		return ErrArtifactStoreRequired
	}
	data, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "artifacts.json"), append(data, '\n'), 0o644)
}

// TemplateLayout is the built-in renderer: a pasteable 公众号 HTML draft and an
// ordered image/copy pairing for 小红书.
type TemplateLayout struct{}

var _ LayoutRenderer = TemplateLayout{}

func (TemplateLayout) Render(ctx context.Context, req LayoutRequest) (LayoutResult, error) {
	if err := ctx.Err(); err != nil {
		return LayoutResult{}, err
	}
	if strings.TrimSpace(req.OutDir) == "" {
		return LayoutResult{}, errors.New("layout: output directory is required")
	}
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return LayoutResult{}, err
	}
	switch req.Format {
	case FormatArticle:
		return renderArticleLayout(req)
	case FormatNote:
		return renderNoteLayout(req)
	default:
		return LayoutResult{}, fmt.Errorf("layout: unsupported format %q", req.Format)
	}
}

func renderArticleLayout(req LayoutRequest) (LayoutResult, error) {
	cover, inline := splitImages(req.Images)

	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>" + html.EscapeString(req.Copy.Title) + "</title>\n</head>\n<body>\n")
	b.WriteString("<h1>" + html.EscapeString(req.Copy.Title) + "</h1>\n")
	if req.Copy.Digest != "" {
		b.WriteString("<p class=\"digest\"><em>" + html.EscapeString(req.Copy.Digest) + "</em></p>\n")
	}
	if cover != nil {
		b.WriteString(figureHTML(req.OutDir, *cover, "封面"))
	}
	b.WriteString(renderArticleBody(req.Copy.Body))
	for _, img := range inline {
		b.WriteString(figureHTML(req.OutDir, img, "配图"))
	}
	if req.Copy.CTA != "" {
		b.WriteString("<p class=\"cta\">" + html.EscapeString(req.Copy.CTA) + "</p>\n")
	}
	if len(req.Copy.Sources) > 0 {
		b.WriteString("<h2>来源</h2>\n<ul>\n")
		for _, source := range req.Copy.Sources {
			b.WriteString("<li>" + html.EscapeString(source) + "</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	b.WriteString("</body>\n</html>\n")

	path := filepath.Join(req.OutDir, "article.html")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return LayoutResult{}, err
	}
	return LayoutResult{Path: path, Provider: "builtin-template"}, nil
}

// renderArticleBody converts the light markdown the writing prompt asks for
// (blank-line paragraphs, "## " headings, "- " bullets) into escaped HTML.
func renderArticleBody(body string) string {
	var b strings.Builder
	inList := false
	inPara := false
	flushPara := func() {
		if inPara {
			b.WriteString("</p>\n")
			inPara = false
		}
	}
	flushList := func() {
		if inList {
			b.WriteString("</ul>\n")
			inList = false
		}
	}

	for _, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			flushPara()
			flushList()
		case strings.HasPrefix(line, "### "):
			flushPara()
			flushList()
			b.WriteString("<h3>" + html.EscapeString(strings.TrimSpace(line[4:])) + "</h3>\n")
		case strings.HasPrefix(line, "## "):
			flushPara()
			flushList()
			b.WriteString("<h2>" + html.EscapeString(strings.TrimSpace(line[3:])) + "</h2>\n")
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "· "):
			flushPara()
			if !inList {
				b.WriteString("<ul>\n")
				inList = true
			}
			item := strings.TrimPrefix(strings.TrimPrefix(line, "- "), "· ")
			b.WriteString("<li>" + html.EscapeString(strings.TrimSpace(item)) + "</li>\n")
		default:
			flushList()
			if inPara {
				b.WriteString("<br>")
			} else {
				b.WriteString("<p>")
				inPara = true
			}
			b.WriteString(html.EscapeString(line))
		}
	}
	flushPara()
	flushList()
	return b.String()
}

// NoteLayout is the ordered image/copy pairing 小红书 publishing consumes.
type NoteLayout struct {
	Format string      `json:"format"`
	Title  string      `json:"title"`
	Hook   string      `json:"hook,omitempty"`
	Body   string      `json:"body"`
	CTA    string      `json:"cta,omitempty"`
	Tags   []string    `json:"tags,omitempty"`
	Images []NoteImage `json:"images"`
}

// NoteImage pairs one image with the caption it carries in the note.
type NoteImage struct {
	Order   int    `json:"order"`
	Purpose string `json:"purpose"`
	Path    string `json:"path"`
	Caption string `json:"caption,omitempty"`
}

func renderNoteLayout(req LayoutRequest) (LayoutResult, error) {
	layout := NoteLayout{
		Format: FormatNote,
		Title:  req.Copy.Title,
		Hook:   req.Copy.Hook,
		Body:   req.Copy.Body,
		CTA:    req.Copy.CTA,
		Tags:   req.Copy.Tags,
	}
	segments := bodySegments(req.Copy.Body)
	for i, img := range req.Images {
		caption := ""
		switch {
		case i == 0 || img.Purpose == ImagePurposeCover:
			if req.Copy.Hook != "" {
				caption = req.Copy.Hook
			} else {
				caption = req.Copy.Title
			}
		case i-1 < len(segments):
			caption = segments[i-1]
		}
		layout.Images = append(layout.Images, NoteImage{
			Order:   i + 1,
			Purpose: img.Purpose,
			Path:    img.Path,
			Caption: caption,
		})
	}

	data, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		return LayoutResult{}, err
	}
	path := filepath.Join(req.OutDir, "note-layout.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return LayoutResult{}, err
	}
	return LayoutResult{Path: path, Provider: "builtin-template"}, nil
}

func splitImages(images []ImageAsset) (*ImageAsset, []ImageAsset) {
	var cover *ImageAsset
	var inline []ImageAsset
	for i := range images {
		if images[i].Purpose == ImagePurposeCover && cover == nil {
			cover = &images[i]
			continue
		}
		inline = append(inline, images[i])
	}
	if cover == nil && len(inline) > 0 {
		cover = &inline[0]
		inline = inline[1:]
	}
	return cover, inline
}

func hasCoverImage(images []ImageAsset) bool {
	for _, img := range images {
		if img.Purpose == ImagePurposeCover {
			return true
		}
	}
	return false
}

// bodySegments splits the note body into caption-sized chunks for image pairing.
func bodySegments(body string) []string {
	var out []string
	for _, block := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		first := strings.TrimSpace(strings.SplitN(block, "\n", 2)[0])
		first = strings.TrimPrefix(first, "- ")
		first = strings.TrimPrefix(first, "· ")
		if runes := []rune(first); len(runes) > 40 {
			first = string(runes[:40])
		}
		out = append(out, first)
	}
	return out
}

func figureHTML(outDir string, img ImageAsset, alt string) string {
	src := imageSrc(outDir, img.Path)
	if src == "" {
		return ""
	}
	return "<figure><img src=\"" + html.EscapeString(src) + "\" alt=\"" + html.EscapeString(alt) + "\"></figure>\n"
}

// imageSrc prefers a path relative to the draft so the artifact directory stays
// portable; anything outside it (e.g. a remote URL) is used verbatim.
func imageSrc(outDir, path string) string {
	if path == "" {
		return ""
	}
	if rel, err := filepath.Rel(outDir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}
