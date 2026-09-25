package rsi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Strategy-level lessons (P8a): RSI explore runs and -reflect rounds distill
// experience into memory, but only GROUNDED strategy lessons may reach
// PROMPT.md — the file is injected into every future session's system prompt,
// so an ungrounded lesson there would steer every task in the project.
//
// Grounding threshold: a lesson must carry at least GroundingThreshold
// independent verification signals. In the flywheel those signals are the
// visible regression re-run and the hidden held-out set — a candidate that
// survived both is grounded twice; without a hidden set there is only one
// signal and nothing lands.

// GroundingThreshold is the minimum Verified count for a strategy lesson to
// enter PROMPT.md.
const GroundingThreshold = 2

// StrategyLesson is a policy-level lesson proposed for PROMPT.md.
type StrategyLesson struct {
	Title    string
	Guidance string
	Verified int // independent verification signals backing the lesson
}

const (
	strategyStart = "<!-- niuniu-agent:strategy start -->"
	strategyEnd   = "<!-- niuniu-agent:strategy end -->"
)

// LandStrategyLessons writes the grounded subset of lessons into
// <cwd>/.niuniu-agent/PROMPT.md under a managed section. Sub-threshold
// lessons are dropped; with no grounded lessons the file is left untouched.
// The managed section is replaced wholesale on each landing (idempotent, no
// accumulation) and the rest of the file is preserved byte-for-byte; the
// file is created when missing. Returns the number of lessons landed.
func LandStrategyLessons(cwd string, lessons []StrategyLesson) (int, error) {
	var grounded []StrategyLesson
	seen := map[string]bool{}
	for _, l := range lessons {
		if l.Verified < GroundingThreshold || l.Guidance == "" || seen[l.Title] {
			continue
		}
		seen[l.Title] = true
		grounded = append(grounded, l)
	}
	if len(grounded) == 0 {
		return 0, nil
	}

	path := filepath.Join(cwd, ".niuniu-agent", "PROMPT.md")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	base := stripStrategySection(string(existing))

	var b strings.Builder
	b.WriteString(base)
	b.WriteString(strategyStart)
	b.WriteString("\n# Self-evolved strategy (grounded)\n\n")
	for _, l := range grounded {
		b.WriteString(fmt.Sprintf("- **%s**: %s\n", l.Title, l.Guidance))
	}
	b.WriteString(strategyEnd)
	b.WriteString("\n")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return 0, err
	}
	return len(grounded), nil
}

// stripStrategySection removes a previously landed managed section, keeping
// all user-authored content around it.
func stripStrategySection(s string) string {
	start := strings.Index(s, strategyStart)
	if start < 0 {
		return strings.TrimRight(s, "\n")
	}
	end := strings.Index(s[start:], strategyEnd)
	if end < 0 {
		// Malformed (no end marker): drop everything from the start marker on
		// so a partial write can never duplicate sections.
		return strings.TrimRight(s[:start], "\n")
	}
	before := strings.TrimRight(s[:start], "\n")
	after := strings.TrimSpace(s[start+end+len(strategyEnd):])
	switch {
	case before == "":
		return after
	case after == "":
		return before
	default:
		return before + "\n" + after
	}
}
