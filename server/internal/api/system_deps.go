package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type SystemDepsHandler struct {
	svc          *service.SystemDepsService
	personalMode bool
}

func NewSystemDepsHandler(svc *service.SystemDepsService, personalMode bool) *SystemDepsHandler {
	return &SystemDepsHandler{svc: svc, personalMode: personalMode}
}

// Probe godoc
// @Summary      Probe system dependencies
// @Description  Returns install status and version for node/python3/git/claude
// @Tags         SystemDeps
// @Produce      json
// @Success      200  {object}  service.SystemDepsInfo
// @Router       /system-deps [get]
func (h *SystemDepsHandler) Probe(c *gin.Context) {
	info := h.svc.Probe(c.Request.Context())
	info.PersonalMode = h.personalMode
	c.JSON(http.StatusOK, info)
}

type installReq struct {
	Name string `json:"name"`
}

type installResp struct {
	JobID       string `json:"job_id"`
	FallbackURL string `json:"fallback_url,omitempty"`
}

// Install godoc
// @Summary      Start an install job
// @Tags         SystemDeps
// @Accept       json
// @Produce      json
// @Param        body  body  installReq  true  "tool name"
// @Success      200   {object}  installResp
// @Failure      400,403,409 {object} ErrorResponse
// @Router       /system-deps/install [post]
func (h *SystemDepsHandler) Install(c *gin.Context) {
	var body installReq
	if err := c.ShouldBindJSON(&body); err != nil || body.Name == "" {
		BadRequest(c, "name required")
		return
	}
	jobID, fallback, err := h.svc.Install(c.Request.Context(), body.Name)
	switch {
	case errors.Is(err, service.ErrUnknownTool):
		BadRequest(c, "unknown tool")
		return
	case errors.Is(err, service.ErrInstallDisabled):
		RespondError(c, http.StatusForbidden, "INSTALL_DISABLED", "install not allowed on this deployment")
		return
	case errors.Is(err, service.ErrJobInFlight):
		RespondError(c, http.StatusConflict, "INSTALL_IN_FLIGHT", "another install is running")
		return
	case err != nil:
		RespondError(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	c.JSON(http.StatusOK, installResp{JobID: jobID, FallbackURL: fallback})
}

// Stream godoc
// @Summary      SSE log stream for install job
// @Tags         SystemDeps
// @Produce      text/event-stream
// @Param        id  query  string  true  "job id"
// @Router       /system-deps/install/stream [get]
func (h *SystemDepsHandler) Stream(c *gin.Context) {
	jobID := c.Query("id")
	if jobID == "" {
		BadRequest(c, "id required")
		return
	}
	ch, unsub, err := h.svc.Subscribe(jobID)
	if err != nil {
		RespondError(c, http.StatusNotFound, "JOB_NOT_FOUND", "install job not found")
		return
	}
	defer unsub()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	c.Stream(func(w io.Writer) bool {
		select {
		case <-c.Request.Context().Done():
			return false
		case <-ticker.C:
			// keepalive comment to defeat idle-connection proxies (e.g. Caddy)
			fmt.Fprintf(w, ": ping\n\n")
			return true
		case evt, more := <-ch:
			if !more {
				return false
			}
			b, err := json.Marshal(evt)
			if err != nil {
				slog.Error("system_deps stream marshal", "error", err)
				return true
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			if evt.Done {
				return false
			}
			return true
		}
	})
}

type setGitIdentityReq struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// SetGitIdentity godoc
// @Summary      Set git global user.name / user.email
// @Tags         SystemDeps
// @Accept       json
// @Produce      json
// @Param        body  body  setGitIdentityReq  true  "name + email"
// @Success      200
// @Failure      400  {object}  ErrorResponse
// @Router       /system-deps/git-identity [post]
func (h *SystemDepsHandler) SetGitIdentity(c *gin.Context) {
	var body setGitIdentityReq
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid body")
		return
	}
	if err := h.svc.SetGitIdentity(c.Request.Context(), body.Name, body.Email); err != nil {
		if errors.Is(err, git.ErrInvalidIdentity) {
			RespondError(c, http.StatusBadRequest, "INVALID_GIT_IDENTITY",
				"name must be non-empty and email must be a valid address")
			return
		}
		RespondError(c, http.StatusInternalServerError, "GIT_NOT_AVAILABLE", err.Error())
		return
	}
	c.Status(http.StatusOK)
}
