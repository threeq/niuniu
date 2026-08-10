package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type ProjectHandler struct {
	svc          *service.ProjectService
	kanbanSvc    *service.KanbanService
	blueprintSvc *service.ProjectBlueprintService
	Authz        *service.Authz
	DB           *sql.DB // used for batch owner-name lookup on list endpoints
}

func NewProjectHandler(svc *service.ProjectService, kanbanSvc *service.KanbanService, blueprintSvc *service.ProjectBlueprintService, authz *service.Authz) *ProjectHandler {
	return &ProjectHandler{svc: svc, kanbanSvc: kanbanSvc, blueprintSvc: blueprintSvc, Authz: authz}
}

type CreateProjectRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

type UpdateProjectRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// validProjectStatuses defines allowed values for the status query parameter.
var validProjectStatuses = map[string]bool{"active": true, "hidden": true}

// List returns all projects
// @Summary      List projects
// @Description  Get all projects
// @Tags         Projects
// @Accept       json
// @Produce      json
// @Param        status  query    string  false  "Comma-separated statuses (active,hidden)"  default(active)
// @Success      200  {array}   ProjectResponse
// @Failure      400  {object}  Error
// @Failure      500  {object}  Error
// @Router       /projects [get]
func (h *ProjectHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	// Parse status filter: ?status=active,hidden (default: active)
	statusParam := c.DefaultQuery("status", "active")
	statuses := strings.Split(statusParam, ",")

	// Validate all status values
	for _, s := range statuses {
		if !validProjectStatuses[s] {
			BadRequest(c, "invalid status value: "+s+"; allowed: active, hidden")
			return
		}
	}

	// Optional owner filter: ?owner=org:<slug> | user:<id>. Used by the org
	// detail "资源" tab to count rows belonging to a specific org. Without
	// this gate the response was the caller's full accessible set, so every
	// org card showed the same number.
	ownerF, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}

	// When auth is active (userID > 0), scope to accessible projects.
	// When auth is disabled (userID == 0), fall back to unscoped list.
	if userID > 0 {
		projects, err := h.svc.ListForUser(c.Request.Context(), userID)
		if err != nil {
			InternalError(c, err)
			return
		}
		// Post-filter by status (ListForUser doesn't support status filtering).
		// Build a set for O(1) lookup.
		statusSet := make(map[string]bool, len(statuses))
		for _, s := range statuses {
			statusSet[s] = true
		}
		filtered := make([]store.Project, 0, len(projects))
		for _, p := range projects {
			if !statusSet[p.Status] {
				continue
			}
			if !ownerF.Match(p.OwnerType, p.OwnerID) {
				continue
			}
			filtered = append(filtered, p)
		}
		// Enrich with issue / ws stats. Without this, mobile + web project
		// cards on auth-enabled deployments saw empty issue_stats / ws_stats
		// (zeros across the board) — the previous code constructed
		// ProjectWithStats with only the Project field populated.
		result, err := h.svc.AttachStats(c.Request.Context(), filtered)
		if err != nil {
			InternalError(c, err)
			return
		}
		if h.DB != nil {
			refs := make([]ownerRef, len(result))
			for i, pw := range result {
				refs[i] = ownerRef{pw.Project.OwnerType, pw.Project.OwnerID}
			}
			lk, _ := newOwnerLookup(c.Request.Context(), h.DB, refs)
			c.JSON(http.StatusOK, toProjectResponsesWithStatsAndLookup(result, lk))
			return
		}
		c.JSON(http.StatusOK, toProjectResponsesWithStats(result))
		return
	}

	projects, err := h.svc.ListWithStats(c.Request.Context(), statuses)
	if err != nil {
		InternalError(c, err)
		return
	}
	if ownerF.Type != "" {
		filtered := projects[:0]
		for _, pw := range projects {
			if ownerF.Match(pw.Project.OwnerType, pw.Project.OwnerID) {
				filtered = append(filtered, pw)
			}
		}
		projects = filtered
	}
	if h.DB != nil {
		refs := make([]ownerRef, len(projects))
		for i, pw := range projects {
			refs[i] = ownerRef{pw.Project.OwnerType, pw.Project.OwnerID}
		}
		lk, _ := newOwnerLookup(c.Request.Context(), h.DB, refs)
		c.JSON(http.StatusOK, toProjectResponsesWithStatsAndLookup(projects, lk))
		return
	}
	c.JSON(http.StatusOK, toProjectResponsesWithStats(projects))
}

type UpdateProjectStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

