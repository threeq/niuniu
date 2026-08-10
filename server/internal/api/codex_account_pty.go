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

// CodexAccountPTYHandler bridges a WS connection to a `codex auth login` PTY
// child process so admins can complete the OAuth flow from the SPA without
// needing local terminal access. Mirror of ClaudeAccountPTYHandler.
type CodexAccountPTYHandler struct {
	svc      *service.CodexAccountService
	upgrader *websocket.Upgrader
}

func NewCodexAccountPTYHandler(svc *service.CodexAccountService) *CodexAccountPTYHandler {
	return &CodexAccountPTYHandler{
		svc: svc,
		upgrader: &websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     checkWSOrigin,
		},
	}
}

// Handle serves /ws/codex-accounts/:id/login-pty?ws_token=<single-use>.
//
// Token is issued by POST /api/codex-accounts (Create) or
// POST /api/codex-accounts/:id/login (re-login). Single-use; 5-min expiry.
func (h *CodexAccountPTYHandler) Handle(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
		return
	}
	tok := c.Query("ws_token")
	if tok == "" {
		// Accept ?token= as a backwards-compat alias (claude path did this).
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
		if errors.Is(err, service.ErrCodexAccountNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	// Build env: inject CODEX_HOME for the managed account.
	var env []string
	if row.ConfigDir != "" {
		env = []string{"CODEX_HOME=" + row.ConfigDir}
	}

	// Spawn from $HOME so codex auth login behaves predictably regardless of
	// the server's CWD (which may be /data inside the team-edition container).
	workDir, _ := os.UserHomeDir()

	proc, err := terminal.NewPTYProcess("codex", []string{"auth", "login"}, workDir, env)
	if err != nil {
		_ = conn.WriteJSON(gin.H{"error": "spawn_failed", "detail": err.Error()})
		_ = conn.Close()
		return
	}

	if err := terminal.Bridge(conn, proc, func() bool { return true }); err != nil {
		slog.Debug("codex login pty bridge ended", "accountID", id, "err", err)
	}

	waitErr := proc.Wait()
	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	bgCtx := context.Background()
	if exitCode == 0 {
		if err := h.svc.RefreshFromDisk(bgCtx, id); err != nil {
			slog.Warn("codex login pty: RefreshFromDisk failed", "accountID", id, "err", err)
		}
	} else {
		_ = h.svc.MarkLoginFailed(bgCtx, id)
	}
}
