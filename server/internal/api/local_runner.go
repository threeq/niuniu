package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/niuniu-dev/niuniu/internal/service"
)

// LocalRunnerHandler serves the per-workspace local-runner contract defined by
// the SPA in lib/local-runner-api.ts (Epic #526 子B):
//
//	GET    /api/workspaces/:id/local-runner        -> LocalRunnerStateDTO
//	PUT    /api/workspaces/:id/local-runner        (config) -> LocalRunnerStateDTO
//	DELETE /api/workspaces/:id/local-runner        (unbind)
//	WS     /ws/workspaces/:id/local-runner/logs    -> LocalRunnerLog stream (SPA)
//	WS     /ws/workspaces/:id/local-runner/runner  <- desktop reverse channel
type LocalRunnerHandler struct {
	svc   *service.LocalRunnerService
	authz *service.Authz
}

func NewLocalRunnerHandler(svc *service.LocalRunnerService, authz *service.Authz) *LocalRunnerHandler {
	return &LocalRunnerHandler{svc: svc, authz: authz}
}

// localRunnerConfigDTO is the snake_case wire config (matches LocalRunnerConfigDTO).
type localRunnerConfigDTO struct {
	LocalDir           string   `json:"local_dir"`
	PromptSnippet      string   `json:"prompt_snippet"`
	AllowedCommands    []string `json:"allowed_commands"`
	AlwaysAllowPersist bool     `json:"always_allow_persist"`
}

// localRunnerStateDTO matches LocalRunnerStateDTO {status, config|null}.
type localRunnerStateDTO struct {
	Status string                `json:"status"`
	Config *localRunnerConfigDTO `json:"config"`
}

func toConfigDTO(cfg *service.LocalRunnerConfig) *localRunnerConfigDTO {
	if cfg == nil {
		return nil
	}
	cmds := cfg.AllowedCommands
	if cmds == nil {
		cmds = []string{}
	}
	return &localRunnerConfigDTO{
		LocalDir:           cfg.LocalDir,
		PromptSnippet:      cfg.PromptSnippet,
		AllowedCommands:    cmds,
		AlwaysAllowPersist: cfg.AlwaysAllowPersist,
	}
}

func (h *LocalRunnerHandler) authzWorkspace(c *gin.Context) (int64, bool) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || wsID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad workspace id"})
		return 0, false
	}
	uid := c.GetInt64("auth_user_id")
	if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), uid, wsID); err != nil {
		switch {
		case errors.Is(err, service.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "workspace not found"})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return 0, false
	}
	return wsID, true
}

// Get — GET /api/workspaces/:id/local-runner
func (h *LocalRunnerHandler) Get(c *gin.Context) {
	wsID, ok := h.authzWorkspace(c)
	if !ok {
		return
	}
	status, cfg, err := h.svc.Status(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, localRunnerStateDTO{Status: string(status), Config: toConfigDTO(cfg)})
}

// Put — PUT /api/workspaces/:id/local-runner
func (h *LocalRunnerHandler) Put(c *gin.Context) {
	wsID, ok := h.authzWorkspace(c)
	if !ok {
		return
	}
	var req localRunnerConfigDTO
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.LocalDir == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "local_dir is required"})
		return
	}
	cfg := service.LocalRunnerConfig{
		LocalDir:           req.LocalDir,
		PromptSnippet:      req.PromptSnippet,
		AllowedCommands:    req.AllowedCommands,
		AlwaysAllowPersist: req.AlwaysAllowPersist,
	}
	saved, err := h.svc.SaveConfig(c.Request.Context(), wsID, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	status, _, err := h.svc.Status(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, localRunnerStateDTO{Status: string(status), Config: toConfigDTO(saved)})
}

