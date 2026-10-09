// Structured compaction state: instead of re-summarizing a prose summary on
// every chained compaction (which compounds distortion — "summary of a
// summary"), the summarizer emits a fixed-schema JSON state that is MERGED
// into the session's accumulated state. Key decisions and touched files
// accumulate (deduped, capped); open items are replaced by the newest. The
// merged state is rendered into the [auto-compacted] message AND persisted
// to disk (Options.CompactStatePath), so exact details survive in file form
// and the agent can Read them back — the context only carries the compact
// rendering plus a pointer.
package loop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Caps keep the merged state bounded — an unbounded accumulator would
// eventually crowd out the working window it exists to protect.
const (
	maxConstraints  = 10
	maxKeyDecisions = 20
	maxFilesTouched = 30
	maxOpenItems    = 15
	maxDeadEnds     = 10
)

// CompactState is the structured, mergeable compaction state.
type CompactState struct {
	Goal         string   `json:"goal"`
	Constraints  []string `json:"constraints,omitempty"`
	KeyDecisions []string `json:"key_decisions"`
	DeadEnds     []string `json:"dead_ends,omitempty"`
	FilesTouched []string `json:"files_touched,omitempty"`
	OpenItems    []string `json:"open_items"`
	// Impression is the ≤200-character cross-session project impression the
	// summarizer emits alongside the state (same LLM call — no extra cost).
	// It is persisted to the per-project impression.md and injected into
	// FUTURE sessions' system prompts; it is not rendered into the
	// [auto-compacted] message (the current session already carries it in
	// its prompt prefix). Carried across chained compactions newest-wins.
	Impression string `json:"impression,omitempty"`
	Compactions int    `json:"compactions"`
}

// stateSchema is the JSON contract embedded in the summarizer system prompt.
const stateSchema = `{"goal": "...", "constraints": ["..."], "key_decisions": ["..."], "dead_ends": ["approach tried and why it failed"], "files_touched": ["path"], "open_items": ["next step, in order"], "impression": "技术栈: ...\n关键决策: ...\n用户脾气: ...\n当前阶段: ..."}`

// parseStateJSON extracts the CompactState from a summarizer response,
// tolerating markdown code fences. Requires key_decisions and open_items —
// a state without them cannot anchor a continuation.
func parseStateJSON(text string) (*CompactState, error) {
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i:]
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	// Tolerate prose around the JSON object.
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	var st CompactState
	if err := json.Unmarshal([]byte(s), &st); err != nil {
		return nil, fmt.Errorf("state json: %w", err)
	}
	if st.KeyDecisions == nil || st.OpenItems == nil {
		return nil, fmt.Errorf("state json missing key_decisions/open_items")
	}
	return &st, nil
}

// mergeState folds a fresh summarizer state into the accumulated one.
// Decisions/files/constraints/dead-ends accumulate (deduped, order-stable,
// capped — oldest dropped first), open items are replaced wholesale (they
// describe the present), compactions counts the chain length.
func mergeState(old, nw *CompactState) *CompactState {
	out := &CompactState{Compactions: 1}
	if old != nil {
		out.Compactions = old.Compactions + 1
		out.Constraints = append([]string{}, old.Constraints...)
		out.KeyDecisions = append([]string{}, old.KeyDecisions...)
		out.DeadEnds = append([]string{}, old.DeadEnds...)
		out.FilesTouched = append([]string{}, old.FilesTouched...)
	}
	if nw == nil {
		return out
	}
	if nw.Goal != "" {
		out.Goal = nw.Goal
	} else if old != nil {
		out.Goal = old.Goal
	}
	// Impression follows Goal's newest-wins rule, with the previous
	// impression kept when the summarizer omits it (an update pass must not
	// silently erase the file's content).
	if nw.Impression != "" {
		out.Impression = nw.Impression
	} else if old != nil {
		out.Impression = old.Impression
	}
	out.Constraints = cappedMerge(out.Constraints, nw.Constraints, maxConstraints)
	out.KeyDecisions = cappedMerge(out.KeyDecisions, nw.KeyDecisions, maxKeyDecisions)
	out.DeadEnds = cappedMerge(out.DeadEnds, nw.DeadEnds, maxDeadEnds)
	out.FilesTouched = cappedMerge(out.FilesTouched, nw.FilesTouched, maxFilesTouched)
	if len(nw.OpenItems) > 0 {
		out.OpenItems = nw.OpenItems
		if len(out.OpenItems) > maxOpenItems {
			out.OpenItems = out.OpenItems[:maxOpenItems]
		}
	} else if old != nil {
		out.OpenItems = old.OpenItems
	}
	return out
}

// cappedMerge appends new items not already present, then keeps the LAST cap
// entries (newest survive; stale prefixes fall off).
func cappedMerge(base, add []string, cap int) []string {
	seen := make(map[string]bool, len(base)+len(add))
	for _, s := range base {
		seen[s] = true
	}
	out := append([]string{}, base...)
	for _, s := range add {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) > cap {
		out = out[len(out)-cap:]
	}
	return out
}

// renderState formats the merged state for the [auto-compacted] message —
// compact, scannable, and byte-stable for the remainder of the session.
func renderState(st *CompactState) string {
	var b strings.Builder
	if st.Goal != "" {
		b.WriteString("Goal: " + st.Goal + "\n")
	}
	if len(st.Constraints) > 0 {
		b.WriteString("Constraints: " + strings.Join(st.Constraints, "; ") + "\n")
	}
	if len(st.KeyDecisions) > 0 {
		b.WriteString("Key decisions:\n")
		for _, d := range st.KeyDecisions {
			b.WriteString("- " + d + "\n")
		}
	}
	if len(st.DeadEnds) > 0 {
		// The single highest-value section for long-horizon reasoning: without
		// it the continuation model re-derives (and re-fails) the same paths.
		b.WriteString("Dead ends (tried and failed — do NOT retry):\n")
		for _, d := range st.DeadEnds {
			b.WriteString("- " + d + "\n")
		}
	}
	if len(st.FilesTouched) > 0 {
		b.WriteString("Files touched: " + strings.Join(st.FilesTouched, ", ") + "\n")
	}
	if len(st.OpenItems) > 0 {
		b.WriteString("Open items:\n")
		for _, o := range st.OpenItems {
			b.WriteString("- " + o + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// persistState writes the merged state as JSON. Best-effort: a failed write
// must never abort the turn — the in-context rendering still carries the
// state.
func persistState(path string, st *CompactState) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
