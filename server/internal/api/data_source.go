// Data-source REST endpoints. Routes live under /api/me/* (caller-scoped) —
// owner is the caller's personal owner in M1, mirroring the external-credential
// handler. The handler is a thin pass-through over DataSourceService:
// request/response shaping + error mapping live here; encryption / persistence
// / connector dispatch live in the service.
//
// Error mapping:
//   - dataconn.ErrUnsupported (wrapped by the service)  -> 422
//   - ErrDataSourceNotFound (authz miss or missing row) -> 404
//   - ErrDataSourceNameTaken                            -> 409
//   - bad body / bad id                                 -> 400
//   - Verify Ping failure                               -> 502
package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// DataSourceHandler binds the data-source endpoints. pool.Evict is invoked on
// delete so any future pooled connection for the source is dropped.
type DataSourceHandler struct {
	svc  *service.DataSourceService
	pool *dataconn.Pool
	// Authz gates the project-scoped association endpoints; set after
	// construction in server wiring (mirrors the other handlers).
	Authz *service.Authz
}

func NewDataSourceHandler(svc *service.DataSourceService, pool *dataconn.Pool) *DataSourceHandler {
	return &DataSourceHandler{svc: svc, pool: pool}
}

// resolveCaller reads the caller's user_id (set by auth.IdentityResolver as
// "auth_user_id"). M1 scopes data sources to the caller's personal owner, so
// orgIDs is nil; this mirrors ExternalCredentialHandler.resolveCallerOwner.
func (h *DataSourceHandler) resolveCaller(c *gin.Context) (userID int64, ok bool) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return 0, false
	}
	return uid, true
}

// callerOrgIDs resolves the orgs the caller belongs to (for owner-visible
// queries). Nil when Authz is unwired (some test setups) — personal scope only.
func (h *DataSourceHandler) callerOrgIDs(c *gin.Context, uid int64) []int64 {
	if h.Authz == nil {
		return nil
	}
	owners, err := h.Authz.Accessible(c.Request.Context(), uid)
	if err != nil {
		return nil
	}
	return owners.OrgIDs
}

// resolveCreateOwner validates the owner fields on a create body and returns
// the (ownerType, ownerID) the row should carry. Personal owner must be the
// caller; org owner requires live membership (EnsureOwnerWritable — any role
// may create resources, spec §6.5). Writes the error response and returns
// ok=false on rejection.
func (h *DataSourceHandler) resolveCreateOwner(c *gin.Context, uid int64, ownerType string, ownerID int64) (string, int64, bool) {
	switch ownerType {
	case "", "user":
		if ownerID != 0 && ownerID != uid {
			writeAuthzError(c, service.ErrForbidden)
			return "", 0, false
		}
		return "user", uid, true
	case "org":
		if ownerID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "owner_id is required for owner_type=org"})
			return "", 0, false
		}
		if h.Authz == nil {
			writeAuthzError(c, service.ErrForbidden)
			return "", 0, false
		}
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), uid, service.OwnerRef{Type: "org", ID: ownerID}); err != nil {
			writeAuthzError(c, err)
			return "", 0, false
		}
		return "org", ownerID, true
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "owner_type must be user or org"})
		return "", 0, false
	}
}

// dtoToJSON renders a DataSourceDTO with last_verified_at as RFC3339-or-null.
func dtoToJSON(d service.DataSourceDTO) gin.H {
	bindings := d.Bindings
	if bindings == nil {
		bindings = []service.DataSourceBinding{}
	}
	return gin.H{
		"id":                  d.ID,
		"owner_type":          d.OwnerType,
		"owner_id":            d.OwnerID,
		"name":                d.Name,
		"kind":                d.Kind,
		"config":              d.Config, // redacted (no password)
		"scope_config":        d.ScopeConfig,
		"default_access_mode": d.DefaultAccessMode,
		"require_confirm":     d.RequireConfirm,
		"last_verified_at":    nullTimeToPtr(d.LastVerifiedAt),
		"bindings":            bindings,
	}
}

// bindingBody is the wire shape of a single visibility binding in create/update
// request bodies.
type bindingBody struct {
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
}

func toServiceBindings(in []bindingBody) []service.DataSourceBinding {
	out := make([]service.DataSourceBinding, 0, len(in))
	for _, b := range in {
		out = append(out, service.DataSourceBinding{TargetType: b.TargetType, TargetID: b.TargetID})
	}
	return out
}

