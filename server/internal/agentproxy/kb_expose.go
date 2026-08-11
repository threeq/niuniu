package agentproxy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KBDatasetDir is a bound knowledge base's read-only content directory exposed
// to the workspace agent for direct Read/Grep/Glob (KB base4, the "C" ability).
// It mirrors service.KBDatasetDir; the field set is duplicated here so this
// package stays import-light (service already imports agentproxy, so the reverse
// would form a cycle — same pattern as MCPGenerateResult).
type KBDatasetDir struct {
	Name        string
	Description string
	Root        string // absolute on-disk directory; read-only
}

// KBDatasetResolver resolves the read-only dataset directories of the knowledge
// bases bound to a workspace's project. Implemented by service.KBService via the
// NewAgentProxyKBResolver shim and injected with SetKBResolver. Resolving at
// spawn (rather than at workspace creation) means the exposed set always tracks
// the workspace's current KB bindings.
type KBDatasetResolver interface {
	WorkspaceDatasetDirs(ctx context.Context, workspaceID int64) ([]KBDatasetDir, error)
}

// kbDatasetRoots extracts the absolute roots from a slice of dataset dirs, in
// order, for use as --add-dir arguments and read-only deny inputs.
func kbDatasetRoots(dirs []KBDatasetDir) []string {
	if len(dirs) == 0 {
		return nil
	}
	roots := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if r := strings.TrimSpace(d.Root); r != "" {
			roots = append(roots, r)
		}
	}
	return roots
}

const (
	kbBlockBegin = "<!-- BEGIN niuniu:kb-datasets -->"
	kbBlockEnd   = "<!-- END niuniu:kb-datasets -->"
)

// kbInstructionFile returns the primary agent-instruction filename for a CLI
// type, matching generateWorkspaceAgentInstructions' choice (codex→AGENTS.md,
// qwen→QWEN.md, default→CLAUDE.md).
func kbInstructionFile(cliType string) string {
	switch cliType {
	case "codex":
		return "AGENTS.md"
	case "qwen":
		return "QWEN.md"
	case "omp":
		return "OMP.md"
	case "goose":
		return "GOOSE.md"
	default:
		return "CLAUDE.md"
	}
}

// renderKBBlock builds the managed instruction section telling the agent where
// the bound knowledge bases are mounted read-only and how to read them. Returns
// "" when there are no dataset dirs (the block is then removed entirely).
func renderKBBlock(dirs []KBDatasetDir) string {
	if len(dirs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(kbBlockBegin + "\n")
	sb.WriteString("## 知识库 · 可直读目录（只读 / read-only）\n\n")
	sb.WriteString("以下知识库已绑定到本工作空间，其内容目录已**只读**挂载给你。" +
		"可直接用 `Read` / `Grep` / `Glob` 读取全文、精确检索、按结构浏览" +
		"（与 `kb_search` 互补：检索用 kb_search，读全文/精确定位用直读）。\n\n")
	sb.WriteString("⚠️ 这些目录**只读**：请勿写入、修改或删除其中任何文件。\n\n")
	for _, d := range dirs {
		line := fmt.Sprintf("- **%s** — `%s`", d.Name, filepath.ToSlash(d.Root))
		if desc := strings.TrimSpace(d.Description); desc != "" {
			line += " — " + desc
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString(kbBlockEnd + "\n")
	return sb.String()
}

// upsertKBBlock replaces any existing managed KB block in content with block,
// or appends it when absent. An empty block removes the section. Returns the new
// content. Pure (no I/O) so it is unit-testable.
func upsertKBBlock(content, block string) string {
	start := strings.Index(content, kbBlockBegin)
	if start >= 0 {
		if end := strings.Index(content[start:], kbBlockEnd); end >= 0 {
			end = start + end + len(kbBlockEnd)
			// Swallow a single trailing newline so repeated upserts don't grow
			// blank lines.
			tail := content[end:]
			tail = strings.TrimPrefix(tail, "\n")
			before := strings.TrimRight(content[:start], "\n")
			if block == "" {
				if before == "" {
					return tail
				}
				if tail == "" {
					return before + "\n"
				}
				return before + "\n\n" + tail
			}
			if before == "" {
				if tail == "" {
					return block
				}
				return block + "\n" + tail
			}
			if tail == "" {
				return before + "\n\n" + block
			}
			return before + "\n\n" + block + "\n" + tail
		}
	}
	if block == "" {
		return content
	}
	trimmed := strings.TrimRight(content, "\n")
	if trimmed == "" {
		return block
	}
	return trimmed + "\n\n" + block
}

// writeKBInstructionBlock refreshes the managed KB section in the workspace's
// primary agent-instruction file so the agent is told, at every spawn, which
// knowledge bases it can directly read and that they are read-only. Best-effort:
// a missing instruction file is created with just the block; any error is
// returned for the caller to log without failing the spawn.
func writeKBInstructionBlock(workDir, cliType string, dirs []KBDatasetDir) error {
	path := filepath.Join(workDir, kbInstructionFile(cliType))
	block := renderKBBlock(dirs)

	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read instruction file: %w", err)
		}
		if block == "" {
			return nil // nothing to write, nothing existed
		}
		return os.WriteFile(path, []byte(block+"\n"), 0o644)
	}
	updated := upsertKBBlock(string(existing), block)
	if updated == string(existing) {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}
