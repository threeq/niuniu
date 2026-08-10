// Package api: ProjectBlueprintHandler — REST endpoints for project blueprints
// (UI: "项目模板" / project template). A blueprint snapshots a project's kanban
// columns + default scenes and can be applied when creating a new project.
//
// Routes:
//
//	POST   /api/projects/:id/blueprints     Save project :id as a new blueprint
//	GET    /api/project-blueprints          List blueprints visible to the caller
//	DELETE /api/project-blueprints/:id       Delete a blueprint
//
// Applying a blueprint happens in ProjectHandler.Create via the optional
// blueprint_id field — see project.go.
package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type ProjectBlueprintHandler struct {
	svc   *service.ProjectBlueprintService
	authz *service.Authz
}

func NewProjectBlueprintHandler(svc *service.ProjectBlueprintService, authz *service.Authz) *ProjectBlueprintHandler {
	return &ProjectBlueprintHandler{svc: svc, authz: authz}
}

// ProjectBlueprintSummary is the list/create response. It exposes counts rather
// than the full column/scene payload — the create-project picker only needs
// name + a sense of size.
type ProjectBlueprintSummary struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Owner       OwnerDTO `json:"owner"`
	Source      string   `json:"source"` // 'user' | 'builtin'
	Slug        string   `json:"slug"`
	IsBuiltin   bool     `json:"is_builtin"`
	ColumnCount int      `json:"column_count"`
	SceneCount  int      `json:"scene_count"`
	CreatedAt   string   `json:"created_at"`
}

func toBlueprintSummary(bp service.ProjectBlueprint) ProjectBlueprintSummary {
	return ProjectBlueprintSummary{
		ID:          bp.ID,
		Name:        bp.Name,
		Description: bp.Description,
		Owner:       ownerDTOFromRef(bp.Owner.Type, bp.Owner.ID),
		Source:      bp.Source,
		Slug:        bp.Slug,
		IsBuiltin:   bp.Source == "builtin",
		ColumnCount: len(bp.Columns),
		SceneCount:  len(bp.Scenes),
		CreatedAt:   bp.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// resolveOwnerParam reads an optional owner_type/owner_id query pair, defaulting
// to the caller's personal space. It enforces write access to the resolved
// owner (templates are listed/defaulted within an owner the caller controls).
func (h *ProjectBlueprintHandler) resolveOwnerParam(c *gin.Context, userID int64) (service.OwnerRef, bool) {
	owner := service.OwnerRef{Type: "user", ID: userID}
	if t := c.Query("owner_type"); t != "" {
		id, err := strconv.ParseInt(c.Query("owner_id"), 10, 64)
		if err != nil {
			BadRequest(c, "invalid owner_id")
			return owner, false
		}
		// Treat the SPA no-currentUser fallback {user,0} as "default to caller".
		if !(t == "user" && id == 0) {
			owner = service.OwnerRef{Type: t, ID: id}
		}
	}
	if h.authz != nil && userID > 0 {
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return owner, false
		}
	}
	return owner, true
}

type saveBlueprintRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// SaveFromProject handles POST /projects/:id/blueprints.
func (h *ProjectBlueprintHandler) SaveFromProject(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	var req saveBlueprintRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	// The blueprint is owned by the source project's owner; the caller must be
	// able to read that project and write to its owner.
	owner := service.OwnerRef{Type: "user", ID: userID}
	if h.authz != nil && userID > 0 {
		o, err := h.authz.CanAccessProject(c.Request.Context(), userID, pid)
		if err != nil {
			writeAuthzError(c, err)
			return
		}
		owner = o
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	bp, err := h.svc.SaveFromProject(c.Request.Context(), pid, req.Name, req.Description, owner.Type, owner.ID)
	if err != nil {
		if errors.Is(err, service.ErrBlueprintNameExists) {
			BadRequest(c, err.Error())
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toBlueprintSummary(bp))
}

// List handles GET /project-blueprints[?owner_type=&owner_id=]. Returns the
// blueprints usable by the owner (all builtins + that owner's own), defaulting
// to the caller's personal space.
func (h *ProjectBlueprintHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	owner, ok := h.resolveOwnerParam(c, userID)
	if !ok {
		return
	}
	list, err := h.svc.ListForOwner(c.Request.Context(), owner)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]ProjectBlueprintSummary, 0, len(list))
	for _, bp := range list {
		out = append(out, toBlueprintSummary(bp))
	}
	c.JSON(http.StatusOK, out)
}

// GetDefault handles GET /project-blueprints/default[?owner_type=&owner_id=].
// Returns the blueprint id pre-selected when creating a project for the owner
// (0 when no blueprint exists at all).
func (h *ProjectBlueprintHandler) GetDefault(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	owner, ok := h.resolveOwnerParam(c, userID)
	if !ok {
		return
	}
	id, err := h.svc.ResolveDefaultID(c.Request.Context(), owner)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"blueprint_id": id})
}

type setDefaultRequest struct {
	OwnerType string `json:"owner_type"`
	OwnerID   int64  `json:"owner_id"`
}

