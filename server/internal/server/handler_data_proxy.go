// handler_data_proxy.go — HTTP handlers for the data integration proxy.
//
// Routes served (registered in router.go under the /mcp group):
//
//	POST /mcp/data-proxy/query   — run_data_query MCP tool
//	GET  /mcp/data-proxy/sources — list_data_sources MCP tool
//
// MCPTokenAuth has already validated the bearer token and set `auth_user_id`
// + `mcp_workspace_id` on the gin.Context; owner_type / owner_id / session_id
// are resolved from the workspace row (the middleware doesn't propagate them —
// see api/mcp_token.go, same pattern as MCPPermissionHandler).
//
// A *service.DataProxyError (gate rejection) maps to HTTP 403 with
// {message, code} so the MCP shim can extract the message — except
// code=="invalid_input" (malformed operation / bad statement), which maps to
// 400. Policy refusals from Classify (deny list / whitelist miss) arrive as
// DataProxyError{denied} → 403. Only live execution / connection failures
// fall through to 502.
package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// dataProxyQuery handles POST /mcp/data-proxy/query.
func (s *Server) dataProxyQuery(c *gin.Context) {
	userID, ws, ok := s.resolveDataProxySession(c)
	if !ok {
		return
	}

	var req struct {
		SourceID  int64               `json:"source_id"`
		Source    string              `json:"source"`
		Statement string              `json:"statement"`
		Params    []any               `json:"params"`
		RowLimit  int                 `json:"row_limit"`
		Operation *dataProxyOperation `json:"operation"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	if req.Statement == "" && req.Operation == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "statement or operation is required", "code": "bad_request"})
		return
	}
	if req.Statement != "" && req.Operation != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "statement and operation are mutually exclusive", "code": "bad_request"})
		return
	}
	if req.SourceID == 0 && req.Source == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_id or source is required", "code": "bad_request"})
		return
	}

	// Org-owned workspaces make the org's sources visible to the proxy; personal
	// workspaces scope to the caller's personal owner (orgIDs nil).
	orgIDs := wsOrgIDs(ws)

	// Resolve the source reference (numeric id OR name) against the set of
	// sources VISIBLE to this workspace (bound to it, its project, or an
	// attached scene). Gating the numeric-id path too is essential: otherwise an
	// agent could reach an unbound source by guessing its id.
	ref := req.Source
	if ref == "" && req.SourceID != 0 {
		ref = strconv.FormatInt(req.SourceID, 10)
	}
	sourceID, rerr := s.dataSourceSvc.ResolveSourceIDForWorkspace(c.Request.Context(), ref, ws, userID, orgIDs)
	if rerr != nil {
		if errors.Is(rerr, service.ErrDataSourceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "data source not found or not associated with this workspace: " + ref, "code": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": rerr.Error(), "code": "internal"})
		return
	}

	in := service.DataQueryInput{
		WorkspaceID: ws.ID,
		OwnerType:   ws.OwnerType,
		OwnerID:     ws.OwnerID,
		UserID:      userID,
		SessionID:   ws.SessionID.String,
		OrgIDs:      orgIDs,
		SourceID:    sourceID,
		Statement:   req.Statement,
		Params:      req.Params,
		RowLimit:    req.RowLimit,
	}
	if op := req.Operation; op != nil {
		in.Command, in.Args = op.Command, op.Args
		in.Collection, in.MongoOp = op.Collection, op.MongoOp
		in.Filter, in.Pipeline, in.Document = op.Filter, op.Pipeline, op.Document
		in.Index, in.ESMethod, in.ESPath, in.Query = op.Index, op.ESMethod, op.ESPath, op.Query
		in.HTTPMethod, in.HTTPPath = op.HTTPMethod, op.HTTPPath
		in.HTTPQuery, in.HTTPBody = op.HTTPQuery, op.HTTPBody
		in.HTTPListPath = op.HTTPListPath
	}

	rs, err := s.dataProxySvc.Query(c.Request.Context(), in)
	if err != nil {
		if pe, ok := service.IsDataProxyError(err); ok {
			status := http.StatusForbidden
			if pe.Code == "invalid_input" {
				status = http.StatusBadRequest
			}
			c.JSON(status, gin.H{"message": pe.Message, "code": pe.Code})
			return
		}
		if errors.Is(err, service.ErrDataSourceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "data source not found", "code": "not_found"})
			return
		}
		// Backstop: connectors re-run Classify inside Execute (defense in
		// depth), so a policy refusal can also surface from the execute stage.
		// It must read as a client-side rejection, never a 502.
		if errors.Is(err, dataconn.ErrUnsupported) || errors.Is(err, dataconn.ErrDeniedByPolicy) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error(), "code": "unsupported"})
			return
		}
		// Anything else is a live execution / connection failure (502).
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "code": "query_error"})
		return
	}

	c.JSON(http.StatusOK, rs)
}

// dataProxyOperation is the structured (non-SQL) form of a data query, the
// wire shape of run_data_query's `operation` parameter (#361). Exactly one of
// `statement` or `operation` may be set, and only the field group matching
// the source kind is allowed (service.buildOperation enforces both).
type dataProxyOperation struct {
	// Redis
	Command string   `json:"command"`
	Args    []string `json:"args"`
	// Mongo
	Collection string           `json:"collection"`
	MongoOp    string           `json:"mongo_op"`
	Filter     map[string]any   `json:"filter"`
	Pipeline   []map[string]any `json:"pipeline"`
	Document   map[string]any   `json:"document"`
	// Elasticsearch
	Index    string         `json:"index"`
	ESMethod string         `json:"es_method"`
	ESPath   string         `json:"es_path"`
	Query    map[string]any `json:"query"`
	// HTTP
	HTTPMethod   string         `json:"http_method"`
	HTTPPath     string         `json:"http_path"`
	HTTPQuery    map[string]any `json:"http_query"`
	HTTPBody     map[string]any `json:"http_body"`
	HTTPListPath string         `json:"http_list_path"`
}

// dataProxyCreateSource handles POST /mcp/data-proxy/sources (create_data_source
// MCP tool). It creates a data source owned by the workspace owner and binds it
// to the workspace's project so the agent can immediately query it. When
// `verify` is set, it pings the new source and reports the result without
// failing the create. Returns 201 with {source, verified?}.
func (s *Server) dataProxyCreateSource(c *gin.Context) {
	userID, ws, ok := s.resolveDataProxySession(c)
	if !ok {
		return
	}
	var req struct {
		Name              string         `json:"name"`
		Kind              string         `json:"kind"`
		Config            map[string]any `json:"config"`
		ScopeConfig       map[string]any `json:"scope_config"`
		DefaultAccessMode string         `json:"default_access_mode"`
		RequireConfirm    string         `json:"require_confirm"`
		Verify            bool           `json:"verify"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "bad_request"})
		return
	}
	if req.Name == "" || req.Kind == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and kind are required", "code": "bad_request"})
		return
	}

	dto, err := s.dataSourceSvc.CreateForWorkspace(c.Request.Context(), ws, userID, service.CreateDataSourceInput{
		Name:              req.Name,
		Kind:              req.Kind,
		Config:            req.Config,
		ScopeConfig:       req.ScopeConfig,
		DefaultAccessMode: req.DefaultAccessMode,
		RequireConfirm:    req.RequireConfirm,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrWorkspaceNoProject):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error(), "code": "no_project"})
		case errors.Is(err, service.ErrDataSourceNameTaken):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "name_taken"})
		case errors.Is(err, dataconn.ErrUnsupported):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error(), "code": "unsupported"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "code": "internal"})
		}
		return
	}

	resp := gin.H{"source": dto}
	if req.Verify {
		if verr := s.dataSourceSvc.Verify(c.Request.Context(), dto.ID, userID, wsOrgIDs(ws)); verr != nil {
			resp["verified"] = false
			resp["verify_error"] = verr.Error()
		} else {
			resp["verified"] = true
		}
	}
	c.JSON(http.StatusCreated, resp)
}

