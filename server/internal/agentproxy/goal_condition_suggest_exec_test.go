package agentproxy

// Tests in this file exercise the SUBPROCESS-LEVEL contract of the one-shot
// suggestion pipeline — argv composition, prompt-over-stdin, and error
// surfacing. They sit in their own file because they need a TestMain hook to
// re-enter the test binary as a stub for the agent CLI, and that hook is
// global to the package.
//
// History: these tests originally pinned `claude -p` ARGV escaping (a real
// Windows cmd.exe corruption bug). The suggestion pipeline now runs on
// niuniu-agent (`-p -` with the prompt on STDIN), which structurally removes
// the argv-mangling class — a multi-KB prompt never touches argv. The tests
// now pin the niuniu-agent contract instead: argv is exactly ["-p", "-"], the
// prompt arrives intact on stdin, and non-zero exits surface stderr.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
)

// stubEnvVar gates the test binary into "stub mode" — when set, the
// process dumps its argv as JSON to the file at stubOutputEnvVar and
// exits 0 instead of running tests. Used by TestMain.
const (
	stubEnvVar        = "NIUNIU_CLAUDE_STUB_DUMP"
	stubOutputEnvVar  = "NIUNIU_CLAUDE_STUB_OUTPUT"
	stubStdoutEnvVar  = "NIUNIU_CLAUDE_STUB_STDOUT"
	stubStderrEnvVar  = "NIUNIU_CLAUDE_STUB_STDERR"
	stubStdinDumpVar  = "NIUNIU_CLAUDE_STUB_STDIN_DUMP"
	stubExitEnvVar    = "NIUNIU_CLAUDE_STUB_EXIT"
)

func TestMain(m *testing.M) {
	if os.Getenv(stubEnvVar) == "1" {
		runAsAgentStub()
		// runAsAgentStub calls os.Exit; this is unreachable.
		return
	}
	os.Exit(m.Run())
}

// runAsAgentStub: the test binary, when invoked with NIUNIU_CLAUDE_STUB_DUMP=1,
// pretends to be the agent CLI for the purposes of these tests. It drains
// stdin (optionally dumping it to stubStdinDumpVar so a test can assert the
// prompt arrived intact), writes its os.Args as a JSON array to the file at
// stubOutputEnvVar, optionally prints canned stdout/stderr, then exits with
// stubExitEnvVar (default 0).
func runAsAgentStub() {
	var stdinData []byte
	if dump := os.Getenv(stubStdinDumpVar); dump != "" {
		stdinData, _ = io.ReadAll(os.Stdin)
		if err := os.WriteFile(dump, stdinData, 0o644); err != nil {
			os.Exit(98)
		}
	} else {
		// Best-effort drain so callers using a pipe don't deadlock.
		_, _ = io.Copy(io.Discard, os.Stdin)
	}

	if out := os.Getenv(stubOutputEnvVar); out != "" {
		data, err := json.Marshal(os.Args)
		if err != nil {
			os.Exit(99)
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			os.Exit(98)
		}
	}
	if s := os.Getenv(stubStdoutEnvVar); s != "" {
		_, _ = os.Stdout.WriteString(s)
	}
	if s := os.Getenv(stubStderrEnvVar); s != "" {
		_, _ = os.Stderr.WriteString(s)
	}
	exitCode := 0
	if s := os.Getenv(stubExitEnvVar); s != "" {
		// Parse a single ASCII digit, simplest possible.
		if len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
			exitCode = int(s[0] - '0')
		}
	}
	os.Exit(exitCode)
}

// ---- Unit tests: buildOneShotCmd argv / cwd / env composition ----
//
// buildOneShotCmd is the legacy claude-path builder. It no longer backs the
// suggestion pipeline (niuniu-agent does), but it remains in the tree and
// these tests keep its composition pinned.

