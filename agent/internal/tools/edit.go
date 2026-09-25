package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Edit does exact-string replacement in a file. Occurrences must be unique
// unless replace_all is set — ambiguity is an error that tells the model how
// to fix its call (include more surrounding context), not a silent guess.
//
// Reliability fallback: when exact matching fails, a normalized match
// (per-line trailing-whitespace strip + CRLF→LF) is attempted — the two most
// common model mistakes. Normalization never rewrites the file's own bytes:
// only the matched span is replaced, everything else stays byte-identical.
// A total miss returns the closest file line as a self-correction hint.
type Edit struct{}

// editInput.
type editInput struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (Edit) Def() model.ToolDef {
	return model.ToolDef{
		Name: "Edit",
		Description: "Replaces an exact string in a file. old_string must match the file content exactly " +
			"(including whitespace/indentation) and be unique in the file; set replace_all to replace every occurrence. " +
			"Read the file first if unsure of the exact text.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"path":{"type":"string","description":"File path to edit"},` +
			`"old_string":{"type":"string","description":"Exact text to replace (must be unique unless replace_all)"},` +
			`"new_string":{"type":"string","description":"Replacement text"},` +
			`"replace_all":{"type":"boolean","description":"Replace every occurrence (default false)"}},` +
			`"required":["path","old_string","new_string"]}`),
	}
}

func (Edit) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in editInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	switch {
	case in.Path == "":
		return "", errors.New("path is required")
	case in.OldString == "":
		return "", errors.New("old_string is required (empty old_string would match everywhere)")
	case in.OldString == in.NewString:
		return "", errors.New("old_string and new_string are identical; nothing to do")
	}
	raw, err := os.ReadFile(in.Path)
	if err != nil {
		return "", err
	}
	content := string(raw)

	count := strings.Count(content, in.OldString)
	if count == 0 {
		span, ok := locateNormalized(content, in.OldString)
		if !ok {
			return "", fmt.Errorf("old_string not found in %s. Closest line in file:\n  %s\n— quote the exact text including whitespace and indentation", in.Path, closestLine(content, in.OldString))
		}
		updated := content[:span[0]] + in.NewString + content[span[1]:]
		if err := os.WriteFile(in.Path, []byte(updated), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("edited %s: 1 replacement (normalized whitespace match)", in.Path), nil
	}
	if count > 1 && !in.ReplaceAll {
		return "", fmt.Errorf("old_string appears %d times in %s — add surrounding context to make it unique, or set replace_all", count, in.Path)
	}
	updated := strings.ReplaceAll(content, in.OldString, in.NewString)
	if err := os.WriteFile(in.Path, []byte(updated), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s: %d replacement(s)", in.Path, count), nil
}

// locateNormalized finds old in content under per-line trailing-whitespace
// normalization (and CRLF→LF), returning the byte span IN THE ORIGINAL
// content corresponding to the match. Ambiguity under normalization is
// refused (same policy as exact matching).
func locateNormalized(content, old string) ([2]int, bool) {
	idx := make([]int, 0, len(content)+1) // normalized index → original index
	var norm strings.Builder
	for i := 0; i < len(content); i++ {
		if content[i] == '\r' {
			continue
		}
		norm.WriteByte(content[i])
		idx = append(idx, i)
	}
	idx = append(idx, len(content))

	nOld := normalizeLines(old)
	if nOld == "" {
		return [2]int{}, false
	}
	nContent := norm.String()
	first := strings.Index(nContent, nOld)
	if first < 0 || strings.Count(nContent, nOld) > 1 {
		return [2]int{}, false
	}
	return [2]int{idx[first], idx[first+len(nOld)]}, true
}

// normalizeLines strips trailing whitespace per line and unifies CRLF so
// "text\r\n" and "text  \n" both compare equal to "text\n".
func normalizeLines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	return strings.Join(lines, "\n")
}

// closestLine returns the file line most similar to the first line of the
// needle, by token overlap — a self-correction hint, not a fuzzy match.
func closestLine(content, needle string) string {
	first := needle
	if i := strings.IndexByte(needle, '\n'); i >= 0 {
		first = needle[:i]
	}
	want := strings.Fields(strings.ToLower(first))
	best, bestScore := "(no similar line found)", 0
	for _, ln := range strings.Split(content, "\n") {
		have := strings.Fields(strings.ToLower(ln))
		score := 0
		for _, w := range want {
			for _, h := range have {
				if w == h {
					score++
					break
				}
			}
		}
		if score > bestScore {
			best, bestScore = strings.TrimSpace(ln), score
		}
	}
	return best
}