// SetDefault handles PUT /project-blueprints/:id/default. Sets the calling
// owner's default-blueprint pointer to :id. The target owner defaults to the
// caller's personal space; an explicit owner in the body must be writable.
func (h *ProjectBlueprintHandler) SetDefault(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid blueprint ID")
		return
	}
	var req setDefaultRequest
	_ = c.ShouldBindJSON(&req)
	owner := service.OwnerRef{Type: "user", ID: userID}
	if req.OwnerType != "" && !(req.OwnerType == "user" && req.OwnerID == 0) {
		owner = service.OwnerRef{Type: req.OwnerType, ID: req.OwnerID}
	}
	if h.authz != nil && userID > 0 {
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if err := h.svc.SetDefault(c.Request.Context(), owner, id); err != nil {
		switch {
		case errors.Is(err, service.ErrNotFound):
			NotFound(c, "blueprint")
		case errors.Is(err, service.ErrForbidden):
			writeAuthzError(c, err)
		default:
			InternalError(c, err)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete handles DELETE /project-blueprints/:id.
func (h *ProjectBlueprintHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid blueprint ID")
		return
	}
	bp, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			NotFound(c, "blueprint")
			return
		}
		InternalError(c, err)
		return
	}
	// Builtin templates ship with the system and cannot be deleted (only
	// duplicated / re-defaulted). Users delete their own templates.
	if bp.Source == "builtin" {
		BadRequest(c, "builtin templates cannot be deleted")
		return
	}
	if h.authz != nil && userID > 0 {
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), userID, bp.Owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ── full detail + CRUD (settings → 项目模板 manager) ─────────────────────────

// BlueprintColumnDTO is one column in a template's detail / create / update
// payload. Position is output-only (server reindexes by array order on write).
type BlueprintColumnDTO struct {
	Name          string `json:"name"`
	Position      int64  `json:"position"`
	OpPrimitive   string `json:"op_primitive"`
	OpInstruction string `json:"op_instruction"`
	WhenToUse     string `json:"when_to_use"`
	Lifecycle     string `json:"lifecycle_mapping"`
}

// BlueprintSceneDTO is one scene reference in a template's detail payload.
type BlueprintSceneDTO struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Source      string `json:"source"`
}

// ProjectBlueprintDetail is the full template payload (summary + columns + scenes).
type ProjectBlueprintDetail struct {
	ProjectBlueprintSummary
	Columns []BlueprintColumnDTO `json:"columns"`
	Scenes  []BlueprintSceneDTO  `json:"scenes"`
}

func toColumnDTOs(seeds []service.ColumnSeed) []BlueprintColumnDTO {
	out := make([]BlueprintColumnDTO, 0, len(seeds))
	for _, s := range seeds {
		out = append(out, BlueprintColumnDTO{
			Name: s.Name, Position: s.Position, OpPrimitive: s.OpPrimitive,
			OpInstruction: s.Instruction, WhenToUse: s.WhenToUse, Lifecycle: s.Lifecycle,
		})
	}
	return out
}

func toBlueprintDetail(bp service.ProjectBlueprint) ProjectBlueprintDetail {
	scenes := make([]BlueprintSceneDTO, 0, len(bp.Scenes))
	for _, sc := range bp.Scenes {
		scenes = append(scenes, BlueprintSceneDTO{Slug: sc.Slug, DisplayName: sc.DisplayName, Source: sc.Source})
	}
	return ProjectBlueprintDetail{
		ProjectBlueprintSummary: toBlueprintSummary(bp),
		Columns:                 toColumnDTOs(bp.Columns),
		Scenes:                  scenes,
	}
}

// seedsFromColumnInputs maps request columns to ColumnSeed, defaulting an empty
// op_primitive to 'none' and validating the value.
func seedsFromColumnInputs(cols []BlueprintColumnDTO) ([]service.ColumnSeed, error) {
	out := make([]service.ColumnSeed, 0, len(cols))
	for i, c := range cols {
		prim := c.OpPrimitive
		if prim == "" {
			prim = "none"
		}
		if prim != "none" && prim != "instruct" && prim != "complete" {
			return nil, errBadPrimitive
		}
		name := c.Name
		out = append(out, service.ColumnSeed{
			Name: name, Position: int64(i), Lifecycle: c.Lifecycle,
			OpPrimitive: prim, Instruction: c.OpInstruction, WhenToUse: c.WhenToUse,
		})
	}
	return out, nil
}

var errBadPrimitive = errors.New("op_primitive must be one of none/instruct/complete")

// readAccess verifies the caller may read a (possibly builtin) blueprint.
func (h *ProjectBlueprintHandler) readAccess(c *gin.Context, userID int64, bp service.ProjectBlueprint) bool {
	if h.authz == nil || userID <= 0 || bp.Source == "builtin" {
		return true
	}
	if err := h.authz.CanAccessOwner(c.Request.Context(), userID, bp.Owner); err != nil {
		writeAuthzError(c, err)
		return false
	}
	return true
}