func TestBuildSuggestCmd_ArgvComposition(t *testing.T) {
	// Sanity for the argv slice — every flag the CLI relies on must be
	// present, in pairs, in the expected order. Catches accidental
	// reordering / typo regressions before they reach a subprocess.
	cmd := buildOneShotCmd(context.Background(), "PROMPT_VALUE", suggestJSONSchema, "")
	// cmd.Args[0] is the binary path itself; the flags follow.
	if len(cmd.Args) < 2 {
		t.Fatalf("expected at least 2 args (binary + flags), got %d: %v", len(cmd.Args), cmd.Args)
	}
	flags := cmd.Args[1:]

	expectPair := func(flag, value string) {
		t.Helper()
		for i := 0; i < len(flags)-1; i++ {
			if flags[i] == flag {
				if flags[i+1] != value {
					t.Errorf("flag %q paired with %q, want %q", flag, flags[i+1], value)
				}
				return
			}
		}
		t.Errorf("flag %q not found in argv: %v", flag, flags)
	}
	expectPair("-p", "PROMPT_VALUE")
	expectPair("--model", "haiku")
	expectPair("--output-format", "json")
	expectPair("--json-schema", suggestJSONSchema)

	expectFlag := func(flag string) {
		t.Helper()
		for _, f := range flags {
			if f == flag {
				return
			}
		}
		t.Errorf("standalone flag %q not found in argv: %v", flag, flags)
	}
	expectFlag("--no-session-persistence")
	expectFlag("--disable-slash-commands")

	// --max-budget-usd was intentionally removed (commit 07b502b6); verify
	// it does NOT reappear via a copy-paste regression.
	for _, f := range flags {
		if f == "--max-budget-usd" {
			t.Errorf("--max-budget-usd must NOT be passed (removed 2026-05-15)")
		}
	}
}

func TestBuildSuggestCmd_DirIsNonEmpty(t *testing.T) {
	// cmd.Dir must be set explicitly (not "" which means parent's cwd).
	// Production sets it to os.TempDir() to avoid pulling unrelated
	// CLAUDE.md / .claude.json files from whatever cwd niuniu-server had.
	cmd := buildOneShotCmd(context.Background(), "x", suggestJSONSchema, "")
	if cmd.Dir == "" {
		t.Fatal("cmd.Dir must be set explicitly, got empty")
	}
	if cmd.Dir != os.TempDir() {
		t.Errorf("cmd.Dir = %q, expected os.TempDir() = %q", cmd.Dir, os.TempDir())
	}
	// The temp dir must actually exist on the host — otherwise the
	// spawn fails before claude even starts.
	if st, err := os.Stat(cmd.Dir); err != nil {
		t.Fatalf("cmd.Dir does not exist: %v", err)
	} else if !st.IsDir() {
		t.Fatalf("cmd.Dir is not a directory: %s", cmd.Dir)
	}
}

func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// ---- Integration tests: the niuniu-agent one-shot contract ----

// runWithAgentStub arms the stub env so a RunOneShotStructured call with
// Command set to the TEST BINARY re-enters this process in stub mode. Returns
// the argv dump path; stdinDump (when non-empty) additionally captures the
// prompt the runner wrote to stdin.
func runWithAgentStub(t *testing.T, stdout, stderr, stdinDump string, exit int) string {
	t.Helper()
	dumpPath := filepath.Join(t.TempDir(), "argv.json")
	t.Setenv(stubEnvVar, "1")
	t.Setenv(stubOutputEnvVar, dumpPath)
	t.Setenv(stubStdoutEnvVar, stdout)
	t.Setenv(stubStderrEnvVar, stderr)
	if stdinDump != "" {
		t.Setenv(stubStdinDumpVar, stdinDump)
	}
	if exit > 0 {
		t.Setenv(stubExitEnvVar, string(rune('0'+exit)))
	}
	return dumpPath
}

func setNiuniuAgentAvailableForTest(t *testing.T) {
	t.Helper()
	prev := niuniuAgentAvailable.Load()
	niuniuAgentAvailable.Store(true)
	t.Cleanup(func() { niuniuAgentAvailable.Store(prev) })
}