// List returns redacted DTOs for every data source in the caller's accessible
// owner set (personal + orgs).
func (h *DataSourceHandler) List(c *gin.Context) {
	uid, ok := h.resolveCaller(c)
	if !ok {
		return
	}
	sources, err := h.svc.ListForOwner(c.Request.Context(), uid, h.callerOrgIDs(c, uid))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	items := make([]gin.H, len(sources))
	for i, s := range sources {
		items[i] = dtoToJSON(s)
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// createDataSourceBody is the POST body shape. OwnerType/OwnerID select the
// owning account: omitted -> the caller's personal owner; owner_type=org
// creates a TEAM source (any org member may, EnsureOwnerWritable). Bindings,
// when present, scope the new source's visibility (empty/omitted -> invisible
// to every agent).
type createDataSourceBody struct {
	Name              string         `json:"name" binding:"required"`
	Kind              string         `json:"kind" binding:"required"`
	OwnerType         string         `json:"owner_type"`
	OwnerID           int64          `json:"owner_id"`
	Config            map[string]any `json:"config"`
	ScopeConfig       map[string]any `json:"scope_config"`
	DefaultAccessMode string         `json:"default_access_mode"`
	RequireConfirm    string         `json:"require_confirm"`
	Bindings          []bindingBody  `json:"bindings"`
}

// Create inserts a new data source. Unsupported kinds return 422; per-owner
// name clashes return 409. Returns the full redacted DTO (the SPA's
// createDataSource is typed as DataSource and renders the owner badge from
// it).
func (h *DataSourceHandler) Create(c *gin.Context) {
	uid, ok := h.resolveCaller(c)
	if !ok {
		return
	}
	var b createDataSourceBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ownerType, ownerID, ok := h.resolveCreateOwner(c, uid, b.OwnerType, b.OwnerID)
	if !ok {
		return
	}
	id, err := h.svc.Create(c.Request.Context(), service.CreateDataSourceInput{
		OwnerType:         ownerType,
		OwnerID:           ownerID,
		UserID:            uid,
		Name:              b.Name,
		Kind:              b.Kind,
		Config:            b.Config,
		ScopeConfig:       b.ScopeConfig,
		DefaultAccessMode: b.DefaultAccessMode,
		RequireConfirm:    b.RequireConfirm,
	})
	if err != nil {
		h.mapErr(c, err)
		return
	}
	// The creator manages the new row within its owner's scope: personal
	// sources scope to the caller, org sources to the org.
	var orgIDs []int64
	if ownerType == "org" {
		orgIDs = []int64{ownerID}
	}
	// Visibility bindings are stored after the row exists. A create with no
	// bindings leaves the source invisible to every agent (the intended
	// default) until the user associates it with a workspace/project/scene.
	if err := h.svc.SetBindings(c.Request.Context(), id, uid, orgIDs, toServiceBindings(b.Bindings)); err != nil {
		h.mapErr(c, err)
		return
	}
	dto, err := h.svc.Get(c.Request.Context(), id, uid, orgIDs)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, dtoToJSON(*dto))
}

// updateDataSourceBody is the PATCH body. Config is optional: when omitted the
// existing encrypted config is preserved (rename / scope change without
// re-entering the password).
type updateDataSourceBody struct {
	Name              string         `json:"name" binding:"required"`
	Config            map[string]any `json:"config"`
	ScopeConfig       map[string]any `json:"scope_config"`
	DefaultAccessMode string         `json:"default_access_mode"`
	RequireConfirm    string         `json:"require_confirm"`
	// Bindings is a pointer so an omitted field leaves the existing visibility
	// bindings untouched; a present (possibly empty) array replaces them.
	Bindings *[]bindingBody `json:"bindings"`
}

// Update applies a full update to a data source in the caller's accessible
// owner set (personal sources; org sources for members).
func (h *DataSourceHandler) Update(c *gin.Context) {
	uid, ok := h.resolveCaller(c)
	if !ok {
		return
	}
	orgIDs := h.callerOrgIDs(c, uid)
	id, err := parseIDParam(c)
	if err != nil {
		return
	}
	var b updateDataSourceBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.Update(c.Request.Context(), id, uid, orgIDs, service.UpdateDataSourceInput{
		Name:              b.Name,
		Config:            b.Config,
		ScopeConfig:       b.ScopeConfig,
		DefaultAccessMode: b.DefaultAccessMode,
		RequireConfirm:    b.RequireConfirm,
	}); err != nil {
		h.mapErr(c, err)
		return
	}
	if b.Bindings != nil {
		if err := h.svc.SetBindings(c.Request.Context(), id, uid, orgIDs, toServiceBindings(*b.Bindings)); err != nil {
			h.mapErr(c, err)
			return
		}
	}
	c.Status(http.StatusOK)
}

// Delete removes a data source in the caller's accessible owner set and evicts
// any pooled connection for it.
func (h *DataSourceHandler) Delete(c *gin.Context) {
	uid, ok := h.resolveCaller(c)
	if !ok {
		return
	}
	id, err := parseIDParam(c)
	if err != nil {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id, uid, h.callerOrgIDs(c, uid)); err != nil {
		h.mapErr(c, err)
		return
	}
	h.pool.Evict(id)
	c.Status(http.StatusNoContent)
}

// Verify pings the data source connection and, on success, bumps
// last_verified_at. A connection failure maps to 502 so the SPA can show
// "verification failed" distinct from a 4xx config error.
func (h *DataSourceHandler) Verify(c *gin.Context) {
	uid, ok := h.resolveCaller(c)
	if !ok {
		return
	}
	id, err := parseIDParam(c)
	if err != nil {
		return
	}
	if err := h.svc.Verify(c.Request.Context(), id, uid, h.callerOrgIDs(c, uid)); err != nil {
		if errors.Is(err, service.ErrDataSourceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "data source not found"})
			return
		}
		if errors.Is(err, dataconn.ErrUnsupported) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		// Anything else is a live connection / ping failure.
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "error_kind": "verify_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"verified": true})
}

