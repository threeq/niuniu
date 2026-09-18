package agentproxy

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"
)

// A user message over largeInputRuneLimit characters would blow a huge hole in
// the agent's context window when sent verbatim — and worse, the message is
// PERSISTED in the chat history, so every later `--resume` replays it into the
// context again. Stamping only the outbound copy would therefore be pointless:
// the stored message itself must be the short pointer prompt. So when the text
// exceeds the limit, rewriteLargeInput writes the original to
// <workDir>/.team/prompts/prompt-<ts>.txt (the agent's cwd, .team/ being the
// existing niuniu-managed dir convention) and returns a short prompt asking the
// agent to Read that file and follow what's inside.
//
// Fail-open: any filesystem error logs a warning and returns the original text
// — a degraded oversized send beats a blocked one.

// largeInputRuneLimit counts CHARACTERS (runes), not bytes — CJK input is the
// common big-paste case and 3 bytes/char would otherwise trigger at ~33k chars.
const largeInputRuneLimit = 100_000

const largeInputPromptFmt = `[niuniu] 本条用户消息原文较长（约 %d 字符），为避免占满你的上下文窗口，已自动保存到工作区文件 %q。

请先用 Read 工具读取该文件（内容超过单次读取上限时分段读完全部），然后严格按照文件中的内容与指令执行，不要遗漏其中任何要求。`

// rewriteLargeInput returns the content to persist+send for a user turn: the
// original text when it is within the limit, otherwise a short prompt pointing
// at the file the original was spilled to. workspaceID only labels the log
// lines; now is injected for tests.
func rewriteLargeInput(workspaceID int64, workDir, content string, now time.Time) string {
	runes := utf8.RuneCountInString(content)
	if runes <= largeInputRuneLimit {
		return content
	}
	relDir := filepath.Join(".team", "prompts")
	if err := os.MkdirAll(filepath.Join(workDir, relDir), 0o755); err != nil {
		slog.Warn("agent: large input spill dir failed, sending original",
			"workspaceID", workspaceID, "dir", relDir, "error", err)
		return content
	}
	name := fmt.Sprintf("prompt-%s.txt", now.Format("20060102-150405.000"))
	relPath := filepath.Join(relDir, name)
	if err := os.WriteFile(filepath.Join(workDir, relPath), []byte(content), 0o644); err != nil {
		slog.Warn("agent: large input spill write failed, sending original",
			"workspaceID", workspaceID, "path", relPath, "error", err)
		return content
	}
	// Forward slashes in the PROMPT (not the on-disk write): backslash paths
	// leak into bash quoting and Read-tool args on Windows; agents on every OS
	// accept the forward-slash form relative to their cwd.
	prompt := fmt.Sprintf(largeInputPromptFmt, runes, filepath.ToSlash(relPath))
	slog.Info("agent: large input spilled to file",
		"workspaceID", workspaceID, "path", relPath, "runes", runes)
	return prompt
}
