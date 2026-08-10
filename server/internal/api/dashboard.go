// Package api: DashboardHandler — REST endpoints for the data-dashboard
// subsystem (saved queries, dashboards, panels, panel data, one-step pin).
//
// Routes (wired in server/internal/server/router.go, under /api):
//
//	GET    /api/saved-queries                       List accessible saved queries
//	POST   /api/saved-queries                       Save a read-only query
//	DELETE /api/saved-queries/:id                   Delete a saved query
//	GET    /api/dashboards                          List accessible dashboards (array)
//	POST   /api/dashboards                          Create a dashboard
//	GET    /api/dashboards/:id                      Get a dashboard
//	PATCH  /api/dashboards/:id                      Rename / re-layout a dashboard
//	DELETE /api/dashboards/:id                      Delete a dashboard
//	POST   /api/dashboards/pin                      One-step pin (auto-create default)
//	GET    /api/dashboards/:id/panels              List a dashboard's panels
//	POST   /api/dashboards/:id/panels              Add a panel
//	DELETE /api/dashboards/:id/panels/:pid         Delete a panel
//	GET    /api/dashboards/:id/panels/:pid/data    Run the panel's read-only query
//
// Error mapping mirrors the rest of the API:
//   - ErrSavedQueryNotReadOnly                          -> 422
//   - ErrSavedQueryNotFound / ErrDashboardNotFound /
//     ErrPanelNotFound / ErrDataSourceNotFound          -> 404
//   - dataconn.ErrUnsupported (unsupported source kind) -> 422
//   - DataProxyError (panel data gate rejection)        -> 403 {message, code}
//   - bad body / bad id                                 -> 400
package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// DashboardHandler binds the dashboard endpoints. authz gathers the caller's
// accessible org set so org-owned dashboards (when the SPA exposes org context)
// are visible; in M1 the SPA pins to the caller's personal owner.
type DashboardHandler struct {
	svc   *service.DashboardService
	authz *service.Authz
}

func NewDashboardHandler(svc *service.DashboardService, authz *service.Authz) *DashboardHandler {
	return &DashboardHandler{svc: svc, authz: authz}
}

// caller reads the authenticated user id and resolves the accessible org set.
func (h *DashboardHandler) caller(c *gin.Context) (userID int64, orgIDs []int64, ok bool) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return 0, nil, false
	}
	if acc, err := h.authz.Accessible(c.Request.Context(), uid); err == nil && acc != nil {
		orgIDs = acc.OrgIDs
	}
	return uid, orgIDs, true
}

// --- Saved queries ---

type saveQueryBody struct {
	// SourceID is optional: omit it (0) to save a static (source-less) chart
	// snapshot; provide it to save a re-runnable read-only query.
	SourceID    int64          `json:"source_id"`
	WorkspaceID int64          `json:"workspace_id"`
	Name        string         `json:"name" binding:"required"`
	Operation   map[string]any `json:"operation"`
	ChartSpec   map[string]any `json:"chart_spec"`
	Snapshot    map[string]any `json:"snapshot"`
}

