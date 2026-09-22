package loop

import (
	"unicode"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Local token estimation: the compact trigger compares the provider-reported
// context size (Usage tokens) against the threshold — but a gateway that
// doesn't report usage (or a mid-round decision) needs a local estimate.
// Zero-dependency heuristic: CJK ≈ 1 token per rune, Latin words ≈
// 1.3 tokens per word (avg word 4 chars + punctuation/space overhead).
// Accuracy target is ±30% — enough to decide "compact now vs one more
// round", never used for billing.

// EstimateTokens estimates the token count of s.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	var cjk, other int
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjk++
		} else {
			other++
		}
	}
	// CJK: ~1 token per character (BPE merges are short for CJK).
	// Other: ~4 chars per token (English-ish prose/code average).
	tokens := cjk + (other+3)/4
	if tokens == 0 {
		tokens = 1
	}
	return tokens
}

// EstimateMessagesTokens estimates the whole wire payload of a conversation:
// system prompt plus every message's blocks (text bodies, tool names, and a
// per-message structural overhead).
func EstimateMessagesTokens(system string, msgs []model.Message) int {
	total := EstimateTokens(system)
	for _, m := range msgs {
		total += 8 // role + block framing overhead
		for _, b := range m.Blocks {
			switch b.Type {
			case model.BlockText, model.BlockThinking:
				total += EstimateTokens(b.Text)
			case model.BlockToolResult:
				total += EstimateTokens(b.Text) + EstimateTokens(b.ToolUseID)
			case model.BlockToolUse:
				total += EstimateTokens(b.Name) + EstimateTokens(string(b.Input))
			case model.BlockImage:
				total += 1600 // ~medium image cost, order-of-magnitude only
			}
		}
	}
	return total
}
