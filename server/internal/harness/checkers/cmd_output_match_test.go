package checkers

import (
	"context"
	"runtime"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

// echoCoverage returns a cross-platform command that prints
// `coverage: <pct>%` on stdout.
func echoCoverage(pct string) string {
	if runtime.GOOS == "windows" {
		return "cmd /c echo coverage: " + pct + "%"
	}
	return "echo coverage: " + pct + "%"
}

func TestCmdOutputMatch_Pass(t *testing.T) {
	c := NewCmdOutputMatch()
	s := harness.Spec{
		Kind:           harness.KindCommandOutputMatch,
		Command:        echoCoverage("85.5"),
		ExtractRegex:   `coverage:\s+(\d+\.?\d*)`,
		ThresholdValue: 80.0,
		ThresholdOp:    harness.ThresholdGTE,
		TimeoutSec:     10,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "pass" {
		t.Fatalf("expected pass, got %s: %s (details=%s)", res.Status, res.Message, res.Details)
	}
}

func TestCmdOutputMatch_FailBelowThreshold(t *testing.T) {
	c := NewCmdOutputMatch()
	s := harness.Spec{
		Kind:           harness.KindCommandOutputMatch,
		Command:        echoCoverage("50.0"),
		ExtractRegex:   `coverage:\s+(\d+\.?\d*)`,
		ThresholdValue: 80.0,
		ThresholdOp:    harness.ThresholdGTE,
		TimeoutSec:     10,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "fail" {
		t.Fatalf("expected fail, got %s: %s", res.Status, res.Message)
	}
}

func TestCmdOutputMatch_ErrorOnInvalidOp(t *testing.T) {
	c := NewCmdOutputMatch()
	s := harness.Spec{
		Kind:           harness.KindCommandOutputMatch,
		Command:        echoCoverage("85.5"),
		ExtractRegex:   `coverage:\s+(\d+\.?\d*)`,
		ThresholdValue: 80.0,
		ThresholdOp:    "??",
		TimeoutSec:     10,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "error" {
		t.Fatalf("expected error, got %s: %s", res.Status, res.Message)
	}
}

func TestCmdOutputMatch_SkipNoCommand(t *testing.T) {
	c := NewCmdOutputMatch()
	s := harness.Spec{
		Kind:           harness.KindCommandOutputMatch,
		Command:        "",
		ExtractRegex:   `coverage:\s+(\d+\.?\d*)`,
		ThresholdValue: 80.0,
		ThresholdOp:    harness.ThresholdGTE,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "skip" {
		t.Fatalf("expected skip, got %s: %s", res.Status, res.Message)
	}
}

func TestCmdOutputMatch_SkipNoExtractRegex(t *testing.T) {
	c := NewCmdOutputMatch()
	s := harness.Spec{
		Kind:           harness.KindCommandOutputMatch,
		Command:        echoCoverage("85.5"),
		ExtractRegex:   "",
		ThresholdValue: 80.0,
		ThresholdOp:    harness.ThresholdGTE,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "skip" {
		t.Fatalf("expected skip, got %s: %s", res.Status, res.Message)
	}
}

func TestCmdOutputMatch_ErrorRegexDoesNotMatch(t *testing.T) {
	c := NewCmdOutputMatch()
	s := harness.Spec{
		Kind:           harness.KindCommandOutputMatch,
		Command:        echoCoverage("85.5"),
		ExtractRegex:   `nomatch:\s+(\d+)`,
		ThresholdValue: 80.0,
		ThresholdOp:    harness.ThresholdGTE,
		TimeoutSec:     10,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "error" {
		t.Fatalf("expected error when regex does not match, got %s: %s", res.Status, res.Message)
	}
}