// ListSavedQueries returns the caller's accessible saved queries.
func (h *DashboardHandler) ListSavedQueries(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	items, err := h.svc.ListSavedQueries(c.Request.Context(), uid, orgIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// CreateSavedQuery persists a read-only query for the caller's personal owner.
func (h *DashboardHandler) CreateSavedQuery(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	var b saveQueryBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	wsID := h.sanitizeWorkspaceID(c, uid, b.WorkspaceID)
	id, err := h.svc.SaveQuery(c.Request.Context(), service.SaveQueryInput{
		OwnerType:   "user",
		OwnerID:     uid,
		UserID:      uid,
		OrgIDs:      orgIDs,
		SourceID:    b.SourceID,
		WorkspaceID: wsID,
		Name:        b.Name,
		Operation:   b.Operation,
		ChartSpec:   b.ChartSpec,
		Snapshot:    b.Snapshot,
	})
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// DeleteSavedQuery removes a saved query the caller can access.
func (h *DashboardHandler) DeleteSavedQuery(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	id, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	if err := h.svc.DeleteSavedQuery(c.Request.Context(), id, uid, orgIDs); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// --- Dashboards ---

type createDashboardBody struct {
	Name   string         `json:"name"`
	Layout map[string]any `json:"layout"`
}

// ListDashboards returns the caller's accessible dashboards as an array. The SPA
// gates the nav entry on this array being non-empty.
func (h *DashboardHandler) ListDashboards(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	items, err := h.svc.ListDashboards(c.Request.Context(), uid, orgIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// CreateDashboard creates a dashboard for the caller's personal owner.
func (h *DashboardHandler) CreateDashboard(c *gin.Context) {
	uid, _, ok := h.caller(c)
	if !ok {
		return
	}
	var b createDashboardBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, err := h.svc.CreateDashboard(c.Request.Context(), service.CreateDashboardInput{
		OwnerType: "user",
		OwnerID:   uid,
		Name:      b.Name,
		Layout:    b.Layout,
	})
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// GetDashboard returns a single dashboard the caller can access.
func (h *DashboardHandler) GetDashboard(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	id, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	dto, err := h.svc.GetDashboard(c.Request.Context(), id, uid, orgIDs)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, dto)
}

type updateDashboardBody struct {
	Name   string         `json:"name" binding:"required"`
	Layout map[string]any `json:"layout"`
}

// UpdateDashboard renames / re-layouts a dashboard the caller can access.
func (h *DashboardHandler) UpdateDashboard(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	id, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	var b updateDashboardBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.UpdateDashboard(c.Request.Context(), id, uid, orgIDs, b.Name, b.Layout); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// DeleteDashboard removes a dashboard the caller can access.
func (h *DashboardHandler) DeleteDashboard(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	id, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	if err := h.svc.DeleteDashboard(c.Request.Context(), id, uid, orgIDs); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// --- Pin ---

type pinBody struct {
	// SourceID is optional: omit it (0) to pin a self-contained static chart
	// (snapshot); provide it to pin a re-runnable read-only query.
	SourceID    int64          `json:"source_id"`
	WorkspaceID int64          `json:"workspace_id"`
	DashboardID int64          `json:"dashboard_id"`
	Name        string         `json:"name" binding:"required"`
	Operation   map[string]any `json:"operation"`
	ChartSpec   map[string]any `json:"chart_spec"`
	Snapshot    map[string]any `json:"snapshot"`
}

// Pin is the one-step "pin chart to dashboard" endpoint. With no dashboard_id it
// auto-creates the caller's default dashboard (first pin makes the nav entry
// appear). A supplied workspace_id is recorded only when the caller can access
// it; an inaccessible workspace_id is dropped (it is a navigation convenience,
// not an authorization boundary, so it must not block the pin).
func (h *DashboardHandler) Pin(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	var b pinBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	wsID := h.sanitizeWorkspaceID(c, uid, b.WorkspaceID)
	res, err := h.svc.Pin(c.Request.Context(), service.PinInput{
		OwnerType:   "user",
		OwnerID:     uid,
		UserID:      uid,
		OrgIDs:      orgIDs,
		SourceID:    b.SourceID,
		WorkspaceID: wsID,
		DashboardID: b.DashboardID,
		Name:        b.Name,
		Operation:   b.Operation,
		ChartSpec:   b.ChartSpec,
		Snapshot:    b.Snapshot,
	})
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// --- Panels ---

type addPanelBody struct {
	SavedQueryID       int64          `json:"saved_query_id" binding:"required"`
	Title              string         `json:"title"`
	VizType            string         `json:"viz_type"`
	ChartSpec          map[string]any `json:"chart_spec"`
	GridX              int64          `json:"grid_x"`
	GridY              int64          `json:"grid_y"`
	GridW              int64          `json:"grid_w"`
	GridH              int64          `json:"grid_h"`
	RefreshIntervalSec int64          `json:"refresh_interval_sec"`
}

// ListPanels returns the panels of a dashboard the caller can access.
func (h *DashboardHandler) ListPanels(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	dashID, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	items, err := h.svc.ListPanels(c.Request.Context(), dashID, uid, orgIDs)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// AddPanel binds a saved query to a dashboard as a panel.
func (h *DashboardHandler) AddPanel(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	dashID, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	var b addPanelBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, err := h.svc.AddPanel(c.Request.Context(), service.AddPanelInput{
		DashboardID:        dashID,
		SavedQueryID:       b.SavedQueryID,
		Title:              b.Title,
		VizType:            b.VizType,
		ChartSpec:          b.ChartSpec,
		GridX:              b.GridX,
		GridY:              b.GridY,
		GridW:              b.GridW,
		GridH:              b.GridH,
		RefreshIntervalSec: b.RefreshIntervalSec,
	}, uid, orgIDs)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// DeletePanel removes a panel from a dashboard the caller can access.
func (h *DashboardHandler) DeletePanel(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	dashID, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	panelID, err := parseDashIDParam(c, "pid")
	if err != nil {
		return
	}
	if err := h.svc.DeletePanel(c.Request.Context(), dashID, panelID, uid, orgIDs); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// PanelData runs the panel's read-only saved query through the data-proxy gate
// and returns the normalized ResultSet.
func (h *DashboardHandler) PanelData(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	dashID, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	panelID, err := parseDashIDParam(c, "pid")
	if err != nil {
		return
	}
	rs, err := h.svc.PanelData(c.Request.Context(), dashID, panelID, uid, orgIDs)
	if err != nil {
		if pe, isPE := service.IsDataProxyError(err); isPE {
			c.JSON(http.StatusForbidden, gin.H{"message": pe.Message, "code": pe.Code})
			return
		}
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, rs)
}

type movePanelBody struct {
	TargetDashboardID int64 `json:"target_dashboard_id" binding:"required"`
}

// MovePanel moves a panel to another dashboard (both accessible to the caller).
func (h *DashboardHandler) MovePanel(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	dashID, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	panelID, err := parseDashIDParam(c, "pid")
	if err != nil {
		return
	}
	var b movePanelBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.MovePanel(c.Request.Context(), dashID, panelID, b.TargetDashboardID, uid, orgIDs); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusOK)
}

// CopyPanel duplicates a panel onto another dashboard (both accessible).
func (h *DashboardHandler) CopyPanel(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	dashID, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	panelID, err := parseDashIDParam(c, "pid")
	if err != nil {
		return
	}
	var b movePanelBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, err := h.svc.CopyPanel(c.Request.Context(), dashID, panelID, b.TargetDashboardID, uid, orgIDs)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// ListWorkspacePanels returns the panels pinned from a workspace (the
// in-workspace "data" view). A workspace the caller cannot access yields an
// empty list — this is a convenience view, not an authorization boundary.
func (h *DashboardHandler) ListWorkspacePanels(c *gin.Context) {
	uid, orgIDs, ok := h.caller(c)
	if !ok {
		return
	}
	wsID, err := parseDashIDParam(c, "id")
	if err != nil {
		return
	}
	if _, aerr := h.authz.CanAccessWorkspace(c.Request.Context(), uid, wsID); aerr != nil {
		c.JSON(http.StatusOK, gin.H{"items": []any{}})
		return
	}
	items, err := h.svc.ListPanelsForWorkspace(c.Request.Context(), wsID, uid, orgIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// --- helpers ---

// sanitizeWorkspaceID keeps a supplied origin workspace id only when the caller
// can access it. An inaccessible id is dropped to 0 (NULL) rather than rejected:
// the origin workspace is a click-to-jump convenience, not a security boundary.
func (h *DashboardHandler) sanitizeWorkspaceID(c *gin.Context, userID, wsID int64) int64 {
	if wsID <= 0 {
		return 0
	}
	if _, err := h.authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
		return 0
	}
	return wsID
}

// mapErr translates DashboardService errors into HTTP status codes.
func (h *DashboardHandler) mapErr(c *gin.Context, err error) {
	var notRO *service.ErrSavedQueryNotReadOnly
	switch {
	case errors.As(err, &notRO):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error(), "code": "not_read_only"})
	case errors.Is(err, service.ErrSavedQueryNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "saved query not found"})
	case errors.Is(err, service.ErrDashboardNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "dashboard not found"})
	case errors.Is(err, service.ErrPanelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "dashboard panel not found"})
	case errors.Is(err, service.ErrDataSourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "data source not found"})
	case errors.Is(err, dataconn.ErrUnsupported):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// parseDashIDParam parses and validates a path param, writing 400 on failure.
func parseDashIDParam(c *gin.Context, name string) (int64, error) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad " + name})
		return 0, errors.New("bad id")
	}
	return id, nil
}
