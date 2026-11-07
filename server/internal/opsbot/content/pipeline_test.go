package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeCopy struct{ calls int }

func (f *fakeCopy) Write(ctx context.Context, req CopyRequest) (CopyResult, error) {
	f.calls++
	title := "秋季护理的三个关键步骤"
	if req.Format == FormatNote {
		title = "秋季护肤别乱买"
	}
	return CopyResult{
		Title:    title,
		Digest:   "换季只是换掉不合时宜的习惯",
		Hook:     "换季先别换整套护肤品",
		Body:     "第一段讲清换季为什么容易出问题。\n\n第二段给出三条今天就能做的行动建议。",
		CTA:      "你换季最头疼的是什么？",
		Tags:     []string{"秋季护肤", "换季", "护肤"},
		Sources:  []string{"个人经验"},
		Provider: "fake-copy",
		CostUSD:  0.12,
	}, nil
}

type failingCopy struct{}

func (failingCopy) Write(ctx context.Context, req CopyRequest) (CopyResult, error) {
	return CopyResult{}, errors.New("model unavailable")
}

type longTitleCopy struct{}

func (longTitleCopy) Write(ctx context.Context, req CopyRequest) (CopyResult, error) {
	return CopyResult{
		Title:    strings.Repeat("长", 30),
		Body:     "正文",
		Provider: "fake-copy",
	}, nil
}

type fakeImages struct{ calls int }

func (f *fakeImages) Generate(ctx context.Context, req ImageRequest) (ImageResult, error) {
	f.calls++
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		return ImageResult{}, err
	}
	out := make([]ImageAsset, 0, len(req.Specs))
	for _, spec := range req.Specs {
		name := fmt.Sprintf("%s-%d.png", spec.Purpose, spec.Index)
		path := filepath.Join(req.OutDir, name)
		if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
			return ImageResult{}, err
		}
		out = append(out, ImageAsset{
			Path:    path,
			Purpose: spec.Purpose,
			Ratio:   spec.Ratio,
			Width:   spec.Width,
			Height:  spec.Height,
		})
	}
	return ImageResult{Images: out, Provider: "fake-images", CostUSD: 0.03}, nil
}

type failingImages struct{}

func (failingImages) Generate(ctx context.Context, req ImageRequest) (ImageResult, error) {
	return ImageResult{}, errors.New("image provider down")
}

type fakeLayout struct{ calls int }

func (f *fakeLayout) Render(ctx context.Context, req LayoutRequest) (LayoutResult, error) {
	f.calls++
	path := filepath.Join(req.OutDir, "layout.out")
	if err := os.WriteFile(path, []byte("layout"), 0o644); err != nil {
		return LayoutResult{}, err
	}
	return LayoutResult{Path: path, Provider: "fake-layout"}, nil
}

type failingLayout struct{}

func (failingLayout) Render(ctx context.Context, req LayoutRequest) (LayoutResult, error) {
	return LayoutResult{}, errors.New("renderer crashed")
}

type fakeArtifactStore struct {
	dir      string
	versions int
}

func (s *fakeArtifactStore) AllocateVersion(ctx context.Context, issueID int64) (string, int, error) {
	s.versions++
	dir := filepath.Join(s.dir, fmt.Sprintf("v%d", s.versions))
	return dir, s.versions, os.MkdirAll(dir, 0o755)
}

