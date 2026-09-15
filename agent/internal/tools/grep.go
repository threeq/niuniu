package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Grep searches file contents with a regular expression, recursively from a
// root (default: working directory). Pure Go (stdlib regexp) so the agent
// stays a zero-dependency single binary; binary files are skipped.
type Grep struct{}

// grepCaps bound one search.
const (
	grepMaxLine   = 250
	grepMaxResult = 200
)

// grepInput.
type grepInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Include string `json:"include"` // glob filter on file names, e.g. "*.go"
}

func (Grep) Def() model.ToolDef {
	return model.ToolDef{
		Name: "Grep",
		Description: "Searches file contents with a regular expression (Go RE2 syntax) under path (default: working directory), " +
			"recursively, skipping binary files. Filter files with a name glob via include (e.g. \"*.go\"). " +
			"Returns file:line: text matches, capped at 200 lines.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"pattern":{"type":"string","description":"Regular expression to search for"},` +
			`"path":{"type":"string","description":"Directory or file to search (default: working directory)"},` +
			`"include":{"type":"string","description":"Filename glob filter, e.g. \"*.go\""}},` +
			`"required":["pattern"]}`),
	}
}

func (Grep) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in grepInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %v (Go RE2 syntax)", err)
	}
	var fileFilter func(string) bool
	if in.Include != "" {
		fileFilter = func(name string) bool { ok, _ := filepath.Match(in.Include, filepath.Base(name)); return ok }
	}

	root := in.Path
	if root == "" {
		root = "."
	}
	var b strings.Builder
	shown := 0
	truncated := false

	search := func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || shown >= grepMaxResult {
			if shown >= grepMaxResult {
				return filepath.SkipAll
			}
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		if fileFilter != nil && !fileFilter(path) {
			return nil
		}
		if hits, ok := grepFile(re, path); ok {
			for _, h := range hits {
				if shown >= grepMaxResult {
					truncated = true
					return filepath.SkipAll
				}
				fmt.Fprintf(&b, "%s\n", h)
				shown++
			}
		}
		return nil
	}
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		// Single-file search.
		if hits, ok := grepFile(re, root); ok {
			for _, h := range hits {
				fmt.Fprintf(&b, "%s\n", h)
				shown++
				if shown >= grepMaxResult {
					truncated = true
					break
				}
			}
		}
	} else if err := filepath.WalkDir(root, search); err != nil && err != filepath.SkipAll {
		return "", err
	}

	if shown == 0 {
		return "(no matches)", nil
	}
	if truncated {
		b.WriteString(fmt.Sprintf("…(results truncated at %d lines)", grepMaxResult))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// grepFile returns matching "path:line: text" lines; ok is false when the
// file is unreadable or binary.
func grepFile(re *regexp.Regexp, path string) ([]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	sniff := make([]byte, 8<<10)
	n, _ := io.ReadFull(f, sniff)
	if n > 0 && bytes.IndexByte(sniff[:n], 0) >= 0 {
		return nil, false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, false
	}

	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if re.MatchString(text) {
			t := text
			if len(t) > grepMaxLine {
				t = t[:grepMaxLine] + "…"
			}
			out = append(out, fmt.Sprintf("%s:%d: %s", path, line, t))
		}
	}
	if sc.Err() != nil {
		return out, len(out) > 0
	}
	return out, len(out) > 0
}
