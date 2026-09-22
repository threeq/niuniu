package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Monitor watches a background bash (existing by id, or started inline from
// command) and COLLECTS the lines matching filter until a terminal
// condition: process exit, timeout, or max_matches reached. Where BashOutput
// is a poll ("what's the state?"), Monitor is a wait ("tell me when/what").
// Blocking by design — the model calls it to synchronize on an event.

const (
	monitorPollInterval = 250 * time.Millisecond
	monitorMaxMatches   = 50
)

type monitorInput struct {
	BashID     string `json:"bash_id"`
	Command    string `json:"command"`
	Filter     string `json:"filter"`
	TimeoutSec int    `json:"timeout_seconds"`
	MaxMatches int    `json:"max_matches"`
}

// Monitor watches background output for matching lines.
type Monitor struct{}

func (Monitor) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "Monitor",
		Description: "Watches a background bash (by bash_id, or started inline from command) and returns the lines matching `filter` (regex), waiting until the process exits, the timeout elapses, or max_matches are collected. Use to synchronize on an event: a build finishing, a server printing its ready line, an error appearing in a log.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"bash_id":{"type":"string","description":"Id of an already-running background bash (from Bash run_in_background)"},` +
			`"command":{"type":"string","description":"Start this command in the background and monitor it (mutually exclusive with bash_id)"},` +
			`"filter":{"type":"string","description":"Regular expression; matching lines are collected (RE2 syntax)"},` +
			`"timeout_seconds":{"type":"integer","description":"Give up after this long (default 60, max 600)"},` +
			`"max_matches":{"type":"integer","description":"Stop after this many matches (default 50)"}},` +
			`"required":["filter"]}`),
	}
}

func (Monitor) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in monitorInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if in.Filter == "" {
		return "", fmt.Errorf("filter is required (an unfiltered monitor would stream everything)")
	}
	re, err := regexp.Compile(in.Filter)
	if err != nil {
		return "", fmt.Errorf("bad filter: %w", err)
	}
	if in.BashID == "" && in.Command == "" {
		return "", fmt.Errorf("bash_id or command is required")
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > 600*time.Second {
		timeout = 600 * time.Second
	}
	maxMatches := in.MaxMatches
	if maxMatches <= 0 || maxMatches > monitorMaxMatches {
		maxMatches = monitorMaxMatches
	}

	// Inline command: start it detached, monitor its output.
	bgID := in.BashID
	startedInline := false
	if bgID == "" {
		id, p := startBackgroundBash(in.Command)
		if p.exited && p.code == -1 {
			return "failed to start: " + p.buf.String(), nil
		}
		bgID, startedInline = id, true
	}

	bgMu.Lock()
	p := bgProcs[bgID]
	bgMu.Unlock()
	if p == nil {
		return "", fmt.Errorf("unknown bash_id %q", bgID)
	}

	deadline := time.Now().Add(timeout)
	var matches []string
	consumed := 0 // bytes of the buffer already split into lines
	pending := "" // trailing half-line (no \n yet)
	scan := func(final bool) {
		out := p.buf.String()
		if len(out) > consumed {
			chunk := pending + out[consumed:]
			consumed = len(out)
			lines := strings.Split(chunk, "\n")
			pending = lines[len(lines)-1]
			for _, ln := range lines[:len(lines)-1] {
				ln = strings.TrimRight(ln, "\r")
				if ln != "" && re.MatchString(ln) {
					matches = append(matches, ln)
				}
			}
		}
		if final && pending != "" {
			ln := strings.TrimRight(pending, "\r")
			pending = ""
			if ln != "" && re.MatchString(ln) {
				matches = append(matches, ln)
			}
		}
	}
	for {
		scan(false)
		if len(matches) >= maxMatches {
			return monitorReport(matches, bgID, startedInline, "max_matches reached"), nil
		}
		bgMu.Lock()
		exited := p.exited
		bgMu.Unlock()
		if exited {
			scan(true) // flush the trailing half-line after exit
			if len(matches) == 0 {
				return monitorReport(nil, bgID, startedInline, "process exited with no matching lines"), nil
			}
			return monitorReport(matches, bgID, startedInline, "process exited"), nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if len(matches) == 0 {
				return monitorReport(nil, bgID, startedInline, "timeout — no matching lines yet; process still running"), nil
			}
			return monitorReport(matches, bgID, startedInline, "timeout — partial matches; process still running"), nil
		}
		time.Sleep(monitorPollInterval)
	}
}

func monitorReport(matches []string, bgID string, inline bool, status string) string {
	var b strings.Builder
	if inline {
		b.WriteString("monitored inline command")
	} else {
		b.WriteString("monitored " + bgID)
	}
	b.WriteString(" — " + status + "\n")
	if len(matches) == 0 {
		b.WriteString("(no matching lines)")
	}
	for _, m := range matches {
		b.WriteString(m + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
