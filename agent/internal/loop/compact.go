// Auto-compact: when a session's context approaches the model's practical
// window, earlier messages are condensed into a summary and replaced, keeping
// a recent window verbatim. Tool_use/tool_result pairing stays valid because
// the cut always lands on a plain text user message — everything after such a
// message is self-consistent (results always immediately follow their uses).
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
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
// Structured summary: fixed sections so the continuation model can locate
// context at a glance (and tests can pin the shape).
const compactSystem = `You compress a coding agent's conversation history into a compact summary for the model to continue the work with. Output EXACTLY one JSON object, no prose, matching this schema:

` + stateSchema + `

Field rules:
- goal: the user's objective and task origin, one sentence.
- constraints: settled constraints the continuation must respect.
- key_decisions: settled choices and file paths touched (with what changed) — the reasoning the continuation must not re-derive.
- dead_ends: approaches that were tried and FAILED, each with the failure reason — the continuation must not retry them. Include every dead end visible in the history; this field prevents repeated failures.
- files_touched: repository paths modified so far.
- open_items: current task status and the concrete next steps, in order.
- impression: a cross-session impression of this PROJECT for future sessions, at most 200 characters total, exactly four labeled lines — 技术栈 / 关键决策 / 用户脾气（协作风格与偏好）/ 当前阶段. Durable project-level facts and observed user preferences only, never transient task state. When a previous impression is provided in the final user message, carry it forward and update only what changed; never drop its still-accurate lines.

Do not answer, do not comment — output the JSON only. If a previous state is provided in the final user message, PRESERVE its still-relevant key_decisions and files_touched (merged with what the recent history adds); only supersede what has changed.`

const compactInstruction = `Summarize the conversation above as the state JSON.`

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
//
// Structured path: the summarizer is asked for fixed-schema JSON; on success
// the state is MERGED into s.state (decisions/files accumulate — chained
// compactions no longer compound "summary of a summary" distortion), the
// merged state is rendered into the summary message and persisted to
// Options.CompactStatePath (the message carries a pointer so the agent can
// Read exact details back). On parse failure the raw text is used as a plain
// summary (legacy behavior).
func (s *Session) compact(ctx context.Context, keep int, statePath, impressionPath, historyDir string) {
	cut := compactCut(s.messages, keep)
	if cut <= 0 {
		return
	}
	early := s.messages[:cut]
	summary, err := s.summarize(ctx, early)
	if err != nil {
		return
	}
	body := summary
	if st, perr := parseStateJSON(summary); perr == nil {
		merged := mergeState(s.state, st)
		s.state = merged
		body = renderState(merged)
		if perr := persistState(statePath, merged); perr == nil && statePath != "" {
			body += "\n\nFull structured state persisted to " + statePath +
				" — Read it for exact details (decisions, files, next steps)."
		}
		// Impression refresh rides the same summarizer response (no extra
		// LLM call). Best-effort: a failed write never blocks the turn.
		if werr := tools.WriteImpression(impressionPath, merged.Impression); werr != nil {
			slog.Warn("loop: impression write failed", "err", werr)
		}
	}
	// Archive the evicted messages verbatim so the HistorySearch tool can
	// pull exact details back on demand. Best-effort: never blocks the turn.
	if historyDir != "" {
		if aerr := tools.SaveHistoryArchive(historyDir, early); aerr == nil {
			body += "\n\n(The full messages removed above are archived and searchable — call the HistorySearch tool with keywords to retrieve exact earlier details.)"
		}
	}
	kept := make([]model.Message, 0, len(s.messages)-cut+1)
	kept = append(kept, model.Message{Role: model.RoleUser, Blocks: []model.Block{{
		Type: model.BlockText,
		Text: "[auto-compacted] Summary of the earlier conversation:\n\n" + body,
	}}})
	kept = append(kept, s.messages[cut:]...)
	s.messages = kept
}

// summarize asks the model to condense early into a structured state. When
// a previous state exists it is appended for the model to preserve
// still-relevant decisions from (chained-compaction continuity).
func (s *Session) summarize(ctx context.Context, early []model.Message) (string, error) {
	instr := compactInstruction
	if s.state != nil {
		if prev, err := json.Marshal(s.state); err == nil {
			instr += "\n\nPrevious state (preserve still-relevant key_decisions, dead_ends and files_touched; supersede only what changed):\n" + string(prev)
		}
	}
	msgs := make([]model.Message, 0, len(early)+1)
	msgs = append(msgs, early...)
	msgs = append(msgs, model.Message{Role: model.RoleUser, Blocks: []model.Block{
		{Type: model.BlockText, Text: instr},
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
