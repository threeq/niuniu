// Project impression: a ≤200-character cross-session summary of a project
// (tech stack / key decisions / user temperament / current phase), distilled
// by the compact chain and injected into new sessions' system prompts as
// background atmosphere. Entry-level facts live in the memory store; the
// impression is only the fallback feel of the project.
package tools

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxImpressionRunes hard-caps an impression at 200 字 (runes). Enforced on
// write AND on load: a compact's oversized output is truncated here, and a
// hand-edited oversized file must not crowd the system prompt either.
const MaxImpressionRunes = 200

// normalizeImpression validates and caps raw impression text. Corrupted
// content (invalid UTF-8) degrades to empty; the 200-rune cap is absolute.
func normalizeImpression(raw string) string {
	if !utf8.ValidString(raw) {
		return ""
	}
	s := strings.TrimSpace(raw)
	if runes := []rune(s); len(runes) > MaxImpressionRunes {
		s = string(runes[:MaxImpressionRunes])
	}
	return s
}

// WriteImpression persists text as the impression file at path. Empty (or
// normalization-emptied) content is a no-op — a compact without impression
// output must never wipe an existing impression. Callers treat errors as
// best-effort: a failed write must not block the turn.
func WriteImpression(path, text string) error {
	s := normalizeImpression(text)
	if s == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(s+"\n"), 0o644)
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
	s := normalizeImpression(string(data))
	if s == "" {
		return "", false
	}
	return s, true
}