// mapErr translates service errors into the standard HTTP status codes.
func (h *DataSourceHandler) mapErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrDataSourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "data source not found"})
	case errors.Is(err, service.ErrDataSourceNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, dataconn.ErrUnsupported):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// parseIDParam parses and validates the :id path param, writing a 400 and
// returning a non-nil error on failure.
func parseIDParam(c *gin.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad data source id"})
		return 0, errors.New("bad id")
	}
	return id, nil
}

// ----------------------------------------------------------------------------
// Project-scoped data-source association (project settings "数据源" panel).
// Routes: /api/projects/:id/data-sources[/:sid]. These manage a single
// (source, project) visibility binding without touching the source's other
// bindings, so a project owner can associate/disassociate data sources for the
// project's agents from the project settings page.
// ----------------------------------------------------------------------------

// projectIDParam parses the project :id path param for the association routes.
func (h *DataSourceHandler) projectIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad project id"})
		return 0, false
	}
	return id, true
}

// ListProjectSources returns the data sources associated with a project
// (minimal id/name/kind). Requires read access on the project.
func (h *DataSourceHandler) ListProjectSources(c *gin.Context) {
	uid, ok := h.resolveCaller(c)
	if !ok {
		return
	}
	pid, ok := h.projectIDParam(c)
	if !ok {
		return
	}
	if _, err := h.Authz.CanAccessProject(c.Request.Context(), uid, pid); err != nil {
		writeAuthzError(c, err)
		return
	}
	sources, err := h.svc.ListForProject(c.Request.Context(), pid, uid, h.callerOrgIDs(c, uid))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	items := make([]gin.H, len(sources))
	for i, s := range sources {
		items[i] = gin.H{"id": s.ID, "name": s.Name, "kind": s.Kind}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// projectDataSourceBody is the POST body for associating an existing data
// source with a project.
type projectDataSourceBody struct {
	SourceID int64 `json:"source_id" binding:"required"`
}

// AddProjectSource binds an existing data source to the project. Write-gated
// via EnsureOwnerWritable; the source must be accessible to the caller.
func (h *DataSourceHandler) AddProjectSource(c *gin.Context) {
	uid, ok := h.resolveCaller(c)
	if !ok {
		return
	}
	pid, ok := h.projectIDParam(c)
	if !ok {
		return
	}
	owner, err := h.Authz.CanAccessProject(c.Request.Context(), uid, pid)
	if err != nil {
		writeAuthzError(c, err)
		return
	}
	if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		return
	}
	var b projectDataSourceBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.AddBinding(c.Request.Context(), b.SourceID, uid, h.callerOrgIDs(c, uid),
		service.DataSourceBinding{TargetType: "project", TargetID: pid}); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusCreated)
}

// RemoveProjectSource removes the (source, project) binding. Same write gate.
func (h *DataSourceHandler) RemoveProjectSource(c *gin.Context) {
	uid, ok := h.resolveCaller(c)
	if !ok {
		return
	}
	pid, ok := h.projectIDParam(c)
	if !ok {
		return
	}
	owner, err := h.Authz.CanAccessProject(c.Request.Context(), uid, pid)
	if err != nil {
		writeAuthzError(c, err)
		return
	}
	if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		return
	}
	sid, perr := strconv.ParseInt(c.Param("sid"), 10, 64)
	if perr != nil || sid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad data source id"})
		return
	}
	if err := h.svc.RemoveBinding(c.Request.Context(), sid, uid, h.callerOrgIDs(c, uid),
		service.DataSourceBinding{TargetType: "project", TargetID: pid}); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
