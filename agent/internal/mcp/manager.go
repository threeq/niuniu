package mcp

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// connectTimeout bounds each server's spawn + initialize handshake.
const connectTimeout = 30 * time.Second

// Manager owns the MCP servers started for one session (cwd).
type Manager struct {
	clients map[string]*Client
}

// Start reads <cwd>/.mcp.json and connects every configured server. A
// missing manifest yields an empty manager; a broken manifest or a failing
// server is logged and skipped — never fatal, and healthy servers still
// come up. Callers own the returned manager's lifetime (Close when the
// session ends).
func Start(cwd string) *Manager {
	m := &Manager{clients: make(map[string]*Client)}
	cfgs, err := LoadConfig(filepath.Join(cwd, ConfigFileName))
	if err != nil {
		slog.Warn("mcp: ignoring bad manifest", "path", ConfigFileName, "err", err)
		return m
	}
	for name, cfg := range cfgs {
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		c, err := Connect(ctx, name, cfg)
		cancel()
		if err != nil {
			slog.Warn("mcp: server failed to start (skipped)", "server", name, "err", err)
			continue
		}
		m.clients[name] = c
	}
	return m
}

// RegisterInto registers every healthy server's tools into reg.
func (m *Manager) RegisterInto(reg *tools.Registry) {
	for _, c := range m.clients {
		c.RegisterInto(reg)
	}
}

// ToolNames returns the registered tool names (mcp__<server>__<tool>),
// mainly for diagnostics and tests.
func (m *Manager) ToolNames() map[string]bool {
	out := make(map[string]bool)
	for _, c := range m.clients {
		for _, def := range c.ToolDefs() {
			out[ToolName(c.name, def.Name)] = true
		}
	}
	return out
}

// Close shuts every server down. Safe to call more than once. Implements
// io.Closer so hosts can tie server lifetime to a session's.
func (m *Manager) Close() error {
	for name, c := range m.clients {
		_ = c.Close()
		delete(m.clients, name)
	}
	return nil
}
