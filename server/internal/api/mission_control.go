// Mission control (design §6.6 / §6.7.8) — two independent metric panels for
// the Intake and Completion capabilities, plus the global intake pause switch.
//
// The metrics are read-only aggregates scoped to the caller's accessible
// projects. Both routes sit behind the normal authenticated API group; the
// workspace overview is where the SPA renders the panels, so there is no extra
// per-project authz beyond the accessible-project scope the service applies.
package api

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// MissionControlHandler serves GET /api/mission-control and
// PUT /api/mission-control/pause.
type MissionControlHandler struct {
	Svc *service.MissionControlService
	DB  *sql.DB
}

// Get serves GET /api/mission-control.
//
// @Summary      Mission control metrics
// @Description  Two independent panels: Intake (today accepted / pending / approval / misfire / duplicate / event-to-start, per source+chat daily counts, and per-binding delivery health with recent success/failure and 24h counters) and Completion (in progress / stuck / first pass / avg rounds / fake completion / interventions / today tokens+cost). Scoped to the caller's accessible projects; ?owner=user:<id>|org:<slug> narrows further.
// @Tags         MissionControl
// @Produce      json
// @Param        owner  query     string  false  "owner filter"
// @Success      200    {object}  service.MissionControlView
// @Failure      400    {object}  Error
// @Failure      401    {object}  Error
// @Failure      500    {object}  Error
// @Router       /mission-control [get]
func (h *MissionControlHandler) Get(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	owner, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	var filter *service.OwnerRef
	if owner.Type != "" {
		filter = &service.OwnerRef{Type: owner.Type, ID: owner.ID}
	}
	view, err := h.Svc.View(c.Request.Context(), uid, filter)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// Ops serves GET /api/mission-control/ops.
//
// @Summary      Mission control ops bots
// @Description  Ops Bot console aggregate grouped owner → channel (Platform Pack) → account. Each account carries status, today/week published output, latest daily metrics, current-month budget headroom, pending approval count, and risk level/reasons. A single account's read failure is reported on that row instead of failing the whole panel.
// @Tags         MissionControl
// @Produce      json
// @Param        owner  query     string  false  "owner filter"
// @Success      200    {object}  service.MissionControlOpsView
// @Failure      400    {object}  Error
// @Failure      401    {object}  Error
// @Failure      500    {object}  Error
// @Router       /mission-control/ops [get]
func (h *MissionControlHandler) Ops(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	owner, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	var filter *service.OwnerRef
	if owner.Type != "" {
		filter = &service.OwnerRef{Type: owner.Type, ID: owner.ID}
	}
	view, err := h.Svc.OpsView(c.Request.Context(), uid, filter)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// SetPause serves PUT /api/mission-control/pause.
//
// @Summary      Pause or resume intake for every project in scope
// @Description  Flips the intake stop switch on all accessible projects (or the ?owner= subset). Pausing only stops inferred/auto work; explicit requests keep flowing.
// @Tags         MissionControl
// @Accept       json
// @Produce      json
// @Param        owner  query     string              false  "owner filter"
// @Param        body   body      object{paused=bool} true   "Pause state"
// @Success      200    {object}  object{paused=bool,projects=int}
// @Failure      400    {object}  Error
// @Failure      401    {object}  Error
// @Router       /mission-control/pause [put]
func (h *MissionControlHandler) SetPause(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	owner, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	var body struct {
		Paused *bool `json:"paused"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Paused == nil {
		BadRequest(c, "paused is required")
		return
	}
	var filter *service.OwnerRef
	if owner.Type != "" {
		filter = &service.OwnerRef{Type: owner.Type, ID: owner.ID}
	}
	n, err := h.Svc.SetPaused(c.Request.Context(), uid, filter, *body.Paused)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"paused": *body.Paused, "projects": n})
}
