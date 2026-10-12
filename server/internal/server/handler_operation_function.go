// handler_operation_function.go — HTTP handlers for sandbox operation
// functions (Epic #726 / #739).
//
// Two surfaces, and the split is the point:
//
//   - /api/me/operation-functions is the HUMAN surface. It can run a function
//     by hand, enable or disable one, roll one back, and read the fence's
//     self-check. It still cannot publish: publication rides the provider draft
//     pipeline, where the confirmation card is.
//   - /mcp/operation-functions is the AGENT surface. It may list what exists
//     and CALL it. It may not create, edit, enable, delete or roll back — those
//     change what the integration may reach, and an agent has no authority to
//     grant itself that.
package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/sandbox"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// runFenceSelfTest builds the three probes the isolation acceptance needs — a
// file the user can read but the box must not, and a socket the user can reach
// but the box must not — and runs them inside the fence.
func (s *Server) runFenceSelfTest(c *gin.Context) (*sandbox.FenceReport, error) {
	dir, err := os.MkdirTemp("", "niuniu-fence-selftest")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	probeFile := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(probeFile, []byte("niuniu-fence-selftest"), 0o600); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	return sandbox.FenceSelfTest(ctx, probeFile, ln.Addr().String())
}

// --- SPA (human) surface --------------------------------------------------

func (s *Server) functionList(c *gin.Context) {
	ownerType, ownerID, uid, ok := s.functionOwner(c)
	if !ok {
		return
	}
	_ = uid
	views, err := s.functionSvc.List(c.Request.Context(), ownerType, ownerID)
	if err != nil {
		writeFunctionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": views})
}

func (s *Server) functionAudit(c *gin.Context) {
	ownerType, ownerID, _, ok := s.functionOwner(c)
	if !ok {
		return
	}
	records, err := s.functionSvc.Audit(c.Request.Context(), ownerType, ownerID, queryInt64(c, "limit"))
	if err != nil {
		writeFunctionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": records})
}

// functionSandbox reports the OS fence's verdict for this host. It is what the
// UI renders as the degradation banner, and it is the reason a function can be
// published but not run: the refusal is visible rather than silent.
func (s *Server) functionSandbox(c *gin.Context) {
	avail := sandbox.SelfCheck(c.Query("refresh") == "true")
	c.JSON(http.StatusOK, gin.H{"data": avail})
}

// functionSandboxSelfTest runs the isolation acceptance: read a file, open a
// socket, start a process — from inside the box.
func (s *Server) functionSandboxSelfTest(c *gin.Context) {
	report, err := s.runFenceSelfTest(c)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"code": "SANDBOX_UNAVAILABLE", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": report})
}

func (s *Server) functionSetEnabled(c *gin.Context) {
	ownerType, ownerID, _, ok := s.functionOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid function id"}})
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "enabled (boolean) is required"}})
		return
	}
	if err := s.functionSvc.SetEnabled(c.Request.Context(), ownerType, ownerID, id, *body.Enabled); err != nil {
		writeFunctionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": id, "enabled": *body.Enabled}})
}

func (s *Server) functionDelete(c *gin.Context) {
	ownerType, ownerID, _, ok := s.functionOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid function id"}})
		return
	}
	if err := s.functionSvc.Delete(c.Request.Context(), ownerType, ownerID, id); err != nil {
		writeFunctionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"deleted": true, "id": id}})
}

func (s *Server) functionVersions(c *gin.Context) {
	ownerType, ownerID, _, ok := s.functionOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid function id"}})
		return
	}
	// A function id alone must not be enough to read another owner's function:
	// the write paths already gate on ownership, and these reads have to as
	// well or the gate is trivially bypassed.
	if err := s.functionSvc.EnsureOwner(c.Request.Context(), ownerType, ownerID, id); err != nil {
		writeFunctionError(c, err)
		return
	}
	versions, err := s.functionSvc.ListVersions(c.Request.Context(), id, queryInt64(c, "limit"))
	if err != nil {
		writeFunctionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": versions})
}

func (s *Server) functionObservation(c *gin.Context) {
	ownerType, ownerID, _, ok := s.functionOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid function id"}})
		return
	}
	// The observation trail contains the functions's request history, so it is
	// owner-scoped for the same reason the versions are.
	if err := s.functionSvc.EnsureOwner(c.Request.Context(), ownerType, ownerID, id); err != nil {
		writeFunctionError(c, err)
		return
	}
	trail, err := s.functionSvc.ObservationTrail(c.Request.Context(), id, queryInt64(c, "limit"))
	if err != nil {
		writeFunctionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": trail})
}

