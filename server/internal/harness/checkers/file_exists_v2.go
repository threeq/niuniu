package checkers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

// FileExistsV2 is the generic typed file_exists checker that consumes
// spec.FilePaths (a JSON array of string paths) and verifies each path
// exists under env.WorkspacePath.
type FileExistsV2 struct{}

// NewFileExistsV2 returns a new FileExistsV2 checker.
func NewFileExistsV2() *FileExistsV2 { return &FileExistsV2{} }

// Kind reports the registered Kind for this checker.
func (c *FileExistsV2) Kind() string { return harness.KindFileExists }

// Run parses spec.FilePaths as a JSON string array and checks each path's
// existence relative to env.WorkspacePath.
func (c *FileExistsV2) Run(ctx context.Context, spec harness.Spec, env harness.CheckEnv) harness.CheckResult {
	start := time.Now()

	raw := strings.TrimSpace(spec.FilePaths)
	if raw == "" || raw == "[]" {
		return harness.CheckResult{
			Status:     "skip",
			Message:    "no file paths configured",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	var paths []string
	if err := json.Unmarshal([]byte(raw), &paths); err != nil {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("invalid file_paths JSON: %v", err),
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	if len(paths) == 0 {
		return harness.CheckResult{
			Status:     "skip",
			Message:    "no file paths configured",
			DurationMs: time.Since(start).Milliseconds(),
		}
	}

	var missing []string
	for _, p := range paths {
		full := p
		if env.WorkspacePath != "" {
			full = filepath.Join(env.WorkspacePath, p)
		}
		if _, err := os.Stat(full); err != nil {
			missing = append(missing, p)
		}
	}

	elapsed := time.Since(start).Milliseconds()

	if len(missing) == 0 {
		return harness.CheckResult{
			Status:     "pass",
			Message:    fmt.Sprintf("all %d path(s) exist", len(paths)),
			DurationMs: elapsed,
		}
	}

	return harness.CheckResult{
		Status:     "fail",
		Message:    fmt.Sprintf("%d of %d path(s) missing", len(missing), len(paths)),
		Details:    "missing: " + strings.Join(missing, ", "),
		DurationMs: elapsed,
	}
}
