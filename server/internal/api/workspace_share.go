package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/service"
)

// WorkspaceShareHandler 管理已登录侧的分享 CRUD（创建/列表/撤销）。
// 公开访问端点在 share_public.go。feature 门（LicenseFeatureGate）与
// authz 在 router 层挂。
type WorkspaceShareHandler struct {
	svc   *service.WorkspaceShareService
	Authz *service.Authz
}

func NewWorkspaceShareHandler(svc *service.WorkspaceShareService, authz *service.Authz) *WorkspaceShareHandler {
	return &WorkspaceShareHandler{svc: svc, Authz: authz}
}

// shareResponse 是列表/创建响应行。只暴露必要字段：token 永不出现在响应里
// （创建时单独返回一次 raw token），密码只有哈希。
type shareResponse struct {
	ID         int64      `json:"id"`
	WorkspaceID int64     `json:"workspaceId"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	AllowChat  bool       `json:"allowChat"`
	CreatedBy  int64      `json:"createdBy"`
	LastAccessAt *time.Time `json:"lastAccessAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type createShareReq struct {
	// Password 明文访问密码（响应不回显；前端自己持有供展示/复制）。
	Password string `json:"password" binding:"required"`
	// ExpiresAt RFC3339。必填：分享必须有时效。
	ExpiresAt time.Time `json:"expiresAt" binding:"required"`
	// AllowChat 可选：解锁的观看者可向工作空间 agent 发消息。默认 false。
	AllowChat bool `json:"allowChat"`
}

func toShareResponse(s service.ShareInfo) shareResponse {
	resp := shareResponse{
		ID:          s.Share.ID,
		WorkspaceID: s.Share.WorkspaceID,
		ExpiresAt:   s.Share.ExpiresAt,
		AllowChat:   s.Share.AllowChat == 1,
		CreatedBy:   s.Share.CreatedBy,
		CreatedAt:   s.Share.CreatedAt,
	}
	if s.Share.RevokedAt.Valid {
		t := s.Share.RevokedAt.Time
		resp.RevokedAt = &t
	}
	if s.Share.LastAccessAt.Valid {
		t := s.Share.LastAccessAt.Time
		resp.LastAccessAt = &t
	}
	return resp
}

// Create — POST /workspaces/:id/shares
//
// 创建分享，返回行 + 一次性的 raw token/path/url。raw token 不入库（库里只
// 有 sha256），服务端也无法再次取回——丢了只能重建分享。
func (h *WorkspaceShareHandler) Create(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	userID := c.GetInt64("auth_user_id")
	if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
		writeAuthzError(c, aerr)
		return
	}

	var req createShareReq
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "password and expiresAt are required")
		return
	}

	info, err := h.svc.Create(c.Request.Context(), workspaceID, userID, req.Password, req.ExpiresAt, req.AllowChat)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"share": toShareResponse(info),
		"token": info.RawToken,
		"path":  info.Path,
		"url":   info.URL,
	})
}

// List — GET /workspaces/:id/shares
func (h *WorkspaceShareHandler) List(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	userID := c.GetInt64("auth_user_id")
	if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
		writeAuthzError(c, aerr)
		return
	}

	shares, err := h.svc.List(c.Request.Context(), workspaceID)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]shareResponse, 0, len(shares))
	for _, s := range shares {
		out = append(out, toShareResponse(service.ShareInfo{Share: s}))
	}
	c.JSON(http.StatusOK, gin.H{"shares": out})
}

// Revoke — DELETE /workspaces/:id/shares/:shareId（幂等）
func (h *WorkspaceShareHandler) Revoke(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	shareID, err := strconv.ParseInt(c.Param("shareId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid share ID")
		return
	}
	userID := c.GetInt64("auth_user_id")
	if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
		writeAuthzError(c, aerr)
		return
	}

	if err := h.svc.Revoke(c.Request.Context(), workspaceID, shareID); err != nil {
		if errors.Is(err, service.ErrShareUnavailable) {
			NotFound(c, "SHARE")
			return
		}
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type patchShareReq struct {
	// AllowChat 切换观看者是否可以向工作空间 agent 发消息。
	AllowChat *bool `json:"allowChat" binding:"required"`
}

// Patch — PATCH /workspaces/:id/shares/:shareId
//
// 目前只有 allowChat 一个可变字段。幂等：重复设置同值无副作用。
func (h *WorkspaceShareHandler) Patch(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	shareID, err := strconv.ParseInt(c.Param("shareId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid share ID")
		return
	}
	userID := c.GetInt64("auth_user_id")
	if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
		writeAuthzError(c, aerr)
		return
	}

	var req patchShareReq
	if err := c.ShouldBindJSON(&req); err != nil || req.AllowChat == nil {
		BadRequest(c, "allowChat is required")
		return
	}

	if err := h.svc.SetAllowChat(c.Request.Context(), workspaceID, shareID, *req.AllowChat); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