// Delete — DELETE /api/workspaces/:id/local-runner
func (h *LocalRunnerHandler) Delete(c *gin.Context) {
	wsID, ok := h.authzWorkspace(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteConfig(c.Request.Context(), wsID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

const (
	localRunnerWriteWait = 10 * time.Second
	localRunnerPongWait  = 35 * time.Second
)

// LogsStream — WS /ws/workspaces/:id/local-runner/logs (SPA consumer, #494).
// Replays the recent buffer then streams live log lines.
func (h *LocalRunnerHandler) LogsStream(c *gin.Context) {
	wsID, ok := h.authzWorkspace(c)
	if !ok {
		return
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("local-runner logs ws upgrade failed", "error", err)
		return
	}
	subID, ch, replay := h.svc.SubscribeLogs(wsID)
	slog.Info("local-runner logs ws connected", "workspace_id", wsID, "sub", subID)
	defer func() {
		h.svc.UnsubscribeLogs(wsID, subID)
		conn.Close()
	}()

	// Read goroutine — drains pongs / detects client disconnect.
	go func() {
		conn.SetReadDeadline(time.Now().Add(localRunnerPongWait))
		for {
			if _, _, rerr := conn.ReadMessage(); rerr != nil {
				return
			}
			conn.SetReadDeadline(time.Now().Add(localRunnerPongWait))
		}
	}()

	// Flush the replay buffer first so a late subscriber sees history.
	for _, entry := range replay {
		data, merr := json.Marshal(entry)
		if merr != nil {
			continue
		}
		conn.SetWriteDeadline(time.Now().Add(localRunnerWriteWait))
		if werr := conn.WriteMessage(websocket.TextMessage, data); werr != nil {
			return
		}
	}
	for data := range ch {
		conn.SetWriteDeadline(time.Now().Add(localRunnerWriteWait))
		if werr := conn.WriteMessage(websocket.TextMessage, data); werr != nil {
			return
		}
	}
}

// RunnerChannel — WS /ws/workspaces/:id/local-runner/runner (desktop registers
// its reverse channel, #468/#469). Presence is tracked for the lifetime of the
// socket; disconnect withdraws the runner and degrades status (#476/#495).
func (h *LocalRunnerHandler) RunnerChannel(c *gin.Context) {
	wsID, ok := h.authzWorkspace(c)
	if !ok {
		return
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("local-runner reverse-channel ws upgrade failed", "error", err)
		return
	}
	rc := h.svc.RegisterRunner(c.Request.Context(), wsID)
	slog.Info("local-runner reverse channel connected", "workspace_id", wsID)
	defer func() {
		h.svc.UnregisterRunner(c.Request.Context(), rc)
		conn.Close()
	}()

	// Write loop: drain server->runner command dispatch.
	go func() {
		for {
			select {
			case <-rc.Closed:
				return
			case frame, okc := <-rc.Send:
				if !okc {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(localRunnerWriteWait))
				if werr := conn.WriteMessage(websocket.TextMessage, frame); werr != nil {
					return
				}
			}
		}
	}()

	// Read loop: ingest log / exit / response frames (correlation lives in the
	// service so it stays unit-testable).
	conn.SetReadDeadline(time.Now().Add(localRunnerPongWait))
	for {
		_, msg, rerr := conn.ReadMessage()
		if rerr != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(localRunnerPongWait))
		h.svc.HandleRunnerFrame(wsID, rc, msg)
	}
}

// --- MCP tool dispatch endpoints (called by niuniu-mcp local_* tools) --------
//
// These are mounted under /mcp and read the token-bound mcp_workspace_id (never
// a client-supplied id), so an agent can only reach its own workspace's runner.

// runnerReplyJSON is the response shape returned to the MCP tool.
type runnerReplyJSON struct {
	OK      bool   `json:"ok"`
	Stdout  string `json:"stdout,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
	Exit    int    `json:"exit"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

func replyToJSON(r service.RunnerReply) runnerReplyJSON {
	return runnerReplyJSON{OK: r.OK, Stdout: r.Stdout, Stderr: r.Stderr, Exit: r.Exit, Content: r.Content, Error: r.Error}
}

// mcpWorkspace resolves the token-bound workspace, or writes an error response.
func (h *LocalRunnerHandler) mcpWorkspace(c *gin.Context) (int64, bool) {
	wsID := c.GetInt64("mcp_workspace_id")
	if wsID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no workspace bound to this session"})
		return 0, false
	}
	return wsID, true
}

// MCPAvailable — GET /mcp/local-runner/available: reports runner presence.
func (h *LocalRunnerHandler) MCPAvailable(c *gin.Context) {
	wsID, ok := h.mcpWorkspace(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"online": h.svc.IsOnline(wsID)})
}

// MCPExec — POST /mcp/local-runner/exec {command}: run a command on the runner.
func (h *LocalRunnerHandler) MCPExec(c *gin.Context) {
	wsID, ok := h.mcpWorkspace(c)
	if !ok {
		return
	}
	var req struct {
		Command string `json:"command"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Command == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "command is required"})
		return
	}
	rep, err := h.svc.ExecCommand(c.Request.Context(), wsID, req.Command)
	h.writeDispatchResult(c, rep, err)
}

// MCPRead — POST /mcp/local-runner/read {path}: read a file from the runner dir.
func (h *LocalRunnerHandler) MCPRead(c *gin.Context) {
	wsID, ok := h.mcpWorkspace(c)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}
	rep, err := h.svc.ReadFile(c.Request.Context(), wsID, req.Path)
	h.writeDispatchResult(c, rep, err)
}

// MCPSync — POST /mcp/local-runner/sync: force a remote->local worktree sync.
func (h *LocalRunnerHandler) MCPSync(c *gin.Context) {
	wsID, ok := h.mcpWorkspace(c)
	if !ok {
		return
	}
	rep, err := h.svc.Sync(c.Request.Context(), wsID)
	h.writeDispatchResult(c, rep, err)
}

// writeDispatchResult maps a dispatch outcome to HTTP. An offline runner is a
// 409 with a clear message so the AI perceives the degradation (#476), not a
// silent empty result.
func (h *LocalRunnerHandler) writeDispatchResult(c *gin.Context, rep service.RunnerReply, err error) {
	switch {
	case errors.Is(err, service.ErrRunnerOffline):
		c.JSON(http.StatusConflict, gin.H{"error": "local runner offline — tool unavailable; fall back to server-side execution"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusOK, replyToJSON(rep))
	}
}