func TestPipelineStagesAndArtifactManifest(t *testing.T) {
	store := &fakeArtifactStore{dir: t.TempDir()}
	copyGen := &fakeCopy{}
	images := &fakeImages{}
	layout := &fakeLayout{}
	p := &Pipeline{
		Copy:   copyGen,
		Images: images,
		Layout: layout,
		Store:  store,
	}

	art, err := p.Run(context.Background(), BotRef{ID: 1, PackID: "wechat_mp"},
		OpsContentBrief{Topic: "秋季护理", Format: "article"}, 100)
	require.NoError(t, err)
	require.NotEmpty(t, art.CopyPath)
	require.NotEmpty(t, art.Images)
	require.Equal(t, 1, art.Version)
	require.FileExists(t, filepath.Join(art.Dir, "artifacts.json"))
	require.Equal(t, 1, copyGen.calls)
	require.Equal(t, 1, images.calls)
	require.Equal(t, 1, layout.calls)

	require.Len(t, art.Stages, 4)
	require.Equal(t, []string{StageTopic, StageCopy, StageVisual, StageLayout}, []string{
		art.Stages[0].Stage, art.Stages[1].Stage, art.Stages[2].Stage, art.Stages[3].Stage,
	})
	require.Equal(t, "fake-copy", art.Stages[1].Provider)
	require.InDelta(t, 0.15, art.TotalCostUSD, 1e-9)
	require.FileExists(t, art.TopicPath)
	require.FileExists(t, filepath.Join(art.Dir, "copy.json"))
	require.FileExists(t, art.LayoutPath)

	data, err := os.ReadFile(filepath.Join(art.Dir, "artifacts.json"))
	require.NoError(t, err)
	var manifest Artifact
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.Equal(t, 100, int(manifest.IssueID))
	require.Equal(t, 1, manifest.Version)
	require.Equal(t, "wechat_mp", manifest.PackID)
	require.Len(t, manifest.Stages, 4)
	for _, stage := range manifest.Stages {
		require.Equal(t, StatusOK, stage.Status)
		require.NotEmpty(t, stage.Files, "stage %s has no files", stage.Stage)
	}
	require.Equal(t, "fake-images", manifest.Stages[2].Provider)
	require.InDelta(t, 0.15, manifest.TotalCostUSD, 1e-9)
}

func TestPipelineStopsOnStageFailure(t *testing.T) {
	p := &Pipeline{Copy: failingCopy{}}
	_, err := p.Run(context.Background(), BotRef{ID: 1, PackID: "wechat_mp"},
		OpsContentBrief{Format: "article"}, 100)
	require.ErrorContains(t, err, "copy stage")

	var stageErr *StageError
	require.ErrorAs(t, err, &stageErr)
	require.Equal(t, StageCopy, stageErr.Stage)
}

func TestPipelineStopsBeforeLayoutWhenVisualFails(t *testing.T) {
	layout := &fakeLayout{}
	store := &fakeArtifactStore{dir: t.TempDir()}
	p := &Pipeline{
		Copy:   &fakeCopy{},
		Images: failingImages{},
		Layout: layout,
		Store:  store,
	}

	art, err := p.Run(context.Background(), BotRef{ID: 2, PackID: "xiaohongshu"},
		OpsContentBrief{Topic: "换季护肤", Format: "note"}, 200)
	require.ErrorContains(t, err, "visual stage")
	require.Equal(t, 0, layout.calls, "layout must not run after a visual failure")
	require.NotEmpty(t, art.Dir)
	require.Equal(t, StatusFailed, art.Stages[len(art.Stages)-1].Status)

	// The failed run still records how far it got.
	data, readErr := os.ReadFile(filepath.Join(art.Dir, "artifacts.json"))
	require.NoError(t, readErr)
	var manifest Artifact
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.Len(t, manifest.Stages, 3)
	require.Equal(t, StageVisual, manifest.Stages[2].Stage)
	require.Equal(t, StatusFailed, manifest.Stages[2].Status)
	require.NotEmpty(t, manifest.Stages[2].Error)
}

func TestPipelineBothPlatformsProducePublishableLayouts(t *testing.T) {
	tests := []struct {
		name       string
		packID     string
		format     string
		layoutName string
		minImages  int
	}{
		{name: "wechat article", packID: "wechat_mp", format: FormatArticle, layoutName: "article.html", minImages: 2},
		{name: "xiaohongshu note", packID: "xiaohongshu", format: FormatNote, layoutName: "note-layout.json", minImages: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeArtifactStore{dir: t.TempDir()}
			p := &Pipeline{Copy: &fakeCopy{}, Images: &fakeImages{}, Store: store}
			art, err := p.Run(context.Background(), BotRef{ID: 3, PackID: tc.packID},
				OpsContentBrief{Topic: "换季护肤", Format: tc.format}, 300)
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(art.Images), tc.minImages)
			require.FileExists(t, art.LayoutPath)
			require.Equal(t, tc.layoutName, filepath.Base(art.LayoutPath))

			// Cover comes first and inline images follow, at the pack's sizes.
			require.Equal(t, ImagePurposeCover, art.Images[0].Purpose)
			require.NotZero(t, art.Images[0].Width)
			require.NotZero(t, art.Images[0].Height)
			for _, img := range art.Images[1:] {
				require.Equal(t, ImagePurposeInline, img.Purpose)
			}

			if tc.format == FormatNote {
				var layout NoteLayout
				data, err := os.ReadFile(art.LayoutPath)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(data, &layout))
				require.Len(t, layout.Images, len(art.Images))
				for i, img := range layout.Images {
					require.Equal(t, i+1, img.Order)
					require.NotEmpty(t, img.Path)
				}
				require.NotEmpty(t, layout.Images[0].Caption)
			} else {
				html, err := os.ReadFile(art.LayoutPath)
				require.NoError(t, err)
				require.Contains(t, string(html), "<!doctype html>")
				require.Contains(t, string(html), "秋季护理的三个关键步骤")
				require.Contains(t, string(html), "images/cover-0.png")
			}
		})
	}
}