func (h *ProjectHandler) UpdateStatus(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req UpdateProjectStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	project, err := h.svc.UpdateStatus(c.Request.Context(), id, req.Status)
	if err != nil {
		if strings.Contains(err.Error(), "invalid project status") {
			BadRequest(c, err.Error())
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectResponse(project))
}

// Get retrieves a project by ID
// @Summary      Get a project
// @Description  Get a project by ID
// @Tags         Projects
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Project ID"
// @Success      200  {object}  ProjectResponse
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /projects/{id} [get]
func (h *ProjectHandler) Get(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	project, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		slog.Warn("GetProject failed", "id", id, "error", err)
		NotFound(c, "PROJECT")
		return
	}
	resp := toProjectResponse(project)
	bindings, err := h.svc.ListRepositories(c.Request.Context(), id)
	if err != nil {
		slog.Warn("project ListRepositories failed", "id", id, "error", err)
	} else {
		resp.Repositories = toProjectRepositoryBindings(bindings)
	}
	c.JSON(http.StatusOK, resp)
}

// Create creates a new project with default columns
// @Summary      Create a project
// @Description  Create a new project with default columns using the kanban service
// @Tags         Projects
// @Accept       json
// @Produce      json
// @Param        request  body      CreateProjectRequest  true  "Project details"
// @Success      201     {object}  ProjectResponse
// @Failure      400     {object}  Error
// @Failure      500     {object}  Error
// @Router       /projects [post]
func (h *ProjectHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req struct {
		Name           string  `json:"name" binding:"required"`
		Description    string  `json:"description"`
		Color          *string `json:"color"` // optional; nil/"" → no color
		DefaultCliType string  `json:"default_cli_type"` // optional; "" → 'claude'
		BlueprintID    *int64  `json:"blueprint_id,omitempty"`
		Owner          *struct {
			Type string `json:"type"`
			ID   int64  `json:"id"`
		} `json:"owner,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	// Validate the optional default agent up front so a bad value is a clean 400
	// rather than an orphaned project (create succeeds, post-update would fail).
	if req.DefaultCliType != "" {
		if _, ok := service.ValidCliTypes[req.DefaultCliType]; !ok {
			BadRequest(c, "invalid default_cli_type")
			return
		}
	}

	// Resolve owner: explicit body wins, but treat both nil and the SPA
	// no-currentUser fallback {type:"user", id:0} as "default to caller".
	// Without the second clause, a personal-edition SPA (no token, no /me
	// bootstrap) would fail EnsureOwnerWritable because 0 != caller id.
	var owner service.OwnerRef
	if req.Owner != nil && req.Owner.Type != "" && !(req.Owner.Type == "user" && req.Owner.ID == 0) {
		owner = service.OwnerRef{Type: req.Owner.Type, ID: req.Owner.ID}
	} else {
		owner = service.OwnerRef{Type: "user", ID: userID}
	}
	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	// Create project. With a blueprint_id, seed the project's columns from the
	// blueprint and attach its scenes; otherwise use the default five columns.
	// Color isn't part of the kanban path; if the caller passed one, set it via
	// the dedicated UpdateColor call right after so the create-with-color UX
	// works end-to-end without bloating the kanban signature.
	// Resolve the blueprint to apply: an explicit blueprint_id wins; otherwise
	// fall back to the owner's default template (builtin default after first-boot
	// seeding). Only if no blueprint exists at all do we use the hardcoded
	// default columns — so project creation is template-driven end to end.
	var blueprintID int64
	if req.BlueprintID != nil && *req.BlueprintID > 0 {
		blueprintID = *req.BlueprintID
	} else if h.blueprintSvc != nil {
		if def, derr := h.blueprintSvc.ResolveDefaultID(c.Request.Context(), owner); derr == nil {
			blueprintID = def
		} else {
			slog.Warn("CreateProject: ResolveDefaultID failed", "error", derr)
		}
	}

	var project store.Project
	var err error
	if blueprintID > 0 && h.blueprintSvc != nil {
		bp, bpErr := h.blueprintSvc.Get(c.Request.Context(), blueprintID)
		if bpErr != nil {
			if errors.Is(bpErr, service.ErrNotFound) {
				BadRequest(c, "blueprint not found")
				return
			}
			InternalError(c, bpErr)
			return
		}
		// Builtin blueprints carry the global (owner_type='user', owner_id=0)
		// sentinel and are usable by everyone — the blueprint list query returns
		// them to all callers, so the new-project picker offers them to every
		// user. CanAccessOwner has no sentinel exemption (unlike CanAccessEnvPreset
		// / CanAccessQuickAction / CanAccessHarnessSpec), so gating a builtin here
		// rejected (0 != userID) → ErrForbidden → 403 whenever auth is enabled.
		// Mirror ProjectBlueprintService.SetDefault: builtins are always usable;
		// only user-owned blueprints need the per-owner access check.
		if userID > 0 && h.Authz != nil && bp.Source != "builtin" {
			if err := h.Authz.CanAccessOwner(c.Request.Context(), userID, bp.Owner); err != nil {
				writeAuthzError(c, err)
				return
			}
		}
		project, err = h.kanbanSvc.CreateProjectWithColumns(c.Request.Context(), req.Name, req.Description, owner.Type, owner.ID, bp.Columns)
		if err == nil {
			if scErr := h.blueprintSvc.AttachScenesToProject(c.Request.Context(), project.ID, owner, bp.Scenes); scErr != nil {
				slog.Warn("CreateProject: AttachScenesToProject failed", "id", project.ID, "blueprint", bp.ID, "error", scErr)
				// Non-fatal: the project + columns exist; scenes are recoverable
				// via the project default-scenes management UI.
			}
		}
	} else {
		project, err = h.kanbanSvc.CreateProjectWithDefaults(c.Request.Context(), req.Name, req.Description, owner.Type, owner.ID)
	}
	if err != nil {
		if errors.Is(err, service.ErrProjectNameExists) {
			// Stable code so the client can render a clear, localized message
			// that names the conflicting project (the message already embeds it).
			RespondError(c, http.StatusBadRequest, "PROJECT_NAME_TAKEN", err.Error())
			return
		}
		InternalError(c, err)
		return
	}
	if req.Color != nil && *req.Color != "" {
		updated, colorErr := h.svc.UpdateColor(c.Request.Context(), project.ID, req.Color)
		if colorErr != nil {
			slog.Warn("CreateProject: UpdateColor failed", "id", project.ID, "error", colorErr)
			// Don't fail the create — the project exists and the color edit
			// is recoverable through the dedicated PUT /:id/color endpoint.
		} else {
			project = updated
		}
	}
	// Apply the project's default agent CLI right after create (mirrors color).
	// Validated above, so this only fails on an infra error — non-fatal: the
	// project exists and defaults to 'claude'.
	if req.DefaultCliType != "" && req.DefaultCliType != "claude" {
		updated, cliErr := h.svc.UpdateDefaultCliType(c.Request.Context(), project.ID, req.DefaultCliType)
		if cliErr != nil {
			slog.Warn("CreateProject: UpdateDefaultCliType failed", "id", project.ID, "error", cliErr)
		} else {
			project = updated
		}
	}
	c.JSON(http.StatusCreated, toProjectResponse(project))
}

// UpdateColorRequest is the body for PUT /projects/:id/color.
// Color is a pointer so the JSON `null` literal clears the color, while a
// quoted string sets it. An empty string is treated as `null` (cleared).
type UpdateColorRequest struct {
	Color *string `json:"color"`
}

// UpdateColor sets or clears the project's color token.
// @Summary      Update project color
// @Description  Set or clear the project's color token (UI sidebar).
// @Tags         Projects
// @Accept       json
// @Produce      json
// @Param        id       path      string              true  "Project ID"
// @Param        request  body      UpdateColorRequest  true  "Color (null to clear)"
// @Success      200      {object}  ProjectResponse
// @Failure      400      {object}  Error
// @Failure      404      {object}  Error
// @Failure      500      {object}  Error
// @Router       /projects/{id}/color [put]
func (h *ProjectHandler) UpdateColor(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req UpdateColorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid body")
		return
	}
	project, err := h.svc.UpdateColor(c.Request.Context(), id, req.Color)
	if err != nil {
		slog.Warn("UpdateColor failed", "id", id, "error", err)
		NotFound(c, "PROJECT")
		return
	}
	c.JSON(http.StatusOK, toProjectResponse(project))
}

// UpdateDefaultCliTypeRequest is the body for PUT /projects/:id/default-cli-type.
type UpdateDefaultCliTypeRequest struct {
	DefaultCliType string `json:"default_cli_type" binding:"required"`
}

// UpdateDefaultCliType sets the project's default agent CLI (pre-selected at
// workspace creation; used verbatim for issue-auto-created workspaces).
// @Summary      Update project default agent
// @Tags         Projects
// @Accept       json
// @Produce      json
// @Param        id       path      string                       true  "Project ID"
// @Param        request  body      UpdateDefaultCliTypeRequest  true  "Default cli_type"
// @Success      200      {object}  ProjectResponse
// @Failure      400      {object}  Error
// @Router       /projects/{id}/default-cli-type [put]
func (h *ProjectHandler) UpdateDefaultCliType(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req UpdateDefaultCliTypeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid body")
		return
	}
	project, err := h.svc.UpdateDefaultCliType(c.Request.Context(), id, req.DefaultCliType)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCliType) {
			BadRequest(c, err.Error())
			return
		}
		slog.Warn("UpdateDefaultCliType failed", "id", id, "error", err)
		NotFound(c, "PROJECT")
		return
	}
	c.JSON(http.StatusOK, toProjectResponse(project))
}

// Update updates an existing project
// @Summary      Update a project
// @Description  Update project details
// @Tags         Projects
// @Accept       json
// @Produce      json
// @Param        id       path      string                true  "Project ID"
// @Param        request  body      UpdateProjectRequest  true  "Updated project details"
// @Success      200      {object}  ProjectResponse
// @Failure      400      {object}  Error
// @Failure      404      {object}  Error
// @Failure      500      {object}  Error
// @Router       /projects/{id} [put]
func (h *ProjectHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		BadRequest(c, "failed to read body")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req UpdateProjectRequest
	if err := dec.Decode(&req); err != nil {
		BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if req.Name == "" {
		BadRequest(c, "name is required")
		return
	}
	project, err := h.svc.Update(c.Request.Context(), id, req.Name, req.Description)
	if err != nil {
		slog.Warn("UpdateProject failed", "id", id, "error", err)
		NotFound(c, "PROJECT")
		return
	}
	c.JSON(http.StatusOK, toProjectResponse(project))
}

// Delete removes a project
// @Summary      Delete a project
// @Description  Delete a project and all associated data
// @Tags         Projects
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Project ID"
// @Success      204
// @Failure      500  {object}  Error
// @Router       /projects/{id} [delete]
func (h *ProjectHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, id); err != nil {
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

// ListRepositories returns the project's associated repositories.
func (h *ProjectHandler) ListRepositories(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	bindings, err := h.svc.ListRepositories(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toProjectRepositoryBindings(bindings))
}

type AddProjectRepositoryRequest struct {
	RepositoryID  int64  `json:"repository_id" binding:"required"`
	DefaultBranch string `json:"default_branch"`
}

// AddRepository associates a single repository with the project.
func (h *ProjectHandler) AddRepository(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req AddProjectRepositoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	if err := h.svc.AddRepository(c.Request.Context(), id, req.RepositoryID, req.DefaultBranch); err != nil {
		switch {
		case errors.Is(err, service.ErrOwnerMismatch):
			BadRequest(c, err.Error())
		case errors.Is(err, service.ErrAlreadyAssociated):
			Conflict(c, err.Error())
		default:
			InternalError(c, err)
		}
		return
	}

	bindings, err := h.svc.ListRepositories(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}
	for _, b := range bindings {
		if b.Repository.ID == req.RepositoryID {
			c.JSON(http.StatusCreated, toProjectRepositoryBindings([]service.ProjectRepoBinding{b})[0])
			return
		}
	}
	// Insert succeeded but the binding disappeared (extremely rare). Log + 500.
	slog.Warn("AddRepository: binding not found after insert", "project_id", id, "repository_id", req.RepositoryID)
	InternalError(c, errors.New("binding not found after insert"))
}

// RemoveRepository disassociates a repository. Idempotent.
func (h *ProjectHandler) RemoveRepository(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repoID"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if err := h.svc.RemoveRepository(c.Request.Context(), id, repoID); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type UpdateProjectRepositoryRequest struct {
	DefaultBranch string `json:"default_branch" binding:"required"`
}

// UpdateRepository changes the project_default_branch on an existing binding.
func (h *ProjectHandler) UpdateRepository(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	repoID, err := strconv.ParseInt(c.Param("repoID"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req UpdateProjectRepositoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	// Pre-check: binding must exist for PATCH.
	if _, err := h.svc.Q().GetProjectRepository(c.Request.Context(), store.GetProjectRepositoryParams{
		ProjectID: id, RepositoryID: repoID,
	}); err != nil {
		NotFound(c, "PROJECT_REPOSITORY")
		return
	}
	if err := h.svc.UpdateRepositoryBranch(c.Request.Context(), id, repoID, req.DefaultBranch); err != nil {
		InternalError(c, err)
		return
	}
	bindings, err := h.svc.ListRepositories(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}
	for _, b := range bindings {
		if b.Repository.ID == repoID {
			c.JSON(http.StatusOK, toProjectRepositoryBindings([]service.ProjectRepoBinding{b})[0])
			return
		}
	}
	slog.Warn("UpdateRepository: binding not found after update", "project_id", id, "repository_id", repoID)
	InternalError(c, errors.New("binding not found after update"))
}

// ListAssignableUsers returns the users who may be assigned to issues in the
// project. Resolves to the single owner for user-owned projects and to the
// full org-member list (with role) for org-owned projects. Used by the issue
// detail UI to populate the assignee dropdown.
func (h *ProjectHandler) ListAssignableUsers(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, projectID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	users, err := h.kanbanSvc.ListAssignableUsersForProject(c.Request.Context(), projectID)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": users})
}
