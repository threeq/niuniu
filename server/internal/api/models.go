// Shared model library, usage and budget endpoints (design §11.1).
//
// Owner resolution mirrors the env-provider/env-account handlers: an explicit
// body owner wins, otherwise the caller's personal owner is used. Reads and
// writes go through Authz so a caller can never act on another owner's rows
// (cross-owner writes answer 403, not "not found").
//
// Error mapping:
//   - system model mutation         -> 400 (hide it with an owner row instead)
//   - cross-owner / no permission   -> 403
//   - refresh without /models       -> 422
//   - provider refresh network error-> 502
//   - budget below month usage      -> 409 (would be exceeded immediately)
package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// ModelHandler serves /api/models, /api/model-usage and /api/model-budget.
type ModelHandler struct {
	library *service.ModelLibraryService
	refresh *service.ModelRefreshService
	usage   *service.ModelUsageService
	budget  *service.ModelBudgetService
	// Authz gates owner access; nil disables the checks (auth-off editions and
	// unit-test setups), matching the other owner-scoped handlers.
	Authz *service.Authz
}

func NewModelHandler(
	library *service.ModelLibraryService,
	refresh *service.ModelRefreshService,
	usage *service.ModelUsageService,
	budget *service.ModelBudgetService,
) *ModelHandler {
	return &ModelHandler{library: library, refresh: refresh, usage: usage, budget: budget}
}

type modelOwnerPayload struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

type upsertModelRequest struct {
	Name        string             `json:"name" binding:"required"`
	DisplayName string             `json:"display_name"`
	Enabled     *bool              `json:"enabled"`
	Owner       *modelOwnerPayload `json:"owner,omitempty"`
}

type updateModelRequest struct {
	DisplayName *string `json:"display_name"`
	Enabled     *bool   `json:"enabled"`
}

type refreshPreviewRequest struct {
	ProviderID int64              `json:"provider_id" binding:"required"`
	Owner      *modelOwnerPayload `json:"owner,omitempty"`
}

type importModelsRequest struct {
	Names []string           `json:"names" binding:"required"`
	Owner *modelOwnerPayload `json:"owner,omitempty"`
}

type setBudgetRequest struct {
	MonthlyTokens *int64 `json:"monthly_tokens" binding:"required"`
}

// callerOwner resolves the owner for a request: explicit override first,
// otherwise the caller's personal owner. The SPA's no-currentUser fallback
// {type:"user", id:0} counts as "default to caller".
func callerOwner(c *gin.Context, override *modelOwnerPayload) (service.OwnerRef, bool) {
	userID := c.GetInt64("auth_user_id")
	if override != nil && override.Type != "" && !(override.Type == "user" && override.ID == 0) {
		owner := service.OwnerRef{Type: override.Type, ID: override.ID}
		if err := owner.Validate(); err != nil {
			BadRequest(c, err.Error())
			return service.OwnerRef{}, false
		}
		return owner, true
	}
	owner := service.OwnerRef{Type: "user", ID: userID}
	if err := owner.Validate(); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return service.OwnerRef{}, false
	}
	return owner, true
}

// queryOwner resolves the owner from ?owner_type=&owner_id=, defaulting to the
// caller's personal owner.
func queryOwner(c *gin.Context) (service.OwnerRef, bool) {
	override := &modelOwnerPayload{
		Type: c.Query("owner_type"),
		ID:   parseIntOrZero(c.Query("owner_id")),
	}
	return callerOwner(c, override)
}

func (h *ModelHandler) requireRead(c *gin.Context, owner service.OwnerRef) bool {
	userID := c.GetInt64("auth_user_id")
	if h.Authz == nil || userID <= 0 {
		return true
	}
	if err := h.Authz.EnsureOwnerReadable(c.Request.Context(), userID, owner); err != nil {
		writeAuthzError(c, err)
		return false
	}
	return true
}