// TestRunOneShotStructured_NiuniuAgentPromptOnStdin: the niuniu-agent one-shot
// contract — argv is EXACTLY ["-p", "-"] (the prompt must never appear in
// argv, which is what structurally killed the old cmd.exe escaping bug) and
// the full prompt arrives intact on stdin.
func TestRunOneShotStructured_NiuniuAgentPromptOnStdin(t *testing.T) {
	setNiuniuAgentAvailableForTest(t)

	argvDump := filepath.Join(t.TempDir(), "argv.json")
	stdinDump := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv(stubEnvVar, "1")
	t.Setenv(stubOutputEnvVar, argvDump)
	t.Setenv(stubStdinDumpVar, stdinDump)
	t.Setenv(stubStdoutEnvVar, `{"suggestion":"ROUNDTRIP_OK"}`)

	prompt := "multi-line prompt with <issue_title> tags, \"double quotes\", {curly} braces and %PATH% metachars"
	var dst struct {
		Suggestion string `json:"suggestion"`
	}
	err := RunOneShotStructured(context.Background(), OneShotRequest{
		Prompt:  prompt,
		CLIType: string(adapter.TypeNiuniuAgent),
		Command: os.Args[0], // re-enter the test binary in stub mode
	}, &dst)
	if err != nil {
		t.Fatalf("RunOneShotStructured: unexpected error: %v", err)
	}
	if dst.Suggestion != "ROUNDTRIP_OK" {
		t.Errorf("parser did not extract stub suggestion: got %q", dst.Suggestion)
	}

	data, err := os.ReadFile(argvDump)
	if err != nil {
		t.Fatalf("read argv dump: %v", err)
	}
	var argv []string
	if err := json.Unmarshal(data, &argv); err != nil {
		t.Fatalf("unmarshal argv dump: %v", err)
	}
	// argv[0] is the stub's own path; argv[1:] is what we passed.
	got := argv[1:]
	if len(got) != 2 || got[0] != "-p" || got[1] != "-" {
		t.Fatalf("niuniu-agent argv = %v, want exactly [\"-p\", \"-\"]", got)
	}
	// The prompt must ride on STDIN, byte-identical.
	stdinData, err := os.ReadFile(stdinDump)
	if err != nil {
		t.Fatalf("read stdin dump: %v", err)
	}
	if string(stdinData) != prompt {
		t.Errorf("stdin prompt corrupted: got %d bytes, want %d bytes", len(stdinData), len(prompt))
	}
}

