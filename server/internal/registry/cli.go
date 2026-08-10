package registry

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// CLISource discovers agents by running `claude agents` and reading
// plugin agent files from the Claude plugin cache directory.
type CLISource struct {
	claudeCmd string // path or name of the claude binary
	homeDir   string

	mu     sync.RWMutex
	cached []AgentInfo
}

func NewCLISource(claudeCmd string) *CLISource {
	home, _ := os.UserHomeDir()
	return &CLISource{
		claudeCmd: claudeCmd,
		homeDir:   home,
	}
}

func (s *CLISource) Type() string { return "local" }

func (s *CLISource) List(ctx context.Context) ([]AgentInfo, error) {
	s.mu.RLock()
	if s.cached != nil {
		defer s.mu.RUnlock()
		return s.cached, nil
	}
	s.mu.RUnlock()

	return s.refresh(ctx)
}

func (s *CLISource) Get(ctx context.Context, name string) (*AgentDetail, error) {
	agents, err := s.List(ctx)
	if err != nil {
		return nil, err
	}

	var info *AgentInfo
	for i := range agents {
		if agents[i].Name == name {
			info = &agents[i]
			break
		}
	}
	if info == nil {
		return nil, fmt.Errorf("agent %q not found", name)
	}

	if info.FilePath != "" {
		data, err := os.ReadFile(info.FilePath)
		if err != nil {
			return nil, fmt.Errorf("read agent file %s: %w", info.FilePath, err)
		}
		_, body, err := parseFrontmatter(string(data))
		if err != nil {
			return nil, fmt.Errorf("parse agent file %s: %w", info.FilePath, err)
		}
		return &AgentDetail{
			AgentInfo: *info,
			Content:   body,
		}, nil
	}

	// Built-in agent without file
	return &AgentDetail{
		AgentInfo: *info,
		Content:   fmt.Sprintf("Built-in agent: %s", name),
	}, nil
}

func (s *CLISource) Refresh(ctx context.Context) error {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()

	_, err := s.refresh(ctx)
	return err
}

func (s *CLISource) refresh(ctx context.Context) ([]AgentInfo, error) {
	fileIndex := s.buildFileIndex()

	cliAgents, cliOK := s.runCLI(ctx)
	if cliOK && len(cliAgents) > 0 {
		for i := range cliAgents {
			a := &cliAgents[i]
			shortName := a.Name
			if idx := strings.Index(a.Name, ":"); idx > 0 {
				shortName = a.Name[idx+1:]
			}
			if fi, ok := fileIndex[shortName]; ok {
				if a.Description == "" {
					a.Description = fi.description
				}
				a.FilePath = fi.filePath
			}
		}
	} else {
		cliAgents = agentsFromFileIndex(fileIndex)
	}

	s.mu.Lock()
	s.cached = cliAgents
	s.mu.Unlock()

	return cliAgents, nil
}

// runCLI invokes `claude agents` and parses its output. On any error
// (subprocess failure, exit non-zero, or the "not available" sentinel)
// it returns ok=false so the caller can fall back to the filesystem
// index. Claude Code v2.1.141's `agents` subcommand manages background
// agents and is gated behind a feature flag — when disabled it prints
// `'claude agents' is not available in this environment.` and exits 1,
// which is unrelated to the custom-agent listing this source surfaces.
func (s *CLISource) runCLI(ctx context.Context) ([]AgentInfo, bool) {
	cmd := exec.CommandContext(ctx, s.claudeCmd, "agents")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		slog.DebugContext(ctx, "claude agents subprocess failed; falling back to filesystem index",
			"err", err,
			"stderr", strings.TrimSpace(stderr.String()),
		)
		return nil, false
	}
	combined := string(out) + stderr.String()
	if strings.Contains(combined, "is not available in this environment") {
		slog.DebugContext(ctx, "claude agents subcommand unavailable; falling back to filesystem index")
		return nil, false
	}
	return parseCLIOutput(string(out)), true
}

