package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// OrgHandler handles /api/orgs and related endpoints.
type OrgHandler struct {
	svc *service.OrgService
}

func NewOrgHandler(svc *service.OrgService) *OrgHandler {
	return &OrgHandler{svc: svc}
}

// callerInfo extracts the caller's user ID and username from the gin context.
func callerInfo(c *gin.Context) (int64, string, bool) {
	v, exists := c.Get("auth_user_id")
	if !exists {
		return 0, "", false
	}
	uid, ok := v.(int64)
	if !ok {
		return 0, "", false
	}
	label := ""
	if u, ok := c.Get("auth_username"); ok {
		if s, ok := u.(string); ok {
			label = s
		}
	}
	return uid, label, true
}

func respondOrgErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN", "message": err.Error()}})
	case errors.Is(err, service.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
	case errors.Is(err, service.ErrUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "USER_NOT_FOUND", "message": "目标用户不存在"}})
	case errors.Is(err, service.ErrAlreadyMember):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "ALREADY_MEMBER", "message": "该用户已是本组织成员"}})
	case errors.Is(err, service.ErrSlugConflict):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "SLUG_CONFLICT", "message": "slug 已被使用"}})
	case errors.Is(err, service.ErrInvalidRole):
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "INVALID_ROLE", "message": err.Error()}})
	case errors.Is(err, service.ErrLastOwner):
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "LAST_OWNER", "message": err.Error()}})
	default:
		InternalError(c, err)
	}
}

// requireGlobalAdmin returns false and writes 403 if the caller is not a global admin.
func requireGlobalAdmin(c *gin.Context) bool {
	role, _ := c.Get("auth_role")
	if r, ok := role.(string); ok && r == "admin" {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN", "message": "global admin role required"}})
	return false
}

// POST /api/orgs — requires global admin
func (h *OrgHandler) CreateOrg(c *gin.Context) {
	if !requireGlobalAdmin(c) {
		return
	}
	callerID, callerLabel, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}

	var req struct {
		Name        string `json:"name" binding:"required"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	org, err := h.svc.Create(c.Request.Context(), callerID, callerLabel, req.Name, req.Slug, req.Description)
	if err != nil {
		respondOrgErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id":          org.ID,
		"slug":        org.Slug,
		"name":        org.Name,
		"description": org.Description,
	})
}

// GET /api/orgs — authenticated; returns orgs for the calling user
func (h *OrgHandler) ListOrgsForUser(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	rows, err := h.svc.ListForUser(c.Request.Context(), callerID)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		out = append(out, gin.H{
			"id":          r.ID,
			"slug":        r.Slug,
			"name":        r.Name,
			"description": r.Description,
			"role":        r.Role,
		})
	}
	c.JSON(http.StatusOK, out)
}

// GET /api/orgs/all — global admin only; returns every org with the caller's role
func (h *OrgHandler) ListAllOrgs(c *gin.Context) {
	if !requireGlobalAdmin(c) {
		return
	}
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	rows, err := h.svc.ListAll(c.Request.Context(), callerID)
	if err != nil {
		respondOrgErr(c, err)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		role := ""
		if r.Role.Valid {
			role = r.Role.String
		}
		out = append(out, gin.H{
			"id":          r.ID,
			"slug":        r.Slug,
			"name":        r.Name,
			"description": r.Description,
			"role":        role,
		})
	}
	c.JSON(http.StatusOK, out)
}

// GET /api/orgs/:id — org member
func (h *OrgHandler) GetOrg(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}
	org, err := h.svc.Get(c.Request.Context(), callerID, orgID)
	if err != nil {
		respondOrgErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":          org.ID,
		"slug":        org.Slug,
		"name":        org.Name,
		"description": org.Description,
	})
}

// PATCH /api/orgs/:id — org owner/admin
func (h *OrgHandler) UpdateOrg(c *gin.Context) {
	callerID, callerLabel, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}
	var req struct {
		Name        string `json:"name" binding:"required"`
		Slug        string `json:"slug" binding:"required"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	org, err := h.svc.Update(c.Request.Context(), callerID, callerLabel, orgID, req.Name, req.Slug, req.Description)
	if err != nil {
		respondOrgErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":          org.ID,
		"slug":        org.Slug,
		"name":        org.Name,
		"description": org.Description,
	})
}

// DELETE /api/orgs/:id — org owner
func (h *OrgHandler) DeleteOrg(c *gin.Context) {
	callerID, callerLabel, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}
	if err := h.svc.Delete(c.Request.Context(), callerID, callerLabel, orgID); err != nil {
		respondOrgErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// GET /api/orgs/:id/members — org member
func (h *OrgHandler) ListMembers(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}
	members, err := h.svc.ListMembers(c.Request.Context(), callerID, orgID)
	if err != nil {
		respondOrgErr(c, err)
		return
	}
	out := make([]gin.H, 0, len(members))
	for _, m := range members {
		out = append(out, gin.H{
			"user_id":      m.UserID,
			"username":     m.Username,
			"display_name": m.DisplayName,
			"role":         m.Role,
		})
	}
	c.JSON(http.StatusOK, out)
}

// POST /api/orgs/:id/members — org owner/admin
func (h *OrgHandler) AddMember(c *gin.Context) {
	callerID, callerLabel, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}
	var req struct {
		UserID int64  `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	if req.UserID <= 0 {
		BadRequest(c, "user_id is required")
		return
	}
	if req.Role == "" {
		BadRequest(c, "role is required")
		return
	}
	if err := h.svc.AddMember(c.Request.Context(), callerID, callerLabel, orgID, req.UserID, req.Role); err != nil {
		respondOrgErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "member added"})
}

// PATCH /api/orgs/:id/members/:user_id — org owner
func (h *OrgHandler) UpdateMemberRole(c *gin.Context) {
	callerID, callerLabel, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}
	targetUserID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid user_id")
		return
	}
	var req struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.svc.UpdateMemberRole(c.Request.Context(), callerID, callerLabel, orgID, targetUserID, req.Role); err != nil {
		respondOrgErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "role updated"})
}

// DELETE /api/orgs/:id/members/:user_id — org owner/admin OR the member themselves
func (h *OrgHandler) RemoveMember(c *gin.Context) {
	callerID, callerLabel, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}
	targetUserID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid user_id")
		return
	}
	if err := h.svc.RemoveMember(c.Request.Context(), callerID, callerLabel, orgID, targetUserID); err != nil {
		respondOrgErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// POST /api/orgs/:id/transfer-ownership — org owner
func (h *OrgHandler) TransferOwnership(c *gin.Context) {
	callerID, callerLabel, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}
	var req struct {
		TargetUserID int64 `json:"target_user_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.svc.TransferOwnership(c.Request.Context(), callerID, callerLabel, orgID, req.TargetUserID); err != nil {
		respondOrgErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ownership transferred"})
}

// GET /api/orgs/:id/audit-log — org owner/admin
func (h *OrgHandler) ListAuditLog(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}

	limit, _ := strconv.ParseInt(c.DefaultQuery("limit", "50"), 10, 64)
	offset, _ := strconv.ParseInt(c.DefaultQuery("offset", "0"), 10, 64)

	entries, err := h.svc.ListAuditLog(c.Request.Context(), callerID, orgID, limit, offset)
	if err != nil {
		respondOrgErr(c, err)
		return
	}
	c.JSON(http.StatusOK, entries)
}
