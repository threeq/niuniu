package opsbot

// PackDraftGenerator adapts packs.Generator — which speaks OpenAPI documents,
// browser recipes and capability matrices — to the small conversational
// interface Onboarding needs.
//
// The first turn always asks for a source: an OpenAPI document URL or JSON, an
// existing MCP provider (`mcp:<provider>`), or `skip` for a browser-recipe
// draft. Whatever the generator still lacks after that becomes PackQuestions
// and is asked one at a time; no fact is ever inferred.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

const (
	qOpenAPIDocument = "openapi_document"
	qMetricsMap      = "metrics_field_map"
)

type PackDraftGenerator struct {
	Generator *packs.Generator
}

var _ AnswerablePackGenerator = (*PackDraftGenerator)(nil)

func (a *PackDraftGenerator) Draft(ctx context.Context, owner OwnerRef, channel string) (PackDraft, error) {
	return a.DraftWithAnswers(ctx, owner, channel, nil)
}

func (a *PackDraftGenerator) DraftWithAnswers(ctx context.Context, owner OwnerRef, channel string, answers map[string]string) (PackDraft, error) {
	if a == nil || a.Generator == nil {
		return PackDraft{}, errors.New("opsbot: pack generator is not configured")
	}
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return PackDraft{}, errors.New("opsbot: channel name is required")
	}

	docAnswer := strings.TrimSpace(answers[qOpenAPIDocument])
	if docAnswer == "" {
		return PackDraft{
			Questions: []PackQuestion{{
				Key: qOpenAPIDocument,
				Prompt: fmt.Sprintf("接入「%s」：请提供官方 OpenAPI 文档的 URL 或 JSON 内容；"+
					"若已有 MCP provider，请回复 mcp:<provider>；两者都没有请回复 skip，"+
					"我会生成浏览器 recipe 草稿并逐项确认缺失信息。", channel),
			}},
		}, nil
	}

	input := packs.GenerateInput{
		Channel:        channel,
		OwnerType:      owner.Type,
		OwnerID:        owner.ID,
		Answers:        answers,
		SkipIncomplete: true,
	}
	switch {
	case isDeclineAnswer(docAnswer):
		// browser recipe draft
	case strings.HasPrefix(strings.ToLower(docAnswer), "mcp:"):
		input.ExistingMCP = strings.TrimSpace(docAnswer[len("mcp:"):])
	case strings.HasPrefix(docAnswer, "{"):
		input.OpenAPIBytes = []byte(docAnswer)
	default:
		input.OpenAPIURL = docAnswer
	}

	draft, err := a.Generator.Draft(ctx, input)
	if err != nil {
		return PackDraft{}, err
	}
	return packDraftFrom(draft), nil
}

func (a *PackDraftGenerator) Test(ctx context.Context, owner OwnerRef, packID string, version int64) error {
	if a == nil || a.Generator == nil {
		return errors.New("opsbot: pack generator is not configured")
	}
	return a.Generator.Test(ctx, packs.Owner{Type: owner.Type, ID: owner.ID}, packID, int(version))
}

func (a *PackDraftGenerator) Publish(ctx context.Context, owner OwnerRef, packID string, version int64) error {
	if a == nil || a.Generator == nil {
		return errors.New("opsbot: pack generator is not configured")
	}
	return a.Generator.Publish(ctx, packs.Owner{Type: owner.Type, ID: owner.ID}, packID, int(version))
}

func packDraftFrom(d *packs.Draft) PackDraft {
	out := PackDraft{
		PackID:      d.Manifest.ID,
		Version:     int64(d.Manifest.Version),
		DisplayName: d.Manifest.DisplayName,
	}
	if out.Version <= 0 {
		out.Version = 1
	}

	for _, entry := range d.Matrix.Capabilities {
		route := entry.Transport
		if route == "" {
			out.Capabilities = append(out.Capabilities,
				fmt.Sprintf("%s：未启用（%s）", entry.Capability, firstOr(strings.Join(entry.Missing, "、"), "来源未声明")))
			continue
		}
		if entry.Provider != "" {
			route += "(" + entry.Provider + ")"
		}
		switch {
		case !entry.Bound:
			out.Capabilities = append(out.Capabilities,
				fmt.Sprintf("%s：%s，未启用（缺 %s）", entry.Capability, route, strings.Join(entry.Missing, "、")))
		case len(entry.Missing) > 0:
			out.Capabilities = append(out.Capabilities,
				fmt.Sprintf("%s：%s，待确认 %s", entry.Capability, route, strings.Join(entry.Missing, "、")))
		default:
			out.Capabilities = append(out.Capabilities, fmt.Sprintf("%s：%s，就绪", entry.Capability, route))
		}
	}

	risks := map[string]bool{}
	for _, entry := range d.Matrix.Capabilities {
		for _, risk := range entry.Risks {
			if !risks[risk] {
				risks[risk] = true
				out.Risks = append(out.Risks, risk)
			}
		}
	}
	for _, question := range d.Questions {
		out.Questions = append(out.Questions, PackQuestion{Key: question.Key, Prompt: question.Prompt})
	}
	return out
}

func isDeclineAnswer(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "skip", "no", "none", "off", "不需要", "跳过", "没有":
		return true
	}
	return false
}

func firstOr(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