// agentsFromFileIndex synthesizes AgentInfo records purely from the
// filesystem index (used when `claude agents` cannot enumerate them).
// Names follow the `<plugin>:<short>` convention the CLI itself used.
func agentsFromFileIndex(index map[string]fileInfo) []AgentInfo {
	keys := make([]string, 0, len(index))
	for k := range index {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	agents := make([]AgentInfo, 0, len(index))
	for _, shortName := range keys {
		fi := index[shortName]
		fullName := shortName
		if fi.plugin != "" {
			fullName = fi.plugin + ":" + shortName
		}
		info := AgentInfo{
			Source:      "local",
			Name:        fullName,
			Description: fi.description,
			FilePath:    fi.filePath,
			Tags:        []string{"plugin"},
		}
		if fi.plugin != "" {
			info.Author = fi.plugin
		}
		if fi.model != "" {
			info.Tags = append(info.Tags, "model:"+fi.model)
		}
		agents = append(agents, info)
	}
	return agents
}

// parseCLIOutput parses the text output of `claude agents`.
// Format:
//
//	Plugin agents:
//	  everything-claude-code:architect · opus
//	Built-in agents:
//	  claude-code-guide · haiku
func parseCLIOutput(output string) []AgentInfo {
	var agents []AgentInfo
	section := ""

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Plugin agents:") {
			section = "plugin"
			continue
		}
		if strings.HasPrefix(trimmed, "Built-in agents:") {
			section = "builtin"
			continue
		}
		if strings.HasSuffix(trimmed, "active agents") {
			continue
		}

		// Parse "name · model"
		parts := strings.SplitN(trimmed, "·", 2)
		if len(parts) != 2 {
			continue
		}
		fullName := strings.TrimSpace(parts[0])
		model := strings.TrimSpace(parts[1])

		info := AgentInfo{
			Source: "local",
			Name:   fullName, // full qualified name: "everything-claude-code:architect"
			Tags:   []string{"model:" + model},
		}

		if section == "plugin" {
			if colonIdx := strings.Index(fullName, ":"); colonIdx > 0 {
				info.Author = fullName[:colonIdx]
			}
			info.Tags = append(info.Tags, "plugin")
		} else {
			info.Tags = append(info.Tags, "builtin")
		}

		agents = append(agents, info)
	}

	return agents
}

type fileInfo struct {
	description string
	filePath    string
	plugin      string
	model       string
}

// buildFileIndex scans ~/.claude/plugins/cache/*/agents/*.md
// to build a lookup from agent-name → file info.
func (s *CLISource) buildFileIndex() map[string]fileInfo {
	index := make(map[string]fileInfo)

	pluginCacheDir := filepath.Join(s.homeDir, ".claude", "plugins", "cache")
	publishers, err := os.ReadDir(pluginCacheDir)
	if err != nil {
		return index
	}

	for _, pub := range publishers {
		if !pub.IsDir() {
			continue
		}
		pubDir := filepath.Join(pluginCacheDir, pub.Name())
		plugins, err := os.ReadDir(pubDir)
		if err != nil {
			continue
		}
		for _, plugin := range plugins {
			if !plugin.IsDir() {
				continue
			}
			pluginDir := filepath.Join(pubDir, plugin.Name())
			versions, err := os.ReadDir(pluginDir)
			if err != nil {
				continue
			}
			// Use the latest version directory (last when sorted)
			var latestVer string
			for _, v := range versions {
				if v.IsDir() && v.Name() != "unknown" {
					latestVer = v.Name()
				}
			}
			if latestVer == "" {
				continue
			}
			agentsDir := filepath.Join(pluginDir, latestVer, "agents")
			s.scanAgentsDir(agentsDir, plugin.Name(), index)
		}
	}

	return index
}

func (s *CLISource) scanAgentsDir(dir, pluginName string, index map[string]fileInfo) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		filePath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		fm, _, err := parseFrontmatter(string(data))
		if err != nil {
			continue
		}
		agentName := fm["name"]
		if agentName == "" {
			agentName = strings.TrimSuffix(entry.Name(), ".md")
		}
		// Don't overwrite — first found wins (earlier version dirs are scanned first)
		if _, exists := index[agentName]; !exists {
			index[agentName] = fileInfo{
				description: fm["description"],
				filePath:    filePath,
				plugin:      pluginName,
				model:       fm["model"],
			}
		}
	}
}
