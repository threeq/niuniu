package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/autostart"
)

// AutostartHandler exposes the "launch at login" toggle for the personal
// (bundled-desktop) edition. It only makes sense when niuniu-server runs on the
// user's own machine spawned by niuniu-desktop, which passes its executable
// path via NIUNIU_PERSONAL_EXE. When that env is empty (team / hosted mode) the
// feature reports supported=false and the SPA hides the toggle.
type AutostartHandler struct {
	mgr *autostart.Manager // nil => unsupported (not personal/desktop)
}

// NewAutostartHandler builds a handler. Pass os.Getenv("NIUNIU_PERSONAL_EXE");
// an empty string disables the feature.
func NewAutostartHandler(exePath string) *AutostartHandler {
	h := &AutostartHandler{}
	if exePath != "" {
		h.mgr = autostart.New(exePath)
	}
	return h
}

type autostartStatus struct {
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`
	// Minimized reports whether autostart launches start minimized to the tray.
	// Only meaningful when Enabled is true.
	Minimized bool `json:"minimized"`
}

// Get — GET /api/autostart -> {supported, enabled, minimized}.
func (h *AutostartHandler) Get(c *gin.Context) {
	if h.mgr == nil {
		c.JSON(http.StatusOK, autostartStatus{Supported: false})
		return
	}
	en, min, err := h.mgr.Status()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, autostartStatus{Supported: true, Enabled: en, Minimized: min})
}

// Put — PUT /api/autostart {enabled, minimized} -> applies, returns latest
// {supported, enabled, minimized}. minimized is honored only when enabled=true.
func (h *AutostartHandler) Put(c *gin.Context) {
	var req struct {
		Enabled   bool `json:"enabled"`
		Minimized bool `json:"minimized"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
		return
	}
	if h.mgr == nil {
		c.JSON(http.StatusOK, autostartStatus{Supported: false})
		return
	}
	var err error
	if req.Enabled {
		err = h.mgr.Enable(req.Minimized)
	} else {
		err = h.mgr.Disable()
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	en, min, err := h.mgr.Status()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, autostartStatus{Supported: true, Enabled: en, Minimized: min})
}
