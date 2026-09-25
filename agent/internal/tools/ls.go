package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// LS lists a directory's entries. Go-native so it behaves identically on
// Windows/macOS/Linux without depending on a shell being installed.
type LS struct{}

// lsInput.
type lsInput struct {
	Path string `json:"path"`
}

func (LS) Def() model.ToolDef {
	return model.ToolDef{
		Name: "LS",
		Description: "Lists the entries of a directory (name, type, size in bytes). " +
			"Use this to explore the file system. Omit path to list the working directory.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Directory path to list"}}}`),
	}
}

func (LS) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in lsInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
	}
	dir := in.Path
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(&b, "%s/\n", e.Name())
			continue
		}
		info, err := e.Info()
		if err != nil {
			fmt.Fprintf(&b, "%s\n", e.Name())
			continue
		}
		fmt.Fprintf(&b, "%s  %d bytes\n", e.Name(), info.Size())
	}
	if b.Len() == 0 {
		return "(empty directory)", nil
	}
	return b.String(), nil
}
