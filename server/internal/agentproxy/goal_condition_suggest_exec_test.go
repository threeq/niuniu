package agentproxy

// Tests in this file exercise the SUBPROCESS-LEVEL contract of
// SuggestGoalCondition — argv composition, cwd, env, and the actual
// Windows command-line escaping of args that contain JSON (curly
// braces, embedded double quotes, newlines). They sit in their own
// file because they need a TestMain hook to re-enter the test binary
// as a stub for "claude", and that hook is global to the package.
//
// Background — why the heavyweight setup: PowerShell-direct invocations
// of the same args reliably get a clean envelope back from claude CLI,
// but niuniu-server-spawned invocations have produced `{"status":"ready"}`
// (handshake-only) and `{"error":"no schema or question provided"}` in
// production. The user's working hypothesis is that something in the Go
// exec layer (npm .cmd shim re-parse, stdin handle inheritance, cwd =
// os.TempDir() walking up into a polluted ancestor) is mangling the
// child's view of argv. These tests verify that hypothesis by:
//
//   1. Asserting buildOneShotCmd composes argv/cwd/env exactly as
//      expected (catches refactor regressions; no subprocess).
//   2. Stubbing the "claude" binary with the test binary itself, then
//      running the full SuggestGoalCondition pipeline, and reading back
//      the EXACT argv the stub received. If argv survived the round-trip
//      unchanged, Go's exec layer is not the bug source.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubEnvVar gates the test binary into "stub mode" — when set, the
// process dumps its argv as JSON to the file at stubOutputEnvVar and
// exits 0 instead of running tests. Used by TestMain.
const (
	stubEnvVar       = "NIUNIU_CLAUDE_STUB_DUMP"
	stubOutputEnvVar = "NIUNIU_CLAUDE_STUB_OUTPUT"
	stubStdoutEnvVar = "NIUNIU_CLAUDE_STUB_STDOUT"
	stubExitEnvVar   = "NIUNIU_CLAUDE_STUB_EXIT"
)

func TestMain(m *testing.M) {
	if os.Getenv(stubEnvVar) == "1" {
		runAsClaudeStub()
		// runAsClaudeStub calls os.Exit; this is unreachable.
		return
	}
	os.Exit(m.Run())
}

