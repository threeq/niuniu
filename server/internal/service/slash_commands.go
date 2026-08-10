package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/config"
)

// SlashCommand represents a discovered slash command from plugins/skills.
type SlashCommand struct {
	Name        string `json:"name"`        // e.g. "commit", "superpowers:brainstorming"
	Description string `json:"description"` // from frontmatter
	Source      string `json:"source"`      // "builtin", "command", "skill"
	Plugin      string `json:"plugin"`      // plugin id, empty for builtins
}

type SlashCommandService struct {
	cfg *config.AgentConfig
}

func NewSlashCommandService(cfg *config.AgentConfig) *SlashCommandService {
	return &SlashCommandService{cfg: cfg}
}

// builtinCommands are interactive-only CLI commands. They don't work in -p mode,
// so the frontend must handle them locally instead of sending to the agent.
//
// Commands fall into three categories:
//   - "local":      handled entirely by the frontend (e.g. /clear, /cost)
//   - "translate":  translated to a natural language prompt and sent to the agent (e.g. /review, /compact)
//   - "unsupported": not available in workspace chat — shows an info message
var builtinCommands = []SlashCommand{
	// --- locally handled ---
	{Name: "clear", Description: "清除当前会话，重新开始新对话", Source: "builtin"},
	{Name: "cost", Description: "显示当前会话的 API 费用统计", Source: "builtin"},
	{Name: "status", Description: "显示 Agent 当前运行状态和会话信息", Source: "builtin"},
	{Name: "model", Description: "查看或切换当前使用的模型", Source: "builtin"},
	{Name: "help", Description: "显示可用命令帮助（工作空间模式）", Source: "builtin"},

	// --- translated to prompts ---
	{Name: "compact", Description: "压缩对话上下文，释放 token 空间以继续长对话", Source: "builtin"},
	{Name: "review", Description: "让 Agent 审查当前工作空间的代码变更", Source: "builtin"},
	{Name: "memory", Description: "让 Agent 查看或管理 CLAUDE.md 记忆文件", Source: "builtin"},
	{Name: "init", Description: "让 Agent 在工作空间中初始化 CLAUDE.md", Source: "builtin"},

	// --- unsupported in -p mode (show info message, listed last) ---
	{Name: "doctor", Description: "⚠️ 仅在 Claude CLI 交互模式下可用", Source: "builtin"},
	{Name: "bug", Description: "⚠️ 仅在 Claude CLI 交互模式下可用", Source: "builtin"},
	{Name: "vim", Description: "⚠️ 仅在 Claude CLI 交互模式下可用", Source: "builtin"},
	{Name: "history", Description: "⚠️ 仅在 Claude CLI 交互模式下可用", Source: "builtin"},
	{Name: "login", Description: "⚠️ 仅在 Claude CLI 交互模式下可用", Source: "builtin"},
	{Name: "logout", Description: "⚠️ 仅在 Claude CLI 交互模式下可用", Source: "builtin"},
	{Name: "config", Description: "⚠️ 仅在 Claude CLI 交互模式下可用", Source: "builtin"},
	{Name: "permissions", Description: "⚠️ 仅在 Claude CLI 交互模式下可用", Source: "builtin"},
	{Name: "mcp", Description: "⚠️ 仅在 Claude CLI 交互模式下可用", Source: "builtin"},
}

// codexBuiltinCommands is the curated list of slash commands always offered to
// codex workspaces in the chat-input autocomplete. We intentionally do NOT
// hardcode superpowers:* entries here even though some users install the
// `superpowers` plugin globally in their codex install: niuniu has no codex
// plugin discovery (cf. listPlugins, which is claude-only), and the user-side
// global install already publishes `/spec` / `/plan` / `/review` / `/coding`
// plus the `superpowers:*` aliases through codex itself, so duplicating them
// in this list would either lie (when the plugin is not installed) or
// double-list (when it is). Users can still type those commands manually and
// codex will handle them.
var codexBuiltinCommands = []SlashCommand{
	{Name: "clear", Description: "清除当前会话，重新开始新对话", Source: "builtin"},
	{Name: "cost", Description: "显示当前会话的费用统计", Source: "builtin"},
	{Name: "status", Description: "显示 Agent 当前运行状态和会话信息", Source: "builtin"},
	{Name: "model", Description: "查看当前使用的模型", Source: "builtin"},
	{Name: "help", Description: "显示可用命令帮助（工作空间模式）", Source: "builtin"},
	{Name: "compact", Description: "压缩对话上下文，释放 token 空间以继续长对话", Source: "builtin"},
	{Name: "review", Description: "让 Agent 审查当前工作空间的代码变更", Source: "builtin"},
	{Name: "memory", Description: "让 Agent 查看或管理 AGENTS.md 记忆文件", Source: "builtin"},
	{Name: "init", Description: "让 Agent 在工作空间中初始化 AGENTS.md", Source: "builtin"},
}

