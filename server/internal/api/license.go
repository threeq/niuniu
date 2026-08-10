package api

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/license"
)

// LicenseHandler serves license status (any user) and admin install/detail.
type LicenseHandler struct {
	svc license.Gate
}

func NewLicenseHandler(svc license.Gate) *LicenseHandler {
	return &LicenseHandler{svc: svc}
}

// GetStatus returns the UI-safe status for the banner. Any authenticated user.
func (h *LicenseHandler) GetStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.Status(c.Request.Context()))
}

// GetAdminDetail returns the status (super-admin only); reserved for future
// admin-only fields.
func (h *LicenseHandler) GetAdminDetail(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.Status(c.Request.Context()))
}

type installLicenseReq struct {
	License string `json:"license"`
}

// Install accepts either a JSON body {license:"<blob>"} or a multipart file
// field named "file". Super-admin only (route guard).
func (h *LicenseHandler) Install(c *gin.Context) {
	blob := ""
	if f, err := c.FormFile("file"); err == nil {
		fh, ferr := f.Open()
		if ferr != nil {
			BadRequest(c, "cannot read uploaded file")
			return
		}
		defer fh.Close()
		data, readErr := io.ReadAll(io.LimitReader(fh, 1<<20))
		if readErr != nil {
			BadRequest(c, "failed reading uploaded file")
			return
		}
		blob = string(data)
	} else {
		var req installLicenseReq
		if err := c.ShouldBindJSON(&req); err != nil || req.License == "" {
			BadRequest(c, "missing license content")
			return
		}
		blob = req.License
	}
	if err := h.svc.Install(c.Request.Context(), blob); err != nil {
		switch err {
		case license.ErrExpired:
			RespondError(c, http.StatusBadRequest, "LICENSE_INVALID", "license is already expired")
		case license.ErrInvalidContent:
			RespondError(c, http.StatusBadRequest, "LICENSE_INVALID", "license content is invalid (unsupported edition or validity window)")
		case license.ErrSignature, license.ErrMalformed, license.ErrNoKeys:
			RespondError(c, http.StatusBadRequest, "LICENSE_INVALID", "license signature is invalid")
		default:
			InternalError(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, h.svc.Status(c.Request.Context()))
}