// runAsClaudeStub: the test binary, when invoked with NIUNIU_CLAUDE_STUB_DUMP=1,
// pretends to be `claude` for the purposes of these tests. It writes its
// os.Args (everything the real CLI would see) as a JSON array to the file
// path in NIUNIU_CLAUDE_STUB_OUTPUT, optionally prints a canned stdout from
// NIUNIU_CLAUDE_STUB_STDOUT, then exits with NIUNIU_CLAUDE_STUB_EXIT (default 0).
//
// CRITICAL: We must drain stdin before exit. If the parent wired stdin to a
// pipe and is blocking on Write, exiting without draining can hang the
// parent on Windows. cmd.Stdin = os.DevNull in production avoids this, but
// the tests verify behavior across configurations.
func runAsClaudeStub() {
	// Best-effort drain stdin so callers using a pipe don't deadlock.
	_, _ = io.Copy(io.Discard, os.Stdin)

	out := os.Getenv(stubOutputEnvVar)
	if out != "" {
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

// TestBuildSuggestCmd_OneShotProviderEnvInjectedAndSanitized verifies that the
// marked one-shot provider preset (resolved via OneShotProviderEnvFunc) is
// injected into the subprocess env AND that a stale host ANTHROPIC_API_KEY is
// dropped once the preset's Bearer token is present — the "configured 智谱 but
// 403 on some computers" fix applied to the one-shot path, with zero caller
// threading.
func TestBuildSuggestCmd_OneShotProviderEnvInjectedAndSanitized(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-stale-host-key")
	prev := OneShotProviderEnvFunc
	OneShotProviderEnvFunc = func(context.Context) []string {
		return []string{
			"ANTHROPIC_AUTH_TOKEN=zhipu-real-key",
			"ANTHROPIC_BASE_URL=https://open.bigmodel.cn/api/anthropic",
		}
	}
	t.Cleanup(func() { OneShotProviderEnvFunc = prev })

	cmd := buildOneShotCmd(context.Background(), "x", suggestJSONSchema, "")

	if hasEnvKey(cmd.Env, "ANTHROPIC_API_KEY") {
		t.Errorf("stale ANTHROPIC_API_KEY must be stripped when a Bearer token is set: %v", cmd.Env)
	}
	if !hasEnvKey(cmd.Env, "ANTHROPIC_AUTH_TOKEN") {
		t.Errorf("provider ANTHROPIC_AUTH_TOKEN must be present in subprocess env")
	}
	if !hasEnvKey(cmd.Env, "ANTHROPIC_BASE_URL") {
		t.Errorf("provider ANTHROPIC_BASE_URL must be present in subprocess env")
	}
}

func TestBuildSuggestCmd_EnvInheritedPlusConfigDir(t *testing.T) {
	// With configDir == "" → no CLAUDE_CONFIG_DIR override. With a value,
	// it must appear exactly once and as the LAST entry (so it overrides
	// any inherited value from os.Environ()).
	cmd := buildOneShotCmd(context.Background(), "x", suggestJSONSchema, "")
	if hasEnvKey(cmd.Env, "CLAUDE_CONFIG_DIR") {
		// It's allowed if the parent already had it set, but if the parent
		// didn't, we mustn't inject one.
		if !hasEnvKey(os.Environ(), "CLAUDE_CONFIG_DIR") {
			t.Errorf("unexpected CLAUDE_CONFIG_DIR in env when configDir=\"\" and parent has none")
		}
	}

	cmd = buildOneShotCmd(context.Background(), "x", suggestJSONSchema, `C:\path\to\account`)
	last := ""
	count := 0
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			count++
			last = kv
		}
	}
	if count == 0 {
		t.Fatal("expected CLAUDE_CONFIG_DIR in env when configDir is non-empty")
	}
	want := `CLAUDE_CONFIG_DIR=C:\path\to\account`
	if last != want {
		t.Errorf("last CLAUDE_CONFIG_DIR entry = %q, want %q (appended-last semantics matter — last wins)", last, want)
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

// ---- Integration tests: full argv round-trip through a stub binary ----

// runWithClaudeStub temporarily replaces claudeBinary with the test binary
// itself + sets the stub env. Returns the path that the stub will write
// argv to; caller reads + unmarshals it after the run.
func runWithClaudeStub(t *testing.T, stdout string, exit int) string {
	t.Helper()
	dumpPath := filepath.Join(t.TempDir(), "argv.json")
	prevBin := claudeBinary
	claudeBinary = os.Args[0]
	t.Setenv(stubEnvVar, "1")
	t.Setenv(stubOutputEnvVar, dumpPath)
	t.Setenv(stubStdoutEnvVar, stdout)
	if exit > 0 {
		t.Setenv(stubExitEnvVar, string(rune('0'+exit)))
	}
	t.Cleanup(func() { claudeBinary = prevBin })
	return dumpPath
}

func TestSuggestGoalCondition_ArgvSurvivesExecRoundTrip(t *testing.T) {
	// THE central test for the user's hypothesis: when Go's exec.Command
	// hands `--json-schema {"type":"object",...}` and a multi-line prompt
	// containing `<issue_title>` tags + double quotes to a real subprocess,
	// does the subprocess receive the exact same strings?
	//
	// If this passes, Go's exec layer is NOT the bug source.
	// If it fails, we've found the corruption point.
	prevAvail := cliAvailable.Load()
	cliAvailable.Store(true)
	defer cliAvailable.Store(prevAvail)

	// Canned stdout that the parser would accept as a successful suggestion.
	// Schema-validated path (structured_output) so we exercise the full
	// happy-path codepath, not just argv passing.
	stubStdout := `{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"suggestion":"ROUNDTRIP_OK"}}`
	dumpPath := runWithClaudeStub(t, stubStdout, 0)

	title := `Add /api/health endpoint`
	description := `Wire GET /api/health returning 200 + {"status":"ok"}. Cover with one unit test. Edge: %PATH% and {curly} braces in description should pass through unmangled.`
	suggestion, err := SuggestGoalCondition(context.Background(), title, description, "")
	if err != nil {
		t.Fatalf("SuggestGoalCondition: unexpected error: %v", err)
	}
	if suggestion != "ROUNDTRIP_OK" {
		t.Errorf("parser did not extract stub suggestion: got %q", suggestion)
	}

	// Now the diagnostic gold: read what argv the stub actually received.
	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read argv dump: %v", err)
	}
	var argv []string
	if err := json.Unmarshal(data, &argv); err != nil {
		t.Fatalf("unmarshal argv dump: %v", err)
	}
	if len(argv) < 2 {
		t.Fatalf("stub received only %d args: %v", len(argv), argv)
	}
	// argv[0] is the stub's own path; argv[1:] is what we passed.
	got := argv[1:]

	// Helper to assert a flag-value pair survived intact.
	requirePair := func(flag, want string) {
		t.Helper()
		for i := 0; i < len(got)-1; i++ {
			if got[i] == flag {
				if got[i+1] != want {
					t.Errorf("flag %q VALUE corrupted: got %q (%d bytes), want %q (%d bytes)",
						flag, got[i+1], len(got[i+1]), want, len(want))
				}
				return
			}
		}
		t.Errorf("flag %q not seen by subprocess; full argv: %v", flag, got)
	}

	// The schema must arrive byte-identical — this is the canary for
	// cmd.exe / makeCmdLine mangling of `{` `}` `"`.
	requirePair("--json-schema", suggestJSONSchema)
	// The prompt is built from the template + sanitized title/desc.
	// Reconstruct the expected value the same way SuggestGoalCondition does
	// so the assertion is robust against future template tweaks.
	safeT, safeD := sanitizeSuggestInputs(title, description)
	wantPrompt := strings.Replace(suggestPromptTemplate, "%s", safeT, 1)
	wantPrompt = strings.Replace(wantPrompt, "%s", safeD, 1)
	requirePair("-p", wantPrompt)

	// Standalone flags too.
	mustContain := func(s string) {
		t.Helper()
		for _, a := range got {
			if a == s {
				return
			}
		}
		t.Errorf("standalone flag %q not seen by subprocess; full argv: %v", s, got)
	}
	mustContain("--no-session-persistence")
	mustContain("--disable-slash-commands")
}

func TestSuggestGoalCondition_ArgvSurvivesShellMetacharacters(t *testing.T) {
	// Specifically target chars cmd.exe interprets when re-parsing %*:
	// % (variable expansion), & | ^ < > (command separators / redirect),
	// ! (delayed expansion if enabled), `"` (quote terminator).
	// If we ever route back through claude.cmd these would corrupt argv.
	prevAvail := cliAvailable.Load()
	cliAvailable.Store(true)
	defer cliAvailable.Store(prevAvail)

	dumpPath := runWithClaudeStub(t,
		`{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"suggestion":"OK"}}`, 0)

	// Title carries the metachar zoo. Description is plain so we isolate
	// the variable.
	title := `metachars: %PATH% & | ^ < > ! "quoted"`
	if _, err := SuggestGoalCondition(context.Background(), title, "plain desc", ""); err != nil {
		t.Fatalf("SuggestGoalCondition: %v", err)
	}

	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	if err := json.Unmarshal(data, &argv); err != nil {
		t.Fatal(err)
	}
	// Find the -p value and assert each metachar appears in it.
	var prompt string
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-p" {
			prompt = argv[i+1]
			break
		}
	}
	if prompt == "" {
		t.Fatal("no -p flag found in subprocess argv")
	}
	// The title is sanitized for issue_title / issue_description angle
	// brackets, but the metachar zoo above doesn't trip those. Verify
	// each char survived.
	for _, want := range []string{"%PATH%", "& | ^", "< >", "!", `"quoted"`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("subprocess received corrupted -p value (missing %q); full prompt:\n%s", want, prompt)
		}
	}
}

