package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Write creates or overwrites a file (parent directories are created).
type Write struct{}

// writeInput.
type writeInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (Write) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "Write",
		Description: "Creates or overwrites a file with the given content (parent directories are created automatically). Prefer Edit for modifying existing files.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"path":{"type":"string","description":"File path to write"},` +
			`"content":{"type":"string","description":"Full file content"}},` +
			`"required":["path","content"]}`),
	}
}

func (Write) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in writeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if in.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if dir := filepath.Dir(in.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create parent dirs: %w", err)
		}
	}
	if err := os.WriteFile(in.Path, []byte(in.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d bytes)", in.Path, len(in.Content)), nil
}
