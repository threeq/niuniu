package agentproxy

import (
	"context"
	"strings"
	"testing"
)

// buildPermissionArgsForTest is a thin wrapper around buildPermissionArgs so
// the unit test stays readable.
func buildPermissionArgsForTest(t *testing.T, env map[string]string, mcpAvailable bool) []string {
	t.Helper()
	return buildPermissionArgs(env["NIUNIU_PERMISSION_MODE"], env["NIUNIU_ALLOWED_TOOLS"], mcpAvailable)
}

func permArgsContains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

func permArgsContainsValue(args []string, s string) bool {
	for _, a := range args {
		if strings.Contains(a, s) {
			return true
		}
	}
	return false
}

func permArgsFlagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestProxyArgs_DefaultMode_InjectsPromptTool(t *testing.T) {
	args := buildPermissionArgsForTest(t, map[string]string{"NIUNIU_PERMISSION_MODE": "default"}, true)
	if !permArgsContains(args, "--permission-prompt-tool") || !permArgsContainsValue(args, "mcp__niuniu__niuniu_permission_prompt") {
		t.Errorf("default mode should inject prompt-tool; got %v", args)
	}
}

func TestProxyArgs_BypassMode_NoPromptTool(t *testing.T) {
	args := buildPermissionArgsForTest(t, map[string]string{"NIUNIU_PERMISSION_MODE": "bypassPermissions"}, true)
	if permArgsContains(args, "--permission-prompt-tool") {
		t.Errorf("bypass mode should not inject prompt-tool; got %v", args)
	}
}

func TestProxyArgs_DefaultSafeToolsMerged(t *testing.T) {
	args := buildPermissionArgsForTest(t, map[string]string{
		"NIUNIU_PERMISSION_MODE": "default",
		"NIUNIU_ALLOWED_TOOLS":   "Edit",
	}, true)
	allowed := permArgsFlagValue(args, "--allowedTools")
	for _, tool := range []string{"Read", "Glob", "Grep", "Edit"} {
		if !strings.Contains(allowed, tool) {
			t.Errorf("allowedTools should contain %s; got %s", tool, allowed)
		}
	}
}

func TestProxyArgs_DefaultMode_NoMCP_SkipsPromptTool(t *testing.T) {
	args := buildPermissionArgsForTest(t, map[string]string{"NIUNIU_PERMISSION_MODE": "default"}, false)
	if permArgsContains(args, "--permission-prompt-tool") {
		t.Errorf("no-MCP path should skip prompt-tool flag; got %v", args)
	}
}

// Autohost is niuniu's CLI-equivalent of bypassPermissions: no prompt tool,
// no allowedTools restriction. The CLI must receive --permission-mode
// bypassPermissions (the autohost label is internal to niuniu).
func TestProxyArgs_AutohostMode_MapsToBypassAtCLI(t *testing.T) {
	args := buildPermissionArgsForTest(t, map[string]string{"NIUNIU_PERMISSION_MODE": "autohost"}, true)
	if got := permArgsFlagValue(args, "--permission-mode"); got != "bypassPermissions" {
		t.Errorf("autohost should map to bypassPermissions at CLI; got --permission-mode %q", got)
	}
	if permArgsContains(args, "--permission-prompt-tool") {
		t.Errorf("autohost mode should not inject prompt-tool; got %v", args)
	}
	if permArgsContains(args, "--allowedTools") {
		t.Errorf("autohost mode should skip --allowedTools (mirrors bypass); got %v", args)
	}
}

// Built-in AskUserQuestion has no TTY in niuniu chat; the MCP substitute
// niuniu_ask_user_question handles it. When MCP is attached, the built-in
// must be disabled so Claude doesn't try it first and waste a turn on the
// error. Applied in ALL permission modes because the tool is independent
// of permission-prompt-tool gating.
func TestProxyArgs_DisablesBuiltinAskUserQuestion_WhenMCP(t *testing.T) {
	for _, mode := range []string{"default", "acceptEdits", "bypassPermissions", "autohost"} {
		args := buildPermissionArgsForTest(t, map[string]string{"NIUNIU_PERMISSION_MODE": mode}, true)
		if got := permArgsFlagValue(args, "--disallowedTools"); got != "AskUserQuestion" {
			t.Errorf("mode=%s with MCP available: expected --disallowedTools AskUserQuestion, got %q (full args: %v)", mode, got, args)
		}
	}
}

