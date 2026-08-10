package service

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestWorkspaceMCPDetector_Detect_MonorepoMix(t *testing.T) {
	// 1. Build a fake monorepo
	repoA := t.TempDir() // go backend
	mustWrite(t, filepath.Join(repoA, "go.mod"), "module example.com/a\n")

	repoB := t.TempDir() // React frontend
	mustWrite(t, filepath.Join(repoB, "package.json"), `{
	  "dependencies": {"react": "^19", "vite": "^5"}
	}`)

	// 2. Set up a fake claude install that has playwright, context7, pencil
	configRoot := t.TempDir()
	mustWriteJSON(t, filepath.Join(configRoot, ".claude.json"), map[string]any{
		"mcpServers": map[string]any{
			"playwright":      map[string]any{"command": "npx", "args": []string{"@playwright/mcp@latest"}},
			"chrome-devtools": map[string]any{"command": "npx", "args": []string{"chrome-devtools-mcp@latest"}},
			"context7":        map[string]any{"command": "npx", "args": []string{"-y", "@upstash/context7-mcp"}},
		},
	})

	d := NewWorkspaceMCPDetector(NewClaudeMCPRegistry())
	res, err := d.Detect([]string{repoA, repoB}, configRoot)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	sort.Strings(res.Recommended)
	want := []string{"chrome-devtools", "context7", "playwright"}
	if !reflect.DeepEqual(res.Recommended, want) {
		t.Errorf("Recommended = %v, want %v", res.Recommended, want)
	}
}

func TestWorkspaceMCPDetector_Detect_SkipsBlacklistedDirs(t *testing.T) {
	repo := t.TempDir()
	// Real signal at depth 2
	mustWrite(t, filepath.Join(repo, "apps", "web", "package.json"), `{"dependencies":{"react":"19"}}`)
	// Decoy inside node_modules (must be ignored)
	mustWrite(t, filepath.Join(repo, "node_modules", "fake", "go.mod"), "module fake\n")

	configRoot := t.TempDir()
	mustWriteJSON(t, filepath.Join(configRoot, ".claude.json"), map[string]any{
		"mcpServers": map[string]any{
			"playwright":      map[string]any{"command": "npx", "args": []string{"@playwright/mcp@latest"}},
			"chrome-devtools": map[string]any{"command": "npx", "args": []string{"chrome-devtools-mcp@latest"}},
			"context7":        map[string]any{"command": "npx", "args": []string{"-y", "@upstash/context7-mcp"}},
		},
	})

	d := NewWorkspaceMCPDetector(NewClaudeMCPRegistry())
	res, err := d.Detect([]string{repo}, configRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Expect frontend-web signals, NOT go-backend
	hasContext7 := false
	hasPlaywright := false
	for _, n := range res.Recommended {
		if n == "context7" {
			hasContext7 = true
		}
		if n == "playwright" {
			hasPlaywright = true
		}
	}
	if !hasContext7 || !hasPlaywright {
		t.Errorf("expected context7+playwright, got %v", res.Recommended)
	}
}

func TestWorkspaceMCPDetector_Detect_RegistryIntersection(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "package.json"), `{"dependencies":{"react":"19"}}`)

	// Registry has only context7 — no playwright
	configRoot := t.TempDir()
	mustWriteJSON(t, filepath.Join(configRoot, ".claude.json"), map[string]any{
		"mcpServers": map[string]any{
			"context7": map[string]any{"command": "npx", "args": []string{"-y", "@upstash/context7-mcp"}},
		},
	})

	d := NewWorkspaceMCPDetector(NewClaudeMCPRegistry())
	res, err := d.Detect([]string{repo}, configRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range res.Recommended {
		if n == "playwright" {
			t.Errorf("playwright must not be recommended when not in registry")
		}
	}
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
