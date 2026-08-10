package checkers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

type fileExistsConfig struct {
	Paths []string `json:"paths"`
}

// FileExists checks that all configured file paths exist relative to the workspace.
// Config: {"paths": ["design.md", "src/main.go"]}
type FileExists struct{}

func (c *FileExists) Name() string { return "file-exists" }

func (c *FileExists) Check(ctx context.Context, opts harness.CheckOpts) harness.CheckResult {
	if opts.WorkspacePath == "" {
		return harness.CheckResult{Status: "skip", Message: "no workspace path available"}
	}

	var cfg fileExistsConfig
	if opts.SpecConfig != "" {
		json.Unmarshal([]byte(opts.SpecConfig), &cfg)
	}
	if len(cfg.Paths) == 0 {
		return harness.CheckResult{Status: "skip", Message: "no paths configured"}
	}

	var missing []string
	for _, p := range cfg.Paths {
		full := filepath.Join(opts.WorkspacePath, p)
		if _, err := os.Stat(full); err != nil {
			missing = append(missing, p)
		}
	}

	if len(missing) == 0 {
		return harness.CheckResult{
			Status:  "pass",
			Message: fmt.Sprintf("all %d required files exist", len(cfg.Paths)),
		}
	}

	return harness.CheckResult{
		Status:  "fail",
		Message: fmt.Sprintf("%d of %d required files missing", len(missing), len(cfg.Paths)),
		Details: "missing: " + strings.Join(missing, ", "),
	}
}
