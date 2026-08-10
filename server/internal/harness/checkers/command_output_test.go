package checkers

import (
	"context"
	"runtime"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

func TestCommandOutput_Pass(t *testing.T) {
	cmd := "echo coverage: 85.3%"
	if runtime.GOOS == "windows" {
		cmd = "cmd /c echo coverage: 85.3%"
	}
	c := &CommandOutput{}
	res := c.Check(context.Background(), harness.CheckOpts{
		WorkspacePath: t.TempDir(),
		SpecConfig:    `{"command": "` + cmd + `", "pattern": "coverage:"}`,
	})
	if res.Status != "pass" {
		t.Errorf("expected pass, got %s: %s", res.Status, res.Message)
	}
}

func TestCommandOutput_FailNoMatch(t *testing.T) {
	cmd := "echo no coverage info"
	if runtime.GOOS == "windows" {
		cmd = "cmd /c echo no coverage info"
	}
	c := &CommandOutput{}
	res := c.Check(context.Background(), harness.CheckOpts{
		WorkspacePath: t.TempDir(),
		SpecConfig:    `{"command": "` + cmd + `", "pattern": "TASK_BREAKDOWN"}`,
	})
	if res.Status != "fail" {
		t.Errorf("expected fail, got %s: %s", res.Status, res.Message)
	}
}

func TestCommandOutput_SkipNoConfig(t *testing.T) {
	c := &CommandOutput{}
	res := c.Check(context.Background(), harness.CheckOpts{
		SpecConfig: `{}`,
	})
	if res.Status != "skip" {
		t.Errorf("expected skip, got %s: %s", res.Status, res.Message)
	}
}

func TestCommandOutput_CommandFails(t *testing.T) {
	cmd := "false"
	if runtime.GOOS == "windows" {
		cmd = "cmd /c exit 1"
	}
	c := &CommandOutput{}
	res := c.Check(context.Background(), harness.CheckOpts{
		WorkspacePath: t.TempDir(),
		SpecConfig:    `{"command": "` + cmd + `", "pattern": "anything"}`,
	})
	if res.Status != "fail" {
		t.Errorf("expected fail, got %s: %s", res.Status, res.Message)
	}
}
