package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/terminal"
)

// The browser-hosted CLI login (#677) is what makes team edition able to
// authenticate the agent CLIs at all: the /api/shell/*-login endpoints spawn a
// native terminal window on the server host and hard-403 outside personal mode,
// which in a container means "no login, ever". These tests pin the pre-upgrade
// contract — the parts we can exercise without a real WebSocket client, since
// everything past upgrader.Upgrade needs a live socket.

func TestCLILoginTerminal_RejectsUnknownTool(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &CLILoginHandler{
		lookPath: func(string) (string, error) { return "/fake/claude", nil },
		homeDir:  func() (string, error) { return "/home/test", nil },
		newPTY: func(string, []string, string, []string) (*terminal.PTYProcess, error) {
			t.Fatal("PTY must not be created for an unsupported tool")
			return nil, nil
		},
	}
	r := gin.New()
	r.GET("/ws/cli-login/:tool/terminal", h.Terminal)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws/cli-login/bash/terminal", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported tool, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// A missing CLI must surface as a plain HTTP error rather than a WebSocket that
// opens and immediately dies — the SPA can render the former, not the latter.
func TestCLILoginTerminal_404WhenCLIMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &CLILoginHandler{
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		homeDir:  func() (string, error) { return "/home/test", nil },
		newPTY: func(string, []string, string, []string) (*terminal.PTYProcess, error) {
			t.Fatal("PTY must not be created when the CLI is absent")
			return nil, nil
		},
	}
	r := gin.New()
	r.GET("/ws/cli-login/:tool/terminal", h.Terminal)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws/cli-login/codex/terminal", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when CLI missing, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// Unlike the native-terminal endpoints, this handler must NOT gate on personal
// mode — that gate is exactly the bug (#677). Reaching the PTY-construction step
// for both CLIs proves the request is not rejected before it. We fail the PTY
// build to stop short of upgrader.Upgrade, which needs a real socket.
func TestCLILoginTerminal_NoPersonalModeGate_AndCorrectArgv(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		tool     string
		wantBin  string
		wantArgs []string
	}{
		// `claude` has no login subcommand: bare invocation opens the TUI whose
		// /login flow does the auth. `codex` has an explicit one.
		{tool: "claude", wantBin: "claude", wantArgs: nil},
		{tool: "codex", wantBin: "codex", wantArgs: []string{"login"}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			var gotBin, gotDir string
			var gotArgs []string
			h := &CLILoginHandler{
				lookPath: func(string) (string, error) { return "/fake/" + tc.wantBin, nil },
				homeDir:  func() (string, error) { return "/home/test", nil },
				newPTY: func(cmd string, args []string, dir string, _ []string) (*terminal.PTYProcess, error) {
					gotBin, gotArgs, gotDir = cmd, args, dir
					return nil, errors.New("stop before websocket upgrade")
				},
			}
			r := gin.New()
			r.GET("/ws/cli-login/:tool/terminal", h.Terminal)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws/cli-login/"+tc.tool+"/terminal", nil))

			if w.Code == http.StatusForbidden {
				t.Fatal("handler must not 403: the personal-mode gate is the bug this endpoint fixes")
			}
			if gotBin != tc.wantBin {
				t.Errorf("bin: want %q, got %q", tc.wantBin, gotBin)
			}
			if len(gotArgs) != len(tc.wantArgs) {
				t.Errorf("args: want %#v, got %#v", tc.wantArgs, gotArgs)
			} else {
				for i := range tc.wantArgs {
					if gotArgs[i] != tc.wantArgs[i] {
						t.Errorf("args: want %#v, got %#v", tc.wantArgs, gotArgs)
						break
					}
				}
			}
			// $HOME, not a workspace: both CLIs persist credentials relative to
			// the home dir, and a stray keystroke can't touch repo files there.
			if gotDir != "/home/test" {
				t.Errorf("workdir: want %q, got %q", "/home/test", gotDir)
			}
		})
	}
}
