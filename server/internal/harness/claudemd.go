package harness

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const harnessSectionStart = "<!-- HARNESS:START -->"
const harnessSectionEnd = "<!-- HARNESS:END -->"

// RemoveLegacyHarnessSection strips the retired HARNESS section from a workspace
// instruction file. Nothing writes that section any more — engineering standards
// reach the agent through generateWorkspaceAgentInstructions (which renders them
// inline) and the BOARD menu below. This exists only to clean up files written by
// an older build, so a stale section does not keep instructing the agent forever.
func RemoveLegacyHarnessSection(wsPath, cliType string) {
	removeSection(wsPath, instructionFileForCLI(cliType), harnessSectionStart, harnessSectionEnd)
}

// Board menu markers (AI-native board, stage 5). The board column "menu" is a
// separate marked section so it can be replaced/removed independently of the
// HARNESS section.
// The board column menu and the autohost "[AUTOHOST_DONE]" guidance share this one
// marker pair: the autohost guidance is appended as its own "## 自动托管" section
// inside the BOARD block (see service.renderAutohostSentinel). They stay semantically
// distinct (separate headings) without introducing a second comment-marker seam that
// some markdown renderers collapse onto a single line.
const boardSectionStart = "<!-- BOARD:START -->"
const boardSectionEnd = "<!-- BOARD:END -->"

// claudeMDMu serializes all read-modify-write operations on CLAUDE.md files
// to prevent concurrent writes from clobbering each other.
var claudeMDMu sync.Mutex

// injectSection is the shared implementation for injecting a marked section
// into a workspace agent instruction file. It handles both insert and replace, and
// returns an error if the file has a corrupted marker (start without end).
func injectSection(wsPath, fileName, startMarker, endMarker, section string) error {
	claudeMDMu.Lock()
	defer claudeMDMu.Unlock()

	claudePath := filepath.Join(wsPath, fileName)
	existing, _ := os.ReadFile(claudePath)
	content := string(existing)

	wrapped := "\n" + startMarker + "\n" + section + "\n" + endMarker + "\n"

	if startIdx := strings.Index(content, startMarker); startIdx >= 0 {
		endIdx := strings.Index(content, endMarker)
		if endIdx < 0 {
			// Corrupted: start marker without end marker. Remove the partial content
			// from startIdx to EOF, then append the new section.
			content = strings.TrimRight(content[:startIdx], "\n") + wrapped
		} else {
			end := endIdx + len(endMarker)
			if end < len(content) && content[end] == '\n' {
				end++
			}
			content = content[:startIdx] + wrapped + content[end:]
		}
	} else {
		content += wrapped
	}

	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write %s: %w", fileName, err)
	}
	return nil
}

// removeSection is the shared implementation for removing a marked section
// from a workspace agent instruction file.
func removeSection(wsPath, fileName, startMarker, endMarker string) {
	claudeMDMu.Lock()
	defer claudeMDMu.Unlock()

	claudePath := filepath.Join(wsPath, fileName)
	data, err := os.ReadFile(claudePath)
	if err != nil {
		return
	}
	content := string(data)
	startIdx := strings.Index(content, startMarker)
	if startIdx < 0 {
		return
	}
	endIdx := strings.Index(content, endMarker)
	if endIdx < 0 {
		// Corrupted: start without end. Remove from startIdx to EOF.
		content = strings.TrimRight(content[:startIdx], "\n") + "\n"
	} else {
		end := endIdx + len(endMarker)
		if end < len(content) && content[end] == '\n' {
			end++
		}
		content = content[:startIdx] + content[end:]
	}
	os.WriteFile(claudePath, []byte(content), 0644)
}

// InjectBoardSection writes the AI-native board column menu into the instruction
// file read by the workspace's configured CLI (Codex: AGENTS.md, Claude: CLAUDE.md),
// wrapped in BOARD markers so it can be replaced or removed independently of the
// harness section. Re-injecting replaces the existing board section in place.
func InjectBoardSection(wsPath, cliType, section string) error {
	fileName := instructionFileForCLI(cliType)
	if err := injectSection(wsPath, fileName, boardSectionStart, boardSectionEnd, section); err != nil {
		return err
	}
	slog.Info("board: injected column menu into agent instructions", "wsPath", wsPath, "file", fileName)
	return nil
}

// RemoveBoardSection removes the board column menu from the workspace's CLI
// instruction file.
func RemoveBoardSection(wsPath, cliType string) {
	fileName := instructionFileForCLI(cliType)
	removeSection(wsPath, fileName, boardSectionStart, boardSectionEnd)
	slog.Info("board: removed column menu from agent instructions", "wsPath", wsPath, "file", fileName)
}

func instructionFileForCLI(cliType string) string {
	switch cliType {
	case "codex":
		return "AGENTS.md"
	case "qwen":
		// Must match the primary instruction file written for qwen workspaces in
		// WorkspaceService.generateWorkspaceAgentInstructions, so the board menu
		// lands where the qwen agent actually reads it.
		return "QWEN.md"
	case "omp":
		// Same sync requirement as qwen — see generateWorkspaceAgentInstructions.
		return "OMP.md"
	case "goose":
		return "GOOSE.md"
	case "cursor":
		// cursor-agent reads AGENTS.md, like Codex.
		return "AGENTS.md"
	default:
		return "CLAUDE.md"
	}
}

// GenerateCLAUDEMDRules produces a markdown section for active harness specs.
func GenerateCLAUDEMDRules(specs []Spec) string {
	// Filter to enabled specs only
	var enabled []Spec
	for _, s := range specs {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	if len(enabled) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n## Engineering Standards (Harness)\n\n")
	sb.WriteString("The following engineering standards are enforced. Violations with severity `error` block phase transitions.\n\n")

	// Category titles
	cats := map[string]string{
		"commit":   "Commit Standards",
		"quality":  "Code Quality",
		"workflow": "Development Workflow",
		"agent":    "Agent Behavior",
	}

	// Group by category
	grouped := make(map[string][]Spec)
	for _, s := range enabled {
		grouped[s.Category] = append(grouped[s.Category], s)
	}

	// Render in fixed order
	for _, cat := range []string{"commit", "quality", "workflow", "agent"} {
		catSpecs, ok := grouped[cat]
		if !ok {
			continue
		}
		fmt.Fprintf(&sb, "### %s\n\n", cats[cat])
		for _, s := range catSpecs {
			level := "[MAY]"
			switch s.Severity {
			case "error":
				level = "[MUST]"
			case "warning":
				level = "[SHOULD]"
			}
			fmt.Fprintf(&sb, "- %s **%s** (%s)\n", level, s.Name, s.Severity)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
