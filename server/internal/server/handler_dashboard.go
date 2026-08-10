// handler_dashboard.go — MCP handlers for the data-dashboard tools.
//
// Routes served (registered in router.go under the /mcp group):
//
//	POST /mcp/dashboards/pin   (pin_query tool)
//	GET  /mcp/dashboards       (list_dashboards tool)
//
// MCPTokenAuth has already validated the bearer token and set auth_user_id +
// mcp_workspace_id on the gin.Context. The CURRENT WORKSPACE is resolved from
// that context and injected as the saved query's origin workspace_id, so the
// agent never has to pass it (and cannot spoof another workspace). Owner +
// accessible org set are resolved from the workspace row, mirroring
// handler_data_proxy.go.
package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// mcpDashboardContext resolves (userID, workspaceID, ownerType, ownerID, orgIDs)
// from the MCP-authenticated context + workspace row. Writes the error response
// and returns ok=false on failure.
func (s *Server) mcpDashboardContext(c *gin.Context) (userID, wsID int64, ownerType string, ownerID int64, orgIDs []int64, ok bool) {
	userID = c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required", "code": "UNAUTHORIZED"})
		return
	}
	wsID = c.GetInt64("mcp_workspace_id")
	if wsID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no workspace context", "code": "UNAUTHORIZED"})
		return
	}
	ws, err := s.queries.GetWorkspace(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace lookup: " + err.Error(), "code": "internal"})
		return
	}
	ownerType = ws.OwnerType
	ownerID = ws.OwnerID
	if ws.OwnerType == "org" {
		orgIDs = []int64{ws.OwnerID}
	}
	ok = true
	return
}

// dashboardPin handles POST /mcp/dashboards/pin (the pin_query tool). The origin
// workspace_id is taken from the session context, not the request body.
func (s *Server) dashboardPin(c *gin.Context) {
	userID, wsID, ownerType, ownerID, orgIDs, ok := s.mcpDashboardContext(c)
	if !ok {
		return
	}

	var req struct {
		SourceID    int64          `json:"source_id"`
		Source      string         `json:"source"`
		Name        string         `json:"name"`
		Operation   map[string]any `json:"operation"`
		ChartSpec   map[string]any `json:"chart_spec"`
		Snapshot    map[string]any `json:"snapshot"`
		DashboardID int64          `json:"dashboard_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required", "code": "bad_request"})
		return
	}

	// source/source_id are optional: when both are omitted this is a static
	// (source-less) pin -- a self-contained chart snapshot. When a source IS
	// referenced (by id or name) it is resolved against the set of sources
	// VISIBLE to this workspace, so a pin cannot smuggle an unbound source onto
	// a dashboard (which would otherwise execute it on refresh).
	sourceID := req.SourceID
	ref := req.Source
	if ref == "" && req.SourceID != 0 {
		ref = strconv.FormatInt(req.SourceID, 10)
	}
	if ref != "" {
		ws, werr := s.queries.GetWorkspace(c.Request.Context(), wsID)
		if werr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace lookup: " + werr.Error(), "code": "internal"})
			return
		}
		resolved, rerr := s.dataSourceSvc.ResolveSourceIDForWorkspace(c.Request.Context(), ref, ws, userID, orgIDs)
		if rerr != nil {
			if errors.Is(rerr, service.ErrDataSourceNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "data source not found or not associated with this workspace: " + ref, "code": "not_found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": rerr.Error(), "code": "internal"})
			return
		}
		sourceID = resolved
	}

	res, err := s.dashboardSvc.Pin(c.Request.Context(), service.PinInput{
		OwnerType:   ownerType,
		OwnerID:     ownerID,
		UserID:      userID,
		OrgIDs:      orgIDs,
		SourceID:    sourceID,
		WorkspaceID: wsID, // current workspace from session token
		DashboardID: req.DashboardID,
		Name:        req.Name,
		Operation:   req.Operation,
		ChartSpec:   req.ChartSpec,
		Snapshot:    req.Snapshot,
	})
	if err != nil {
		s.dashboardMCPError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// dashboardList handles GET /mcp/dashboards (the list_dashboards tool).
func (s *Server) dashboardList(c *gin.Context) {
	userID, _, _, _, orgIDs, ok := s.mcpDashboardContext(c)
	if !ok {
		return
	}
	items, err := s.dashboardSvc.ListDashboards(c.Request.Context(), userID, orgIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "code": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// dashboardMCPError maps DashboardService errors to MCP-friendly HTTP responses.
func (s *Server) dashboardMCPError(c *gin.Context, err error) {
	var notRO *service.ErrSavedQueryNotReadOnly
	switch {
	case errors.As(err, &notRO):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error(), "code": "not_read_only"})
	case errors.Is(err, service.ErrDataSourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "data source not found", "code": "not_found"})
	case errors.Is(err, service.ErrDashboardNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "dashboard not found", "code": "not_found"})
	case errors.Is(err, dataconn.ErrUnsupported):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error(), "code": "unsupported"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "code": "internal"})
	}
}
