package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeScript struct {
	calls int
}

func (f *fakeScript) WriteScript(ctx context.Context, req VideoScriptRequest) (VideoScriptResult, error) {
	f.calls++
	return VideoScriptResult{
		Title:     "护肤三步",
		Hook:      "先别换整套护肤品",
		Narration: "第一步清洁，第二步补水，第三步防晒。",
		CTA:       "你换季最头疼的是什么？",
		Provider:  "fake-script",
		CostUSD:   0.01,
	}, nil
}

type fakeStoryboard struct {
	calls int
	shots []VideoShot
}

func (f *fakeStoryboard) PlanStoryboard(ctx context.Context, req StoryboardRequest) (VideoStoryboard, error) {
	f.calls++
	shots := f.shots
	if len(shots) == 0 {
		shots = []VideoShot{
			{StartSec: 0, EndSec: 3, Visual: "hook", Narration: "先别换整套护肤品"},
			{StartSec: 3, EndSec: 15, Visual: "steps", Narration: "三步护理法"},
		}
	}
	return VideoStoryboard{Shots: append([]VideoShot(nil), shots...), Provider: "fake-storyboard", CostUSD: 0.01}, nil
}

type fakeTTS struct {
	calls       int
	durationSec int
}

func (f *fakeTTS) Synthesize(ctx context.Context, req TTSRequest) (TTSResult, error) {
	f.calls++
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return TTSResult{}, err
	}
	path := filepath.Join(req.OutDir, "voice.mp3")
	if err := os.WriteFile(path, []byte("mp3"), 0o644); err != nil {
		return TTSResult{}, err
	}
	duration := f.durationSec
	if duration == 0 {
		duration = 15
	}
	return TTSResult{AudioPath: path, DurationSec: duration, Provider: "fake-tts", CostUSD: 0.02}, nil
}

type fakeFrames struct {
	calls   int
	noCover bool
}

func (f *fakeFrames) RenderFrames(ctx context.Context, req FramesRequest) (FramesResult, error) {
	f.calls++
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return FramesResult{}, err
	}
	var frames []VideoFrame
	if !f.noCover {
		path := filepath.Join(req.OutDir, "cover.png")
		if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
			return FramesResult{}, err
		}
		frames = append(frames, VideoFrame{
			Purpose: ImagePurposeCover,
			Path:    path,
			Ratio:   req.Spec.Ratio,
			Width:   req.Spec.Width,
			Height:  req.Spec.Height,
		})
	}
	for _, shot := range req.Shots {
		path := filepath.Join(req.OutDir, fmt.Sprintf("shot-%d.png", shot.Index))
		if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
			return FramesResult{}, err
		}
		frames = append(frames, VideoFrame{
			Index:   shot.Index,
			Purpose: VideoFramePurposeShot,
			Path:    path,
			Ratio:   req.Spec.Ratio,
			Width:   req.Spec.Width,
			Height:  req.Spec.Height,
		})
	}
	return FramesResult{Frames: frames, Provider: "fake-frames", CostUSD: 0.03}, nil
}

type fakeSubtitles struct {
	calls     int
	emptyPath bool
	noFile    bool
}

func (f *fakeSubtitles) WriteSubtitles(ctx context.Context, req SubtitleRequest) (SubtitleResult, error) {
	f.calls++
	if f.emptyPath {
		return SubtitleResult{Provider: "fake-subtitles"}, nil
	}
	path := filepath.Join(req.OutDir, "subtitles.srt")
	if !f.noFile {
		srt := "1\n00:00:00,000 --> 00:00:03,000\n先别换整套护肤品\n\n2\n00:00:03,000 --> 00:00:15,000\n三步护理法\n"
		if err := os.WriteFile(path, []byte(srt), 0o644); err != nil {
			return SubtitleResult{}, err
		}
	}
	return SubtitleResult{Path: path, Format: "srt", Provider: "fake-subtitles", CostUSD: 0.005}, nil
}

type fakeCompose struct {
	calls       int
	err         error
	durationSec int
	width       int
	height      int
	ratio       string
	blackFrames int
	lastSpec    VideoSpec
}

func (f *fakeCompose) Compose(ctx context.Context, req ComposeRequest) (ComposeResult, error) {
	f.calls++
	f.lastSpec = req.Spec
	if f.err != nil {
		return ComposeResult{}, f.err
	}
	duration := req.DurationSec
	if f.durationSec > 0 {
		duration = f.durationSec
	}
	width, height, ratio := req.Spec.Width, req.Spec.Height, req.Spec.Ratio
	if f.width > 0 && f.height > 0 {
		width, height = f.width, f.height
	}
	if f.ratio != "" {
		ratio = f.ratio
	}
	final := filepath.Join(req.OutDir, "final.mp4")
	if err := os.WriteFile(final, []byte("mp4"), 0o644); err != nil {
		return ComposeResult{}, err
	}
	return ComposeResult{
		FinalPath:   final,
		CoverPath:   req.CoverPath,
		DurationSec: duration,
		Width:       width,
		Height:      height,
		Ratio:       ratio,
		BlackFrames: f.blackFrames,
		Provider:    "fake-compose",
		CostUSD:     0.1,
	}, nil
}