// GetDetail handles GET /project-blueprints/:id — full columns + scenes.
func (h *ProjectBlueprintHandler) GetDetail(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid blueprint ID")
		return
	}
	bp, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			NotFound(c, "blueprint")
			return
		}
		InternalError(c, err)
		return
	}
	if !h.readAccess(c, userID, bp) {
		return
	}
	c.JSON(http.StatusOK, toBlueprintDetail(bp))
}

type createBlueprintRequest struct {
	Name        string               `json:"name" binding:"required"`
	Description string               `json:"description"`
	Owner       *ownerBody           `json:"owner,omitempty"`
	Columns     []BlueprintColumnDTO `json:"columns"`
	Scenes      []BlueprintSceneDTO  `json:"scenes"`
}

// scenesFromDTOs maps request scene refs to service scenes, dropping empty slugs.
func scenesFromDTOs(in []BlueprintSceneDTO) []service.BlueprintScene {
	out := make([]service.BlueprintScene, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s.Slug) == "" {
			continue
		}
		out = append(out, service.BlueprintScene{Slug: s.Slug, DisplayName: s.DisplayName, Source: s.Source})
	}
	return out
}

type ownerBody struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

// ownerFromBody resolves an optional owner body to an OwnerRef (default: caller
// personal) and enforces write access.
func (h *ProjectBlueprintHandler) ownerFromBody(c *gin.Context, userID int64, ob *ownerBody) (service.OwnerRef, bool) {
	owner := service.OwnerRef{Type: "user", ID: userID}
	if ob != nil && ob.Type != "" && !(ob.Type == "user" && ob.ID == 0) {
		owner = service.OwnerRef{Type: ob.Type, ID: ob.ID}
	}
	if h.authz != nil && userID > 0 {
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return owner, false
		}
	}
	return owner, true
}

// Create handles POST /project-blueprints — a brand-new user template.
func (h *ProjectBlueprintHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req createBlueprintRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	owner, ok := h.ownerFromBody(c, userID, req.Owner)
	if !ok {
		return
	}
	seeds, err := seedsFromColumnInputs(req.Columns)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	bp, err := h.svc.Create(c.Request.Context(), owner, req.Name, req.Description, seeds, scenesFromDTOs(req.Scenes))
	if err != nil {
		h.writeServiceErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toBlueprintDetail(bp))
}

type updateBlueprintRequest struct {
	Name        string               `json:"name" binding:"required"`
	Description string               `json:"description"`
	Columns     []BlueprintColumnDTO `json:"columns"`
	// Scenes is a pointer so an absent field keeps existing scenes, while an
	// explicit (possibly empty) array replaces them. The editor always sends it.
	Scenes *[]BlueprintSceneDTO `json:"scenes"`
}

// Update handles PUT /project-blueprints/:id — edit name/description/columns.
func (h *ProjectBlueprintHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid blueprint ID")
		return
	}
	bp, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			NotFound(c, "blueprint")
			return
		}
		InternalError(c, err)
		return
	}
	if h.authz != nil && userID > 0 {
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), userID, bp.Owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req updateBlueprintRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	seeds, err := seedsFromColumnInputs(req.Columns)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	var sceneList []service.BlueprintScene
	replaceScenes := req.Scenes != nil
	if req.Scenes != nil {
		sceneList = scenesFromDTOs(*req.Scenes)
	}
	updated, err := h.svc.Update(c.Request.Context(), id, req.Name, req.Description, seeds, sceneList, replaceScenes)
	if err != nil {
		h.writeServiceErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toBlueprintDetail(updated))
}

type duplicateBlueprintRequest struct {
	Name  string     `json:"name"`
	Owner *ownerBody `json:"owner,omitempty"`
}

// Duplicate handles POST /project-blueprints/:id/duplicate — copy into a new
// user template (works for builtins too: that's how you customise them).
func (h *ProjectBlueprintHandler) Duplicate(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid blueprint ID")
		return
	}
	src, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			NotFound(c, "blueprint")
			return
		}
		InternalError(c, err)
		return
	}
	if !h.readAccess(c, userID, src) {
		return
	}
	var req duplicateBlueprintRequest
	_ = c.ShouldBindJSON(&req)
	owner, ok := h.ownerFromBody(c, userID, req.Owner)
	if !ok {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = src.Name + " (副本)"
	}
	bp, err := h.svc.Duplicate(c.Request.Context(), id, owner, name)
	if err != nil {
		h.writeServiceErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toBlueprintDetail(bp))
}

// writeServiceErr maps blueprint service errors to HTTP responses.
func (h *ProjectBlueprintHandler) writeServiceErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		NotFound(c, "blueprint")
	case errors.Is(err, service.ErrBlueprintNameExists):
		BadRequest(c, err.Error())
	case errors.Is(err, service.ErrForbidden):
		writeAuthzError(c, err)
	default:
		// Validation errors (empty name / no columns / bad primitive) surface as
		// 400; everything else as 500.
		if err != nil && (strings.Contains(err.Error(), "name") || strings.Contains(err.Error(), "column") || strings.Contains(err.Error(), "primitive")) {
			BadRequest(c, err.Error())
			return
		}
		InternalError(c, err)
	}
}