func TestSuggestGoalCondition_StubExitNonZeroSurfacesEnvelope(t *testing.T) {
	// End-to-end: stub exits 1 with an envelope on stdout claiming
	// is_error=true + api_error_status=403; the handler should surface
	// `API 403: <result>` via extractEnvelopeAPIError, NOT a bare
	// "exit status 1".
	prevAvail := cliAvailable.Load()
	cliAvailable.Store(true)
	defer cliAvailable.Store(prevAvail)

	dumpPath := runWithClaudeStub(t,
		`{"type":"result","is_error":true,"api_error_status":403,"result":"Failed to authenticate. API Error: 403 Request not allowed"}`,
		1)
	_ = dumpPath

	_, err := SuggestGoalCondition(context.Background(), "t", "d", "")
	if err == nil {
		t.Fatal("expected error when stub exits non-zero with is_error envelope")
	}
	msg := err.Error()
	if !strings.Contains(msg, "API 403") || !strings.Contains(msg, "Failed to authenticate") {
		t.Errorf("error message should surface API error, got: %s", msg)
	}
	// Must NOT be the bare "exit status 1" — that's the regression we
	// already fixed and would mean a code-path silently bypassed it.
	if strings.HasSuffix(strings.TrimSpace(msg), "exit status 1") {
		t.Errorf("regression: bare 'exit status 1' surfaced again; got: %s", msg)
	}
}

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