func (s *Server) functionRollback(c *gin.Context) {
	ownerType, ownerID, uid, ok := s.functionOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid function id"}})
		return
	}
	var body struct {
		VersionID    int64    `json:"version_id"`
		Confirmed    bool     `json:"confirmed"`
		Acknowledged []string `json:"acknowledged"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()}})
		return
	}
	res, err := s.functionSvc.Rollback(c.Request.Context(), service.FunctionRollbackInput{
		FunctionID: id, VersionID: body.VersionID,
		OwnerType: ownerType, OwnerID: ownerID, ByUser: uid,
		Confirmed: body.Confirmed, AckKeys: body.Acknowledged,
	})
	if err != nil {
		writeFunctionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

// functionInvoke runs a function by hand from the settings page. Caller=user so
// the audit distinguishes a person's test run from an agent's call.
func (s *Server) functionInvoke(c *gin.Context) {
	ownerType, ownerID, uid, ok := s.functionOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid function id"}})
		return
	}
	var body struct {
		Input          map[string]any `json:"input"`
		IdempotencyKey string         `json:"idempotency_key"`
	}
	_ = c.ShouldBindJSON(&body)
	res, err := s.functionSvc.Invoke(c.Request.Context(), service.FunctionInvokeInput{
		OwnerType: ownerType, OwnerID: ownerID, UserID: uid,
		FunctionID: id, Input: body.Input, Caller: service.CallerUser,
		IdempotencyKey: body.IdempotencyKey,
	})
	if err != nil {
		writeFunctionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

// functionRehearse previews an UNSAVED function body: it lints and runs it
// against a mock upstream and returns the manifest. It grants nothing and
// touches no network, so it is safe on the human surface while an author is
// still iterating.
func (s *Server) functionRehearse(c *gin.Context) {
	if _, _, _, ok := s.functionOwner(c); !ok {
		return
	}
	var body struct {
		Provider string                       `json:"provider"`
		Function service.FunctionDraftPayload `json:"function"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()}})
		return
	}
	if strings.TrimSpace(body.Provider) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "provider is required"}})
		return
	}
	spec := body.Function.Spec()
	findings := sandbox.Lint(spec.Code)
	report := (*service.RehearsalReport)(nil)
	if !sandbox.LintBlocks(findings) {
		report = s.functionSvc.Rehearse(c.Request.Context(), service.RehearseRequest{
			ProviderName: body.Provider, Spec: spec, Input: body.Function.SampleInput,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"lint":      findings,
		"blocking":  sandbox.LintBlocks(findings),
		"rehearsal": report,
	}})
}

// --- agent (MCP) surface --------------------------------------------------

// mcpFunctionList is the discovery call: which functions this project's owner
// has published and enabled, with the tool name each projects to.
func (s *Server) mcpFunctionList(c *gin.Context) {
	ownerType, ownerID, _, ok := s.functionOwner(c)
	if !ok {
		return
	}
	views, err := s.functionSvc.List(c.Request.Context(), ownerType, ownerID)
	if err != nil {
		writeFunctionError(c, err)
		return
	}
	provider := strings.TrimSpace(c.Query("provider"))
	available := make([]service.FunctionView, 0, len(views))
	for _, v := range views {
		if !v.Enabled {
			continue
		}
		if provider != "" && v.ProviderName != provider {
			continue
		}
		available = append(available, v)
	}
	c.JSON(http.StatusOK, gin.H{"data": available})
}

// mcpFunctionCall is the invocation an agent makes.
func (s *Server) mcpFunctionCall(c *gin.Context) {
	ownerType, ownerID, uid, ok := s.functionOwner(c)
	if !ok {
		return
	}
	var body struct {
		Provider       string         `json:"provider"`
		Function       string         `json:"function"`
		FunctionID     int64          `json:"function_id"`
		Input          map[string]any `json:"input"`
		SourceKey      string         `json:"source_key"`
		IdempotencyKey string         `json:"idempotency_key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()}})
		return
	}
	wsID := c.GetInt64("mcp_workspace_id")
	res, err := s.functionSvc.Invoke(c.Request.Context(), service.FunctionInvokeInput{
		OwnerType: ownerType, OwnerID: ownerID, UserID: uid,
		FunctionID: body.FunctionID, FunctionName: body.Function, ProviderName: body.Provider,
		Input: body.Input, WorkspaceID: wsID, SourceKey: body.SourceKey,
		Caller: service.CallerAgent, IdempotencyKey: body.IdempotencyKey,
	})
	if err != nil {
		writeFunctionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

// mcpFunctionFence lets the agent discover that this host cannot run functions
// at all, instead of collecting an opaque failure per attempt.
func (s *Server) mcpFunctionFence(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": sandbox.SelfCheck(false)})
}

// --- shared helpers -------------------------------------------------------

// functionOwner resolves the owner scope for a request. On the MCP surface the
// workspace comes from the SESSION TOKEN, never from the body, so an agent
// cannot address another owner's functions.
func (s *Server) functionOwner(c *gin.Context) (string, int64, int64, bool) {
	uid := c.GetInt64("auth_user_id")
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "authentication required"}})
		return "", 0, 0, false
	}
	ownerType, ownerID, ok := s.resolveDraftOwner(c, uid, c.Query("owner_type"), queryInt64(c, "owner_id"))
	if !ok {
		return "", 0, 0, false
	}
	return ownerType, ownerID, uid, true
}

func writeFunctionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrFunctionNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
	case errors.Is(err, service.ErrFunctionDisabled):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "FUNCTION_DISABLED", "message": err.Error()}})
	case errors.Is(err, service.ErrFenceUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "SANDBOX_UNAVAILABLE", "message": err.Error()}})
	case errors.Is(err, service.ErrAgentCannotAuthorize):
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "AGENT_CANNOT_AUTHORIZE", "message": err.Error()}})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
	}
}
