package agentproxy

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// parseBashBackground returns (true, command, true) iff input is a Bash tool
// invocation with run_in_background:true. Title is the command's first non-empty
// line, truncated to 120 chars.
func parseBashBackground(input string) (bg bool, title string, ok bool) {
	var v struct {
		Command         string `json:"command"`
		RunInBackground *bool  `json:"run_in_background"`
		Description     string `json:"description"`
	}
	if err := json.Unmarshal([]byte(input), &v); err != nil {
		return false, "", false
	}
	if v.RunInBackground == nil || !*v.RunInBackground {
		return false, "", false
	}
	t := firstNonEmptyLine(v.Command)
	if t == "" {
		t = v.Description
	}
	return true, truncateTitle(t, 120), true
}

// parseSubagentDescription extracts a human-readable label for a Task tool call.
// Prefers `description`, falls back to the first non-empty line of `prompt`.
func parseSubagentDescription(input string) (string, bool) {
	var v struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(input), &v); err != nil {
		return "", false
	}
	if v.Description != "" {
		return truncateTitle(v.Description, 120), true
	}
	if v.Prompt != "" {
		return truncateTitle(firstNonEmptyLine(v.Prompt), 120), true
	}
	return "", false
}

// parseScheduleWakeup parses a ScheduleWakeup tool_use input.
func parseScheduleWakeup(input string) (delay time.Duration, reason string, ok bool) {
	var v struct {
		DelaySeconds *int   `json:"delaySeconds"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(input), &v); err != nil {
		return 0, "", false
	}
	if v.DelaySeconds == nil {
		return 0, "", false
	}
	return time.Duration(*v.DelaySeconds) * time.Second, truncateTitle(v.Reason, 120), true
}

// bashSpawnIDRe matches the shell_id in a Bash[bg] spawn tool_result. The
// claude-code CLI emits content like:
//
//	Command running in background with ID: b5kcffa1v. Output is being written to: ...
//
// The id is alphanumeric (no whitespace, no punctuation other than the
// trailing period that terminates the sentence).
var bashSpawnIDRe = regexp.MustCompile(`Command running in background with ID:\s*([A-Za-z0-9_-]+)`)

// parseBashSpawnResult extracts the shell_id from a Bash[bg] spawn tool_result
// content. Returns ("", false) if the content does not match the spawn ack
// pattern (e.g. the bash was foreground, or the result is the actual command
// output rather than the start ack).
func parseBashSpawnResult(content string) (string, bool) {
	m := bashSpawnIDRe.FindStringSubmatch(content)
	if len(m) < 2 {
		return "", false
	}
	return m[1], true
}

// parseKillBashInput extracts the shell_id from a KillBash tool_use input.
// claude-code's KillBash uses `shell_id` (matching the field returned by the
// Bash[bg] spawn ack). If a future CLI variant emits a different field name,
// add it here with evidence — don't pre-emptively hedge.
func parseKillBashInput(input string) (string, bool) {
	var v struct {
		ShellID string `json:"shell_id"`
	}
	if err := json.Unmarshal([]byte(input), &v); err != nil {
		return "", false
	}
	if v.ShellID == "" {
		return "", false
	}
	return v.ShellID, true
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

func truncateTitle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