type pluginInfo struct {
	ID          string `json:"id"`
	InstallPath string `json:"installPath"`
	Enabled     bool   `json:"enabled"`
}

// ListCommands discovers all available slash commands.
func (s *SlashCommandService) ListCommands(ctx context.Context, agentCommand, configDir string) ([]SlashCommand, error) {
	if isCodexCommand(agentCommand) {
		commands := make([]SlashCommand, len(codexBuiltinCommands))
		copy(commands, codexBuiltinCommands)
		return commands, nil
	}

	commands := make([]SlashCommand, len(builtinCommands))
	copy(commands, builtinCommands)

	// Get installed plugins via claude CLI
	plugins, err := s.listPlugins(ctx, agentCommand, configDir)
	if err != nil {
		// Return builtins even if plugin discovery fails
		return commands, nil
	}

	for _, p := range plugins {
		if !p.Enabled || p.InstallPath == "" {
			continue
		}

		pluginName := pluginShortName(p.ID)

		// Scan commands/ directory
		cmdsDir := filepath.Join(p.InstallPath, "commands")
		if entries, err := os.ReadDir(cmdsDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
					continue
				}
				name := strings.TrimSuffix(entry.Name(), ".md")
				desc := readFrontmatterField(filepath.Join(cmdsDir, entry.Name()), "description")
				commands = append(commands, SlashCommand{
					Name:        name,
					Description: desc,
					Source:      "command",
					Plugin:      pluginName,
				})
			}
		}

		// Scan skills/ directory
		skillsDir := filepath.Join(p.InstallPath, "skills")
		if entries, err := os.ReadDir(skillsDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				skillFile := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
				if _, err := os.Stat(skillFile); err != nil {
					continue
				}
				name := readFrontmatterField(skillFile, "name")
				if name == "" {
					name = entry.Name()
				}
				desc := readFrontmatterField(skillFile, "description")
				// Skip deprecated skills
				if strings.Contains(strings.ToLower(desc), "deprecated") {
					continue
				}
				commands = append(commands, SlashCommand{
					Name:        pluginName + ":" + name,
					Description: desc,
					Source:      "skill",
					Plugin:      pluginName,
				})
			}
		}
	}

	return commands, nil
}

func (s *SlashCommandService) listPlugins(ctx context.Context, agentCommand, configDir string) ([]pluginInfo, error) {
	if agentCommand == "" {
		if s.cfg != nil {
			agentCommand = s.cfg.ClaudeCode.Command
		}
	}
	if agentCommand == "" {
		agentCommand = "claude"
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, agentCommand, "plugin", "list", "--json")
	if configDir != "" {
		cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("plugin list: %w", err)
	}

	var plugins []pluginInfo
	if err := json.Unmarshal(out, &plugins); err != nil {
		return nil, fmt.Errorf("parse plugins: %w", err)
	}
	return plugins, nil
}

func isCodexCommand(agentCommand string) bool {
	base := strings.ToLower(filepath.Base(agentCommand))
	return strings.HasPrefix(base, "codex")
}

// pluginShortName extracts "superpowers" from "superpowers@claude-plugins-official"
func pluginShortName(id string) string {
	if idx := strings.Index(id, "@"); idx > 0 {
		return id[:idx]
	}
	return id
}

// readFrontmatterField reads a field from YAML frontmatter in a markdown file.
func readFrontmatterField(path, field string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	inFrontmatter := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			if inFrontmatter {
				return "" // end of frontmatter, field not found
			}
			inFrontmatter = true
			continue
		}
		if !inFrontmatter {
			continue
		}
		// Simple YAML parsing: "key: value" or 'key: "value"'
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key != field {
			continue
		}
		val := strings.TrimSpace(parts[1])
		// Remove surrounding quotes
		val = strings.Trim(val, `"'`)
		return val
	}
	return ""
}
