package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// LabelHandler exposes the project-scoped label CRUD endpoints. The svc
// implements the storage + admin gate; the authz pointer is used here only
// for the project-access check on List/Create (Update/Delete already gate
// inside the service via IsProjectAdmin).
type LabelHandler struct {
	svc   *service.LabelService
	authz *service.Authz
}

func NewLabelHandler(svc *service.LabelService, authz *service.Authz) *LabelHandler {
	return &LabelHandler{svc: svc, authz: authz}
}

type createLabelReq struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type updateLabelReq struct {
	Name        *string `json:"name,omitempty"`
	Color       *string `json:"color,omitempty"`
	Description *string `json:"description,omitempty"`
}

// List returns labels for the project. With ?with_usage=true the response
// includes a usage_count field (issue_labels join). The optional ?q= param is
// applied as a case-insensitive substring filter on the name.
func (h *LabelHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.authz != nil {
		if _, err := h.authz.CanAccessProject(c.Request.Context(), userID, projectID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if c.Query("with_usage") == "true" {
		labels, err := h.svc.ListWithUsage(c.Request.Context(), projectID)
		if err != nil {
			InternalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": labels})
		return
	}
	q := c.Query("q")
	labels, err := h.svc.List(c.Request.Context(), userID, projectID, q)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": labels})
}

// Create inserts a new label under the project. 409 with code `label_name_taken`
// when the (project_id, name) pair already exists; 400 for invalid color/name.
func (h *LabelHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.authz != nil {
		if _, err := h.authz.CanAccessProject(c.Request.Context(), userID, projectID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req createLabelReq
	if err := c.BindJSON(&req); err != nil {
		BadRequest(c, "bad json")
		return
	}

	lbl, err := h.svc.Create(c.Request.Context(), userID, projectID, service.LabelCreateInput{
		Name: req.Name, Color: req.Color, Description: req.Description,
	})
	if err != nil {
		var conflict *service.LabelNameConflictError
		switch {
		case errors.As(err, &conflict):
			c.JSON(http.StatusConflict, gin.H{"code": "label_name_taken", "existing": conflict.Existing})
		case errors.Is(err, service.ErrInvalidLabelColor):
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_color"})
		case errors.Is(err, service.ErrInvalidLabelName):
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_name"})
		default:
			InternalError(c, err)
		}
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": lbl})
}

// Update applies a partial update. Authz (admin gate) is enforced inside the
// service via IsProjectAdmin; we only translate the typed errors to HTTP.
func (h *LabelHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	labelID, err := strconv.ParseInt(c.Param("label_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid label ID")
		return
	}
	var req updateLabelReq
	if err := c.BindJSON(&req); err != nil {
		BadRequest(c, "bad json")
		return
	}

	lbl, err := h.svc.Update(c.Request.Context(), userID, labelID, service.LabelUpdateInput{
		Name: req.Name, Color: req.Color, Description: req.Description,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrLabelNotFound):
			c.JSON(http.StatusNotFound, gin.H{"code": "not_found"})
		case errors.Is(err, service.ErrLabelForbidden):
			c.JSON(http.StatusForbidden, gin.H{"code": "forbidden"})
		case errors.Is(err, service.ErrInvalidLabelColor):
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_color"})
		case errors.Is(err, service.ErrInvalidLabelName):
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_name"})
		default:
			var conflict *service.LabelNameConflictError
			if errors.As(err, &conflict) {
				c.JSON(http.StatusConflict, gin.H{"code": "label_name_taken", "existing": conflict.Existing})
				return
			}
			InternalError(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": lbl})
}

// Delete removes the label. Cascading delete of issue_labels is handled by
// the FK constraint in the schema.
func (h *LabelHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	labelID, err := strconv.ParseInt(c.Param("label_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid label ID")
		return
	}

	if err := h.svc.Delete(c.Request.Context(), userID, labelID); err != nil {
		switch {
		case errors.Is(err, service.ErrLabelNotFound):
			c.JSON(http.StatusNotFound, gin.H{"code": "not_found"})
		case errors.Is(err, service.ErrLabelForbidden):
			c.JSON(http.StatusForbidden, gin.H{"code": "forbidden"})
		default:
			InternalError(c, err)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

// ─── Batch label attach/detach for the bulk-actions toolbar (REST+MCP) ───────

type batchIssueLabelsRequest struct {
	IssueIDs       []int64 `json:"issue_ids" binding:"required"`
	AddLabelIDs    []int64 `json:"add_label_ids"`
	RemoveLabelIDs []int64 `json:"remove_label_ids"`
}

// BatchSetIssueLabels applies add/remove label deltas across many issues.
// Per-issue authz via authz.CanAccessIssue produces skip-report rows. Labels
// not belonging to the issue's project are rejected by FK at write time;
// callers should pre-filter to project-local labels (the UI does).
func (h *LabelHandler) BatchSetIssueLabels(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req batchIssueLabelsRequest
	if err := c.BindJSON(&req); err != nil {
		BadRequest(c, "bad json")
		return
	}
	ok := make([]int64, 0, len(req.IssueIDs))
	skipped := make([]service.SkippedItem, 0)
	for _, id := range req.IssueIDs {
		if userID > 0 && h.authz != nil {
			if _, err := h.authz.CanAccessIssue(c.Request.Context(), userID, id); err != nil {
				skipped = append(skipped, service.SkippedItem{ID: id, Reason: "forbidden"})
				continue
			}
		}
		ok = append(ok, id)
	}
	// Apply add + remove in a single transaction so a mid-way failure does not
	// leave the adds committed while reporting a total failure to the client.
	if len(req.AddLabelIDs) > 0 || len(req.RemoveLabelIDs) > 0 {
		if err := h.svc.ApplyIssueLabelDeltas(c.Request.Context(), ok, req.AddLabelIDs, req.RemoveLabelIDs); err != nil {
			InternalError(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, service.BatchResult{Succeeded: ok, Skipped: skipped})
}
