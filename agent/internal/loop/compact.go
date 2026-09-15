// Auto-compact: when a session's context approaches the model's practical
// window, earlier messages are condensed into a summary and replaced, keeping
// a recent window verbatim. Tool_use/tool_result pairing stays valid because
// the cut always lands on a plain text user message — everything after such a
// message is self-consistent (results always immediately follow their uses).
package loop

import (
	"context"
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Compaction defaults. The threshold compares against the context size the
// provider reported for the last round (Usage.ContextTokens); it is a
// fraction of modern windows (GLM 1M, Claude 200K) so a session compacts
// well before hard failure.
const (
	DefaultCompactThreshold = 120_000
	DefaultKeepRecent       = 12
)

// compactSystem drives the summarizer model call. It is distinct from the
// agent system prompt: this call only compresses history.
const compactSystem = `You compress a coding agent's conversation history into a compact summary for the model to continue the work with. Preserve: the user's goals and constraints, key decisions, file paths touched (with what changed), current task status, and concrete next steps. Plain prose plus short lists. Do not answer, do not comment — output the summary only.`

const compactInstruction = `Summarize the conversation above for continuation.`

// compactCut returns the index in msgs where compaction may split: the start
// of the earliest plain-text user message such that at least keep messages
// remain after it. Returns 0 when no safe split exists (nothing to gain).
func compactCut(msgs []model.Message, keep int) int {
	if keep < 0 {
		keep = 0
	}
	if len(msgs) <= keep {
		return 0
	}
	limit := len(msgs) - keep
	for i := limit; i > 0; i-- {
		m := msgs[i]
		if m.Role != model.RoleUser {
			continue
		}
		plainText := true
		for _, b := range m.Blocks {
			if b.Type != model.BlockText {
				plainText = false
				break
			}
		}
		if plainText {
			return i
		}
	}
	return 0
}

// compact condenses s.messages when there is a safe cut. A summarizer
// failure is swallowed (the turn continues with the full history) — losing
// one compaction beats losing the turn.
func (s *Session) compact(ctx context.Context, keep int) {
	cut := compactCut(s.messages, keep)
	if cut <= 0 {
		return
	}
	early := s.messages[:cut]
	summary, err := s.summarize(ctx, early)
	if err != nil {
		return
	}
	kept := make([]model.Message, 0, len(s.messages)-cut+1)
	kept = append(kept, model.Message{Role: model.RoleUser, Blocks: []model.Block{{
		Type: model.BlockText,
		Text: "[auto-compacted] Summary of the earlier conversation:\n\n" + summary,
	}}})
	kept = append(kept, s.messages[cut:]...)
	s.messages = kept
}

// summarize asks the model to condense early into a short brief.
func (s *Session) summarize(ctx context.Context, early []model.Message) (string, error) {
	msgs := make([]model.Message, 0, len(early)+1)
	msgs = append(msgs, early...)
	msgs = append(msgs, model.Message{Role: model.RoleUser, Blocks: []model.Block{
		{Type: model.BlockText, Text: compactInstruction},
	}})
	resp, err := s.m.Complete(ctx, model.Request{
		System:    compactSystem,
		Messages:  msgs,
		MaxTokens: 2048,
	})
	if err != nil {
		return "", fmt.Errorf("summarize: %w", err)
	}
	out := strings.TrimSpace(resp.Message.Text())
	if out == "" {
		return "", fmt.Errorf("summarize: empty summary")
	}
	return out, nil
}
