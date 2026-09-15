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
	switch {
	case count == 0:
		return "", fmt.Errorf("old_string not found in %s — quote the exact text including whitespace and indentation", in.Path)
	case count > 1 && !in.ReplaceAll:
		return "", fmt.Errorf("old_string appears %d times in %s — add surrounding context to make it unique, or set replace_all", count, in.Path)
	}
	updated := strings.ReplaceAll(content, in.OldString, in.NewString)
	if err := os.WriteFile(in.Path, []byte(updated), 0o644); err != nil {
		return "", err
	}
	replaced := count
	if in.ReplaceAll {
		replaced = count
	} else {
		replaced = 1
	}
	return fmt.Sprintf("edited %s: %d replacement(s)", in.Path, replaced), nil
}
