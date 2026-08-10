package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// fakeLookPath returns canned LookPath results keyed by binary name.
type fakeLookPath map[string]bool

func (f fakeLookPath) lookup(name string) (string, error) {
	if ok, found := f[name]; found && ok {
		return "/fake/" + name, nil
	}
	return "", &fakePathErr{name: name}
}

type fakePathErr struct{ name string }

func (e *fakePathErr) Error() string { return "exec: \"" + e.name + "\": not found" }

// fakeEnv returns canned env-var values; missing keys → "".
type fakeEnv map[string]string

func (f fakeEnv) get(name string) string { return f[name] }

func TestBuildClaudeLoginCmd_Windows_WithWT(t *testing.T) {
	lp := fakeLookPath{"wt": true}.lookup
	cmd, err := buildClaudeLoginCmd("windows", `C:\Users\Alice`, lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"wt", "-d", `C:\Users\Alice`, "cmd", "/K", "claude"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("argv mismatch:\n  want %#v\n  got  %#v", want, cmd.Args)
	}
}

func TestBuildClaudeLoginCmd_Windows_WithoutWT_UsesEmptyTitle(t *testing.T) {
	lp := fakeLookPath{}.lookup
	cmd, err := buildClaudeLoginCmd("windows", `C:\Users\Alice`, lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must use empty-string title — `start "Claude"` would let `start`
	// interpret Claude as a program when Go's argv→commandline serializer
	// drops the quotes around a token without spaces.
	want := []string{"cmd", "/C", "start", "", "/D", `C:\Users\Alice`, "cmd", "/K", "claude"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("argv mismatch:\n  want %#v\n  got  %#v", want, cmd.Args)
	}
}

func TestBuildClaudeLoginCmd_Darwin_Plain(t *testing.T) {
	cmd, err := buildClaudeLoginCmd("darwin", `/Users/alice`, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantScript := `tell application "Terminal" to do script "cd \"/Users/alice\" && claude"`
	want := []string{"osascript", "-e", wantScript}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("argv mismatch:\n  want %#v\n  got  %#v", want, cmd.Args)
	}
}

func TestBuildClaudeLoginCmd_Darwin_QuoteOnly(t *testing.T) {
	cmd, err := buildClaudeLoginCmd("darwin", `/Users/al"ice`, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantScript := `tell application "Terminal" to do script "cd \"/Users/al\"ice\" && claude"`
	if cmd.Args[2] != wantScript {
		t.Errorf("script mismatch:\n  want %q\n  got  %q", wantScript, cmd.Args[2])
	}
}

func TestBuildClaudeLoginCmd_Darwin_BackslashOnly(t *testing.T) {
	cmd, err := buildClaudeLoginCmd("darwin", `/Users/al\ice`, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantScript := `tell application "Terminal" to do script "cd \"/Users/al\\ice\" && claude"`
	if cmd.Args[2] != wantScript {
		t.Errorf("script mismatch:\n  want %q\n  got  %q", wantScript, cmd.Args[2])
	}
}

func TestBuildClaudeLoginCmd_Darwin_BackslashAndQuoteOrder(t *testing.T) {
	// Order matters: must escape \ first, then ". Reversing the order would
	// produce \\\" for `\` (incorrect — re-escapes the just-added `\"`).
	cmd, err := buildClaudeLoginCmd("darwin", `/Users/a\"b`, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantScript := `tell application "Terminal" to do script "cd \"/Users/a\\\"b\" && claude"`
	if cmd.Args[2] != wantScript {
		t.Errorf("escape order broken:\n  want %q\n  got  %q", wantScript, cmd.Args[2])
	}
}

func TestBuildClaudeLoginCmd_Linux_TerminalEnvFirst(t *testing.T) {
	// $TERMINAL beats LookPath ordering even when other terminals are also
	// available.
	lp := fakeLookPath{"kitty": true, "gnome-terminal": true}.lookup
	env := fakeEnv{"TERMINAL": "kitty"}.get
	cmd, err := buildClaudeLoginCmd("linux", "/home/alice", lp, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Args[0] != "kitty" {
		t.Errorf("expected $TERMINAL=kitty to win, got %q", cmd.Args[0])
	}
}

func TestBuildClaudeLoginCmd_Linux_KittyFromLookPath(t *testing.T) {
	lp := fakeLookPath{"kitty": true}.lookup
	cmd, err := buildClaudeLoginCmd("linux", "/home/alice", lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPayload := `cd '/home/alice' && (command -v claude >/dev/null && claude || echo "claude not found in PATH inside this shell — see https://docs.claude.com/en/docs/claude-code/setup"); exec bash`
	want := []string{"kitty", "-d", "/home/alice", "bash", "-lc", wantPayload}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("argv mismatch:\n  want %#v\n  got  %#v", want, cmd.Args)
	}
}

func TestBuildClaudeLoginCmd_Linux_AlacrittyFromLookPath(t *testing.T) {
	lp := fakeLookPath{"alacritty": true}.lookup
	cmd, err := buildClaudeLoginCmd("linux", "/home/alice", lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPayload := `cd '/home/alice' && (command -v claude >/dev/null && claude || echo "claude not found in PATH inside this shell — see https://docs.claude.com/en/docs/claude-code/setup"); exec bash`
	want := []string{"alacritty", "--working-directory", "/home/alice", "-e", "bash", "-lc", wantPayload}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("argv mismatch:\n  want %#v\n  got  %#v", want, cmd.Args)
	}
}

func TestBuildClaudeLoginCmd_Linux_Xfce4TerminalQuotesPayload(t *testing.T) {
	// xfce4-terminal -e is system(3)-style — payload must be a single quoted
	// string, not separate argv slots. Lock the shape so a future "let me
	// normalize this row" cleanup doesn't break it.
	//
	// The payload itself contains single quotes (from shellQuote(home)
	// wrapping `/home/alice`), so the outer shellQuote escapes those embedded
	// quotes via the close-quote / backslash-quote / open-quote idiom.
	lp := fakeLookPath{"xfce4-terminal": true}.lookup
	cmd, err := buildClaudeLoginCmd("linux", "/home/alice", lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantQuoted := `'cd '\''/home/alice'\'' && (command -v claude >/dev/null && claude || echo "claude not found in PATH inside this shell — see https://docs.claude.com/en/docs/claude-code/setup"); exec bash'`
	want := []string{"xfce4-terminal", "--working-directory=/home/alice", "-e", "bash -lc " + wantQuoted}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("argv mismatch:\n  want %#v\n  got  %#v", want, cmd.Args)
	}
}

func TestBuildClaudeLoginCmd_Linux_PrefersKittyOverAlacrittyOverGnome(t *testing.T) {
	// All three available — kitty (earliest in priority list) wins.
	lp := fakeLookPath{"kitty": true, "alacritty": true, "gnome-terminal": true}.lookup
	cmd, err := buildClaudeLoginCmd("linux", "/home/alice", lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Args[0] != "kitty" {
		t.Errorf("expected kitty priority, got %q", cmd.Args[0])
	}
}

func TestBuildClaudeLoginCmd_Linux_FallsThroughToXterm(t *testing.T) {
	lp := fakeLookPath{"xterm": true}.lookup
	cmd, err := buildClaudeLoginCmd("linux", "/home/alice", lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Args[0] != "xterm" {
		t.Errorf("expected xterm fallback, got %q", cmd.Args[0])
	}
}

func TestBuildClaudeLoginCmd_Linux_XTerminalEmulatorIsLastResort(t *testing.T) {
	// xterm beats x-terminal-emulator (which is a Debian alternative whose
	// underlying -e semantics are unstable).
	lp := fakeLookPath{"xterm": true, "x-terminal-emulator": true}.lookup
	cmd, err := buildClaudeLoginCmd("linux", "/home/alice", lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Args[0] != "xterm" {
		t.Errorf("expected xterm priority over x-terminal-emulator, got %q", cmd.Args[0])
	}
}

func TestBuildClaudeLoginCmd_Linux_NoTerminal(t *testing.T) {
	lp := fakeLookPath{}.lookup
	_, err := buildClaudeLoginCmd("linux", "/home/alice", lp, nil)
	if err == nil {
		t.Fatal("expected error when no terminal found")
	}
	if err.Error() != "no_terminal_found" {
		t.Errorf("expected no_terminal_found, got %v", err)
	}
}

func TestBuildClaudeLoginCmd_Linux_HomePathWithSpaces(t *testing.T) {
	lp := fakeLookPath{"kitty": true}.lookup
	home := "/home/foo bar"
	cmd, err := buildClaudeLoginCmd("linux", home, lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Args[2] != home {
		t.Errorf("home should be a separate argv slot, got %q", cmd.Args[2])
	}
	// Bash payload should single-quote the home dir.
	want := "cd '/home/foo bar' && (command -v claude"
	if !strings.HasPrefix(cmd.Args[5], want) {
		t.Errorf("payload should single-quote home, got %q", cmd.Args[5])
	}
}

func TestBuildClaudeLoginCmd_Linux_HomePathWithSingleQuote(t *testing.T) {
	lp := fakeLookPath{"kitty": true}.lookup
	home := "/home/o'brien"
	cmd, err := buildClaudeLoginCmd("linux", home, lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `cd '/home/o'\''brien' && `
	if !strings.HasPrefix(cmd.Args[5], want) {
		t.Errorf("single-quote escape broken, got %q", cmd.Args[5])
	}
}

func TestBuildClaudeLoginCmd_UnsupportedOS(t *testing.T) {
	_, err := buildClaudeLoginCmd("plan9", "/home/alice", nil, nil)
	if err == nil {
		t.Fatal("expected error for unsupported OS")
	}
	if err.Error() != "unsupported_os" {
		t.Errorf("expected unsupported_os, got %v", err)
	}
}

// Sanity: live runtime path doesn't panic on the host running the suite.
// Linux without any terminal in PATH legitimately returns no_terminal_found.
func TestBuildClaudeLoginCmd_Live(t *testing.T) {
	cmd, err := buildClaudeLoginCmd(runtime.GOOS, "/tmp", nil, nil)
	if err != nil && err.Error() != "no_terminal_found" {
		t.Fatalf("live path failed: %v", err)
	}
	if err == nil && cmd == nil {
		t.Fatal("nil cmd with nil error")
	}
}

func TestBuildCodexLoginCmd_Windows_WithWT(t *testing.T) {
	lp := fakeLookPath{"wt": true}.lookup
	cmd, err := buildCodexLoginCmd("windows", `C:\Users\Alice`, lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// `cmd /K codex login` keeps the shell open after `codex login` returns.
	// `codex` + `login` MUST be separate argv slots so cmd.exe parses login
	// as the argument to codex, not as a separate command-line token.
	want := []string{"wt", "-d", `C:\Users\Alice`, "cmd", "/K", "codex", "login"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("argv mismatch:\n  want %#v\n  got  %#v", want, cmd.Args)
	}
}

func TestBuildCodexLoginCmd_Windows_WithoutWT_UsesEmptyTitle(t *testing.T) {
	lp := fakeLookPath{}.lookup
	cmd, err := buildCodexLoginCmd("windows", `C:\Users\Alice`, lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"cmd", "/C", "start", "", "/D", `C:\Users\Alice`, "cmd", "/K", "codex", "login"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("argv mismatch:\n  want %#v\n  got  %#v", want, cmd.Args)
	}
}

func TestBuildCodexLoginCmd_Darwin_RunsCodexLogin(t *testing.T) {
	cmd, err := buildCodexLoginCmd("darwin", `/Users/alice`, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantScript := `tell application "Terminal" to do script "cd \"/Users/alice\" && codex login"`
	want := []string{"osascript", "-e", wantScript}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("argv mismatch:\n  want %#v\n  got  %#v", want, cmd.Args)
	}
}

func TestBuildCodexLoginCmd_Linux_KittyPayloadProbesCodex(t *testing.T) {
	lp := fakeLookPath{"kitty": true}.lookup
	cmd, err := buildCodexLoginCmd("linux", "/home/alice", lp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPayload := `cd '/home/alice' && (command -v codex >/dev/null && codex login || echo "codex not found in PATH inside this shell — see https://github.com/openai/codex"); exec bash`
	want := []string{"kitty", "-d", "/home/alice", "bash", "-lc", wantPayload}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("argv mismatch:\n  want %#v\n  got  %#v", want, cmd.Args)
	}
}

func TestCodexLogin_Forbidden_WhenHosted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &ShellHandler{personalMode: false, starter: func(*exec.Cmd) error { return nil }}
	r := gin.New()
	r.POST("/api/shell/codex-login", h.CodexLogin)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/shell/codex-login", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestCodexLogin_OK_WhenStarterSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	h := &ShellHandler{
		personalMode:  true,
		starter:       func(*exec.Cmd) error { called = true; return nil },
		homeDir:       func() (string, error) { return "/home/test", nil },
		buildCodexCmd: func(_, _ string) (*exec.Cmd, error) { return exec.Command("echo", "ok"), nil },
	}
	r := gin.New()
	r.POST("/api/shell/codex-login", h.CodexLogin)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/shell/codex-login", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (body=%s)", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("starter not invoked despite 204")
	}
}

func TestClaudeLogin_Forbidden_WhenHosted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &ShellHandler{personalMode: false, starter: func(*exec.Cmd) error { return nil }}
	r := gin.New()
	r.POST("/api/shell/claude-login", h.ClaudeLogin)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/shell/claude-login", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("non-JSON body: %s", w.Body.String())
	}
	if body["error"] != "not_supported_in_hosted_mode" {
		t.Errorf("expected not_supported_in_hosted_mode, got %v", body)
	}
}

func TestClaudeLogin_OK_WhenStarterSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	h := &ShellHandler{
		personalMode: true,
		starter:      func(*exec.Cmd) error { called = true; return nil },
		homeDir:      func() (string, error) { return "/home/test", nil },
		buildCmd:     func(_, _ string) (*exec.Cmd, error) { return exec.Command("echo", "ok"), nil },
	}
	r := gin.New()
	r.POST("/api/shell/claude-login", h.ClaudeLogin)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/shell/claude-login", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (body=%s)", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("starter not invoked despite 204")
	}
}

func TestClaudeLogin_500_WhenStarterFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &ShellHandler{
		personalMode: true,
		starter:      func(*exec.Cmd) error { return errors.New("boom") },
		homeDir:      func() (string, error) { return "/home/test", nil },
		buildCmd:     func(_, _ string) (*exec.Cmd, error) { return exec.Command("echo", "ok"), nil },
	}
	r := gin.New()
	r.POST("/api/shell/claude-login", h.ClaudeLogin)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/shell/claude-login", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("non-JSON 500 body: %s", w.Body.String())
	}
	if body["error"] != "boom" {
		t.Errorf("expected boom, got %v", body)
	}
}

func TestClaudeLogin_500_WhenHomeDirFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &ShellHandler{
		personalMode: true,
		starter:      func(*exec.Cmd) error { return nil },
		homeDir:      func() (string, error) { return "", errors.New("nope") },
		buildCmd:     func(_, _ string) (*exec.Cmd, error) { return exec.Command("echo", "ok"), nil },
	}
	r := gin.New()
	r.POST("/api/shell/claude-login", h.ClaudeLogin)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/shell/claude-login", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("non-JSON 500 body: %s", w.Body.String())
	}
	if body["error"] != "no_home_dir" {
		t.Errorf("expected no_home_dir, got %v", body)
	}
}

func TestClaudeLogin_500_WhenBuildCmdFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &ShellHandler{
		personalMode: true,
		starter:      func(*exec.Cmd) error { return nil },
		homeDir:      func() (string, error) { return "/home/test", nil },
		buildCmd:     func(_, _ string) (*exec.Cmd, error) { return nil, errors.New("no_terminal_found") },
	}
	r := gin.New()
	r.POST("/api/shell/claude-login", h.ClaudeLogin)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/shell/claude-login", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("non-JSON 500 body: %s", w.Body.String())
	}
	if body["error"] != "no_terminal_found" {
		t.Errorf("expected no_terminal_found, got %v", body)
	}
}

func TestNewShellHandler_Defaults(t *testing.T) {
	h := NewShellHandler(true)
	if h.starter == nil {
		t.Error("default starter should be set")
	}
	if h.homeDir == nil {
		t.Error("default homeDir should be set")
	}
	if h.buildCmd == nil {
		t.Error("default buildCmd should be set")
	}
	if !h.personalMode {
		t.Error("personalMode should be true as constructed")
	}
	h2 := NewShellHandler(false)
	if h2.personalMode {
		t.Error("personalMode should be false as constructed")
	}
}