func newVideoPipeline(t *testing.T) (*VideoPipeline, *fakeCompose) {
	t.Helper()
	composer := &fakeCompose{}
	return &VideoPipeline{
		Script:     &fakeScript{},
		Storyboard: &fakeStoryboard{},
		TTS:        &fakeTTS{},
		Frames:     &fakeFrames{},
		Subtitles:  &fakeSubtitles{},
		Compose:    composer,
		Store:      &fakeArtifactStore{dir: t.TempDir()},
	}, composer
}

func TestVideoPipelineProducesFinalCut(t *testing.T) {
	composer := &fakeCompose{}
	v := &VideoPipeline{
		Script:     &fakeScript{},
		Storyboard: &fakeStoryboard{},
		TTS:        &fakeTTS{},
		Frames:     &fakeFrames{},
		Subtitles:  &fakeSubtitles{},
		Compose:    composer,
	}

	out, err := v.Run(context.Background(), BotRef{ID: 1, PackID: "xiaohongshu"},
		OpsContentBrief{Topic: "护肤三步", Format: "video"}, 200)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(out.Dir) })

	require.FileExists(t, out.FinalPath)
	require.FileExists(t, out.CoverPath)
	require.NotEmpty(t, out.Subtitles)
	require.FileExists(t, out.Subtitles)
	require.Equal(t, FormatVideo, out.Format)
	require.Equal(t, "3:4", out.Ratio)
	require.Equal(t, 1080, out.Width)
	require.Equal(t, 1440, out.Height)
	require.True(t, out.DurationSec <= 15*60)
	require.Equal(t, 0, out.BlackFrames)
	require.NotEmpty(t, out.Title)
	require.Len(t, out.Stages, 7)
	for _, stage := range out.Stages {
		require.Equal(t, StatusOK, stage.Status, "stage %s", stage.Stage)
	}

	data, err := os.ReadFile(filepath.Join(out.Dir, "artifacts.json"))
	require.NoError(t, err)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	for _, key := range []string{"duration_sec", "ratio", "subtitle_path", "cover_path", "final_path"} {
		require.Contains(t, raw, key)
	}
}

func TestVideoPipelineAdaptsToPlatformSpec(t *testing.T) {
	cases := []struct {
		name    string
		packID  string
		ratio   string
		width   int
		height  int
		maxSecs int
	}{
		{name: "xiaohongshu 3:4 15min", packID: "xiaohongshu", ratio: "3:4", width: 1080, height: 1440, maxSecs: 900},
		{name: "wechat 16:9 30min", packID: "wechat_mp", ratio: "16:9", width: 1920, height: 1080, maxSecs: 1800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, composer := newVideoPipeline(t)
			out, err := v.Run(context.Background(), BotRef{ID: 1, PackID: tc.packID},
				OpsContentBrief{Topic: "护肤三步", Format: "video"}, 201)
			require.NoError(t, err)
			require.Equal(t, tc.ratio, composer.lastSpec.Ratio)
			require.Equal(t, tc.width, composer.lastSpec.Width)
			require.Equal(t, tc.height, composer.lastSpec.Height)
			require.Equal(t, tc.maxSecs, composer.lastSpec.MaxDurationSec)
			require.Equal(t, tc.ratio, out.Ratio)
			require.True(t, out.DurationSec <= tc.maxSecs)
		})
	}
}

func TestVideoPipelineQualityGates(t *testing.T) {
	cases := []struct {
		name      string
		tune      func(v *VideoPipeline, composer *fakeCompose)
		wantStage string
		wantGate  bool
	}{
		{
			name: "black frames",
			tune: func(v *VideoPipeline, composer *fakeCompose) {
				composer.blackFrames = 2
			},
			wantStage: VideoStageAdapt,
			wantGate:  true,
		},
		{
			name: "over duration",
			tune: func(v *VideoPipeline, composer *fakeCompose) {
				composer.durationSec = 901
			},
			wantStage: VideoStageAdapt,
			wantGate:  true,
		},
		{
			name: "ratio mismatch",
			tune: func(v *VideoPipeline, composer *fakeCompose) {
				composer.ratio = "1:1"
				composer.width, composer.height = 1080, 1080
			},
			wantStage: VideoStageAdapt,
			wantGate:  true,
		},
		{
			name: "missing cover",
			tune: func(v *VideoPipeline, composer *fakeCompose) {
				v.Frames = &fakeFrames{noCover: true}
			},
			wantStage: VideoStageAdapt,
			wantGate:  true,
		},
		{
			name: "missing subtitle file",
			tune: func(v *VideoPipeline, composer *fakeCompose) {
				v.Subtitles = &fakeSubtitles{noFile: true}
			},
			wantStage: VideoStageAdapt,
			wantGate:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, composer := newVideoPipeline(t)
			tc.tune(v, composer)
			out, err := v.Run(context.Background(), BotRef{ID: 1, PackID: "xiaohongshu"},
				OpsContentBrief{Topic: "护肤三步", Format: "video"}, 202)
			require.Error(t, err)
			require.True(t, tc.wantGate)
			require.ErrorIs(t, err, ErrVideoQualityGate)
			var stageErr *StageError
			require.ErrorAs(t, err, &stageErr)
			require.Equal(t, tc.wantStage, stageErr.Stage)
			require.Empty(t, out.FinalPath)
		})
	}
}

