package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// AdminUserHandler serves the admin-only "view / delete a user's personal
// resources + one-click purge" endpoints under /api/auth/users/:id. All routes
// are gated by auth.RequireRole("admin") in the router.
type AdminUserHandler struct {
	svc *service.AdminUserService
}

func NewAdminUserHandler(svc *service.AdminUserService) *AdminUserHandler {
	return &AdminUserHandler{svc: svc}
}

// --- DTOs (shapes per design doc §API) ---

type adminUserDTO struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

type adminUserOrgDTO struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	IsLastOwner bool   `json:"is_last_owner"`
}

type adminUserProjectDTO struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type adminUserWorkspaceDTO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	ProjectID *int64 `json:"project_id"`
}

type adminUserRepositoryDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type adminUserResourcesResponse struct {
	User         adminUserDTO             `json:"user"`
	Orgs         []adminUserOrgDTO        `json:"orgs"`
	Projects     []adminUserProjectDTO    `json:"projects"`
	Workspaces   []adminUserWorkspaceDTO  `json:"workspaces"`
	Repositories []adminUserRepositoryDTO `json:"repositories"`
	Counts       service.ResourceCounts   `json:"counts"`
}

type adminUserPurgeResponse struct {
	Deleted service.PurgeSummary `json:"deleted"`
}

// GetResources handles GET /api/auth/users/:id/resources.
func (h *AdminUserHandler) GetResources(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid user ID")
		return
	}
	summary, err := h.svc.ListUserResources(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			NotFound(c, "user")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAdminUserResourcesResponse(summary))
}

// DeleteResource handles DELETE /api/auth/users/:id/resources/:type/:resourceId.
func (h *AdminUserHandler) DeleteResource(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid user ID")
		return
	}
	resourceID, err := strconv.ParseInt(c.Param("resourceId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid resource ID")
		return
	}
	actorID, _ := callerID(c)
	err = h.svc.DeleteUserResource(c.Request.Context(), actorID, userID, c.Param("type"), resourceID)
	switch {
	case err == nil:
		c.Status(http.StatusNoContent)
	case errors.Is(err, service.ErrInvalidResourceType):
		BadRequest(c, err.Error())
	case errors.Is(err, service.ErrNotFound):
		NotFound(c, "resource")
	case errors.Is(err, service.ErrForbidden):
		// Resource exists but is not this user's personal resource.
		RespondError(c, http.StatusForbidden, "FORBIDDEN", "resource does not belong to this user")
	default:
		InternalError(c, err)
	}
}

// Purge handles POST /api/auth/users/:id/purge.
func (h *AdminUserHandler) Purge(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid user ID")
		return
	}
	actorID, _ := callerID(c)
	summary, err := h.svc.PurgeUser(c.Request.Context(), actorID, userID)
	if err != nil {
		var guard *service.PurgeGuardError
		if errors.As(err, &guard) {
			RespondErrorWithDetails(c, http.StatusConflict, "PURGE_BLOCKED",
				"purge blocked by a safety guard", gin.H{"reason": guard.Reason})
			return
		}
		if errors.Is(err, service.ErrNotFound) {
			NotFound(c, "user")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminUserPurgeResponse{Deleted: summary})
}

func toAdminUserResourcesResponse(s *service.UserResourceSummary) adminUserResourcesResponse {
	resp := adminUserResourcesResponse{
		User: adminUserDTO{
			ID:          s.User.ID,
			Username:    s.User.Username,
			DisplayName: s.User.DisplayName,
			Role:        s.User.Role,
			CreatedAt:   s.User.CreatedAt,
		},
		Orgs:         make([]adminUserOrgDTO, 0, len(s.Orgs)),
		Projects:     make([]adminUserProjectDTO, 0, len(s.Projects)),
		Workspaces:   make([]adminUserWorkspaceDTO, 0, len(s.Workspaces)),
		Repositories: make([]adminUserRepositoryDTO, 0, len(s.Repositories)),
		Counts:       s.Counts,
	}
	for _, o := range s.Orgs {
		resp.Orgs = append(resp.Orgs, adminUserOrgDTO{
			ID: o.ID, Slug: o.Slug, Name: o.Name, Role: o.Role, IsLastOwner: o.IsLastOwner,
		})
	}
	for _, p := range s.Projects {
		resp.Projects = append(resp.Projects, adminUserProjectDTO{
			ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt,
		})
	}
	for _, w := range s.Workspaces {
		dto := adminUserWorkspaceDTO{ID: w.ID, Name: w.Name, Status: w.Status}
		if pid, ok := s.WorkspaceProjectIDs[w.ID]; ok && pid > 0 {
			dto.ProjectID = &pid
		}
		resp.Workspaces = append(resp.Workspaces, dto)
	}
	for _, r := range s.Repositories {
		resp.Repositories = append(resp.Repositories, adminUserRepositoryDTO{
			ID: r.ID, Name: r.Name, Path: r.Path,
		})
	}
	return resp
}
