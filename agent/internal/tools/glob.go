package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Glob finds files by pattern relative to a root (default: working
// directory). Supports `**` for any number of directories; a single `*`
// stays within one segment. Pure Go, no shell.
type Glob struct{}

// globMax caps one search.
const globMax = 500

// globInput.
type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (Glob) Def() model.ToolDef {
	return model.ToolDef{
		Name: "Glob",
		Description: "Finds files matching a glob pattern (supports ** for any directory depth, e.g. \"src/**/*.ts\", \"*.go\"). " +
			"Patterns are relative to path (default: working directory). Returns up to 500 paths, sorted.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"pattern":{"type":"string","description":"Glob pattern, ** matches any number of directories"},` +
			`"path":{"type":"string","description":"Root directory (default: working directory)"}},` +
			`"required":["pattern"]}`),
	}
}

func (Glob) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in globInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	root := in.Path
	if root == "" {
		root = "."
	}
	pattern := filepath.ToSlash(in.Pattern)

	var matches []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		if matchGlob(pattern, filepath.ToSlash(rel)) {
			matches = append(matches, filepath.ToSlash(filepath.Join(root, rel)))
			if len(matches) >= globMax {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return "", err
	}
	if len(matches) == 0 {
		return "(no files match " + in.Pattern + ")", nil
	}
	sort.Strings(matches)
	return strings.Join(matches, "\n"), nil
}

// matchGlob matches rel (slash-separated) against pattern with `**`
// semantics, segment by segment.
func matchGlob(pattern, rel string) bool {
	pat := strings.Split(pattern, "/")
	segs := strings.Split(rel, "/")
	return matchSegs(pat, segs)
}

func matchSegs(pat, segs []string) bool {
	switch {
	case len(pat) == 0:
		return len(segs) == 0
	case pat[0] == "**":
		// `**` consumes zero or more segments.
		for i := 0; i <= len(segs); i++ {
			if matchSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	case len(segs) == 0:
		return false
	default:
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false
		}
		return matchSegs(pat[1:], segs[1:])
	}
}