func TestVideoPipelineRecordsCostBySeconds(t *testing.T) {
	v, _ := newVideoPipeline(t)
	var events []ExecEvent
	v.Recorder = func(ctx context.Context, ev ExecEvent) error {
		events = append(events, ev)
		return nil
	}

	out, err := v.Run(context.Background(), BotRef{ID: 7, PackID: "xiaohongshu"},
		OpsContentBrief{Topic: "护肤三步", Format: "video"}, 203)
	require.NoError(t, err)
	require.Len(t, events, 1)

	ev := events[0]
	require.Equal(t, int64(203), ev.IssueID)
	require.Equal(t, IssueExecEventKindCost, ev.Kind)
	require.Equal(t, "video", ev.Summary)
	require.Greater(t, ev.CostUSD, 0.0)
	require.InDelta(t, out.TotalCostUSD, ev.CostUSD, 1e-9)

	var detail struct {
		Category    string  `json:"category"`
		Unit        string  `json:"unit"`
		Quantity    float64 `json:"quantity"`
		DurationSec int     `json:"duration_sec"`
	}
	require.NoError(t, json.Unmarshal([]byte(ev.DetailJSON), &detail))
	require.Equal(t, CostCategoryVideo, detail.Category)
	require.Equal(t, CostUnitSecond, detail.Unit)
	require.Equal(t, out.DurationSec, detail.DurationSec)
	require.InDelta(t, float64(out.DurationSec), detail.Quantity, 1e-9)
}

func TestVideoPipelineStopsAtFailedStage(t *testing.T) {
	v, composer := newVideoPipeline(t)
	composer.err = errors.New("ffmpeg unavailable")

	out, err := v.Run(context.Background(), BotRef{ID: 1, PackID: "xiaohongshu"},
		OpsContentBrief{Topic: "护肤三步", Format: "video"}, 204)
	require.Error(t, err)

	var stageErr *StageError
	require.ErrorAs(t, err, &stageErr)
	require.Equal(t, VideoStageCompose, stageErr.Stage)
	require.Empty(t, out.FinalPath)
	require.Len(t, out.Stages, 6)
	require.Equal(t, VideoStageCompose, out.Stages[len(out.Stages)-1].Stage)
	require.Equal(t, StatusFailed, out.Stages[len(out.Stages)-1].Status)
}

func TestVideoPipelineRejectsStoryboardOverPlatformLimit(t *testing.T) {
	v, _ := newVideoPipeline(t)
	v.Storyboard = &fakeStoryboard{shots: []VideoShot{
		{StartSec: 0, EndSec: 3, Visual: "hook", Narration: "钩子"},
		{StartSec: 3, EndSec: 1000, Visual: "long", Narration: "太长"},
	}}

	_, err := v.Run(context.Background(), BotRef{ID: 1, PackID: "xiaohongshu"},
		OpsContentBrief{Topic: "护肤三步", Format: "video"}, 205)
	require.Error(t, err)

	var stageErr *StageError
	require.ErrorAs(t, err, &stageErr)
	require.Equal(t, VideoStageStoryboard, stageErr.Stage)
}

func TestVideoPipelineRejectsNonVideoFormat(t *testing.T) {
	v, _ := newVideoPipeline(t)
	_, err := v.Run(context.Background(), BotRef{ID: 1, PackID: "xiaohongshu"},
		OpsContentBrief{Topic: "护肤", Format: FormatNote}, 206)
	require.ErrorIs(t, err, ErrInvalidPipelineInput)
}

func TestVideoRatioNormalization(t *testing.T) {
	require.Equal(t, "3:4", normalizeRatioKey("3:4"))
	require.Equal(t, "3:4", normalizeRatioKey("1080x1440"))
	require.Equal(t, "16:9", normalizeRatioKey("1920×1080"))
	require.NotEqual(t, normalizeRatioKey("9:16"), normalizeRatioKey("3:4"))
}