// When MCP is NOT attached, the substitute is unreachable; the built-in
// AskUserQuestion still errors but disabling it would just exchange one
// error for another. Keep --disallowedTools off so behavior is unchanged
// from pre-MCP niuniu.
func TestProxyArgs_DoesNotDisableAskUserQuestion_WithoutMCP(t *testing.T) {
	args := buildPermissionArgsForTest(t, map[string]string{"NIUNIU_PERMISSION_MODE": "default"}, false)
	if permArgsContains(args, "--disallowedTools") {
		t.Errorf("no-MCP path should not inject --disallowedTools; got %v", args)
	}
}

func TestAutohostShouldStop_Sentinel(t *testing.T) {
	if !autohostShouldStop("All done.\n\n[AUTOHOST_DONE]") {
		t.Error("stop sentinel should trigger stop")
	}
	if !autohostShouldStop("[AUTOHOST_DONE]") {
		t.Error("bare sentinel should trigger stop")
	}
}

func TestAutohostShouldStop_NaturalLanguage(t *testing.T) {
	cases := []string{
		"任务已完成，请检查",
		"task complete — pushed to main",
		"All done!",
		"全部完成了",
	}
	for _, c := range cases {
		if !autohostShouldStop(c) {
			t.Errorf("expected stop for %q", c)
		}
	}
}

func TestAutohostShouldStop_FalsePositives(t *testing.T) {
	// Mid-task narratives that mention progress but not completion should not stop.
	cases := []string{
		"Working on step 2 of 5",
		"我准备开始下一步",
		"Next: implement the watchdog",
	}
	for _, c := range cases {
		if autohostShouldStop(c) {
			t.Errorf("did not expect stop for %q", c)
		}
	}
}

// autohostRecoverPrompt should differ from the continue prompt — they steer
// the agent differently. Cheap structural check to prevent accidental merge.
func TestAutohostPrompts_DifferContinueVsRecover(t *testing.T) {
	if autohostContinuePrompt == autohostRecoverPrompt {
		t.Error("continue and recover prompts must be distinct")
	}
	if !strings.Contains(autohostRecoverPrompt, "错误") && !strings.Contains(autohostRecoverPrompt, "error") {
		t.Error("recover prompt should mention the error context")
	}
	if !strings.Contains(autohostContinuePrompt, autohostStopSentinel) ||
		!strings.Contains(autohostRecoverPrompt, autohostStopSentinel) {
		t.Error("both prompts must instruct the agent on the stop sentinel")
	}
}

// Default budget constants should be sane: continue > 0, error_budget > 0,
// and error_budget should be tighter (errors should not loop as freely).
func TestAutohostDefaults_Sanity(t *testing.T) {
	if autohostDefaultBudget <= 0 {
		t.Error("continue budget default must be positive")
	}
	if autohostDefaultErrorBudget <= 0 {
		t.Error("error budget default must be positive")
	}
	if autohostDefaultErrorBudget > autohostDefaultBudget {
		t.Error("error budget should be <= continue budget — errors should be tighter")
	}
}

// Process-level env vars are a documented fallback for prompt overrides
// (workspace env wins; process env is a test/admin escape hatch). Verify
// the fallback contract directly via the int-env reader's twin string-env
// reader — they share the same lookup shape, so we test the harder one.
func TestAutohostPromptEnv_ProcessOverride(t *testing.T) {
	t.Setenv("NIUNIU_AUTOHOST_CONTINUE_PROMPT", "  custom continue  ")
	t.Setenv("NIUNIU_AUTOHOST_RECOVER_PROMPT", "  custom recover  ")

	// Build a bare WorkspaceSession that won't reach the DB (q is nil; the
	// reader handles err gracefully and falls through to env). This keeps
	// the test free of fixtures.
	s := &WorkspaceSession{}
	got := s.readAutohostStringEnv(context.Background(), "NIUNIU_AUTOHOST_CONTINUE_PROMPT", autohostContinuePrompt)
	if got != "custom continue" {
		t.Errorf("process env should win when workspace env unset; got %q", got)
	}
	got = s.readAutohostStringEnv(context.Background(), "NIUNIU_AUTOHOST_RECOVER_PROMPT", autohostRecoverPrompt)
	if got != "custom recover" {
		t.Errorf("process env should win for recover too; got %q", got)
	}
}

// Empty / whitespace-only overrides must NOT brick the watchdog — fall back
// to the default. The DB and env layers both trim, this test exercises env.
func TestAutohostPromptEnv_WhitespaceFallsBackToDefault(t *testing.T) {
	t.Setenv("NIUNIU_AUTOHOST_CONTINUE_PROMPT", "   \n\t  ")
	s := &WorkspaceSession{}
	got := s.readAutohostStringEnv(context.Background(), "NIUNIU_AUTOHOST_CONTINUE_PROMPT", autohostContinuePrompt)
	if got != autohostContinuePrompt {
		t.Errorf("whitespace-only override should fall back to default; got %q", got)
	}
}
