package checkers

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

func TestRegexMatch_CommitMessage_Pass(t *testing.T) {
	c := NewRegexMatch()
	s := harness.Spec{
		Kind:    harness.KindRegexMatch,
		Target:  harness.TargetCommitMessage,
		Pattern: `^feat: .+`,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{CommitMessage: "feat: add feature"})
	if res.Status != "pass" {
		t.Fatalf("expected pass, got %s: %s", res.Status, res.Message)
	}
}

func TestRegexMatch_Fail(t *testing.T) {
	c := NewRegexMatch()
	s := harness.Spec{
		Kind:    harness.KindRegexMatch,
		Target:  harness.TargetCommitMessage,
		Pattern: `^feat: .+`,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{CommitMessage: "wip: something"})
	if res.Status != "fail" {
		t.Fatalf("expected fail, got %s: %s", res.Status, res.Message)
	}
}

func TestRegexMatch_InvalidPattern_Error(t *testing.T) {
	c := NewRegexMatch()
	s := harness.Spec{
		Kind:    harness.KindRegexMatch,
		Target:  harness.TargetCommitMessage,
		Pattern: `[unclosed`,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{CommitMessage: "hello"})
	if res.Status != "error" {
		t.Fatalf("expected error, got %s: %s", res.Status, res.Message)
	}
}

func TestRegexMatch_EmptyPattern_Skip(t *testing.T) {
	c := NewRegexMatch()
	s := harness.Spec{
		Kind:    harness.KindRegexMatch,
		Target:  harness.TargetCommitMessage,
		Pattern: "",
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{CommitMessage: "feat: x"})
	if res.Status != "skip" {
		t.Fatalf("expected skip, got %s: %s", res.Status, res.Message)
	}
}

func TestRegexMatch_EmptyInput_Skip(t *testing.T) {
	c := NewRegexMatch()
	s := harness.Spec{
		Kind:    harness.KindRegexMatch,
		Target:  harness.TargetCommitMessage,
		Pattern: `^feat: .+`,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{})
	if res.Status != "skip" {
		t.Fatalf("expected skip, got %s: %s", res.Status, res.Message)
	}
}

func TestRegexMatch_CaseInsensitiveFlag_Pass(t *testing.T) {
	c := NewRegexMatch()
	s := harness.Spec{
		Kind:         harness.KindRegexMatch,
		Target:       harness.TargetBranchName,
		Pattern:      `^FEAT/`,
		PatternFlags: "i",
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{BranchName: "feat/new-thing"})
	if res.Status != "pass" {
		t.Fatalf("expected pass with case-insensitive flag, got %s: %s", res.Status, res.Message)
	}
}

func TestRegexMatch_AgentOutput_Pass(t *testing.T) {
	c := NewRegexMatch()
	s := harness.Spec{
		Kind:    harness.KindRegexMatch,
		Target:  harness.TargetAgentOutput,
		Pattern: `done`,
	}
	res := c.Run(context.Background(), s, harness.CheckEnv{AgentOutput: "task done successfully"})
	if res.Status != "pass" {
		t.Fatalf("expected pass, got %s: %s", res.Status, res.Message)
	}
}
