package prompt

import (
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// ImpressionSection renders the cross-session project-impression section
// for the system prompt. It sits in the tail variable area of the prompt
// (after Build's stable sections, before the memory recall section): it is
// read ONCE per session build, so it stays byte-stable across rounds and
// the prompt-cache stable-prefix property holds. Missing or corrupted
// impression files degrade to no section — never a failed startup.
func ImpressionSection(cwd string) string {
	body, ok := tools.LoadImpression(tools.ImpressionPath(cwd))
	if !ok {
		return ""
	}
	return `
# Project impression

A ≤200-character impression of this project distilled from past sessions (技术栈 / 关键决策 / 用户脾气 / 当前阶段). Treat it as background atmosphere: entry-level facts still live in Memory, and it may be outdated — verify against reality before relying on it.

` + body + "\n"
}
