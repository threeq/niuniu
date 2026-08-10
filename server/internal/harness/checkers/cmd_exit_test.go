package checkers

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

func TestCmdExit_PassOnZero(t *testing.T) {
	c := NewCmdExit()
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "cmd /c exit 0"
	} else {
		cmd = "true"
	}
	s := harness.Spec{
		Kind:             harness.KindCommandExitCode,
		Command:          cmd,
		ExpectedExitCode: 0,
		TimeoutSec:       10,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "pass" {
		t.Fatalf("expected pass, got %s: %s (details=%s)", res.Status, res.Message, res.Details)
	}
}

func TestCmdExit_FailOnNonZero(t *testing.T) {
	c := NewCmdExit()
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "cmd /c exit 1"
	} else {
		cmd = "false"
	}
	s := harness.Spec{
		Kind:             harness.KindCommandExitCode,
		Command:          cmd,
		ExpectedExitCode: 0,
		TimeoutSec:       10,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "fail" {
		t.Fatalf("expected fail, got %s: %s", res.Status, res.Message)
	}
}

func TestCmdExit_PassOnMatchingNonZero(t *testing.T) {
	c := NewCmdExit()
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "cmd /c exit 2"
	} else {
		// Write a small script that exits 2; strings.Fields would split
		// `sh -c "exit 2"` incorrectly, so we use a script path with no
		// whitespace.
		dir := t.TempDir()
		path := filepath.Join(dir, "exit2.sh")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
			t.Fatalf("write script: %v", err)
		}
		cmd = "/bin/sh " + path
	}
	s := harness.Spec{
		Kind:             harness.KindCommandExitCode,
		Command:          cmd,
		ExpectedExitCode: 2,
		TimeoutSec:       10,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "pass" {
		t.Fatalf("expected pass on matching exit, got %s: %s (details=%s)", res.Status, res.Message, res.Details)
	}
}

func TestCmdExit_SkipOnEmpty(t *testing.T) {
	c := NewCmdExit()
	s := harness.Spec{
		Kind:    harness.KindCommandExitCode,
		Command: "",
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "skip" {
		t.Fatalf("expected skip, got %s: %s", res.Status, res.Message)
	}
}

func TestCmdExit_TimeoutError(t *testing.T) {
	c := NewCmdExit()
	var cmd string
	if runtime.GOOS == "windows" {
		// `ping -n 5 127.0.0.1` is the classic Windows sleep — no console needed.
		cmd = "ping -n 5 127.0.0.1"
	} else {
		cmd = "sleep 5"
	}
	s := harness.Spec{
		Kind:             harness.KindCommandExitCode,
		Command:          cmd,
		ExpectedExitCode: 0,
		TimeoutSec:       1,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "error" {
		t.Fatalf("expected error on timeout, got %s: %s", res.Status, res.Message)
	}
}