// resolveDataProxySession extracts auth_user_id + mcp_workspace_id from the
// context, loads and returns the workspace row. Both data proxy handlers share
// this auth + workspace-lookup logic. Returns ok=false and writes an error
// response if anything is missing or the workspace cannot be loaded.
func (s *Server) resolveDataProxySession(c *gin.Context) (userID int64, ws store.Workspace, ok bool) {
	userID = c.GetInt64("auth_user_id")
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required", "code": "UNAUTHORIZED"})
		return 0, store.Workspace{}, false
	}
	wsID := c.GetInt64("mcp_workspace_id")
	if wsID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no workspace context", "code": "UNAUTHORIZED"})
		return 0, store.Workspace{}, false
	}
	ws, err := s.queries.GetWorkspace(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace lookup: " + err.Error(), "code": "internal"})
		return 0, store.Workspace{}, false
	}
	return userID, ws, true
}

// wsOrgIDs derives the orgIDs slice for owner-filtered queries from a workspace
// row: org-owned workspaces make the org's sources visible; personal workspaces
// return nil (caller's personal sources only).
func wsOrgIDs(ws store.Workspace) []int64 {
	if ws.OwnerType == "org" {
		return []int64{ws.OwnerID}
	}
	return nil
}

// dataProxySources handles GET /mcp/data-proxy/sources.
// Returns a minimal (non-sensitive) list of data sources accessible to the
// workspace owner so the agent can discover what sources are configured and
// their kinds before issuing a run_data_query call.
func (s *Server) dataProxySources(c *gin.Context) {
	userID, ws, ok := s.resolveDataProxySession(c)
	if !ok {
		return
	}

	sources, err := s.dataSourceSvc.ListForWorkspace(c.Request.Context(), ws, userID, wsOrgIDs(ws))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "code": "internal"})
		return
	}

	items := make([]gin.H, len(sources))
	for i, src := range sources {
		items[i] = gin.H{
			"id":                  src.ID,
			"name":                src.Name,
			"kind":                src.Kind,
			"default_access_mode": src.DefaultAccessMode,
			"scope_config":        src.ScopeConfig,
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