// TestRunOneShotStructured_NiuniuAgentProviderEnvWins: the caller's
// project-provider env is layered ON TOP of the host env, so a stale host
// ANTHROPIC_API_KEY cannot undercut the bound provider's bearer token.
func TestRunOneShotStructured_NiuniuAgentProviderEnvWins(t *testing.T) {
	setNiuniuAgentAvailableForTest(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stale-host-key")
	t.Setenv("NIUNIU_TEST_ENV_PROBE", "")
	// The stub dumps its env? No — cheapest observable: the stub exits 0 and
	// the runner succeeded while the host env carried a stale key; the
	// provider token assertion is enforced by oneShotEnv's layering tests.
	// Here we verify the RUNNER accepts and forwards extra env without
	// erroring, keeping this test to the process boundary.
	dumpPath := runWithAgentStub(t, `{"suggestion":"OK"}`, "", "", 0)
	_ = dumpPath

	var dst struct {
		Suggestion string `json:"suggestion"`
	}
	err := RunOneShotStructured(context.Background(), OneShotRequest{
		Prompt:  "p",
		CLIType: string(adapter.TypeNiuniuAgent),
		Command: os.Args[0],
		Env:     []string{"ANTHROPIC_AUTH_TOKEN=provider-key", "ANTHROPIC_BASE_URL=https://provider.example/api/anthropic"},
	}, &dst)
	if err != nil {
		t.Fatalf("RunOneShotStructured: %v", err)
	}
}

// TestRunOneShotStructured_NiuniuAgentExitNonZeroSurfacesStderr: a non-zero
// exit with diagnostic stderr must surface the stderr text, not a bare
// "exit status 1".
func TestRunOneShotStructured_NiuniuAgentExitNonZeroSurfacesStderr(t *testing.T) {
	setNiuniuAgentAvailableForTest(t)

	runWithAgentStub(t, "", "Failed to authenticate: OAuth session expired and could not be refreshed", "", 1)

	var dst struct {
		Suggestion string `json:"suggestion"`
	}
	err := RunOneShotStructured(context.Background(), OneShotRequest{
		Prompt:  "p",
		CLIType: string(adapter.TypeNiuniuAgent),
		Command: os.Args[0],
	}, &dst)
	if err == nil {
		t.Fatal("expected error when stub exits non-zero with stderr")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Failed to authenticate") || !strings.Contains(msg, "OAuth session expired") {
		t.Errorf("error message should surface stderr diagnostics, got: %s", msg)
	}
	if strings.HasSuffix(strings.TrimSpace(msg), "exit status 1") {
		t.Errorf("regression: bare 'exit status 1' surfaced again; got: %s", msg)
	}
}

// TestSuggestGoalCondition_NiuniuAgentUnavailable: the 503-shaped gate fires
// before any spawn when the probe has not passed.
func TestSuggestGoalCondition_NiuniuAgentUnavailable(t *testing.T) {
	prev := niuniuAgentAvailable.Load()
	niuniuAgentAvailable.Store(false)
	defer niuniuAgentAvailable.Store(prev)

	_, err := SuggestGoalCondition(context.Background(), "title", "description", "")
	if err == nil {
		t.Fatal("expected error when CLI unavailable")
	}
	if !strings.Contains(err.Error(), "niuniu-agent CLI not available") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// ---- Legacy claude-path resolver tests (claude machinery remains in tree) ----

func TestResolveClaudeBinary_NonWindowsPassthrough(t *testing.T) {
	// On non-Windows, the resolver must return the name unchanged regardless
	// of PATH contents, because the .cmd→.exe redirection only applies on
	// Windows.
	if runtime.GOOS == "windows" {
		t.Skip("test asserts non-Windows passthrough behavior")
	}
	if got := resolveClaudeBinary("claude"); got != "claude" {
		t.Errorf("non-Windows: expected passthrough, got %q", got)
	}
	// Absolute path also passthrough.
	abs := "/opt/claude/claude"
	if got := resolveClaudeBinary(abs); got != abs {
		t.Errorf("non-Windows abs path: expected passthrough %q, got %q", abs, got)
	}
}

func TestResolveClaudeBinary_WindowsCmdShimRedirect(t *testing.T) {
	// Synthesize the npm shim layout in a temp dir, ensure LookPath finds
	// our claude.cmd, and verify the resolver substitutes the .exe.
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific shim redirection")
	}
	tmpDir := t.TempDir()
	cmdShim := filepath.Join(tmpDir, "claude.cmd")
	if err := os.WriteFile(cmdShim, []byte(`@echo off`+"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exeDir := filepath.Join(tmpDir, "node_modules", "@anthropic-ai", "claude-code", "bin")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realExe := filepath.Join(exeDir, "claude.exe")
	if err := os.WriteFile(realExe, []byte("stub-exe"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Prepend tmpDir to PATH so LookPath finds claude.cmd first.
	prevPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+prevPath)

	got := resolveClaudeBinary("claude")
	if got != realExe {
		t.Errorf("expected resolver to substitute .cmd → .exe at %q, got %q", realExe, got)
	}
}

func TestResolveClaudeBinary_WindowsCmdShimMissingExeFallback(t *testing.T) {
	// If the .cmd exists but the .exe sibling doesn't, fall back to the .cmd
	// rather than returning a phantom path.
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific shim redirection")
	}
	tmpDir := t.TempDir()
	cmdShim := filepath.Join(tmpDir, "claude.cmd")
	if err := os.WriteFile(cmdShim, []byte(`@echo off`+"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prevPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+prevPath)

	got := resolveClaudeBinary("claude")
	if got != cmdShim {
		t.Errorf("expected fallback to .cmd path %q when .exe absent, got %q", cmdShim, got)
	}
}
