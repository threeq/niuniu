package checkers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

func TestFileExists_Pass(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "design.md"), []byte("# Design"), 0o644)
	os.WriteFile(filepath.Join(dir, "spec.md"), []byte("# Spec"), 0o644)

	c := &FileExists{}
	res := c.Check(context.Background(), harness.CheckOpts{
		WorkspacePath: dir,
		SpecConfig:    `{"paths": ["design.md", "spec.md"]}`,
	})
	if res.Status != "pass" {
		t.Errorf("expected pass, got %s: %s", res.Status, res.Message)
	}
}

func TestFileExists_Fail(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "design.md"), []byte("# Design"), 0o644)

	c := &FileExists{}
	res := c.Check(context.Background(), harness.CheckOpts{
		WorkspacePath: dir,
		SpecConfig:    `{"paths": ["design.md", "missing.md"]}`,
	})
	if res.Status != "fail" {
		t.Errorf("expected fail, got %s: %s", res.Status, res.Message)
	}
}

func TestFileExists_SkipNoPaths(t *testing.T) {
	c := &FileExists{}
	res := c.Check(context.Background(), harness.CheckOpts{
		WorkspacePath: t.TempDir(),
		SpecConfig:    `{}`,
	})
	if res.Status != "skip" {
		t.Errorf("expected skip, got %s: %s", res.Status, res.Message)
	}
}

func TestFileExists_SkipNoWorkspace(t *testing.T) {
	c := &FileExists{}
	res := c.Check(context.Background(), harness.CheckOpts{
		SpecConfig: `{"paths": ["design.md"]}`,
	})
	if res.Status != "skip" {
		t.Errorf("expected skip, got %s: %s", res.Status, res.Message)
	}
}
