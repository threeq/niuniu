// Package api: WorkspaceSceneHandler — REST endpoints for per-workspace
// scene layer management + projection inspection + recommendations.
//
// Routes (wired in server/internal/server/router.go):
//
//	GET    /api/workspaces/:id/scene-layers              List layers
//	POST   /api/workspaces/:id/scene-layers              Attach scene
//	PATCH  /api/workspaces/:id/scene-layers/:layerID     Move layer position
//	DELETE /api/workspaces/:id/scene-layers/:layerID     Detach layer
//	GET    /api/workspaces/:id/scene-projection          Current projection
//	POST   /api/workspaces/:id/scene-projection/recompute  Force recompute
//	GET    /api/workspaces/:id/scene-recommendations     Ranked scenes
package api

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type WorkspaceSceneHandler struct {
	layers    *service.SceneLayerService
	projector *service.SceneProjector
	matcher   *service.SceneMatcher
	authz     *service.Authz
	q         *store.Queries
}

func NewWorkspaceSceneHandler(
	layers *service.SceneLayerService,
	projector *service.SceneProjector,
	matcher *service.SceneMatcher,
	authz *service.Authz,
	q *store.Queries,
) *WorkspaceSceneHandler {
	return &WorkspaceSceneHandler{
		layers: layers, projector: projector, matcher: matcher, authz: authz, q: q,
	}
}

// SceneLayerResponse is the wire shape for a single layer row. Base layer
// uses scene_id=null; consumers use is_base to distinguish.
type SceneLayerResponse struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	SceneID     *int64 `json:"scene_id"`
	Position    int64  `json:"position"`
	IsBase      bool   `json:"is_base"`
}

func toSceneLayerResponse(l store.WorkspaceSceneLayer) SceneLayerResponse {
	r := SceneLayerResponse{
		ID:          l.ID,
		WorkspaceID: l.WorkspaceID,
		Position:    l.Position,
		IsBase:      l.IsBase == 1,
	}
	if l.SceneID.Valid {
		v := l.SceneID.Int64
		r.SceneID = &v
	}
	return r
}

// ApplyResultResponse mirrors service.ApplyResult into JSON. Projection is
// a decoded object (already JSON-shaped via marshalling in Apply).
type ApplyResultResponse struct {
	Projection         *service.Projection           `json:"projection"`
	MissingCredentials []service.MissingCredential   `json:"missing_credentials"`
	InstallFailures    []service.PluginInstallResult `json:"install_failures"`
	RestartRequired    bool                          `json:"restart_required"`
	Digest             string                        `json:"digest"`
	DismissedPlugins   []string                      `json:"dismissed_plugins"`
	// MissingRuntimes lists the launcher commands the projection's inline MCP
	// servers need but which are not on PATH (e.g. "uvx" for office-mail). The
	// SPA shows an install banner so the agent doesn't fail at runtime with
	// "uvx: not found". Computed live (PATH can change), never persisted.
	MissingRuntimes []string `json:"missing_runtimes"`
}

// detectMissingRuntimes returns the distinct `command` launchers referenced by
// the projection's inline MCP server configs that are not resolvable on PATH.
// Cheap LookPath per distinct command; order-stable.
func detectMissingRuntimes(proj *service.Projection) []string {
	if proj == nil || len(proj.MCPConfigs) == 0 {
		return []string{}
	}
	seen := map[string]bool{}
	var missing []string
	for _, cfg := range proj.MCPConfigs {
		cmd, _ := cfg["command"].(string)
		cmd = strings.TrimSpace(cmd)
		if cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, cmd)
		}
	}
	if missing == nil {
		return []string{}
	}
	return missing
}

func toApplyResultResponse(r *service.ApplyResult) ApplyResultResponse {
	if r == nil {
		return ApplyResultResponse{
			MissingCredentials: []service.MissingCredential{},
			InstallFailures:    []service.PluginInstallResult{},
			DismissedPlugins:   []string{},
			MissingRuntimes:    []string{},
		}
	}
	mc := r.MissingCredentials
	if mc == nil {
		mc = []service.MissingCredential{}
	}
	fs := r.InstallFailures
	if fs == nil {
		fs = []service.PluginInstallResult{}
	}
	dp := r.DismissedPlugins
	if dp == nil {
		dp = []string{}
	}
	return ApplyResultResponse{
		Projection:         r.Projection,
		MissingCredentials: mc,
		InstallFailures:    fs,
		RestartRequired:    r.RestartRequired,
		Digest:             r.Digest,
		DismissedPlugins:   dp,
		MissingRuntimes:    detectMissingRuntimes(r.Projection),
	}
}

