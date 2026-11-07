package content

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// Image purposes. Every visual stage produces exactly one cover first; inline
// images follow.
const (
	ImagePurposeCover  = "cover"
	ImagePurposeInline = "inline"
)

// ImageGenerator produces the cover and inline images for a piece of content,
// writing files under OutDir. The production implementation fronts media-studio
// or an image-generation provider; tests inject a fake.
type ImageGenerator interface {
	Generate(ctx context.Context, req ImageRequest) (ImageResult, error)
}

// ImageSpec is one image the pack requires: purpose plus the size that keeps
// the platform happy.
type ImageSpec struct {
	Index   int    `json:"index"`
	Purpose string `json:"purpose"`
	Ratio   string `json:"ratio"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

// ImageRequest is handed to the image generator.
type ImageRequest struct {
	PackID string
	Format string
	Topic  string
	Prompt string
	Specs  []ImageSpec
	OutDir string
}

// ImageAsset is one generated file, recorded in artifacts.json.
type ImageAsset struct {
	Path    string `json:"path"`
	Purpose string `json:"purpose"`
	Ratio   string `json:"ratio,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
}

// ImageResult is what the generator produced for one request.
type ImageResult struct {
	Images   []ImageAsset `json:"images"`
	Provider string       `json:"provider"`
	CostUSD  float64      `json:"cost_usd"`
}

// imageSpecs builds the cover plus inline specs from the pack's content schema.
// A non-positive inline count falls back to the per-format default.
func imageSpecs(format string, schema packs.ContentFormat, inline int) []ImageSpec {
	coverWidth, coverHeight := imageDimensions(schema.ImageRatio)
	specs := []ImageSpec{{
		Index:   0,
		Purpose: ImagePurposeCover,
		Ratio:   schema.ImageRatio,
		Width:   coverWidth,
		Height:  coverHeight,
	}}
	if inline <= 0 {
		inline = defaultInlineImages(format)
	}
	for i := 0; i < inline; i++ {
		ratio, width, height := inlineImageSize(format, schema)
		specs = append(specs, ImageSpec{
			Index:   i + 1,
			Purpose: ImagePurposeInline,
			Ratio:   ratio,
			Width:   width,
			Height:  height,
		})
	}
	return specs
}

// defaultInlineImages matches the platform checklist: 公众号 pairs the cover
// with one inline image, 小红书 needs at least three images in total.
func defaultInlineImages(format string) int {
	switch format {
	case FormatArticle:
		return 1
	case FormatNote:
		return 2
	default:
		return 0
	}
}

func inlineImageSize(format string, schema packs.ContentFormat) (string, int, int) {
	// 公众号 body images are 1080px wide (pack layout prompt); the pack's
	// image_ratio only describes the cover.
	if format == FormatArticle {
		return "3:2", 1080, 720
	}
	width, height := imageDimensions(schema.ImageRatio)
	return schema.ImageRatio, width, height
}

// imageDimensions turns a pack size ("900x383") or ratio ("3:4", "9:16") into
// pixels. Ratios are scaled to a 1080px base width.
func imageDimensions(spec string) (int, int) {
	s := strings.ToLower(strings.TrimSpace(spec))
	if s == "" {
		return 0, 0
	}
	var width, height int
	if _, err := fmt.Sscanf(s, "%dx%d", &width, &height); err == nil && width > 0 && height > 0 {
		return width, height
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	ratioW, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	ratioH, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || ratioW <= 0 || ratioH <= 0 {
		return 0, 0
	}
	const baseWidth = 1080
	return baseWidth, baseWidth * ratioH / ratioW
}
