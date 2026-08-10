package checkers_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/harness/checkers"
	"github.com/stretchr/testify/assert"
)

func TestLinter_Name(t *testing.T) {
	c := checkers.NewLinter()
	assert.Equal(t, "linter", c.Name())
}

func TestLinter_EmptyCommand_Skip(t *testing.T) {
	c := checkers.NewLinter()
	result := c.Check(context.Background(), harness.CheckOpts{
		SpecConfig: `{"command": ""}`,
	})
	assert.Equal(t, "skip", result.Status)
}

func TestLinter_NoConfig_Skip(t *testing.T) {
	c := checkers.NewLinter()
	result := c.Check(context.Background(), harness.CheckOpts{
		SpecConfig: "",
	})
	assert.Equal(t, "skip", result.Status)
}

func TestLinter_SuccessCommand_Pass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows: 'true' command not available")
	}
	c := checkers.NewLinter()
	result := c.Check(context.Background(), harness.CheckOpts{
		SpecConfig: `{"command": "true"}`,
	})
	assert.Equal(t, "pass", result.Status)
}

func TestLinter_FailCommand_Fail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows: 'false' command not available")
	}
	c := checkers.NewLinter()
	result := c.Check(context.Background(), harness.CheckOpts{
		SpecConfig: `{"command": "false"}`,
	})
	assert.Equal(t, "fail", result.Status)
}

func TestLinter_Timeout_Error(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows: 'sleep' command not available")
	}
	c := checkers.NewLinter()
	result := c.Check(context.Background(), harness.CheckOpts{
		SpecConfig: `{"command": "sleep 10", "timeout_sec": 1}`,
	})
	assert.Equal(t, "error", result.Status)
	assert.Contains(t, result.Message, "timed out")
}

func TestLinter_OutputTruncated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	c := checkers.NewLinter()
	// Generate output longer than 2000 chars via yes piped through head
	result := c.Check(context.Background(), harness.CheckOpts{
		// yes outputs infinite 'y\n'; we cut it to 3000 lines so combined output > 2000 chars
		SpecConfig: `{"command": "sh -c 'yes | head -3000; exit 1'"}`,
	})
	assert.Equal(t, "fail", result.Status)
	assert.LessOrEqual(t, len(result.Details), 2000)
}