// ListLayers returns the workspace's layer stack in position-asc order.
func (h *WorkspaceSceneHandler) ListLayers(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	layers, err := h.layers.List(c.Request.Context(), wsID)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]SceneLayerResponse, 0, len(layers))
	for _, l := range layers {
		out = append(out, toSceneLayerResponse(l))
	}
	c.JSON(http.StatusOK, out)
}

type attachLayerRequest struct {
	SceneID  int64 `json:"scene_id" binding:"required"`
	Position *int  `json:"position,omitempty"`
}

func (h *WorkspaceSceneHandler) Attach(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req attachLayerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessScene(c.Request.Context(), userID, req.SceneID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	res, err := h.layers.Attach(c.Request.Context(), wsID, req.SceneID, req.Position)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusCreated, toApplyResultResponse(res))
}

// moveLayerRequest uses a *int for Position so that `0` (a valid target
// position — right after the base layer) is accepted. Gin's `required`
// validator rejects Go zero values on plain `int` fields, which would make
// position-0 moves impossible. Code-review finding #4.
type moveLayerRequest struct {
	Position *int `json:"position"`
}

func (h *WorkspaceSceneHandler) Move(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	layerID, err := strconv.ParseInt(c.Param("layerID"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid layer ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req moveLayerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.Position == nil {
		BadRequest(c, "position is required")
		return
	}
	if *req.Position < 0 {
		BadRequest(c, "position must be >= 0")
		return
	}
	res, err := h.layers.Move(c.Request.Context(), wsID, layerID, *req.Position)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, toApplyResultResponse(res))
}

func (h *WorkspaceSceneHandler) Detach(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	layerID, err := strconv.ParseInt(c.Param("layerID"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid layer ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	res, err := h.layers.Detach(c.Request.Context(), wsID, layerID)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, toApplyResultResponse(res))
}

// GetProjection returns the cached projection row. **Idempotent / read-only**
// — when no projection row exists yet, returns an empty ApplyResultResponse
// rather than running the side-effectful Apply pipeline (which would invoke
// plugin install + .mcp.json + CLAUDE.md writes inside a GET handler). Use
// POST /scene-projection/recompute to force a recompute. Code-review #18.
//
// Reopening a workspace additionally reconciles the cached plugin install
// statuses against the on-disk installed state (a local stat, no install
// spawned): a scene skill that is now safely installed — by an earlier Apply's
// auto-install or by a manual `claude plugin install` — stops surfacing as
// pending/failed, so the projection banner disappears once everything is in.
func (h *WorkspaceSceneHandler) GetProjection(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	row, err := h.q.GetProjection(c.Request.Context(), wsID)
	if err != nil {
		// No cached row → empty response. SPA calls POST recompute when it
		// needs an authoritative snapshot.
		c.JSON(http.StatusOK, ApplyResultResponse{
			Projection:         service.NewProjection(),
			MissingCredentials: []service.MissingCredential{},
			InstallFailures:    []service.PluginInstallResult{},
			DismissedPlugins:   []string{},
			MissingRuntimes:    []string{},
		})
		return
	}
	if reconciled, _, rerr := h.projector.ReconcileInstallStatus(c.Request.Context(), wsID); rerr == nil && reconciled != nil {
		row.InstallFailures = service.InstallResultsToJSON(reconciled)
	}
	c.JSON(http.StatusOK, decodeStoredProjection(row))
}

// Recompute is an explicit "force apply" trigger. Used by the SPA's
// "refresh" button when the user wants to re-check missing credentials
// after a binding change.
func (h *WorkspaceSceneHandler) Recompute(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	res, err := h.projector.Apply(c.Request.Context(), wsID)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toApplyResultResponse(res))
}

// InstallPluginsRequest is the body for POST /workspaces/:id/scene/plugins/install.
// `sources` is an optional whitelist of plugin sources to install; empty
// means "install all currently pending plugins for this workspace".
type InstallPluginsRequest struct {
	Sources []string `json:"sources"`
}

// InstallPlugins is the user-initiated install trigger. Scene Apply only
// records the install PLAN — it never invokes `claude plugin install`
// itself, because that's a network + local-CLI side effect users don't
// expect from a "create workspace" click. The SPA renders pending rows
// from scene_projection.install_failures and posts here when the user
// clicks "Install" (with or without a source filter).
func (h *WorkspaceSceneHandler) InstallPlugins(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var body InstallPluginsRequest
	// Tolerate an empty body — many SPA call sites won't filter by source.
	_ = c.ShouldBindJSON(&body)
	results, err := h.projector.InstallPlugins(c.Request.Context(), wsID, body.Sources)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// DismissPluginRequest is the body for POST /workspaces/:id/scene/plugins/dismiss.
// `source` is the plugin source to (un)dismiss; `dismissed` defaults to true so
// an empty/minimal body means "ignore this plugin". Pass `dismissed:false` to
// restore a previously-ignored plugin back into the install banner.
type DismissPluginRequest struct {
	Source    string `json:"source"`
	Dismissed *bool  `json:"dismissed"`
}

// DismissPlugin lets the user hide a scene-declared plugin's pending/failed row
// from the projection banner — the escape hatch for plugins that cannot be
// installed (e.g. a wrong marketplace in the source) or that the user simply
// does not want. The dismissal is persisted per workspace and survives
// recompute; it is reversible via the same endpoint with dismissed=false.
func (h *WorkspaceSceneHandler) DismissPlugin(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var body DismissPluginRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if body.Source == "" {
		BadRequest(c, "source is required")
		return
	}
	dismissed := true
	if body.Dismissed != nil {
		dismissed = *body.Dismissed
	}
	res, err := h.projector.SetPluginDismissed(c.Request.Context(), wsID, body.Source, dismissed)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toApplyResultResponse(res))
}

// RankedSceneResponse mirrors service.RankedScene for the SPA.
type RankedSceneResponse struct {
	Scene SceneResponse     `json:"scene"`
	Score int               `json:"score"`
	Hits  []service.RuleHit `json:"hits"`
}

// Recommendations returns a sorted list of scenes the matcher thinks are
// relevant for this workspace.
func (h *WorkspaceSceneHandler) Recommendations(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	limit := 10
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	ranked, err := h.matcher.Rank(c.Request.Context(), wsID, userID, limit)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]RankedSceneResponse, 0, len(ranked))
	for _, r := range ranked {
		hits := r.Hits
		if hits == nil {
			hits = []service.RuleHit{}
		}
		out = append(out, RankedSceneResponse{
			Scene: toSceneResponse(r.Scene),
			Score: r.Score,
			Hits:  hits,
		})
	}
	c.JSON(http.StatusOK, out)
}

