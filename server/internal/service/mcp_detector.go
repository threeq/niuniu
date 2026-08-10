package service

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// MCPDetectResult is what the detector returns and what the API exposes.
type MCPDetectResult struct {
	Recommended     []string             `json:"recommended"`
	All             []KnownMCP           `json:"all"`
	PluginConflicts []PluginConflictInfo `json:"plugin_conflicts"`
}

type PluginConflictInfo struct {
	MCPName    string `json:"mcp_name"`
	PluginName string `json:"plugin_name"`
	MessageKey string `json:"message_key"`
}

// WorkspaceMCPDetector scans repo paths and recommends MCP servers based on
// detected language/framework signals, intersected with the user's installed
// MCPs (from ClaudeMCPRegistry).
type WorkspaceMCPDetector struct {
	registry *ClaudeMCPRegistry
}

func NewWorkspaceMCPDetector(r *ClaudeMCPRegistry) *WorkspaceMCPDetector {
	return &WorkspaceMCPDetector{registry: r}
}

const maxScanDepth = 3

var skipDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true,
	"node_modules": true, "vendor": true,
	"dist": true, "build": true, "out": true, "target": true,
	".next": true, ".nuxt": true, ".turbo": true,
	"__pycache__": true, ".venv": true, "venv": true,
	".idea": true, ".vscode": true,
	".worktrees": true,
}

// Detect runs scan + intersection.
func (d *WorkspaceMCPDetector) Detect(repoPaths []string, configDir string) (*MCPDetectResult, error) {
	known, _ := d.registry.List(configDir) // ignore err — empty registry is graceful
	registryNames := make(map[string]KnownMCP, len(known))
	for _, k := range known {
		registryNames[k.Name] = k
	}

	signals := map[string]bool{}
	for _, p := range repoPaths {
		if p == "" {
			continue
		}
		scanRepo(p, 0, signals)
	}

	// signal → capability
	caps := map[string]bool{}
	for s := range signals {
		mappings, ok := signalToCapabilities[s]
		if !ok {
			// Silent drift guard: a new scan rule shipped without wiring the
			// capability map would otherwise produce empty recommendations
			// with zero diagnostic. Debug-level so it doesn't spam normal runs.
			slog.Debug("mcp_detector: signal has no capability mapping", "signal", s)
			continue
		}
		for _, c := range mappings {
			caps[c] = true
		}
	}

	// capability → MCP name, then intersect with registry
	recSet := map[string]bool{}
	for c := range caps {
		for _, name := range capabilityToMCPNames[c] {
			if _, ok := registryNames[name]; ok {
				recSet[name] = true
			}
		}
	}
	recommended := make([]string, 0, len(recSet))
	for n := range recSet {
		recommended = append(recommended, n)
	}

	// plugin conflicts (always-loaded MCPs that are plugin-sourced)
	var conflicts []PluginConflictInfo
	for _, k := range known {
		if k.Source == MCPSourcePlugin {
			conflicts = append(conflicts, PluginConflictInfo{
				MCPName:    k.Name,
				PluginName: k.PluginName,
				MessageKey: "mcp.plugin_conflict.global_load",
			})
		}
	}

	return &MCPDetectResult{
		Recommended:     recommended,
		All:             known,
		PluginConflicts: conflicts,
	}, nil
}

var signalToCapabilities = map[string][]string{
	"frontend-web":   {"browser-automation", "frontend-debugging", "library-docs"},
	"nodejs":         {"library-docs"},
	"go-backend":     {"library-docs"},
	"python-backend": {"library-docs"},
	"rust-backend":   {"library-docs"},
	"pencil-design":  {"pencil-design"},
}

var capabilityToMCPNames = map[string][]string{
	"browser-automation": {"playwright"},
	"frontend-debugging": {"chrome-devtools"},
	"library-docs":       {"context7"},
	"pencil-design":      {"pencil"},
}

// scanRepo walks the repo tree up to maxScanDepth and records signals it finds.
func scanRepo(root string, depth int, out map[string]bool) {
	if depth > maxScanDepth {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	// Check this directory's signal files
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case name == "go.mod":
			out["go-backend"] = true
		case name == "pyproject.toml", name == "requirements.txt", name == "Pipfile":
			out["python-backend"] = true
		case name == "Cargo.toml":
			out["rust-backend"] = true
		case name == "package.json":
			classifyPackageJSON(filepath.Join(root, name), out)
		case strings.HasSuffix(strings.ToLower(name), ".pen"):
			out["pencil-design"] = true
		}
	}
	// Descend into subdirs (filtered)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if skipDirs[e.Name()] {
			continue
		}
		scanRepo(filepath.Join(root, e.Name()), depth+1, out)
	}
}

func classifyPackageJSON(path string, out map[string]bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		out["nodejs"] = true
		return
	}
	s := strings.ToLower(string(b))
	for _, kw := range []string{`"react"`, `"vue"`, `"svelte"`, `"@playwright"`, `"vite"`} {
		if strings.Contains(s, kw) {
			out["frontend-web"] = true
			return
		}
	}
	out["nodejs"] = true
}
