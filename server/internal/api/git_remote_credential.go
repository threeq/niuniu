package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// GitRemoteCredentialHandler serves /api/me/git-remote-credentials.
// Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
//   v5 Phase 2 sub-phase A (storage / management).
type GitRemoteCredentialHandler struct {
	svc *service.GitRemoteCredentialService
}

func NewGitRemoteCredentialHandler(svc *service.GitRemoteCredentialService) *GitRemoteCredentialHandler {
	return &GitRemoteCredentialHandler{svc: svc}
}

type gitRemoteCredentialDTO struct {
	ID          int64   `json:"id"`
	Host        string  `json:"host"`
	Username    string  `json:"username"`
	PublicKey   string  `json:"public_key"`
	Fingerprint string  `json:"fingerprint"`
	LastUsedAt  *string `json:"last_used_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func dtoOf(c service.GitRemoteCredential) gitRemoteCredentialDTO {
	dto := gitRemoteCredentialDTO{
		ID:          c.ID,
		Host:        c.Host,
		Username:    c.Username,
		PublicKey:   c.PublicKey,
		Fingerprint: c.Fingerprint,
		CreatedAt:   c.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   c.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if t := c.LastUsedAtTime(); t != nil {
		s := t.UTC().Format("2006-01-02T15:04:05Z")
		dto.LastUsedAt = &s
	}
	return dto
}

// List returns the caller's credentials (redacted — no private key).
func (h *GitRemoteCredentialHandler) List(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"},
		})
		return
	}
	creds, err := h.svc.List(c.Request.Context(), uid)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]gitRemoteCredentialDTO, 0, len(creds))
	for _, cred := range creds {
		out = append(out, dtoOf(cred))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

type gitRemoteCredentialCreateRequest struct {
	Host       string `json:"host" binding:"required"`
	Username   string `json:"username"`
	PrivateKey string `json:"private_key" binding:"required"`
}

// Create accepts a private key, derives + stores. Maps service errors
// to stable HTTP error codes.
func (h *GitRemoteCredentialHandler) Create(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"},
		})
		return
	}
	var req gitRemoteCredentialCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()},
		})
		return
	}
	cred, err := h.svc.Create(c.Request.Context(), service.CreateGitRemoteCredentialInput{
		UserID:     uid,
		Host:       req.Host,
		Username:   req.Username,
		PrivateKey: req.PrivateKey,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidPrivateKey):
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"code": "ERR_INVALID_PRIVATE_KEY",
					"message": "the supplied PEM could not be parsed as an SSH private key"},
			})
		case errors.Is(err, service.ErrPassphraseRequired):
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"code": "ERR_PASSPHRASE_REQUIRED",
					"message": "passphrase-protected keys are not supported; decrypt the key locally first"},
			})
		case errors.Is(err, service.ErrInvalidHost):
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"code": "ERR_INVALID_HOST",
					"message": "host must be a single token without whitespace or shell metacharacters"},
			})
		case errors.Is(err, service.ErrHostConflict):
			c.JSON(http.StatusConflict, gin.H{
				"error": gin.H{"code": "ERR_HOST_CONFLICT",
					"message": "a credential for this host already exists; delete the existing one first"},
			})
		default:
			InternalError(c, err)
		}
		return
	}
	c.JSON(http.StatusCreated, dtoOf(*cred))
}

// Delete removes by id; 404 if not owned by caller.
func (h *GitRemoteCredentialHandler) Delete(c *gin.Context) {
	uid, ok := callerID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"},
		})
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "BAD_REQUEST", "message": "invalid id"},
		})
		return
	}
	if err := h.svc.Delete(c.Request.Context(), uid, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{"code": "NOT_FOUND", "message": "credential not found"},
			})
			return
		}
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
