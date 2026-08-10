package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type DirectoryHandler struct {
	svc *service.DirectoryService
}

func NewDirectoryHandler(svc *service.DirectoryService) *DirectoryHandler {
	return &DirectoryHandler{svc: svc}
}

// SystemInfo returns server system information (OS type and disk drives on Windows)
// @Summary      Get system info
// @Description  Get server operating system and available disk drives (Windows only)
// @Tags         Directories
// @Produce      json
// @Success      200  {object}  SystemInfoResponse
// @Router       /system-info [get]
func (h *DirectoryHandler) SystemInfo(c *gin.Context) {
	info := h.svc.GetSystemInfo()
	c.JSON(http.StatusOK, info)
}

// List returns subdirectories for a given path
// @Summary      List directories
// @Description  Get subdirectories for a given path
// @Tags         Directories
// @Produce      json
// @Param        path  query     string  true  "Absolute path to list"
// @Success      200  {object}  DirectoryListResponse
// @Failure      400  {object}  Error
// @Failure      403  {object}  Error
// @Failure      500  {object}  Error
// @Router       /directories [get]
func (h *DirectoryHandler) List(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		BadRequest(c, "path query parameter is required")
		return
	}

	// Validate path for security
	if err := h.svc.ValidatePath(path); err != nil {
		RespondError(c, http.StatusForbidden, "ACCESS_DENIED", err.Error())
		return
	}

	result, err := h.svc.List(path)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}

	c.JSON(http.StatusOK, result)
}

// SystemInfoResponse is an alias for service.SystemInfo used in Swagger annotations.
type SystemInfoResponse = service.SystemInfo

type DirectoryListResponse struct {
	Path        string           `json:"path"`
	Parent      *string          `json:"parent,omitempty"`
	Directories []DirectoryEntry `json:"directories"`
}

type DirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
