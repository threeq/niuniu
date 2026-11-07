package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// Video pipeline stages, in order. A failure names the stage it stopped at and
// the pipeline never advances past it.
const (
	VideoStageScript     = "script"
	VideoStageStoryboard = "storyboard"
	VideoStageTTS        = "tts"
	VideoStageFrames     = "frames"
	VideoStageSubtitles  = "subtitles"
	VideoStageCompose    = "compose"
	VideoStageAdapt      = "adapt"
)

// Video frames are the cover plus one still per storyboard shot.
const VideoFramePurposeShot = "shot"

// Video cost is recorded in issue_exec_events as seconds of finished video.
const (
	IssueExecEventKindCost = "cost"
	CostCategoryVideo      = "video"
	CostUnitSecond         = "second"
	videoCostSummary       = "video"
)

var (
	// ErrVideoQualityGate covers a finished cut that fails the subtitle, cover,
	// duration, ratio or black-frame gate.
	ErrVideoQualityGate = errors.New("content: video quality gate failed")
)

// VideoSpec is the platform target the run adapts to: ratio, pixel size and
// the maximum duration the pack accepts.
type VideoSpec struct {
	Platform       string `json:"platform"`
	Ratio          string `json:"ratio"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	MaxDurationSec int    `json:"max_duration_sec"`
	SubtitleFormat string `json:"subtitle_format"`
}

// VideoScriptWriter turns a brief and the pack's video-script prompt into the
// narration and title. The production implementation calls an LLM or the
// video-gen MCP; tests inject a fake.
type VideoScriptWriter interface {
	WriteScript(ctx context.Context, req VideoScriptRequest) (VideoScriptResult, error)
}

// VideoScriptRequest is handed to the script writer.
type VideoScriptRequest struct {
	PackID string
	Brief  OpsContentBrief
	Topic  TopicPlan
	Prompt string
	Spec   VideoSpec
}

// VideoScriptResult is the scripted narration.
type VideoScriptResult struct {
	Title     string  `json:"title"`
	Hook      string  `json:"hook,omitempty"`
	Narration string  `json:"narration"`
	CTA       string  `json:"cta,omitempty"`
	Provider  string  `json:"provider"`
	CostUSD   float64 `json:"cost_usd"`
}

// VideoShot is one storyboard shot. Index is normalized to the slice position
// by the storyboard stage so later stages can pair frames and subtitles.
type VideoShot struct {
	Index     int     `json:"index"`
	StartSec  float64 `json:"start_sec"`
	EndSec    float64 `json:"end_sec"`
	Visual    string  `json:"visual"`
	Narration string  `json:"narration"`
}

// StoryboardPlanner splits the script into timed shots.
type StoryboardPlanner interface {
	PlanStoryboard(ctx context.Context, req StoryboardRequest) (VideoStoryboard, error)
}

// StoryboardRequest is handed to the storyboard planner.
type StoryboardRequest struct {
	PackID string
	Brief  OpsContentBrief
	Topic  TopicPlan
	Script VideoScriptResult
	Prompt string
	Spec   VideoSpec
}

// VideoStoryboard is the ordered, timed shot list.
type VideoStoryboard struct {
	Shots    []VideoShot `json:"shots"`
	Provider string      `json:"provider"`
	CostUSD  float64     `json:"cost_usd"`
}

// TTSProvider synthesizes the narration into one audio file.
type TTSProvider interface {
	Synthesize(ctx context.Context, req TTSRequest) (TTSResult, error)
}

// TTSRequest is handed to the speech provider.
type TTSRequest struct {
	PackID string
	Text   string
	Voice  string
	OutDir string
	Spec   VideoSpec
	Shots  []VideoShot
}

// TTSResult is the narration audio and its real length, which is what the
// subtitles and composer time against.
type TTSResult struct {
	AudioPath   string  `json:"audio_path"`
	DurationSec int     `json:"duration_sec"`
	Provider    string  `json:"provider"`
	CostUSD     float64 `json:"cost_usd"`
}

// FrameGenerator renders the cover plus one still per shot.
type FrameGenerator interface {
	RenderFrames(ctx context.Context, req FramesRequest) (FramesResult, error)
}

// FramesRequest is handed to the frame generator.
type FramesRequest struct {
	PackID string
	Prompt string
	Spec   VideoSpec
	Shots  []VideoShot
	OutDir string
}

// VideoFrame is one generated still.
type VideoFrame struct {
	Index   int    `json:"index"`
	Purpose string `json:"purpose"`
	Path    string `json:"path"`
	Ratio   string `json:"ratio,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
}

