package checkers

import (
	"context"
	"runtime"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

func TestCommandExitCode_Pass(t *testing.T) {
	cmd := "echo hello"
	if runtime.GOOS == "windows" {
		cmd = "cmd /c echo hello"
	}
	c := &CommandExitCode{}
	res := c.Check(context.Background(), harness.CheckOpts{
		WorkspacePath: t.TempDir(),
		SpecConfig:    `{"command": "` + cmd + `"}`,
	})
	if res.Status != "pass" {
		t.Errorf("expected pass, got %s: %s", res.Status, res.Message)
	}
}

func TestCommandExitCode_Fail(t *testing.T) {
	cmd := "false"
	if runtime.GOOS == "windows" {
		cmd = "cmd /c exit 1"
	}
	c := &CommandExitCode{}
	res := c.Check(context.Background(), harness.CheckOpts{
		WorkspacePath: t.TempDir(),
		SpecConfig:    `{"command": "` + cmd + `"}`,
	})
	if res.Status != "fail" {
		t.Errorf("expected fail, got %s: %s", res.Status, res.Message)
	}
}

func TestCommandExitCode_SkipNoCommand(t *testing.T) {
	c := &CommandExitCode{}
	res := c.Check(context.Background(), harness.CheckOpts{
		WorkspacePath: t.TempDir(),
		SpecConfig:    `{}`,
	})
	if res.Status != "skip" {
		t.Errorf("expected skip, got %s: %s", res.Status, res.Message)
	}
}