func (h *ModelHandler) requireWrite(c *gin.Context, owner service.OwnerRef) bool {
	userID := c.GetInt64("auth_user_id")
	if h.Authz == nil || userID <= 0 {
		return true
	}
	if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
		writeAuthzError(c, err)
		return false
	}
	return true
}

// requireUsageRead is the stricter gate for usage/cost data: an org's numbers
// are limited to its administrators rather than every member (design §13).
func (h *ModelHandler) requireUsageRead(c *gin.Context, owner service.OwnerRef) bool {
	userID := c.GetInt64("auth_user_id")
	if h.Authz == nil || userID <= 0 {
		return true
	}
	if err := h.Authz.EnsureUsageReadable(c.Request.Context(), userID, owner); err != nil {
		writeAuthzError(c, err)
		return false
	}
	return true
}

// List returns the system rows merged with the owner's rows. ?all=1 includes
// disabled rows for the management page; the default dropdown view hides them.
func (h *ModelHandler) List(c *gin.Context) {
	owner, ok := queryOwner(c)
	if !ok {
		return
	}
	if !h.requireRead(c, owner) {
		return
	}
	includeDisabled := isTruthy(c.Query("all"))
	models, err := h.library.List(c.Request.Context(), owner, includeDisabled)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, models)
}

// Upsert adds or overwrites an owner row. A duplicate name updates the
// existing owner row and answers 200; a new row answers 201.
func (h *ModelHandler) Upsert(c *gin.Context) {
	var req upsertModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	owner, ok := callerOwner(c, req.Owner)
	if !ok {
		return
	}
	if !h.requireWrite(c, owner) {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	name := strings.TrimSpace(req.Name)
	existed := false
	if existing, err := h.library.List(c.Request.Context(), owner, true); err == nil {
		for _, m := range existing {
			if m.Name == name && !(m.OwnerType == "user" && m.OwnerID == 0) {
				existed = true
				break
			}
		}
	}
	model, err := h.library.Upsert(c.Request.Context(), owner, name, req.DisplayName, enabled)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	c.JSON(status, model)
}

// Update patches display name and/or enabled on an owner row.
func (h *ModelHandler) Update(c *gin.Context) {
	id, ok := parseModelID(c)
	if !ok {
		return
	}
	var req updateModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	model, ok := h.loadEditable(c, id)
	if !ok {
		return
	}
	updated, err := h.library.Update(c.Request.Context(),
		service.OwnerRef{Type: model.OwnerType, ID: model.OwnerID},
		id, req.DisplayName, req.Enabled)
	if err != nil {
		h.writeLibraryError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// Delete removes an owner row. System rows are immutable and answer 400.
func (h *ModelHandler) Delete(c *gin.Context) {
	id, ok := parseModelID(c)
	if !ok {
		return
	}
	model, ok := h.loadEditable(c, id)
	if !ok {
		return
	}
	if err := h.library.Delete(c.Request.Context(),
		service.OwnerRef{Type: model.OwnerType, ID: model.OwnerID}, id); err != nil {
		h.writeLibraryError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// loadEditable loads id and enforces the mutation rules shared by PATCH and
// DELETE: system rows are immutable (400) and another owner's row is a
// permission error (403), never a silent "not found".
func (h *ModelHandler) loadEditable(c *gin.Context, id int64) (service.ModelDTO, bool) {
	model, err := h.library.Get(c.Request.Context(), id)
	if err != nil {
		h.writeLibraryError(c, err)
		return service.ModelDTO{}, false
	}
	if model.OwnerType == "user" && model.OwnerID == 0 {
		BadRequest(c, "system model cannot be modified; create an owner row with enabled=false to hide it")
		return service.ModelDTO{}, false
	}
	if !h.requireWrite(c, service.OwnerRef{Type: model.OwnerType, ID: model.OwnerID}) {
		return service.ModelDTO{}, false
	}
	return model, true
}

func (h *ModelHandler) writeLibraryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		NotFound(c, "MODEL")
	case errors.Is(err, service.ErrSystemModelImmutable):
		BadRequest(c, "system model cannot be modified; create an owner row with enabled=false to hide it")
	default:
		InternalError(c, err)
	}
}

// RefreshPreview lists a provider's model catalogue without importing it.
// 422 means the provider has no usable GET /models endpoint; any other fetch
// failure is 502 with the concrete provider endpoint and reason.
func (h *ModelHandler) RefreshPreview(c *gin.Context) {
	var req refreshPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	owner, ok := callerOwner(c, req.Owner)
	if !ok {
		return
	}
	if !h.requireRead(c, owner) {
		return
	}
	candidates, err := h.refresh.RefreshPreview(c.Request.Context(), owner, req.ProviderID)
	switch {
	case errors.Is(err, service.ErrModelsEndpointUnsupported):
		RespondError(c, http.StatusUnprocessableEntity, "MODEL_REFRESH_UNSUPPORTED",
			"provider does not support a /models listing; enter model names manually")
		return
	case errors.Is(err, service.ErrNotFound):
		NotFound(c, "PROVIDER")
		return
	case err != nil:
		RespondError(c, http.StatusBadGateway, "MODEL_REFRESH_FAILED", err.Error())
		return
	}
	resp := gin.H{"models": candidates}
	if len(candidates) == 0 {
		// The picker shows this as a hint; an empty list is still a 200.
		resp["hint"] = "provider returned no models"
	}
	c.JSON(http.StatusOK, resp)
}

// Import merges a list of model names into the owner's library.
func (h *ModelHandler) Import(c *gin.Context) {
	var req importModelsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	owner, ok := callerOwner(c, req.Owner)
	if !ok {
		return
	}
	if !h.requireWrite(c, owner) {
		return
	}
	added, skipped, err := h.library.Import(c.Request.Context(), owner, req.Names)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"added": added, "skipped": skipped})
}

// UsageSummary aggregates model usage for one owner over [from, to), with an
// optional breakdown along a single dimension.
func (h *ModelHandler) UsageSummary(c *gin.Context) {
	owner, ok := queryOwner(c)
	if !ok {
		return
	}
	if !h.requireUsageRead(c, owner) {
		return
	}
	groupBy := strings.TrimSpace(c.Query("group_by"))
	switch groupBy {
	case "", "model", "provider", "project", "workspace", "cli":
	default:
		BadRequest(c, "group_by must be one of model|provider|project|workspace|cli")
		return
	}
	from, to, ok := usageRange(c)
	if !ok {
		return
	}
	summary, err := h.usage.Summary(c.Request.Context(), owner, from, to, groupBy)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

// GetBudget returns the owner's hard cap, this month's usage and every project
// soft cap.
func (h *ModelHandler) GetBudget(c *gin.Context) {
	owner, ok := queryOwner(c)
	if !ok {
		return
	}
	if !h.requireUsageRead(c, owner) {
		return
	}
	view, err := h.budget.Get(c.Request.Context(), owner)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// SetBudget sets the owner's monthly hard cap. 0 means unlimited. A positive
// cap that this month's usage already meets would block the owner immediately,
// so it is rejected with 409 and the used/limit/period figures.
func (h *ModelHandler) SetBudget(c *gin.Context) {
	var req setBudgetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if *req.MonthlyTokens < 0 {
		BadRequest(c, "monthly_tokens must be >= 0")
		return
	}
	owner, ok := callerOwner(c, nil)
	if !ok {
		return
	}
	if !h.requireUsageWrite(c, owner) {
		return
	}
	if *req.MonthlyTokens > 0 {
		view, err := h.budget.Get(c.Request.Context(), owner)
		if err != nil {
			InternalError(c, err)
			return
		}
		if view.UsedTokens >= *req.MonthlyTokens {
			RespondErrorWithDetails(c, http.StatusConflict, "MODEL_BUDGET_EXCEEDED",
				"monthly token budget is already used up; raise the limit or wait for the next period",
				gin.H{
					"used_tokens":    view.UsedTokens,
					"monthly_tokens": *req.MonthlyTokens,
					"period":         view.Period,
				})
			return
		}
	}
	if err := h.budget.Set(c.Request.Context(), owner, *req.MonthlyTokens); err != nil {
		InternalError(c, err)
		return
	}
	h.writeBudget(c, owner)
}

// SetProjectBudget sets a project's advisory soft cap. Soft caps may sit below
// current usage (they only warn), so this never answers 409.
func (h *ModelHandler) SetProjectBudget(c *gin.Context) {
	projectID, ok := parseProjectID(c)
	if !ok {
		return
	}
	var req setBudgetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if *req.MonthlyTokens < 0 {
		BadRequest(c, "monthly_tokens must be >= 0")
		return
	}
	if !h.requireProject(c, projectID) {
		return
	}
	owner, ok := callerOwner(c, nil)
	if !ok {
		return
	}
	if !h.requireUsageWrite(c, owner) {
		return
	}
	if err := h.budget.SetProject(c.Request.Context(), owner, projectID, *req.MonthlyTokens); err != nil {
		BadRequest(c, err.Error())
		return
	}
	h.writeBudget(c, owner)
}

// DeleteProjectBudget removes a project's soft cap. Idempotent.
func (h *ModelHandler) DeleteProjectBudget(c *gin.Context) {
	projectID, ok := parseProjectID(c)
	if !ok {
		return
	}
	if !h.requireProject(c, projectID) {
		return
	}
	owner, ok := callerOwner(c, nil)
	if !ok {
		return
	}
	if !h.requireUsageWrite(c, owner) {
		return
	}
	if err := h.budget.DeleteProject(c.Request.Context(), owner, projectID); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// requireUsageWrite gates budget writes: personal owners may edit their own
// budget, while an org's budget requires an org manager (design §13).
func (h *ModelHandler) requireUsageWrite(c *gin.Context, owner service.OwnerRef) bool {
	if owner.Type == "org" {
		return h.requireUsageRead(c, owner)
	}
	return h.requireWrite(c, owner)
}

// requireProject verifies the caller can access the project a soft cap targets.
func (h *ModelHandler) requireProject(c *gin.Context, projectID int64) bool {
	userID := c.GetInt64("auth_user_id")
	if h.Authz == nil || userID <= 0 {
		return true
	}
	if _, err := h.Authz.CanAccessProject(c.Request.Context(), userID, projectID); err != nil {
		writeAuthzError(c, err)
		return false
	}
	return true
}

func (h *ModelHandler) writeBudget(c *gin.Context, owner service.OwnerRef) {
	view, err := h.budget.Get(c.Request.Context(), owner)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// usageRange reads ?from=&to=; both accept RFC3339 or YYYY-MM-DD (UTC). The
// default window is the current UTC month through now.
func usageRange(c *gin.Context) (time.Time, time.Time, bool) {
	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := now

	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		t, err := parseTimeQuery(raw, false)
		if err != nil {
			BadRequest(c, "invalid from: use RFC3339 or YYYY-MM-DD")
			return time.Time{}, time.Time{}, false
		}
		from = t
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		t, err := parseTimeQuery(raw, true)
		if err != nil {
			BadRequest(c, "invalid to: use RFC3339 or YYYY-MM-DD")
			return time.Time{}, time.Time{}, false
		}
		to = t
	}
	if !from.Before(to) {
		BadRequest(c, "from must be before to")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// parseTimeQuery accepts RFC3339 or a date. A date-only `to` covers the whole
// day, so ?from=2026-11-01&to=2026-11-01 is a valid one-day window.
func parseTimeQuery(raw string, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, err
	}
	t = t.UTC()
	if endOfDay {
		t = t.AddDate(0, 0, 1)
	}
	return t, nil
}

func parseModelID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		BadRequest(c, "invalid model id")
		return 0, false
	}
	return id, true
}

func parseProjectID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		BadRequest(c, "invalid project id")
		return 0, false
	}
	return id, true
}

func parseIntOrZero(raw string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func isTruthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