// decodeStoredProjection unpacks a cached store.WorkspaceSceneProjection row
// into the same ApplyResultResponse shape the live Apply path returns. Keeps
// the SPA's JSON contract single-shape.
func decodeStoredProjection(row store.WorkspaceSceneProjection) ApplyResultResponse {
	var proj service.Projection
	if row.ProjectedDefinition != "" {
		_ = json.Unmarshal([]byte(row.ProjectedDefinition), &proj)
	}
	var missing []service.MissingCredential
	if row.MissingCredentials != "" {
		_ = json.Unmarshal([]byte(row.MissingCredentials), &missing)
	}
	if missing == nil {
		missing = []service.MissingCredential{}
	}
	var failures []service.PluginInstallResult
	if row.InstallFailures != "" {
		_ = json.Unmarshal([]byte(row.InstallFailures), &failures)
	}
	if failures == nil {
		failures = []service.PluginInstallResult{}
	}
	var dismissed []string
	if row.DismissedPlugins != "" {
		_ = json.Unmarshal([]byte(row.DismissedPlugins), &dismissed)
	}
	if dismissed == nil {
		dismissed = []string{}
	}
	// The cached install_failures already excludes dismissed rows (Apply filters
	// before persisting), so no second filter is needed here.
	return ApplyResultResponse{
		Projection:         &proj,
		MissingCredentials: missing,
		InstallFailures:    failures,
		RestartRequired:    row.RestartRequired == 1,
		Digest:             row.Digest,
		DismissedPlugins:   dismissed,
		MissingRuntimes:    detectMissingRuntimes(&proj),
	}
}
