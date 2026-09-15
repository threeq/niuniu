// Package mcp implements a zero-dependency Model Context Protocol (MCP)
// client over stdio: newline-delimited JSON-RPC 2.0, speaking the
// initialize / tools/list / tools/call subset. At session start the agent
// reads <cwd>/.mcp.json (the same projection niuniu's sceneenv writes for
// Claude Code), spawns every configured server as a child process, and
// exposes its tools to the model as mcp__<server>__<tool>.
//
// One server failing to start must never block the others — Start logs and
// continues; healthy servers still register.
package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ServerConfig is one stdio server entry in .mcp.json.
type ServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// ConfigFileName is the per-workspace server manifest niuniu projects.
const ConfigFileName = ".mcp.json"

// LoadConfig parses a .mcp.json manifest. A missing file is not an error —
// it just means no servers (most sessions have none).
func LoadConfig(path string) (map[string]ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]ServerConfig{}, nil
		}
		return nil, err
	}
	var raw struct {
		MCPServers map[string]ServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("mcp: parse %s: %w", path, err)
	}
	for name, sc := range raw.MCPServers {
		if strings.TrimSpace(sc.Command) == "" {
			return nil, fmt.Errorf("mcp: server %q: command is required", name)
		}
	}
	return raw.MCPServers, nil
}