// FramesResult is the cover plus shot stills.
type FramesResult struct {
	Frames   []VideoFrame `json:"frames"`
	Provider string       `json:"provider"`
	CostUSD  float64      `json:"cost_usd"`
}

// SubtitleWriter produces the subtitle track timed to the narration audio.
type SubtitleWriter interface {
	WriteSubtitles(ctx context.Context, req SubtitleRequest) (SubtitleResult, error)
}

// SubtitleRequest is handed to the subtitle writer.
type SubtitleRequest struct {
	Format           string
	Shots            []VideoShot
	AudioDurationSec int
	Spec             VideoSpec
	OutDir           string
}

// SubtitleResult points at the subtitle file.
type SubtitleResult struct {
	Path     string  `json:"path"`
	Format   string  `json:"format"`
	Provider string  `json:"provider"`
	CostUSD  float64 `json:"cost_usd"`
}

// VideoComposer assembles frames, audio and subtitles into the final cut plus a
// cover. It must write both files under OutDir and report the technical facts
// the adapt gate verifies.
type VideoComposer interface {
	Compose(ctx context.Context, req ComposeRequest) (ComposeResult, error)
}

// ComposeRequest is handed to the composer.
type ComposeRequest struct {
	PackID       string
	Spec         VideoSpec
	Shots        []VideoShot
	Frames       []VideoFrame
	AudioPath    string
	SubtitlePath string
	CoverPath    string
	DurationSec  int
	OutDir       string
}

