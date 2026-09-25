package lsp

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"
)

// Manager owns the per-workspace set of running servers: lazily started on
// first navigation to a matching extension, shared across tool calls, and
// reaped by Shutdown (wired to the session closer).
type Manager struct {
	mu      sync.Mutex
	cfgs    []ServerConfig
	clients map[string]*Client // config name → running server
	rootDir string
}

// LoadConfig reads <cwd>/.niuniu-agent/lsp.json. Missing file → nil (the
// LSP tool then reports how to configure servers).
func LoadConfig(cwd string) []ServerConfig {
	data, err := os.ReadFile(cwd + "/.niuniu-agent/lsp.json")
	if err != nil {
		return nil
	}
	var cfgs []ServerConfig
	if err := jsonUnmarshal(data, &cfgs); err != nil {
		return nil
	}
	return cfgs
}

// NewManager creates a manager over the given configs.
func NewManager(cfgs []ServerConfig, rootDir string) *Manager {
	return &Manager{cfgs: cfgs, clients: map[string]*Client{}, rootDir: rootDir}
}

// clientFor returns (starting if needed) the server handling path's
// extension.
func (m *Manager) clientFor(ctx context.Context, path string) (*Client, error) {
	ext := strings.ToLower(ext(path))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cfg := range m.cfgs {
		matched := false
		for _, e := range cfg.Extensions {
			if strings.ToLower(e) == ext {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if c := m.clients[cfg.Name]; c != nil {
			return c, nil
		}
		startCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		c, err := Start(startCtx, cfg, m.rootDir)
		if err != nil {
			return nil, err
		}
		m.clients[cfg.Name] = c
		return c, nil
	}
	return nil, errNoServer{ext: ext}
}

type errNoServer struct{ ext string }

func (e errNoServer) Error() string {
	return "no language server configured for " + e.ext +
		" — add one to .niuniu-agent/lsp.json (e.g. [{\"name\":\"go\",\"command\":\"gopls\",\"args\":[\"serve\"],\"extensions\":[\".go\"]}])"
}

// Definition/References/Hover/Symbols route to the right server.
func (m *Manager) Definition(ctx context.Context, path string, line, char int) ([]Location, error) {
	c, err := m.clientFor(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.Definition(ctx, path, line, char)
}

func (m *Manager) References(ctx context.Context, path string, line, char int) ([]Location, error) {
	c, err := m.clientFor(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.References(ctx, path, line, char)
}

func (m *Manager) Hover(ctx context.Context, path string, line, char int) (string, error) {
	c, err := m.clientFor(ctx, path)
	if err != nil {
		return "", err
	}
	return c.Hover(ctx, path, line, char)
}

func (m *Manager) Symbols(ctx context.Context, path string) ([]Symbol, error) {
	c, err := m.clientFor(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.Symbols(ctx, path)
}

// Shutdown reaps every running server (graceful where possible).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.Shutdown()
	}
	m.clients = map[string]*Client{}
}

func ext(path string) string {
	dot := strings.LastIndexByte(path, '.')
	if dot < 0 {
		return ""
	}
	return path[dot:]
}
