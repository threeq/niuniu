// Package api: SceneHandler — REST endpoints for the scenes resource.
//
// Routes (wired in server/internal/server/router.go):
//
//	GET    /api/scenes              List accessible scenes
//	POST   /api/scenes              Create user-owned scene
//	GET    /api/scenes/:id          Get scene
//	PUT    /api/scenes/:id          Update user-owned scene
//	DELETE /api/scenes/:id          Delete user-owned scene
//	POST   /api/scenes/:id/fork     Fork builtin or other scene into user scope
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type SceneHandler struct {
	svc   *service.SceneService
	authz *service.Authz
}

func NewSceneHandler(svc *service.SceneService, authz *service.Authz) *SceneHandler {
	return &SceneHandler{svc: svc, authz: authz}
}

// SceneResponse is the wire shape for a scene. The `definition` field is
// returned as a decoded object rather than the raw JSON string so the SPA
// doesn't double-parse.
type SceneResponse struct {
	ID          int64                   `json:"id"`
	Owner       OwnerDTO                `json:"owner"`
	Slug        string                  `json:"slug"`
	DisplayName string                  `json:"display_name"`
	Description string                  `json:"description"`
	Tags        []string                `json:"tags"`
	Source      string                  `json:"source"`
	SourceSlug  string                  `json:"source_slug"`
	Definition  service.SceneDefinition `json:"definition"`
	ContentHash string                  `json:"content_hash"`
	Enabled     bool                    `json:"enabled"`
	CreatedAt   string                  `json:"created_at"`
	UpdatedAt   string                  `json:"updated_at"`
}

func toSceneResponse(s store.Scene) SceneResponse {
	var tags []string
	if s.Tags != "" {
		_ = json.Unmarshal([]byte(s.Tags), &tags)
	}
	if tags == nil {
		tags = []string{}
	}
	def, _ := service.DecodeDefinition(s.Definition)
	if def == nil {
		def = &service.SceneDefinition{}
	}
	return SceneResponse{
		ID:          s.ID,
		Owner:       ownerDTOFromRef(s.OwnerType, s.OwnerID),
		Slug:        s.Slug,
		DisplayName: s.DisplayName,
		Description: s.Description,
		Tags:        tags,
		Source:      s.Source,
		SourceSlug:  s.SourceSlug,
		Definition:  *def,
		ContentHash: s.ContentHash,
		Enabled:     s.Enabled != 0,
		CreatedAt:   s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   s.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// List returns the scenes the caller can access: builtin scenes + user-owned
// + org-owned for orgs the caller is a member of. Driven by sqlc
// ListScenesAccessible.
//
// Optional query filters (code-review #11, spec §9):
//
//	?source=builtin|user|registry|all   — defaults to "all"
//	?tag=<exact-tag>                    — single-tag substring on the JSON-encoded tags array
func (h *SceneHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	// Default owner-axis = caller's personal space; ListScenesAccessible's
	// `owner_type/owner_id` parameters only widen the result set, so this is
	// a reasonable default.
	owner := service.OwnerRef{Type: "user", ID: userID}
	scenes, err := h.svc.ListAccessible(c.Request.Context(), owner, userID)
	if err != nil {
		InternalError(c, err)
		return
	}
	source := strings.ToLower(strings.TrimSpace(c.Query("source")))
	tag := strings.TrimSpace(c.Query("tag"))
	out := make([]SceneResponse, 0, len(scenes))
	for _, s := range scenes {
		if source != "" && source != "all" && !sourceMatches(s.Source, source) {
			continue
		}
		if tag != "" && !sceneHasTag(s.Tags, tag) {
			continue
		}
		out = append(out, toSceneResponse(s))
	}
	c.JSON(http.StatusOK, out)
}

// sourceMatches treats the special "registry" filter as "anything starting
// with registry:" so individual registries land under the same bucket.
func sourceMatches(rowSource, filter string) bool {
	switch filter {
	case "builtin", "user":
		return rowSource == filter
	case "registry":
		return strings.HasPrefix(rowSource, "registry")
	}
	return rowSource == filter
}

// sceneHasTag does an exact-element match against the JSON-encoded tags
// column. Cheap: the field is small (<256 bytes typical), so we decode rather
// than substring-grep (which would false-positive on partial words).
func sceneHasTag(tagsJSON string, want string) bool {
	if tagsJSON == "" {
		return false
	}
	var tags []string
	if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
		return false
	}
	for _, t := range tags {
		if strings.EqualFold(t, want) {
			return true
		}
	}
	return false
}

func (h *SceneHandler) Get(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid scene ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessScene(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	s, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		NotFound(c, "SCENE")
		return
	}
	c.JSON(http.StatusOK, toSceneResponse(s))
}

type createSceneRequest struct {
	Owner       *OwnerDTO               `json:"owner,omitempty"`
	Slug        string                  `json:"slug" binding:"required"`
	DisplayName string                  `json:"display_name" binding:"required"`
	Description string                  `json:"description"`
	Tags        []string                `json:"tags"`
	Definition  service.SceneDefinition `json:"definition"`
}

func (h *SceneHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req createSceneRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	owner := resolveOwnerForCreate(req.Owner, userID)
	if h.authz != nil && userID > 0 {
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	s, err := h.svc.Create(c.Request.Context(), owner, req.Slug, req.DisplayName, req.Description, req.Tags, &req.Definition)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusCreated, toSceneResponse(s))
}

type updateSceneRequest struct {
	DisplayName string                  `json:"display_name" binding:"required"`
	Description string                  `json:"description"`
	Tags        []string                `json:"tags"`
	Definition  service.SceneDefinition `json:"definition"`
	Owner       *OwnerDTO               `json:"owner,omitempty"`
}

func (h *SceneHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid scene ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessScene(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req updateSceneRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	var owner *service.OwnerRef
	if req.Owner != nil {
		next := service.OwnerRef{Type: req.Owner.Type, ID: req.Owner.ID}
		if h.authz != nil && userID > 0 {
			if err := h.authz.EnsureOwnerWritable(c.Request.Context(), userID, next); err != nil {
				writeAuthzError(c, err)
				return
			}
		}
		owner = &next
	}
	if err := h.svc.UpdateWithOwner(c.Request.Context(), id, owner, req.DisplayName, req.Description, req.Tags, &req.Definition); err != nil {
		BadRequest(c, err.Error())
		return
	}
	s, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toSceneResponse(s))
}

func (h *SceneHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid scene ID")
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessScene(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.Status(http.StatusNoContent)
}

type forkSceneRequest struct {
	NewSlug string    `json:"new_slug" binding:"required"`
	Owner   *OwnerDTO `json:"owner,omitempty"`
}

func (h *SceneHandler) Fork(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid scene ID")
		return
	}
	var req forkSceneRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessScene(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	owner := resolveOwnerForCreate(req.Owner, userID)
	if h.authz != nil && userID > 0 {
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	s, err := h.svc.Fork(c.Request.Context(), owner, id, req.NewSlug)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusCreated, toSceneResponse(s))
}

// resolveOwnerForCreate returns the OwnerRef the request asked for, or
// defaults to the caller's personal space when omitted.
func resolveOwnerForCreate(dto *OwnerDTO, userID int64) service.OwnerRef {
	if dto != nil && dto.Type != "" && dto.ID != 0 {
		return service.OwnerRef{Type: dto.Type, ID: dto.ID}
	}
	return service.OwnerRef{Type: "user", ID: userID}
}
