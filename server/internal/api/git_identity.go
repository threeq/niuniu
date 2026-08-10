package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// GitIdentityHandler serves /api/me/git-identity. Phase 0 of the per-user
// git identity rollout: lets the authenticated user read & update
// users.email (display_name is already editable via the existing /api/auth
// surface). Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
type GitIdentityHandler struct {
	svc *service.GitIdentityService
}

func NewGitIdentityHandler(svc *service.GitIdentityService) *GitIdentityHandler {
	return &GitIdentityHandler{svc: svc}
}

type gitIdentityResponse struct {
	Email   string `json:"email"`
	Author  string `json:"author"`
	Address string `json:"address"`
}

// Get returns the user's git author identity. `email` is the raw stored
// value (empty string when unset). `author`/`address` are the resolved
// values niuniu will actually inject — the synthetic
// `<username>@niuniu.local` fallback surfaces here so the UI can show "this
// is what your commits will look like".
func (h *GitIdentityHandler) Get(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"},
		})
		return
	}
	email, err := h.svc.GetEmail(c.Request.Context(), uid)
	if err != nil {
		InternalError(c, err)
		return
	}
	id, err := h.svc.Resolve(c.Request.Context(), uid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gitIdentityResponse{
		Email:   email,
		Author:  id.Name,
		Address: id.Email,
	})
}

type gitIdentityUpdateRequest struct {
	Email string `json:"email"`
}

// Put updates users.email for the authenticated user. Empty string clears
// the field (Resolve will fall back to the synthetic local domain).
// Returns 400 ERR_INVALID_EMAIL when the input fails validation.
func (h *GitIdentityHandler) Put(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"},
		})
		return
	}
	var req gitIdentityUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()},
		})
		return
	}
	if err := h.svc.UpdateEmail(c.Request.Context(), uid, req.Email); err != nil {
		if errors.Is(err, service.ErrInvalidEmail) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"code": "ERR_INVALID_EMAIL", "message": "email must be a valid address or empty"},
			})
			return
		}
		InternalError(c, err)
		return
	}
	// Echo the post-update state so the UI doesn't need a second round-trip.
	h.Get(c)
}

// --- per-(user, repository) overrides (v5.1) ---

type repositoryIdentityDTO struct {
	RepositoryID    int64  `json:"repository_id"`
	Name            string `json:"name"`             // raw stored value (may be empty)
	Email           string `json:"email"`            // raw stored value (may be empty)
	ResolvedName    string `json:"resolved_name"`    // after global fallback
	ResolvedEmail   string `json:"resolved_email"`   // after global fallback
	HasOverride     bool   `json:"has_override"`     // true when a row exists
}

// ListRepositoryIdentities returns all of the caller's per-repository
// overrides. The settings UI uses this to render a table.
func (h *GitIdentityHandler) ListRepositoryIdentities(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"},
		})
		return
	}
	overrides, err := h.svc.ListOverrides(c.Request.Context(), uid)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]repositoryIdentityDTO, 0, len(overrides))
	for _, o := range overrides {
		// Resolve for each (so the UI can show "what will actually be used").
		// Worst-case N queries per request; acceptable: most users will
		// have <10 overrides, and Phase 3 can batch if needed.
		resolved, _ := h.svc.ResolveForRepository(c.Request.Context(), uid, o.RepositoryID)
		out = append(out, repositoryIdentityDTO{
			RepositoryID:  o.RepositoryID,
			Name:          o.Name,
			Email:         o.Email,
			ResolvedName:  resolved.Name,
			ResolvedEmail: resolved.Email,
			HasOverride:   true,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// GetRepositoryIdentity returns the effective identity for a (caller, repo).
// Always returns 200 — when no override exists, returns the global value
// with has_override=false (lets the UI render "no override; using global X").
func (h *GitIdentityHandler) GetRepositoryIdentity(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"},
		})
		return
	}
	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}

	o, err := h.svc.GetOverride(c.Request.Context(), uid, repoID)
	if err != nil {
		InternalError(c, err)
		return
	}
	resolved, err := h.svc.ResolveForRepository(c.Request.Context(), uid, repoID)
	if err != nil {
		InternalError(c, err)
		return
	}
	dto := repositoryIdentityDTO{
		RepositoryID:  repoID,
		ResolvedName:  resolved.Name,
		ResolvedEmail: resolved.Email,
	}
	if o != nil {
		dto.Name = o.Name
		dto.Email = o.Email
		dto.HasOverride = true
	}
	c.JSON(http.StatusOK, dto)
}

type putRepositoryIdentityRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// PutRepositoryIdentity upserts an override for (caller, repo_id).
// Empty name + empty email both saved as override-but-fall-through; the
// stricter "no override at all" state is DELETE.
func (h *GitIdentityHandler) PutRepositoryIdentity(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"},
		})
		return
	}
	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}
	var req putRepositoryIdentityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()},
		})
		return
	}
	if _, err := h.svc.UpsertOverride(c.Request.Context(), uid, repoID, req.Name, req.Email); err != nil {
		if errors.Is(err, service.ErrInvalidEmail) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"code": "ERR_INVALID_EMAIL", "message": "email must be a valid address or empty"},
			})
			return
		}
		if errors.Is(err, service.ErrInvalidName) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"code": "ERR_INVALID_NAME", "message": "name must not contain control characters"},
			})
			return
		}
		InternalError(c, err)
		return
	}
	// Echo via Get so client sees the resolved values too.
	h.GetRepositoryIdentity(c)
}

// DeleteRepositoryIdentity clears the override (caller's commits on the
// repo go back to the global default). Idempotent: 204 whether or not a
// row existed.
func (h *GitIdentityHandler) DeleteRepositoryIdentity(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"},
		})
		return
	}
	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteOverride(c.Request.Context(), uid, repoID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Idempotent — nothing to delete.
			c.Status(http.StatusNoContent)
			return
		}
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// parseRepoIDParam extracts :repo_id from the URL. Writes a 400 + false
// when invalid; true + repoID when good.
func parseRepoIDParam(c *gin.Context) (int64, bool) {
	idStr := c.Param("repo_id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "BAD_REQUEST", "message": "invalid repo_id"},
		})
		return 0, false
	}
	return id, true
}
