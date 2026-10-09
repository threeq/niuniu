// Project impression: a ≤200-character cross-session summary of a project
// (tech stack / key decisions / user temperament / current phase), distilled
// by the compact chain and injected into new sessions' system prompts as
// background atmosphere. Entry-level facts live in the memory store; the
// impression is only the fallback feel of the project.
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxImpressionRunes hard-caps an impression at 200 字 (runes). Enforced on
// write AND on load: a compact's oversized output is truncated here, and a
// hand-edited oversized file must not crowd the system prompt either.
const MaxImpressionRunes = 200

// NormalizeImpression validates and caps raw impression text. Corrupted
// content (invalid UTF-8) degrades to empty. The 200-rune cap is absolute
// and applied at LINE boundaries: the impression is a four-line labeled
// block (技术栈 / 关键决策 / 用户脾气 / 当前阶段), so an oversized value
// drops whole trailing lines instead of slicing the fourth line mid-label
// and injecting the fragment into every future session. Only a single line
// that alone exceeds the cap is hard-cut.
func NormalizeImpression(raw string) string {
	if !utf8.ValidString(raw) {
		return ""
	}
	s := strings.TrimSpace(raw)
	if len([]rune(s)) <= MaxImpressionRunes {
		return s
	}
	var kept []string
	n := 0
	for _, ln := range strings.Split(s, "\n") {
		l := strings.TrimSpace(ln)
		if l == "" {
			continue
		}
		rl := len([]rune(l))
		if len(kept) == 0 {
			if rl > MaxImpressionRunes {
				l = string([]rune(l)[:MaxImpressionRunes])
			}
			kept = append(kept, l)
			n = len([]rune(l))
			continue
		}
		if n+rl > MaxImpressionRunes {
			break // keep only whole labeled lines
		}
		kept = append(kept, l)
		n += rl
	}
	return strings.Join(kept, "\n")
}

// WriteImpression persists text as the impression file at path. An empty
// path (impression maintenance disabled — subagent, eval and RSI sessions
// leave Options.ImpressionPath empty) or empty/normalization-emptied
// content is a no-op: a compact without impression output must never wipe an
// existing impression, and a path-less session must not attempt — and warn
// about — a doomed write. The write itself is atomic (unique temp file +
// rename) so a concurrent session's LoadImpression can never read a torn
// half-written file. Callers treat errors as best-effort: a failed write
// must not block the turn.
func WriteImpression(path, text string) error {
	if path == "" {
		return nil
	}
	s := NormalizeImpression(text)
	if s == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.WriteString(s + "\n"); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("impression write: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("impression write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("impression write: %w", err)
	}
	return nil
}

// LoadImpression reads the impression file at path. Missing, corrupted
// (invalid UTF-8), empty, or oversized files degrade gracefully — to ("",
// false) or the capped text — so session startup neither fails nor injects
// garbage because of a bad impression file.
func LoadImpression(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	s := NormalizeImpression(string(data))
	if s == "" {
		return "", false
	}
	return s, true
}
