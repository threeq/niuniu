package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type UsersHandler struct {
	svc *service.UserService
}

func NewUsersHandler(svc *service.UserService) *UsersHandler {
	return &UsersHandler{svc: svc}
}

// Search GET /api/users/search?q=<query>&org_id=<id>
// Returns up to 20 users matching the query, excluding existing members of the org.
// Caller must be owner/admin of the org.
func (h *UsersHandler) Search(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	q := c.Query("q")
	orgIDStr := c.Query("org_id")
	if orgIDStr == "" {
		BadRequest(c, "org_id is required")
		return
	}
	orgID, err := strconv.ParseInt(orgIDStr, 10, 64)
	if err != nil || orgID <= 0 {
		BadRequest(c, "invalid org_id")
		return
	}

	result, err := h.svc.SearchForOrg(c.Request.Context(), callerID, orgID, q)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN", "message": "not allowed"}})
			return
		}
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": result.Users, "total": result.Total})
}
