package lark

import (
	"encoding/json"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

// buildInteractiveCard renders a Feishu interactive-card JSON string: a text
// block plus an action row of buttons. Each button embeds its callback payload
// under value.cb, which comes back on click as a card.action.trigger event
// (parseCardActionEvent reads value.cb into InboundEvent.CallbackData). The
// first button is styled primary (allow), the rest danger (deny) — matching the
// approve/deny permission card.
func buildInteractiveCard(text string, buttons []imbot.Button) string {
	actions := make([]map[string]any, 0, len(buttons))
	for i, b := range buttons {
		style := "danger"
		if i == 0 {
			style = "primary"
		}
		actions = append(actions, map[string]any{
			"tag":   "button",
			"text":  map[string]any{"tag": "plain_text", "content": b.Label},
			"type":  style,
			"value": map[string]any{"cb": b.Value},
		})
	}
	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"elements": []map[string]any{
			{"tag": "markdown", "content": text},
			{"tag": "action", "actions": actions},
		},
	}
	b, _ := json.Marshal(card)
	return string(b)
}

// buildMarkdownCard renders a plain (button-less) outbound message as a Feishu
// interactive card carrying a single `markdown` element, so the content is
// rendered as rich text (bold/italic/links/lists/code) instead of shown raw —
// a `text` message type does NOT render markdown. The markdown component covers
// the common subset (headings/tables/quotes are unsupported by Feishu and fall
// back to their literal text, which is acceptable).
func buildMarkdownCard(text string) string {
	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"elements": []map[string]any{
			{"tag": "markdown", "content": text},
		},
	}
	b, _ := json.Marshal(card)
	return string(b)
}
