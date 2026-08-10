package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// MeHandler handles /api/me/* endpoints.
type MeHandler struct {
	orgSvc *service.OrgService
}

func NewMeHandler(orgSvc *service.OrgService) *MeHandler {
	return &MeHandler{orgSvc: orgSvc}
}

// GET /api/me/orgs — authenticated; returns [{id, slug, name, role}]
func (h *MeHandler) ListOrgs(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	rows, err := h.orgSvc.ListForUser(c.Request.Context(), callerID)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		out = append(out, gin.H{
			"id":   r.ID,
			"slug": r.Slug,
			"name": r.Name,
			"role": r.Role,
		})
	}
	c.JSON(http.StatusOK, out)
}
