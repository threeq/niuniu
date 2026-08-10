package agentproxy

import (
	"context"
	"strings"
	"testing"
)

// The autohost watchdog now decides purely on the [AUTOHOST_DONE] sentinel +
// budget (the LLM judge was removed). The user's completion condition is routed
// into the injected continue/recover prompt instead. These tests cover that
// message-construction logic and the sentinel detector.

func TestAutohostShouldStop(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"sentinel", "done now\n[AUTOHOST_DONE]", true},
		{"phrase-zh", "我已经把任务完成了", true},
		{"phrase-en", "All done here.", true},
		{"none", "still working on the next step", false},
		{"empty", "", false},
		// Tightened heuristic: substring false positives must NOT stop.
		{"subtask-continue", "这个子任务完成了，继续下一步", false}, // continuation marker on the line
		{"subtask-bare", "子任务完成了", false},                   // 子 prefix → SUBtask, not whole task
		{"progress-report", "任务完成度 80%", false},             // 完成度 progress fragment, not done
		{"last-line-continue", "任务完成\n继续观察 CI 结果", false},  // concluding line says continue
		{"done-last-line", "## 结果\n任务完成：已合并回 main", true}, // genuine verdict on the last line
		{"phrase-done-all", "已完成所有需求改动并提交", true},      // 已完成所有 stays a stop
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autohostShouldStop(tc.text); got != tc.want {
				t.Fatalf("autohostShouldStop(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// buildAutohostContinuePrompt: with a completion condition it must inject the
// condition verbatim AND keep the sentinel marker so the agent can self-declare
// done; without one it falls back to the plain default continue prompt.
func TestBuildAutohostContinuePrompt_WithCondition(t *testing.T) {
	s, _ := newCondTestSession(t)
	cond := "所有单测通过且已提交"
	got := s.buildAutohostContinuePrompt(context.Background(), cond)
	if !strings.Contains(got, cond) {
		t.Fatalf("continue prompt missing condition text: %q", got)
	}
	if !strings.Contains(got, autohostStopSentinel) {
		t.Fatalf("continue prompt missing sentinel marker: %q", got)
	}
}

func TestBuildAutohostContinuePrompt_NoCondition(t *testing.T) {
	s, _ := newCondTestSession(t)
	got := s.buildAutohostContinuePrompt(context.Background(), "   ")
	if got != autohostContinuePrompt {
		t.Fatalf("empty condition should yield the default continue prompt, got %q", got)
	}
}

// buildAutohostRecoverPrompt mirrors the continue builder on the error path.
func TestBuildAutohostRecoverPrompt_WithCondition(t *testing.T) {
	s, _ := newCondTestSession(t)
	cond := "构建成功且回归用例通过"
	got := s.buildAutohostRecoverPrompt(context.Background(), cond)
	if !strings.Contains(got, cond) {
		t.Fatalf("recover prompt missing condition text: %q", got)
	}
	if !strings.Contains(got, autohostStopSentinel) {
		t.Fatalf("recover prompt missing sentinel marker: %q", got)
	}
}

func TestBuildAutohostRecoverPrompt_NoCondition(t *testing.T) {
	s, _ := newCondTestSession(t)
	got := s.buildAutohostRecoverPrompt(context.Background(), "")
	if got != autohostRecoverPrompt {
		t.Fatalf("empty condition should yield the default recover prompt, got %q", got)
	}
}

// The condition-aware prompt must honor the "condition priority over env
// override" decision: an env continue-prompt override is ignored when a
// completion condition is configured.
func TestBuildAutohostContinuePrompt_ConditionBeatsEnvOverride(t *testing.T) {
	s, q := newCondTestSession(t)
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTOHOST_CONTINUE_PROMPT", "CUSTOM-OVERRIDE")
	cond := "迁移脚本执行完毕"
	got := s.buildAutohostContinuePrompt(context.Background(), cond)
	if strings.Contains(got, "CUSTOM-OVERRIDE") {
		t.Fatalf("env override must be ignored when a condition is set, got %q", got)
	}
	if !strings.Contains(got, cond) {
		t.Fatalf("continue prompt missing condition text: %q", got)
	}
	// With no condition, the env override IS honored.
	if got := s.buildAutohostContinuePrompt(context.Background(), ""); got != "CUSTOM-OVERRIDE" {
		t.Fatalf("env override should apply when no condition, got %q", got)
	}
}