func TestPipelineEachRunGetsNewVersion(t *testing.T) {
	root := t.TempDir()
	store := &FileArtifactStore{Root: root}
	p := &Pipeline{Copy: &fakeCopy{}, Images: &fakeImages{}, Store: store}

	first, err := p.Run(context.Background(), BotRef{ID: 4, PackID: "wechat_mp"},
		OpsContentBrief{Topic: "秋季护理", Format: FormatArticle}, 400)
	require.NoError(t, err)
	second, err := p.Run(context.Background(), BotRef{ID: 4, PackID: "wechat_mp"},
		OpsContentBrief{Topic: "秋季护理", Format: FormatArticle}, 400)
	require.NoError(t, err)

	require.Equal(t, 1, first.Version)
	require.Equal(t, 2, second.Version)
	require.NotEqual(t, first.Dir, second.Dir)
	require.FileExists(t, filepath.Join(root, "400", "v1", "artifacts.json"))
	require.FileExists(t, filepath.Join(root, "400", "v2", "artifacts.json"))
}

func TestPipelineRejectsCopyOverPackLimits(t *testing.T) {
	p := &Pipeline{Copy: longTitleCopy{}, Store: &fakeArtifactStore{dir: t.TempDir()}}
	_, err := p.Run(context.Background(), BotRef{ID: 5, PackID: "xiaohongshu"},
		OpsContentBrief{Topic: "护肤", Format: FormatNote}, 500)
	require.ErrorContains(t, err, "copy stage")
	require.ErrorContains(t, err, "pack allows 20")
}

func TestPipelineRejectsUnsupportedFormat(t *testing.T) {
	p := &Pipeline{Copy: &fakeCopy{}, Images: &fakeImages{}, Store: &fakeArtifactStore{dir: t.TempDir()}}
	_, err := p.Run(context.Background(), BotRef{ID: 6, PackID: "wechat_mp"},
		OpsContentBrief{Topic: "产品介绍", Format: FormatVideo}, 600)
	require.ErrorIs(t, err, ErrInvalidPipelineInput)
	require.ErrorContains(t, err, `does not support format "video"`)
}

func TestFileArtifactStoreAllocatesSequentialVersions(t *testing.T) {
	store := &FileArtifactStore{Root: t.TempDir()}
	dir1, version1, err := store.AllocateVersion(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, version1)
	require.DirExists(t, dir1)

	dir2, version2, err := store.AllocateVersion(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 2, version2)
	require.NotEqual(t, dir1, dir2)
}

func TestPipelineDefaultsClockAndFormat(t *testing.T) {
	fixed := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	store := &fakeArtifactStore{dir: t.TempDir()}
	p := &Pipeline{
		Copy:   &fakeCopy{},
		Images: &fakeImages{},
		Layout: &fakeLayout{},
		Store:  store,
		Now:    func() time.Time { return fixed },
	}
	art, err := p.Run(context.Background(), BotRef{ID: 7, PackID: "wechat_mp"},
		OpsContentBrief{Topic: "秋季护理"}, 700)
	require.NoError(t, err)
	require.Equal(t, FormatArticle, art.Format)
	require.True(t, fixed.Equal(art.CreatedAt))
}