// ComposeResult is the finished cut. BlackFrames is the composer's technical
// QC count; the adapt gate refuses anything above zero.
type ComposeResult struct {
	FinalPath   string  `json:"final_path"`
	CoverPath   string  `json:"cover_path"`
	DurationSec int     `json:"duration_sec"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Ratio       string  `json:"ratio"`
	BlackFrames int     `json:"black_frames"`
	Provider    string  `json:"provider"`
	CostUSD     float64 `json:"cost_usd"`
	// CostPerSec is an alternative cost model: when CostUSD is zero and this
	// is set, the cost is this rate times the finished duration.
	CostPerSec float64 `json:"cost_per_sec,omitempty"`
}

// VideoArtifact is the result of one video run. Its JSON form is artifacts.json
// for the version directory and carries the platform fields downstream review
// and publishing read.
type VideoArtifact struct {
	IssueID         int64         `json:"issue_id"`
	BotID           int64         `json:"bot_id,omitempty"`
	PackID          string        `json:"pack_id"`
	Format          string        `json:"format"`
	Version         int           `json:"version"`
	Dir             string        `json:"dir,omitempty"`
	Title           string        `json:"title,omitempty"`
	ScriptPath      string        `json:"script_path,omitempty"`
	StoryboardPath  string        `json:"storyboard_path,omitempty"`
	AudioPath       string        `json:"audio_path,omitempty"`
	Frames          []string      `json:"frames,omitempty"`
	Subtitles       string        `json:"subtitle_path,omitempty"`
	FinalPath       string        `json:"final_path,omitempty"`
	CoverPath       string        `json:"cover_path,omitempty"`
	DurationSec     int           `json:"duration_sec"`
	Ratio           string        `json:"ratio"`
	Width           int           `json:"width,omitempty"`
	Height          int           `json:"height,omitempty"`
	BlackFrames     int           `json:"black_frames"`
	Stages          []StageRecord `json:"stages"`
	TotalCostUSD    float64       `json:"total_cost_usd"`
	TotalDurationMS int64         `json:"total_duration_ms"`
	CreatedAt       time.Time     `json:"created_at"`
}

// VideoPipeline runs script → storyboard → tts → frames → subtitles → compose →
// adapt for one video content issue. Every paid capability is an interface so
// the default video-gen MCP and a user-chosen TTS/video model are interchangeable
// at wiring time.
type VideoPipeline struct {
	Script     VideoScriptWriter
	Storyboard StoryboardPlanner
	TTS        TTSProvider
	Frames     FrameGenerator
	Subtitles  SubtitleWriter
	Compose    VideoComposer
	// Voice is passed to the TTS provider; empty lets it use its default.
	Voice string
	// Store allocates the version directory. Nil keeps the run self-contained
	// in a temp directory (useful for previews and tests).
	Store ArtifactStore
	// Packs defaults to the embedded built-in packs when nil.
	Packs PackResolver
	// Recorder writes the per-second video cost to issue_exec_events. Nil
	// skips cost recording.
	Recorder EventRecorder
	// Now overrides the clock in tests.
	Now func() time.Time
}

// Run produces one video version. A stage error stops the run at that stage and
// is returned as *StageError; no later stage runs.
func (p *VideoPipeline) Run(ctx context.Context, bot BotRef, brief OpsContentBrief, issueID int64) (VideoArtifact, error) {
	if issueID <= 0 {
		return VideoArtifact{}, fmt.Errorf("%w: issue id is required", ErrInvalidPipelineInput)
	}
	format := strings.ToLower(strings.TrimSpace(brief.Format))
	if format != "" && format != FormatVideo {
		return VideoArtifact{}, fmt.Errorf("%w: video pipeline cannot produce %q", ErrInvalidPipelineInput, format)
	}
	packID := strings.TrimSpace(bot.PackID)
	if packID == "" {
		packID = strings.TrimSpace(brief.Channel)
	}
	if packID == "" {
		return VideoArtifact{}, fmt.Errorf("%w: pack id is required", ErrInvalidPipelineInput)
	}

	pack, err := p.packs().Resolve(ctx, packID)
	if err != nil {
		return VideoArtifact{}, fmt.Errorf("video pipeline: resolve pack %s: %w", packID, err)
	}
	spec, err := videoSpecFor(packID, pack.Manifest.ContentSchema)
	if err != nil {
		return VideoArtifact{}, err
	}
	brief.Format = FormatVideo
	brief.Channel = packID

	art := VideoArtifact{
		IssueID:   issueID,
		BotID:     bot.ID,
		PackID:    packID,
		Format:    FormatVideo,
		Ratio:     spec.Ratio,
		Width:     spec.Width,
		Height:    spec.Height,
		CreatedAt: p.now(),
	}
	dir, version, err := p.allocate(ctx, issueID)
	if err != nil {
		return art, fmt.Errorf("video pipeline: allocate version: %w", err)
	}
	art.Dir = dir
	art.Version = version

	// script: narration and title.
	started := time.Now()
	script, err := p.scriptStage(ctx, dir, pack, brief, spec)
	scriptRec := StageRecord{
		Stage:      VideoStageScript,
		Status:     StatusOK,
		Files:      stageFiles(dir, "script.json", "script.md"),
		Provider:   script.Provider,
		DurationMS: elapsedMS(started),
		CostUSD:    script.CostUSD,
	}
	if err != nil {
		return p.fail(&art, scriptRec, err)
	}
	p.complete(&art, scriptRec)
	art.Title = script.Title
	art.ScriptPath = joinArtifact(dir, "script.md")

	// storyboard: timed shots.
	started = time.Now()
	storyboard, err := p.storyboardStage(ctx, dir, pack, brief, spec, script)
	storyboardRec := StageRecord{
		Stage:      VideoStageStoryboard,
		Status:     StatusOK,
		Files:      stageFiles(dir, "storyboard.json"),
		Provider:   storyboard.Provider,
		DurationMS: elapsedMS(started),
		CostUSD:    storyboard.CostUSD,
	}
	if err != nil {
		return p.fail(&art, storyboardRec, err)
	}
	p.complete(&art, storyboardRec)
	art.StoryboardPath = joinArtifact(dir, "storyboard.json")

	// tts: the narration audio, whose real length drives the rest of the run.
	started = time.Now()
	tts, err := p.ttsStage(ctx, dir, pack, brief, spec, script, storyboard)
	ttsRec := StageRecord{
		Stage:      VideoStageTTS,
		Status:     StatusOK,
		Files:      stageFilesFor(dir, tts.AudioPath),
		Provider:   tts.Provider,
		DurationMS: elapsedMS(started),
		CostUSD:    tts.CostUSD,
	}
	if err != nil {
		return p.fail(&art, ttsRec, err)
	}
	p.complete(&art, ttsRec)
	art.AudioPath = tts.AudioPath
	art.DurationSec = tts.DurationSec

	// frames: cover plus one still per shot.
	started = time.Now()
	frames, err := p.framesStage(ctx, dir, packID, pack, spec, storyboard)
	framesRec := StageRecord{
		Stage:      VideoStageFrames,
		Status:     StatusOK,
		Files:      frameFiles(dir, frames.Frames),
		Provider:   frames.Provider,
		DurationMS: elapsedMS(started),
		CostUSD:    frames.CostUSD,
	}
	if err != nil {
		return p.fail(&art, framesRec, err)
	}
	p.complete(&art, framesRec)
	art.Frames = frameFiles(dir, frames.Frames)

	// subtitles: timed to the narration audio.
	started = time.Now()
	subtitles, err := p.subtitleStage(ctx, dir, spec, storyboard, tts)
	subtitleRec := StageRecord{
		Stage:      VideoStageSubtitles,
		Status:     StatusOK,
		Files:      stageFilesFor(dir, subtitles.Path),
		Provider:   subtitles.Provider,
		DurationMS: elapsedMS(started),
		CostUSD:    subtitles.CostUSD,
	}
	if err != nil {
		return p.fail(&art, subtitleRec, err)
	}
	p.complete(&art, subtitleRec)
	art.Subtitles = subtitles.Path

	// compose: frames + audio + subtitles into the final cut.
	started = time.Now()
	composed, err := p.composeStage(ctx, dir, packID, spec, storyboard, frames, tts, subtitles)
	composeRec := StageRecord{
		Stage:      VideoStageCompose,
		Status:     StatusOK,
		Files:      stageFilesFor(dir, composed.FinalPath, composed.CoverPath),
		Provider:   composed.Provider,
		DurationMS: elapsedMS(started),
		CostUSD:    composeCost(composed),
	}
	if err != nil {
		return p.fail(&art, composeRec, err)
	}
	p.complete(&art, composeRec)

	// adapt: enforce the platform gate before the cut can reach review.
	started = time.Now()
	composed, err = p.adaptStage(ctx, spec, frames, subtitles, composed)
	adaptRec := StageRecord{
		Stage:      VideoStageAdapt,
		Status:     StatusOK,
		Provider:   "builtin",
		DurationMS: elapsedMS(started),
	}
	if err != nil {
		return p.fail(&art, adaptRec, err)
	}
	p.complete(&art, adaptRec)

	art.FinalPath = composed.FinalPath
	art.CoverPath = composed.CoverPath
	art.DurationSec = composed.DurationSec
	art.Ratio = spec.Ratio
	art.Width = spec.Width
	art.Height = spec.Height
	art.BlackFrames = composed.BlackFrames

	if err := writeVideoArtifactManifest(dir, art); err != nil {
		return art, fmt.Errorf("video pipeline: write artifacts manifest: %w", err)
	}
	if err := p.recordCost(ctx, art); err != nil {
		return art, err
	}
	return art, nil
}

func (p *VideoPipeline) packs() PackResolver {
	if p.Packs != nil {
		return p.Packs
	}
	return BuiltinPacks{}
}

func (p *VideoPipeline) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// allocate uses the configured store, or a temp directory when the run has
// nowhere versioned to write (preview and tests).
func (p *VideoPipeline) allocate(ctx context.Context, issueID int64) (string, int, error) {
	if p.Store != nil {
		return p.Store.AllocateVersion(ctx, issueID)
	}
	dir, err := os.MkdirTemp("", "ops-video-*")
	if err != nil {
		return "", 0, err
	}
	return dir, 0, nil
}

func (p *VideoPipeline) complete(art *VideoArtifact, rec StageRecord) {
	art.Stages = append(art.Stages, rec)
	art.TotalDurationMS += rec.DurationMS
	art.TotalCostUSD += rec.CostUSD
}

// fail records the failed stage (so artifacts.json still shows how far the run
// got) and returns the stage error without running anything else.
func (p *VideoPipeline) fail(art *VideoArtifact, rec StageRecord, cause error) (VideoArtifact, error) {
	rec.Status = StatusFailed
	rec.Error = cause.Error()
	art.Stages = append(art.Stages, rec)
	art.TotalDurationMS += rec.DurationMS
	art.TotalCostUSD += rec.CostUSD
	if art.Dir != "" {
		_ = writeVideoArtifactManifest(art.Dir, *art)
	}
	return *art, &StageError{Stage: rec.Stage, Err: cause}
}

func (p *VideoPipeline) scriptStage(ctx context.Context, dir string, pack Pack, brief OpsContentBrief, spec VideoSpec) (VideoScriptResult, error) {
	if err := ctx.Err(); err != nil {
		return VideoScriptResult{}, err
	}
	if p.Script == nil {
		return VideoScriptResult{}, errors.New("video script writer is not configured")
	}
	prompt, err := promptText(pack.Files, "video-script.md")
	if err != nil {
		return VideoScriptResult{}, err
	}
	result, err := p.Script.WriteScript(ctx, VideoScriptRequest{
		PackID: brief.Channel,
		Brief:  brief,
		Topic:  buildTopicPlan(brief),
		Prompt: prompt,
		Spec:   spec,
	})
	if err != nil {
		return VideoScriptResult{}, err
	}
	if strings.TrimSpace(result.Title) == "" {
		return VideoScriptResult{}, errors.New("video script title is empty")
	}
	if strings.TrimSpace(result.Narration) == "" {
		return VideoScriptResult{}, errors.New("video script narration is empty")
	}
	if result.Provider == "" {
		result.Provider = "unknown"
	}
	if dir == "" {
		return result, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return VideoScriptResult{}, err
	}
	if err := writeJSONFile(filepath.Join(dir, "script.json"), result); err != nil {
		return VideoScriptResult{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "script.md"), []byte(renderVideoScript(result)), 0o644); err != nil {
		return VideoScriptResult{}, err
	}
	return result, nil
}

func (p *VideoPipeline) storyboardStage(ctx context.Context, dir string, pack Pack, brief OpsContentBrief, spec VideoSpec, script VideoScriptResult) (VideoStoryboard, error) {
	if err := ctx.Err(); err != nil {
		return VideoStoryboard{}, err
	}
	if p.Storyboard == nil {
		return VideoStoryboard{}, errors.New("storyboard planner is not configured")
	}
	prompt, err := promptText(pack.Files, "video-storyboard.md")
	if err != nil {
		return VideoStoryboard{}, err
	}
	result, err := p.Storyboard.PlanStoryboard(ctx, StoryboardRequest{
		PackID: brief.Channel,
		Brief:  brief,
		Topic:  buildTopicPlan(brief),
		Script: script,
		Prompt: prompt,
		Spec:   spec,
	})
	if err != nil {
		return VideoStoryboard{}, err
	}
	if err := validateStoryboard(&result, spec); err != nil {
		return result, err
	}
	if result.Provider == "" {
		result.Provider = "unknown"
	}
	if dir == "" {
		return result, nil
	}
	if err := writeJSONFile(filepath.Join(dir, "storyboard.json"), result); err != nil {
		return VideoStoryboard{}, err
	}
	return result, nil
}

func (p *VideoPipeline) ttsStage(ctx context.Context, dir string, pack Pack, brief OpsContentBrief, spec VideoSpec, script VideoScriptResult, storyboard VideoStoryboard) (TTSResult, error) {
	if err := ctx.Err(); err != nil {
		return TTSResult{}, err
	}
	if p.TTS == nil {
		return TTSResult{}, errors.New("tts provider is not configured")
	}
	outDir := filepath.Join(dir, "audio")
	if dir == "" {
		outDir = ""
	}
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return TTSResult{}, err
		}
	}
	result, err := p.TTS.Synthesize(ctx, TTSRequest{
		PackID: brief.Channel,
		Text:   script.Narration,
		Voice:  p.Voice,
		OutDir: outDir,
		Spec:   spec,
		Shots:  storyboard.Shots,
	})
	if err != nil {
		return TTSResult{}, err
	}
	if strings.TrimSpace(result.AudioPath) == "" {
		return TTSResult{}, errors.New("tts provider returned no audio path")
	}
	if result.DurationSec <= 0 {
		return TTSResult{}, errors.New("tts provider returned no duration")
	}
	if _, err := os.Stat(result.AudioPath); err != nil {
		return TTSResult{}, fmt.Errorf("tts audio %s: %w", result.AudioPath, err)
	}
	if result.Provider == "" {
		result.Provider = "unknown"
	}
	return result, nil
}

func (p *VideoPipeline) framesStage(ctx context.Context, dir, packID string, pack Pack, spec VideoSpec, storyboard VideoStoryboard) (FramesResult, error) {
	if err := ctx.Err(); err != nil {
		return FramesResult{}, err
	}
	if p.Frames == nil {
		return FramesResult{}, errors.New("frame generator is not configured")
	}
	if dir == "" {
		return FramesResult{}, ErrArtifactStoreRequired
	}
	prompt, err := promptText(pack.Files, "video-frames.md")
	if err != nil {
		return FramesResult{}, err
	}
	if prompt == "" {
		prompt, err = promptText(pack.Files, coverPromptName(FormatVideo))
		if err != nil {
			return FramesResult{}, err
		}
	}
	outDir := filepath.Join(dir, "frames")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return FramesResult{}, err
	}
	result, err := p.Frames.RenderFrames(ctx, FramesRequest{
		PackID: packID,
		Prompt: prompt,
		Spec:   spec,
		Shots:  storyboard.Shots,
		OutDir: outDir,
	})
	if err != nil {
		return FramesResult{}, err
	}
	if err := validateFrames(result.Frames, len(storyboard.Shots)); err != nil {
		return result, err
	}
	if result.Provider == "" {
		result.Provider = "unknown"
	}
	return result, nil
}

func (p *VideoPipeline) subtitleStage(ctx context.Context, dir string, spec VideoSpec, storyboard VideoStoryboard, tts TTSResult) (SubtitleResult, error) {
	if err := ctx.Err(); err != nil {
		return SubtitleResult{}, err
	}
	if p.Subtitles == nil {
		return SubtitleResult{}, errors.New("subtitle writer is not configured")
	}
	result, err := p.Subtitles.WriteSubtitles(ctx, SubtitleRequest{
		Format:           spec.SubtitleFormat,
		Shots:            storyboard.Shots,
		AudioDurationSec: tts.DurationSec,
		Spec:             spec,
		OutDir:           dir,
	})
	if err != nil {
		return SubtitleResult{}, err
	}
	if strings.TrimSpace(result.Path) == "" {
		return SubtitleResult{}, errors.New("subtitle writer returned no path")
	}
	if result.Format == "" {
		result.Format = spec.SubtitleFormat
	}
	if result.Provider == "" {
		result.Provider = "unknown"
	}
	return result, nil
}

func (p *VideoPipeline) composeStage(ctx context.Context, dir, packID string, spec VideoSpec, storyboard VideoStoryboard, frames FramesResult, tts TTSResult, subtitles SubtitleResult) (ComposeResult, error) {
	if err := ctx.Err(); err != nil {
		return ComposeResult{}, err
	}
	if p.Compose == nil {
		return ComposeResult{}, errors.New("video composer is not configured")
	}
	result, err := p.Compose.Compose(ctx, ComposeRequest{
		PackID:       packID,
		Spec:         spec,
		Shots:        storyboard.Shots,
		Frames:       frames.Frames,
		AudioPath:    tts.AudioPath,
		SubtitlePath: subtitles.Path,
		CoverPath:    coverFramePath(frames.Frames),
		DurationSec:  tts.DurationSec,
		OutDir:       dir,
	})
	if err != nil {
		return ComposeResult{}, err
	}
	if strings.TrimSpace(result.FinalPath) == "" {
		return ComposeResult{}, errors.New("video composer returned no final path")
	}
	if result.Provider == "" {
		result.Provider = "unknown"
	}
	return result, nil
}

// adaptStage enforces the pack's platform fit before the cut can reach review.
// It refuses a cut that is missing subtitles or a cover, over the duration cap,
// off-ratio, or carrying black frames.
func (p *VideoPipeline) adaptStage(ctx context.Context, spec VideoSpec, frames FramesResult, subtitles SubtitleResult, composed ComposeResult) (ComposeResult, error) {
	if err := ctx.Err(); err != nil {
		return composed, err
	}
	if composed.DurationSec <= 0 {
		return composed, videoGateError("final cut duration is unknown")
	}
	if strings.TrimSpace(composed.CoverPath) == "" {
		composed.CoverPath = coverFramePath(frames.Frames)
	}
	if strings.TrimSpace(composed.Ratio) == "" {
		if composed.Width > 0 && composed.Height > 0 {
			composed.Ratio = fmt.Sprintf("%dx%d", composed.Width, composed.Height)
		}
	}
	if !fileNonEmpty(composed.FinalPath) {
		return composed, videoGateError("final cut %q is missing", composed.FinalPath)
	}
	if !fileNonEmpty(composed.CoverPath) {
		return composed, videoGateError("cover is missing")
	}
	if !fileNonEmpty(subtitles.Path) {
		return composed, videoGateError("subtitles %q are missing", subtitles.Path)
	}
	if spec.MaxDurationSec > 0 && composed.DurationSec > spec.MaxDurationSec {
		return composed, videoGateError("duration %ds exceeds platform limit %ds", composed.DurationSec, spec.MaxDurationSec)
	}
	if spec.Ratio != "" {
		actual := strings.TrimSpace(composed.Ratio)
		if actual == "" {
			return composed, videoGateError("final cut ratio is unknown; platform requires %s", spec.Ratio)
		}
		if normalizeRatioKey(actual) != normalizeRatioKey(spec.Ratio) {
			return composed, videoGateError("final cut ratio %s does not match platform %s", actual, spec.Ratio)
		}
	}
	if composed.BlackFrames > 0 {
		return composed, videoGateError("%d black frames detected", composed.BlackFrames)
	}
	return composed, nil
}

// recordCost writes the run's cost as seconds of finished video, the unit the
// ops budget aggregates video spend by.
func (p *VideoPipeline) recordCost(ctx context.Context, art VideoArtifact) error {
	if p.Recorder == nil {
		return nil
	}
	detail, err := json.Marshal(struct {
		Category    string  `json:"category"`
		Unit        string  `json:"unit"`
		Quantity    float64 `json:"quantity"`
		DurationSec int     `json:"duration_sec"`
	}{
		Category:    CostCategoryVideo,
		Unit:        CostUnitSecond,
		Quantity:    float64(art.DurationSec),
		DurationSec: art.DurationSec,
	})
	if err != nil {
		return err
	}
	if err := p.Recorder(ctx, ExecEvent{
		IssueID:    art.IssueID,
		Kind:       IssueExecEventKindCost,
		Summary:    videoCostSummary,
		DetailJSON: string(detail),
		CostUSD:    art.TotalCostUSD,
	}); err != nil {
		return fmt.Errorf("video pipeline: record cost: %w", err)
	}
	return nil
}

// videoSpecFor reads the pack's video format, falling back to the platform's
// published limits for packs whose schema only declares their primary format.
func videoSpecFor(packID string, schema packs.ContentSchema) (VideoSpec, error) {
	if f, ok := schema.Formats[FormatVideo]; ok {
		ratio := strings.TrimSpace(f.ImageRatio)
		width, height := videoDimensions(ratio)
		return VideoSpec{
			Platform:       packID,
			Ratio:          ratio,
			Width:          width,
			Height:         height,
			MaxDurationSec: f.VideoSec,
			SubtitleFormat: "srt",
		}, nil
	}
	switch packID {
	case "xiaohongshu":
		return newVideoSpec(packID, "3:4", 15*60), nil
	case "wechat_mp":
		return newVideoSpec(packID, "16:9", 30*60), nil
	default:
		return VideoSpec{}, fmt.Errorf("%w: pack %s has no video format", ErrInvalidPipelineInput, packID)
	}
}

func newVideoSpec(packID, ratio string, maxSec int) VideoSpec {
	width, height := videoDimensions(ratio)
	return VideoSpec{
		Platform:       packID,
		Ratio:          ratio,
		Width:          width,
		Height:         height,
		MaxDurationSec: maxSec,
		SubtitleFormat: "srt",
	}
}

// videoDimensions uses the delivery sizes the platforms publish, falling back
// to the generic 1080-wide ratio scaling for a custom ratio.
func videoDimensions(ratio string) (int, int) {
	switch normalizeRatioKey(ratio) {
	case "3:4":
		return 1080, 1440
	case "9:16":
		return 1080, 1920
	case "16:9":
		return 1920, 1080
	case "1:1":
		return 1080, 1080
	}
	return imageDimensions(ratio)
}

func validateStoryboard(sb *VideoStoryboard, spec VideoSpec) error {
	if sb == nil || len(sb.Shots) == 0 {
		return errors.New("storyboard has no shots")
	}
	var prevEnd float64
	for i := range sb.Shots {
		shot := &sb.Shots[i]
		shot.Index = i
		if shot.EndSec <= shot.StartSec {
			return fmt.Errorf("storyboard shot %d ends at %.2fs, not after its start", i, shot.EndSec)
		}
		if shot.StartSec < 0 {
			return fmt.Errorf("storyboard shot %d starts before zero", i)
		}
		if shot.StartSec+0.001 < prevEnd {
			return fmt.Errorf("storyboard shot %d overlaps the previous shot", i)
		}
		if strings.TrimSpace(shot.Visual) == "" {
			return fmt.Errorf("storyboard shot %d has no visual", i)
		}
		if strings.TrimSpace(shot.Narration) == "" {
			return fmt.Errorf("storyboard shot %d has no narration", i)
		}
		prevEnd = shot.EndSec
	}
	if spec.MaxDurationSec > 0 && prevEnd > float64(spec.MaxDurationSec) {
		return fmt.Errorf("storyboard is %.0fs, platform allows %ds", prevEnd, spec.MaxDurationSec)
	}
	return nil
}

// validateFrames requires one still per shot plus a cover, each on disk.
func validateFrames(frames []VideoFrame, shots int) error {
	byShot := make(map[int]bool, shots)
	for _, frame := range frames {
		if strings.TrimSpace(frame.Path) == "" {
			return errors.New("frame generator returned a frame with no path")
		}
		if _, err := os.Stat(frame.Path); err != nil {
			return fmt.Errorf("frame %s: %w", frame.Path, err)
		}
		if frame.Purpose == ImagePurposeCover {
			continue
		}
		if frame.Index < 0 || frame.Index >= shots {
			return fmt.Errorf("frame index %d has no matching storyboard shot", frame.Index)
		}
		if byShot[frame.Index] {
			return fmt.Errorf("frame index %d was generated twice", frame.Index)
		}
		byShot[frame.Index] = true
	}
	for i := 0; i < shots; i++ {
		if !byShot[i] {
			return fmt.Errorf("shot %d has no frame", i)
		}
	}
	return nil
}

func coverFramePath(frames []VideoFrame) string {
	for _, frame := range frames {
		if frame.Purpose == ImagePurposeCover {
			return frame.Path
		}
	}
	return ""
}

func composeCost(result ComposeResult) float64 {
	if result.CostUSD > 0 {
		return result.CostUSD
	}
	if result.CostPerSec > 0 && result.DurationSec > 0 {
		return result.CostPerSec * float64(result.DurationSec)
	}
	return 0
}

func videoGateError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrVideoQualityGate, fmt.Sprintf(format, args...))
}

// fileNonEmpty reports whether path names a readable, non-empty file.
func fileNonEmpty(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func renderVideoScript(s VideoScriptResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", s.Title)
	if s.Hook != "" {
		fmt.Fprintf(&b, "**钩子**：%s\n\n", s.Hook)
	}
	b.WriteString("## 口播\n\n")
	b.WriteString(s.Narration)
	b.WriteString("\n")
	if s.CTA != "" {
		fmt.Fprintf(&b, "\n**CTA**：%s\n", s.CTA)
	}
	return b.String()
}

func stageFiles(dir string, names ...string) []string {
	var out []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || name == "." {
			continue
		}
		if fileNonEmpty(filepath.Join(dir, name)) {
			out = append(out, filepath.ToSlash(name))
		}
	}
	return out
}

func stageFilesFor(dir string, paths ...string) []string {
	var out []string
	for _, p := range paths {
		rel := relFile(dir, p)
		if rel != "" && fileNonEmpty(p) {
			out = append(out, rel)
		}
	}
	return out
}

func relFile(dir, path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

func frameFiles(dir string, frames []VideoFrame) []string {
	var out []string
	for _, frame := range frames {
		rel, err := filepath.Rel(dir, frame.Path)
		if err == nil && !strings.HasPrefix(rel, "..") {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

func joinArtifact(dir, name string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

func writeVideoArtifactManifest(dir string, art VideoArtifact) error {
	if dir == "" {
		return ErrArtifactStoreRequired
	}
	return writeJSONFile(filepath.Join(dir, "artifacts.json"), art)
}

func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ratioParts parses "3:4" or "1080x1440" into its two sides.
func ratioParts(s string) (int, int, bool) {
	s = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "×", "x")
	i := strings.IndexAny(s, ":x")
	if i < 0 {
		return 0, 0, false
	}
	width, errW := strconv.Atoi(strings.TrimSpace(s[:i]))
	height, errH := strconv.Atoi(strings.TrimSpace(s[i+1:]))
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// normalizeRatioKey rewrites a ratio or pixel size into a reduced a:b key so
// "1080x1440" and "3:4" compare equal.
func normalizeRatioKey(s string) string {
	width, height, ok := ratioParts(s)
	if !ok {
		return strings.TrimSpace(s)
	}
	g := gcd(width, height)
	return fmt.Sprintf("%d:%d", width/g, height/g)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
