package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// VideoProjectHandler exposes the P3 "video product" thin API (plan §4.2):
// an aggregate read of <ws>/video-project/, revision-guarded product saves,
// change-request creation, and the "送回重生成" dispatch. Served on the
// existing workspaces route group (same authz as the other /:id routes).
type VideoProjectHandler struct {
	svc   *service.VideoProjectService
	Authz *service.Authz
}

func NewVideoProjectHandler(svc *service.VideoProjectService) *VideoProjectHandler {
	return &VideoProjectHandler{svc: svc}
}

// authzWorkspace checks the caller can access the workspace. Returns true on
// success (or when authz is disabled); writes an HTTP error and returns false
// otherwise.
func (h *VideoProjectHandler) authzWorkspace(c *gin.Context, userID, workspaceID int64) bool {
	if userID <= 0 || h.Authz == nil {
		return true
	}
	if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
		writeAuthzError(c, aerr)
		return false
	}
	return true
}

// workspaceParam resolves the workspace id param and authorizes it. Returns
// ok=false after writing the error response.
func (h *VideoProjectHandler) workspaceParam(c *gin.Context) (int64, bool) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return 0, false
	}
	if !h.authzWorkspace(c, userID, wsID) {
		return 0, false
	}
	return wsID, true
}

// writeVideoProjectError maps a service failure: contract errors carry their
// own HTTP status + Chinese message; anything else is a 500.
func writeVideoProjectError(c *gin.Context, err error) {
	var vpErr *service.VideoProjectError
	if errors.As(err, &vpErr) {
		code := "BAD_REQUEST"
		switch vpErr.Status {
		case http.StatusNotFound:
			code = "NOT_FOUND"
		case http.StatusConflict:
			code = "CONFLICT"
		case http.StatusInternalServerError:
			code = "INTERNAL_ERROR"
		}
		RespondError(c, vpErr.Status, code, vpErr.Message)
		return
	}
	InternalError(c, err)
}

// Get returns the aggregate read of the workspace's video-project area.
// A missing video-project directory yields exists=false with empty collections
// (never 404). GET /api/workspaces/:id/video-project
func (h *VideoProjectHandler) Get(c *gin.Context) {
	wsID, ok := h.workspaceParam(c)
	if !ok {
		return
	}
	agg, err := h.svc.GetAggregate(c.Request.Context(), wsID)
	if err != nil {
		writeVideoProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, agg)
}

// videoProductSaveRequest is the PUT products/:key body. expected_revision is
// required for json products (concurrency guard) and may be omitted for
// markdown.
type videoProductSaveRequest struct {
	Content          string `json:"content"`
	ExpectedRevision *int64 `json:"expected_revision"`
}

// PutProduct saves one product and returns the same single-product object the
// aggregate read reports. Revision mismatch → 409.
// PUT /api/workspaces/:id/video-project/products/:key
func (h *VideoProjectHandler) PutProduct(c *gin.Context) {
	wsID, ok := h.workspaceParam(c)
	if !ok {
		return
	}
	var req videoProductSaveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "请求体格式不正确："+err.Error())
		return
	}
	product, err := h.svc.SaveProduct(c.Request.Context(), wsID, c.Param("key"), req.Content, req.ExpectedRevision)
	if err != nil {
		writeVideoProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, product)
}

// videoChangeCreateRequest is the POST changes body.
type videoChangeCreateRequest struct {
	Target  string `json:"target"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// CreateChange records a new modification request (status=pending) and returns
// the change object. POST /api/workspaces/:id/video-project/changes
func (h *VideoProjectHandler) CreateChange(c *gin.Context) {
	wsID, ok := h.workspaceParam(c)
	if !ok {
		return
	}
	var req videoChangeCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "请求体格式不正确："+err.Error())
		return
	}
	chg, err := h.svc.CreateChange(c.Request.Context(), wsID, req.Target, req.Kind, req.Content)
	if err != nil {
		writeVideoProjectError(c, err)
		return
	}
	c.JSON(http.StatusCreated, chg)
}

// videoChangeDispatchRequest is the dispatch body; issue_id is optional.
type videoChangeDispatchRequest struct {
	IssueID int64 `json:"issue_id"`
}

// DispatchChange routes a change back for regeneration and returns the updated
// change plus the target issue.
// POST /api/workspaces/:id/video-project/changes/:changeId/dispatch
func (h *VideoProjectHandler) DispatchChange(c *gin.Context) {
	wsID, ok := h.workspaceParam(c)
	if !ok {
		return
	}
	var req videoChangeDispatchRequest
	// An empty body is allowed (auto-match by target).
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			BadRequest(c, "请求体格式不正确："+err.Error())
			return
		}
	}
	res, err := h.svc.DispatchChange(c.Request.Context(), wsID, c.Param("changeId"), req.IssueID, c.GetInt64("auth_user_id"))
	if err != nil {
		writeVideoProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}
