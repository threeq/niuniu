package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/terminal"
)

// ClaudeAccountPTYHandler bridges a WS connection to a `claude /login` PTY
// child process so admins can complete the OAuth flow from the SPA without
// needing local terminal access (works in personal + team-edition deploys).
type ClaudeAccountPTYHandler struct {
	svc      *service.ClaudeAccountService
	upgrader *websocket.Upgrader
}

func NewClaudeAccountPTYHandler(svc *service.ClaudeAccountService) *ClaudeAccountPTYHandler {
	return &ClaudeAccountPTYHandler{
		svc: svc,
		upgrader: &websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     checkWSOrigin,
		},
	}
}

// Handle serves /ws/claude-accounts/:id/login-pty?token=<ws_token>.
//
// Token is issued by POST /api/claude-accounts (initial create) or
// POST /api/claude-accounts/:id/login (re-login). Single-use; 5-min expiry.
func (h *ClaudeAccountPTYHandler) Handle(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	// ws_token MUST use a separate query param (`ws_token`) from the JWT auth
	// middleware which reads `?token=`. Both gates required: the JWT proves
	// admin role at the WS upgrade, and the ws_token is single-use per-account
	// (binds the connection to a specific account ID).
	tok := c.Query("ws_token")
	if tok == "" {
		// Backwards-compat: clients pre-fix sent `?token=<ws_token>` which
		// collided with auth middleware. Accept old name briefly with a warn.
		tok = c.Query("token")
	}
	if tok == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing_ws_token"})
		return
	}
	tokAccountID, err := h.svc.VerifyLoginToken(tok)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	if tokAccountID != id {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token_account_mismatch"})
		return
	}

	row, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrAccountNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrader writes its own response on failure.
		return
	}

	// Build env: inject CLAUDE_CONFIG_DIR for managed rows; default row uses native ~/.claude/.
	var env []string
	if row.ConfigDir != "" {
		env = []string{"CLAUDE_CONFIG_DIR=" + row.ConfigDir}
	}

	// I4: spawn from $HOME so claude /login behaves predictably regardless of
	// server's CWD (which is /data inside the team-edition container).
	workDir, _ := os.UserHomeDir()

	proc, err := terminal.NewPTYProcess("claude", []string{"/login"}, workDir, env)
	if err != nil {
		_ = conn.WriteJSON(gin.H{"error": "spawn_failed", "detail": err.Error()})
		_ = conn.Close()
		return
	}

	// Reuse terminal.Bridge for the WS↔PTY plumbing — handles binary stream,
	// resize messages, and goroutine lifecycle. onClose returns true so the
	// PTY is closed when the WS disconnects.
	if err := terminal.Bridge(conn, proc, func() bool { return true }); err != nil {
		slog.Debug("login pty bridge ended", "accountID", id, "err", err)
	}

	// Wait for the child process to exit so we can read its exit code.
	waitErr := proc.Wait()
	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1 // killed by signal / failed to start
		}
	}

	bgCtx := context.Background()
	if exitCode == 0 {
		// `claude /login` wrote credentials to config_dir → refresh email/status.
		if err := h.svc.RefreshFromDisk(bgCtx, id); err != nil {
			slog.Warn("login pty: RefreshFromDisk failed", "accountID", id, "err", err)
		}
	} else {
		_ = h.svc.MarkLoginFailed(bgCtx, id)
	}
}
