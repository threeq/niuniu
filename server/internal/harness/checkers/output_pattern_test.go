package checkers

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

func TestOutputPattern_Pass(t *testing.T) {
	c := &OutputPattern{}
	res := c.Check(context.Background(), harness.CheckOpts{
		AgentOutput: "Here is the result:\n<!-- TASK_BREAKDOWN_START -->\n{\"tasks\":[]}\n<!-- TASK_BREAKDOWN_END -->",
		SpecConfig:  `{"pattern": "TASK_BREAKDOWN_START"}`,
	})
	if res.Status != "pass" {
		t.Errorf("expected pass, got %s: %s", res.Status, res.Message)
	}
}

func TestOutputPattern_Fail(t *testing.T) {
	c := &OutputPattern{}
	res := c.Check(context.Background(), harness.CheckOpts{
		AgentOutput: "I finished the analysis but no structured output.",
		SpecConfig:  `{"pattern": "TASK_BREAKDOWN_START"}`,
	})
	if res.Status != "fail" {
		t.Errorf("expected fail, got %s: %s", res.Status, res.Message)
	}
}

func TestOutputPattern_SkipEmptyOutput(t *testing.T) {
	c := &OutputPattern{}
	res := c.Check(context.Background(), harness.CheckOpts{
		AgentOutput: "",
		SpecConfig:  `{"pattern": "TASK_BREAKDOWN_START"}`,
	})
	if res.Status != "skip" {
		t.Errorf("expected skip, got %s: %s", res.Status, res.Message)
	}
}

func TestOutputPattern_SkipEmptyPattern(t *testing.T) {
	c := &OutputPattern{}
	res := c.Check(context.Background(), harness.CheckOpts{
		AgentOutput: "some output",
		SpecConfig:  `{}`,
	})
	if res.Status != "skip" {
		t.Errorf("expected skip, got %s: %s", res.Status, res.Message)
	}
}

func TestOutputPattern_InvalidRegex(t *testing.T) {
	c := &OutputPattern{}
	res := c.Check(context.Background(), harness.CheckOpts{
		AgentOutput: "some output",
		SpecConfig:  `{"pattern": "[invalid"}`,
	})
	if res.Status != "error" {
		t.Errorf("expected error, got %s: %s", res.Status, res.Message)
	}
}
