package checkers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

func TestFileExistsV2_AllPresent_Pass(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(""), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := NewFileExistsV2()
	s := harness.Spec{
		Kind:      harness.KindFileExists,
		FilePaths: `["go.mod","Makefile"]`,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{WorkspacePath: dir})
	if res.Status != "pass" {
		t.Fatalf("expected pass, got %s: %s (details=%s)", res.Status, res.Message, res.Details)
	}
}

func TestFileExistsV2_MissingPath_Fail(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := NewFileExistsV2()
	s := harness.Spec{
		Kind:      harness.KindFileExists,
		FilePaths: `["go.mod","missing.txt"]`,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{WorkspacePath: dir})
	if res.Status != "fail" {
		t.Fatalf("expected fail, got %s: %s", res.Status, res.Message)
	}
	if res.Details == "" {
		t.Fatalf("expected details listing missing paths, got empty")
	}
}

func TestFileExistsV2_Empty_Skip(t *testing.T) {
	c := NewFileExistsV2()
	s := harness.Spec{
		Kind:      harness.KindFileExists,
		FilePaths: "",
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{WorkspacePath: t.TempDir()})
	if res.Status != "skip" {
		t.Fatalf("expected skip on empty, got %s: %s", res.Status, res.Message)
	}
}

func TestFileExistsV2_EmptyArray_Skip(t *testing.T) {
	c := NewFileExistsV2()
	s := harness.Spec{
		Kind:      harness.KindFileExists,
		FilePaths: "[]",
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{WorkspacePath: t.TempDir()})
	if res.Status != "skip" {
		t.Fatalf("expected skip on empty array, got %s: %s", res.Status, res.Message)
	}
}

func TestFileExistsV2_InvalidJSON_Error(t *testing.T) {
	c := NewFileExistsV2()
	s := harness.Spec{
		Kind:      harness.KindFileExists,
		FilePaths: `not-json`,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{WorkspacePath: t.TempDir()})
	if res.Status != "error" {
		t.Fatalf("expected error, got %s: %s", res.Status, res.Message)
	}
}
