// opsbot.go — HTTP surface for the Ops Bot domain (design 2026-11-06 §12).
//
// Ownership is the hard boundary: list/create resolve the owner from ?owner=
// (default: the caller's personal space); every per-bot route derives the owner
// from the bot row itself, never from a query parameter, so a bot id alone can
// never cross owners. Reads call CanAccessOwner, writes EnsureOwnerWritable.
//
// The same handlers serve the minimal console under /api/ops and the MCP tunnel
// under /mcp/ops, so an agent tool call and a UI action see identical status
// codes and bodies.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/opsbot"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// OpsBotHandler binds the Ops Bot routes. Every dependency except Svc is
// optional so tests can exercise a subset; a route whose dependency is absent
// answers 503 instead of panicking.
type OpsBotHandler struct {
	Svc       *opsbot.Service
	Provision *opsbot.Provisioner
	Approval  *opsbot.Approval
	Budget    *opsbot.Budget
	Risk      *opsbot.Risk
	Packs     *packs.Registry
	Authz     *service.Authz
	Queries   *store.Queries
	DB        *sql.DB
	// CredLinks mints the one-time platform-credential link for a bot.
	CredLinks *service.CredentialOnboardingService
	// DataDir roots the browser profile directory.
	DataDir string
}

// --- DTOs ---

type opsBotDTO struct {
	ID                 int64           `json:"id"`
	OwnerType          string          `json:"owner_type"`
	OwnerID            int64           `json:"owner_id"`
	Role               string          `json:"role"`
	PackID             string          `json:"pack_id"`
	PackVersion        int64           `json:"pack_version"`
	AccountRef         string          `json:"account_ref"`
	DisplayName        string          `json:"display_name"`
	Persona            json.RawMessage `json:"persona"`
	CredentialRefs     json.RawMessage `json:"credential_refs"`
	KnowledgeScope     json.RawMessage `json:"knowledge_scope"`
	Goals              json.RawMessage `json:"goals"`
	Cadence            json.RawMessage `json:"cadence"`
	ApprovalPolicy     json.RawMessage `json:"approval_policy"`
	Budget             json.RawMessage `json:"budget"`
	EpicIssueID        *int64          `json:"epic_issue_id"`
	ControlWorkspaceID *int64          `json:"control_workspace_id"`
	DirectorBotID      *int64          `json:"director_bot_id"`
	Status             string          `json:"status"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type opsApprovalDTO struct {
	ID             int64           `json:"id"`
	WorkspaceID    int64           `json:"workspace_id"`
	BotID          int64           `json:"bot_id"`
	Type           string          `json:"type"`
	Status         string          `json:"status"`
	Metadata       json.RawMessage `json:"metadata"`
	Answers        json.RawMessage `json:"answers"`
	DecisionSource string          `json:"decision_source,omitempty"`
	DecidedBy      int64           `json:"decided_by,omitempty"`
	RequestedAt    time.Time       `json:"requested_at"`
	DecidedAt      *time.Time      `json:"decided_at,omitempty"`
	ExpiresAt      time.Time       `json:"expires_at"`
}

type opsPublishDTO struct {
	ID             int64      `json:"id"`
	IssueID        int64      `json:"issue_id"`
	BotID          int64      `json:"bot_id"`
	PackID         string     `json:"pack_id"`
	AccountRef     string     `json:"account_ref"`
	ContentVersion int64      `json:"content_version"`
	IdempotencyKey string     `json:"idempotency_key"`
	Status         string     `json:"status"`
	Receipt        string     `json:"receipt,omitempty"`
	URL            string     `json:"url,omitempty"`
	PublishedAt    *time.Time `json:"published_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type opsMetricsItemDTO struct {
	Date    string          `json:"date"`
	Metrics json.RawMessage `json:"metrics"`
	Source  string          `json:"source"`
}

type opsCalendarItemDTO struct {
	IssueID   int64  `json:"issue_id"`
	Title     string `json:"title"`
	IssueType string `json:"issue_type"`
	Status    string `json:"lifecycle_status"`
	DueDate   string `json:"due_date,omitempty"`
	StartDate string `json:"start_date,omitempty"`
}

// --- conversion helpers ---

// opsJSON returns raw as a JSON object. Empty or invalid input becomes {} so a
// bot row always renders as an object to the client.
func opsJSON(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" || !json.Valid([]byte(raw)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(raw)
}

func opsNullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func botDTO(b store.OpsBot) opsBotDTO {
	return opsBotDTO{
		ID:                 b.ID,
		OwnerType:          b.OwnerType,
		OwnerID:            b.OwnerID,
		Role:               b.Role,
		PackID:             b.PackID,
		PackVersion:        b.PackVersion,
		AccountRef:         b.AccountRef,
		DisplayName:        b.DisplayName,
		Persona:            opsJSON(b.PersonaJson),
		CredentialRefs:     opsJSON(b.CredentialRefsJson),
		KnowledgeScope:     opsJSON(b.KnowledgeScopeJson),
		Goals:              opsJSON(b.GoalsJson),
		Cadence:            opsJSON(b.CadenceJson),
		ApprovalPolicy:     opsJSON(b.ApprovalPolicyJson),
		Budget:             opsJSON(b.BudgetJson),
		EpicIssueID:        opsNullInt64Ptr(b.EpicIssueID),
		ControlWorkspaceID: opsNullInt64Ptr(b.ControlWorkspaceID),
		DirectorBotID:      opsNullInt64Ptr(b.DirectorBotID),
		Status:             b.Status,
		CreatedAt:          b.CreatedAt,
		UpdatedAt:          b.UpdatedAt,
	}
}

type opsApprovalMeta struct {
	BotID int64  `json:"bot_id"`
	Type  string `json:"type"`
}

func approvalDTO(a opsbot.ApprovalRequest) opsApprovalDTO {
	var meta opsApprovalMeta
	_ = json.Unmarshal([]byte(a.MetadataJSON), &meta)
	out := opsApprovalDTO{
		ID:             a.ID,
		WorkspaceID:    a.WorkspaceID,
		BotID:          meta.BotID,
		Type:           meta.Type,
		Status:         a.Status,
		Metadata:       opsJSON(a.MetadataJSON),
		Answers:        opsJSON(a.AnswersJSON),
		DecisionSource: a.DecisionSource,
		DecidedBy:      a.DecidedBy,
		RequestedAt:    a.RequestedAt,
		ExpiresAt:      a.ExpiresAt,
	}
	if !a.DecidedAt.IsZero() {
		decided := a.DecidedAt
		out.DecidedAt = &decided
	}
	return out
}

func publishDTO(r store.OpsPublishRecord) opsPublishDTO {
	out := opsPublishDTO{
		ID:             r.ID,
		IssueID:        r.IssueID,
		BotID:          r.BotID,
		PackID:         r.PackID,
		AccountRef:     r.AccountRef,
		ContentVersion: r.ContentVersion,
		IdempotencyKey: r.IdempotencyKey,
		Status:         r.Status,
		Receipt:        r.Receipt,
		URL:            r.Url,
		CreatedAt:      r.CreatedAt,
	}
	if r.PublishedAt.Valid {
		published := r.PublishedAt.Time
		out.PublishedAt = &published
	}
	return out
}

// --- authorization helpers ---

func (h *OpsBotHandler) callerUserID(c *gin.Context) (int64, bool) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return 0, false
	}
	return uid, true
}

func (h *OpsBotHandler) requireReady(c *gin.Context) bool {
	if h.Svc == nil || h.Authz == nil {
		RespondError(c, http.StatusServiceUnavailable, "OPSBOT_UNAVAILABLE", "ops bot service is not configured")
		return false
	}
	return true
}

// opsAuthorizeOwner is the explicit owner gate for every owner-scoped Ops Bot
// operation. The service layer (opsbot.Service.Get/UpdateConfig/SetStatus/
// KillSwitch) receives only a bot id and performs no ownership check, so the
// handler must prove the target's owner matches the authenticated identity
// here — a writability check alone is not authoritative.
//
//   - personal: owner.ID must BE the caller. This is checked directly, so even
//     a global admin cannot operate on another user's bot through this surface.
//   - org: the caller must be a member (reads) / writable member (writes).
//   - unknown owner type: denied.
//
// A mismatch is 403 and never falls through to the operation.
func (h *OpsBotHandler) opsAuthorizeOwner(c *gin.Context, ownerType string, ownerID int64, write bool) (int64, bool) {
	uid, ok := h.callerUserID(c)
	if !ok {
		return 0, false
	}
	switch ownerType {
	case "user":
		if ownerID != uid {
			RespondError(c, http.StatusForbidden, "OPS_FORBIDDEN", "resource belongs to another owner")
			return 0, false
		}
		return uid, true
	case "org":
		owner := service.OwnerRef{Type: "org", ID: ownerID}
		var err error
		if write {
			err = h.Authz.EnsureOwnerWritable(c.Request.Context(), uid, owner)
		} else {
			err = h.Authz.CanAccessOwner(c.Request.Context(), uid, owner)
		}
		if err != nil {
			writeAuthzError(c, err)
			return 0, false
		}
		return uid, true
	default:
		RespondError(c, http.StatusForbidden, "OPS_FORBIDDEN", "resource has an invalid owner type")
		return 0, false
	}
}

// ownerFromQuery resolves the owner (?owner=user:<id>|org:<slug|id>, absent =
// personal) and enforces the explicit owner gate.
func (h *OpsBotHandler) ownerFromQuery(c *gin.Context) (service.OwnerRef, int64, bool) {
	uid, ok := h.callerUserID(c)
	if !ok {
		return service.OwnerRef{}, 0, false
	}
	f, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return service.OwnerRef{}, 0, false
	}
	owner := service.OwnerRef{Type: "user", ID: uid}
	if f.Type != "" {
		owner = service.OwnerRef{Type: f.Type, ID: f.ID}
	}
	if _, ok := h.opsAuthorizeOwner(c, owner.Type, owner.ID, false); !ok {
		return service.OwnerRef{}, 0, false
	}
	return owner, uid, true
}

func (h *OpsBotHandler) ownerWritable(c *gin.Context) (service.OwnerRef, int64, bool) {
	owner, uid, ok := h.ownerFromQuery(c)
	if !ok {
		return service.OwnerRef{}, 0, false
	}
	if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		return service.OwnerRef{}, 0, false
	}
	return owner, uid, true
}

// botForAccess loads the bot named by :id and applies the explicit owner gate
// against the bot's OWN owner (never a query parameter).
func (h *OpsBotHandler) botForAccess(c *gin.Context, write bool) (store.OpsBot, int64, bool) {
	if _, ok := h.callerUserID(c); !ok {
		return store.OpsBot{}, 0, false
	}
	botID, err := parseIDParamNamed(c, "id")
	if err != nil {
		BadRequest(c, "bad id")
		return store.OpsBot{}, 0, false
	}
	bot, err := h.Svc.Get(c.Request.Context(), botID)
	if err != nil {
		h.respondOpsError(c, err)
		return store.OpsBot{}, 0, false
	}
	uid, ok := h.opsAuthorizeOwner(c, bot.OwnerType, bot.OwnerID, write)
	if !ok {
		return store.OpsBot{}, 0, false
	}
	return bot, uid, true
}

func (h *OpsBotHandler) botForID(c *gin.Context, botID int64, write bool) (store.OpsBot, int64, bool) {
	if _, ok := h.callerUserID(c); !ok {
		return store.OpsBot{}, 0, false
	}
	bot, err := h.Svc.Get(c.Request.Context(), botID)
	if err != nil {
		h.respondOpsError(c, err)
		return store.OpsBot{}, 0, false
	}
	uid, ok := h.opsAuthorizeOwner(c, bot.OwnerType, bot.OwnerID, write)
	if !ok {
		return store.OpsBot{}, 0, false
	}
	return bot, uid, true
}

// parseIDParamNamed is parseIDParam with a customizable name so the ops routes
// can use :id without colliding with other groups' helpers.
func parseIDParamNamed(c *gin.Context, name string) (int64, error) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return id, nil
}

// safeFileSegment reports whether s may be used as one path segment: no
// separators, no traversal, and only the characters a pack id uses.
func safeFileSegment(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func (h *OpsBotHandler) respondOpsError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, opsbot.ErrNotFound):
		RespondError(c, http.StatusNotFound, "OPS_BOT_NOT_FOUND", err.Error())
	case errors.Is(err, opsbot.ErrApprovalNotFound):
		RespondError(c, http.StatusNotFound, "OPS_APPROVAL_NOT_FOUND", err.Error())
	case errors.Is(err, opsbot.ErrPackNotFound), errors.Is(err, packs.ErrPackNotFound), errors.Is(err, os.ErrNotExist):
		RespondError(c, http.StatusNotFound, "OPS_PACK_NOT_FOUND", err.Error())
	case errors.Is(err, opsbot.ErrAccountExists):
		RespondError(c, http.StatusConflict, "OPS_ACCOUNT_EXISTS", err.Error())
	case errors.Is(err, opsbot.ErrAlreadyDecided):
		RespondError(c, http.StatusConflict, "OPS_APPROVAL_ALREADY_DECIDED", err.Error())
	case errors.Is(err, opsbot.ErrBudgetExceeded):
		RespondError(c, http.StatusConflict, "OPS_BUDGET_EXCEEDED", err.Error())
	case errors.Is(err, opsbot.ErrReloginRequired):
		RespondError(c, http.StatusConflict, "OPS_RELOGIN_REQUIRED", err.Error())
	case errors.Is(err, packs.ErrVersionExists):
		RespondError(c, http.StatusConflict, "OPS_PACK_VERSION_EXISTS", err.Error())
	case errors.Is(err, opsbot.ErrInvalidInput),
		errors.Is(err, opsbot.ErrInvalidConfig),
		errors.Is(err, opsbot.ErrInvalidStatus),
		errors.Is(err, opsbot.ErrInvalidDecision),
		errors.Is(err, opsbot.ErrInvalidApprovalType),
		errors.Is(err, opsbot.ErrBudgetNotConfigured),
		errors.Is(err, opsbot.ErrInvalidBudgetLimit),
		errors.Is(err, packs.ErrInvalidPackID),
		errors.Is(err, packs.ErrInvalidVersion),
		errors.Is(err, packs.ErrInvalidOwner):
		RespondError(c, http.StatusBadRequest, "OPS_INVALID_INPUT", err.Error())
	case errors.Is(err, sql.ErrNoRows):
		RespondError(c, http.StatusNotFound, "OPS_NOT_FOUND", "not found")
	default:
		InternalError(c, err)
	}
}

// opsRawObject accepts a config field as either a JSON object or a JSON-encoded
// string containing one, so agents and the SPA can both send natural payloads.
func opsRawObject(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var unquoted string
		if err := json.Unmarshal(raw, &unquoted); err != nil {
			return "", false, err
		}
		return unquoted, true, nil
	}
	return trimmed, true, nil
}

// --- bots ---

func (h *OpsBotHandler) ListBots(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	owner, _, ok := h.ownerFromQuery(c)
	if !ok {
		return
	}
	bots, err := h.Svc.ListByOwner(c.Request.Context(), opsbot.OwnerRef{Type: owner.Type, ID: owner.ID})
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	items := make([]opsBotDTO, 0, len(bots))
	for _, b := range bots {
		items = append(items, botDTO(b))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "count": len(items)})
}

type createOpsBotBody struct {
	Role               string          `json:"role"`
	PackID             string          `json:"pack_id"`
	PackVersion        int64           `json:"pack_version"`
	AccountRef         string          `json:"account_ref"`
	DisplayName        string          `json:"display_name"`
	PersonaJSON        json.RawMessage `json:"persona_json"`
	CredentialRefsJSON json.RawMessage `json:"credential_refs_json"`
	KnowledgeScopeJSON json.RawMessage `json:"knowledge_scope_json"`
	GoalsJSON          json.RawMessage `json:"goals_json"`
	CadenceJSON        json.RawMessage `json:"cadence_json"`
	ApprovalPolicyJSON json.RawMessage `json:"approval_policy_json"`
	BudgetJSON         json.RawMessage `json:"budget_json"`
	DirectorBotID      int64           `json:"director_bot_id"`
	// ProjectID optionally provisions the bot's control Epic + workspace.
	ProjectID int64 `json:"project_id"`
}

func (b createOpsBotBody) configFields() (persona, creds, knowledge, goals, cadence, approval, budget string, err error) {
	fields := []struct {
		raw json.RawMessage
		dst *string
	}{
		{b.PersonaJSON, &persona},
		{b.CredentialRefsJSON, &creds},
		{b.KnowledgeScopeJSON, &knowledge},
		{b.GoalsJSON, &goals},
		{b.CadenceJSON, &cadence},
		{b.ApprovalPolicyJSON, &approval},
		{b.BudgetJSON, &budget},
	}
	for _, f := range fields {
		val, _, ferr := opsRawObject(f.raw)
		if ferr != nil {
			return "", "", "", "", "", "", "", fmt.Errorf("%w: %v", opsbot.ErrInvalidConfig, ferr)
		}
		*f.dst = val
	}
	return persona, creds, knowledge, goals, cadence, approval, budget, nil
}

func (h *OpsBotHandler) CreateBot(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	owner, uid, ok := h.ownerWritable(c)
	if !ok {
		return
	}
	var body createOpsBotBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	persona, creds, knowledge, goals, cadence, approval, budget, err := body.configFields()
	if err != nil {
		h.respondOpsError(c, err)
		return
	}

	var bot store.OpsBot
	if strings.TrimSpace(body.Role) == opsbot.RoleDirector || strings.TrimSpace(body.Role) == "" && body.PackID == "" {
		bot, err = h.Svc.CreateDirector(c.Request.Context(), opsbot.CreateDirectorInput{
			Owner:              opsbot.OwnerRef{Type: owner.Type, ID: owner.ID},
			DisplayName:        body.DisplayName,
			PersonaJSON:        persona,
			KnowledgeScopeJSON: knowledge,
			GoalsJSON:          goals,
			CadenceJSON:        cadence,
			ApprovalPolicyJSON: approval,
			BudgetJSON:         budget,
			CreatedBy:          uid,
		})
	} else {
		bot, err = h.Svc.CreateAccountBot(c.Request.Context(), opsbot.CreateAccountInput{
			Owner:              opsbot.OwnerRef{Type: owner.Type, ID: owner.ID},
			PackID:             body.PackID,
			PackVersion:        body.PackVersion,
			AccountRef:         body.AccountRef,
			DisplayName:        body.DisplayName,
			PersonaJSON:        persona,
			CredentialRefsJSON: creds,
			KnowledgeScopeJSON: knowledge,
			GoalsJSON:          goals,
			CadenceJSON:        cadence,
			ApprovalPolicyJSON: approval,
			BudgetJSON:         budget,
			DirectorBotID:      body.DirectorBotID,
			CreatedBy:          uid,
		})
	}
	if err != nil {
		h.respondOpsError(c, err)
		return
	}

	if body.ProjectID > 0 && h.Provision != nil {
		if err := h.provisionBot(c, bot, owner, body.ProjectID, uid); err != nil {
			// The bot row exists; provisioning is idempotent, so a retry through
			// the same create path would 409. Report the failure explicitly.
			RespondErrorWithDetails(c, http.StatusInternalServerError, "OPS_PROVISION_FAILED", err.Error(),
				gin.H{"bot_id": bot.ID})
			return
		}
		if refreshed, err := h.Svc.Get(c.Request.Context(), bot.ID); err == nil {
			bot = refreshed
		}
	}

	c.JSON(http.StatusCreated, botDTO(bot))
}

func (h *OpsBotHandler) provisionBot(c *gin.Context, bot store.OpsBot, owner service.OwnerRef, projectID, uid int64) error {
	if bot.Role == opsbot.RoleDirector {
		_, _, err := h.Provision.ProvisionDirector(c.Request.Context(), owner.Type, owner.ID, projectID, bot.ID, bot.DisplayName, uid)
		return err
	}
	_, _, err := h.Provision.ProvisionAccount(c.Request.Context(), owner.Type, owner.ID, projectID, bot.ID, bot.DisplayName, uid)
	return err
}

func (h *OpsBotHandler) GetBot(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	bot, _, ok := h.botForAccess(c, false)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, botDTO(bot))
}

type updateOpsBotBody struct {
	DisplayName        *string         `json:"display_name"`
	PersonaJSON        json.RawMessage `json:"persona_json"`
	CredentialRefsJSON json.RawMessage `json:"credential_refs_json"`
	KnowledgeScopeJSON json.RawMessage `json:"knowledge_scope_json"`
	GoalsJSON          json.RawMessage `json:"goals_json"`
	CadenceJSON        json.RawMessage `json:"cadence_json"`
	ApprovalPolicyJSON json.RawMessage `json:"approval_policy_json"`
	BudgetJSON         json.RawMessage `json:"budget_json"`
	PackVersion        *int64          `json:"pack_version"`
}

func opsPatchString(raw json.RawMessage) (*string, error) {
	val, present, err := opsRawObject(raw)
	if err != nil || !present {
		return nil, err
	}
	return &val, nil
}

func (h *OpsBotHandler) UpdateBot(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	bot, _, ok := h.botForAccess(c, true)
	if !ok {
		return
	}
	var body updateOpsBotBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	patch := opsbot.ConfigPatch{
		DisplayName: body.DisplayName,
		PackVersion: body.PackVersion,
	}
	for _, f := range []struct {
		raw json.RawMessage
		dst **string
	}{
		{body.PersonaJSON, &patch.PersonaJSON},
		{body.CredentialRefsJSON, &patch.CredentialRefsJSON},
		{body.KnowledgeScopeJSON, &patch.KnowledgeScopeJSON},
		{body.GoalsJSON, &patch.GoalsJSON},
		{body.CadenceJSON, &patch.CadenceJSON},
		{body.ApprovalPolicyJSON, &patch.ApprovalPolicyJSON},
		{body.BudgetJSON, &patch.BudgetJSON},
	} {
		val, err := opsPatchString(f.raw)
		if err != nil {
			BadRequest(c, err.Error())
			return
		}
		*f.dst = val
	}

	updated, err := h.Svc.UpdateConfig(c.Request.Context(), bot.ID, patch)
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, botDTO(updated))
}

func (h *OpsBotHandler) setBotStatus(c *gin.Context, status string) {
	if !h.requireReady(c) {
		return
	}
	bot, _, ok := h.botForAccess(c, true)
	if !ok {
		return
	}
	var (
		updated store.OpsBot
		err     error
	)
	switch {
	case status == opsbot.StatusDisabled && h.Risk != nil:
		// The risk kill switch also drains queued replies and pages the
		// operator; the plain service path only cancels approvals.
		if _, err = h.Risk.Kill(c.Request.Context(), bot.ID, "operator"); err == nil {
			updated, err = h.Svc.Get(c.Request.Context(), bot.ID)
		}
	case status == opsbot.StatusActive && h.Risk != nil:
		// A resume must pass the re-login check first.
		updated, err = h.Risk.Resume(c.Request.Context(), bot.ID)
	case status == opsbot.StatusDisabled:
		updated, err = h.Svc.KillSwitch(c.Request.Context(), bot.ID)
	default:
		updated, err = h.Svc.SetStatus(c.Request.Context(), bot.ID, status)
	}
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, botDTO(updated))
}

func (h *OpsBotHandler) PauseBot(c *gin.Context)   { h.setBotStatus(c, opsbot.StatusPaused) }
func (h *OpsBotHandler) ResumeBot(c *gin.Context)  { h.setBotStatus(c, opsbot.StatusActive) }
func (h *OpsBotHandler) DisableBot(c *gin.Context) { h.setBotStatus(c, opsbot.StatusDisabled) }

// --- packs ---

func (h *OpsBotHandler) requirePacks(c *gin.Context) bool {
	if h.Packs == nil {
		RespondError(c, http.StatusServiceUnavailable, "OPS_PACKS_UNAVAILABLE", "pack registry is not configured")
		return false
	}
	return true
}

func (h *OpsBotHandler) ListPacks(c *gin.Context) {
	if !h.requireReady(c) || !h.requirePacks(c) {
		return
	}
	owner, _, ok := h.ownerFromQuery(c)
	if !ok {
		return
	}
	// Built-in seeds are copied on first use; a pack the owner already has is
	// never overwritten.
	if err := packs.InstallBuiltins(h.Packs, owner.Type, owner.ID); err != nil {
		h.respondOpsError(c, err)
		return
	}
	manifests, err := h.Packs.List(owner.Type, owner.ID)
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": manifests, "count": len(manifests)})
}

type packImportBody struct {
	Path string `json:"path"`
}

func (h *OpsBotHandler) ImportPack(c *gin.Context) {
	if !h.requireReady(c) || !h.requirePacks(c) {
		return
	}
	owner, _, ok := h.ownerWritable(c)
	if !ok {
		return
	}
	var body packImportBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	path := strings.TrimSpace(body.Path)
	if path == "" {
		BadRequest(c, "path is required")
		return
	}
	// The caller names a local archive; only a regular file is a legitimate
	// input, and the size cap keeps a hostile archive from exhausting memory.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		BadRequest(c, "path must name a readable regular file")
		return
	}
	if info.Size() > 64<<20 {
		BadRequest(c, "pack archive is too large (limit 64 MiB)")
		return
	}
	if err := h.Packs.Import(owner.Type, owner.ID, path); err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"imported": true})
}

func (h *OpsBotHandler) ExportPack(c *gin.Context) {
	if !h.requireReady(c) || !h.requirePacks(c) {
		return
	}
	owner, _, ok := h.ownerFromQuery(c)
	if !ok {
		return
	}
	packID := c.Param("packID")
	// packID becomes a filename below; require a single safe path segment before
	// it reaches filepath.Join (the registry validates it too, but never rely on
	// a downstream check for path construction).
	if !safeFileSegment(packID) {
		BadRequest(c, "bad pack id")
		return
	}
	version, err := strconv.Atoi(c.Param("version"))
	if err != nil || version <= 0 {
		BadRequest(c, "bad version")
		return
	}
	tmpDir, err := os.MkdirTemp("", "ops-pack-export-*")
	if err != nil {
		InternalError(c, err)
		return
	}
	defer os.RemoveAll(tmpDir)
	zipPath := filepath.Join(tmpDir, packID+".zip")
	if err := h.Packs.Export(owner.Type, owner.ID, packID, version, zipPath); err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.FileAttachment(zipPath, fmt.Sprintf("%s-v%d.zip", packID, version))
}

type packGenerateBody struct {
	Manifest packs.Manifest    `json:"manifest"`
	Files    map[string]string `json:"files"`
}

// GeneratePack writes a new draft pack version from an explicit manifest. The
// conversational generator (Task 30) fills the same shape; until it lands this
// is the deterministic "write what I have" path.
func (h *OpsBotHandler) GeneratePack(c *gin.Context) {
	if !h.requireReady(c) || !h.requirePacks(c) {
		return
	}
	owner, _, ok := h.ownerWritable(c)
	if !ok {
		return
	}
	var body packGenerateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if body.Manifest.ID == "" || body.Manifest.Version <= 0 {
		BadRequest(c, "manifest.id and manifest.version are required")
		return
	}
	if body.Manifest.Status == "" {
		body.Manifest.Status = "draft"
	}
	files := make(map[string][]byte, len(body.Files))
	for name, content := range body.Files {
		files[name] = []byte(content)
	}
	root, err := h.Packs.Save(owner.Type, owner.ID, body.Manifest, files)
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"saved": true, "root": root, "manifest": body.Manifest})
}

type packVersionBody struct {
	PackID  string `json:"pack_id"`
	Version int    `json:"version"`
}

func (h *OpsBotHandler) packVersionFrom(c *gin.Context) (packVersionBody, bool) {
	var body packVersionBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return packVersionBody{}, false
	}
	if strings.TrimSpace(body.PackID) == "" || body.Version <= 0 {
		BadRequest(c, "pack_id and version are required")
		return packVersionBody{}, false
	}
	return body, true
}

func (h *OpsBotHandler) ValidatePack(c *gin.Context) {
	if !h.requireReady(c) || !h.requirePacks(c) {
		return
	}
	owner, _, ok := h.ownerFromQuery(c)
	if !ok {
		return
	}
	body, ok := h.packVersionFrom(c)
	if !ok {
		return
	}
	m, err := h.Packs.Load(owner.Type, owner.ID, body.PackID, body.Version)
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"valid": true, "manifest": m})
}

// TestPack is a dry-run of the pack: it loads (which validates the whole
// version directory) and reports the capability bindings that would be used.
func (h *OpsBotHandler) TestPack(c *gin.Context) {
	if !h.requireReady(c) || !h.requirePacks(c) {
		return
	}
	owner, _, ok := h.ownerFromQuery(c)
	if !ok {
		return
	}
	body, ok := h.packVersionFrom(c)
	if !ok {
		return
	}
	m, err := h.Packs.Load(owner.Type, owner.ID, body.PackID, body.Version)
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":           true,
		"manifest":     m,
		"capabilities": m.Capabilities,
		"bindings":     m.Bindings,
	})
}

func (h *OpsBotHandler) PublishPack(c *gin.Context) {
	if !h.requireReady(c) || !h.requirePacks(c) {
		return
	}
	owner, _, ok := h.ownerWritable(c)
	if !ok {
		return
	}
	body, ok := h.packVersionFrom(c)
	if !ok {
		return
	}
	if err := h.Packs.Publish(owner.Type, owner.ID, body.PackID, body.Version); err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"published": true, "pack_id": body.PackID, "version": body.Version})
}

// --- approvals ---

func (h *OpsBotHandler) ListApprovals(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	if h.Approval == nil {
		RespondError(c, http.StatusServiceUnavailable, "OPS_APPROVALS_UNAVAILABLE", "approval service is not configured")
		return
	}
	status := strings.TrimSpace(c.DefaultQuery("status", opsbot.ApprovalStatusPending))
	if status != opsbot.ApprovalStatusPending {
		BadRequest(c, "only status=pending is supported")
		return
	}
	owner, _, ok := h.ownerFromQuery(c)
	if !ok {
		return
	}
	items, err := h.Approval.ListPending(c.Request.Context(), owner.Type, owner.ID)
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	out := make([]opsApprovalDTO, 0, len(items))
	for _, a := range items {
		out = append(out, approvalDTO(a))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "count": len(out)})
}

func (h *OpsBotHandler) approvalForWrite(c *gin.Context) (int64, int64, bool) {
	if h.Approval == nil {
		RespondError(c, http.StatusServiceUnavailable, "OPS_APPROVALS_UNAVAILABLE", "approval service is not configured")
		return 0, 0, false
	}
	if _, ok := h.callerUserID(c); !ok {
		return 0, 0, false
	}
	approvalID, err := parseIDParamNamed(c, "id")
	if err != nil {
		BadRequest(c, "bad id")
		return 0, 0, false
	}
	req, err := h.Approval.Get(c.Request.Context(), approvalID)
	if err != nil {
		h.respondOpsError(c, err)
		return 0, 0, false
	}
	ws, err := h.Queries.GetWorkspace(c.Request.Context(), req.WorkspaceID)
	if err != nil {
		h.respondOpsError(c, err)
		return 0, 0, false
	}
	// Decide() takes only the approval id and does no owner check, so the
	// explicit gate runs here before the state machine is touched.
	ownerUID, ok := h.opsAuthorizeOwner(c, ws.OwnerType, ws.OwnerID, true)
	if !ok {
		return 0, 0, false
	}
	return approvalID, ownerUID, true
}

type decideApprovalBody struct {
	Reason   string            `json:"reason"`
	Answers  map[string]string `json:"answers"`
	ProxyFor int64             `json:"proxy_for"`
}

func (h *OpsBotHandler) decideApproval(c *gin.Context, action string) {
	if !h.requireReady(c) {
		return
	}
	approvalID, uid, ok := h.approvalForWrite(c)
	if !ok {
		return
	}
	var body decideApprovalBody
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			BadRequest(c, err.Error())
			return
		}
	}
	updated, err := h.Approval.Decide(c.Request.Context(), approvalID, opsbot.Decision{
		OperatorID: uid,
		Action:     action,
		Reason:     body.Reason,
		ProxyFor:   body.ProxyFor,
		Answers:    body.Answers,
	})
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, approvalDTO(updated))
}

func (h *OpsBotHandler) ApproveApproval(c *gin.Context) { h.decideApproval(c, opsbot.ActionApproved) }
func (h *OpsBotHandler) RejectApproval(c *gin.Context)  { h.decideApproval(c, opsbot.ActionRejected) }
func (h *OpsBotHandler) ModifyApproval(c *gin.Context)  { h.decideApproval(c, opsbot.ActionModified) }

// --- budget ---

func (h *OpsBotHandler) GetBudget(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	if h.Budget == nil {
		RespondError(c, http.StatusServiceUnavailable, "OPS_BUDGET_UNAVAILABLE", "budget service is not configured")
		return
	}
	bot, _, ok := h.botForAccess(c, false)
	if !ok {
		return
	}
	usage, err := h.Budget.Usage(c.Request.Context(), bot.ID, bot.EpicIssueID.Int64)
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, usage)
}

type setBudgetBody struct {
	MonthlyLimit float64 `json:"monthly_limit"`
	Currency     string  `json:"currency"`
}

// SetBudget writes the monthly limit. A limit already exceeded by this month's
// spend is rejected with 409: the account would be immediately over budget.
func (h *OpsBotHandler) SetBudget(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	if h.Budget == nil {
		RespondError(c, http.StatusServiceUnavailable, "OPS_BUDGET_UNAVAILABLE", "budget service is not configured")
		return
	}
	bot, _, ok := h.botForAccess(c, true)
	if !ok {
		return
	}
	var body setBudgetBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if body.MonthlyLimit < 0 {
		BadRequest(c, "monthly_limit must be >= 0")
		return
	}
	if body.MonthlyLimit > 0 {
		spent, err := h.Budget.Spent(c.Request.Context(), bot.EpicIssueID.Int64)
		if err != nil {
			h.respondOpsError(c, err)
			return
		}
		if spent > body.MonthlyLimit {
			h.respondOpsError(c, fmt.Errorf("%w: spent %.2f exceeds the new limit %.2f",
				opsbot.ErrBudgetExceeded, spent, body.MonthlyLimit))
			return
		}
	}
	if err := h.Budget.SetLimit(c.Request.Context(), bot.ID, body.MonthlyLimit, body.Currency); err != nil {
		h.respondOpsError(c, err)
		return
	}
	usage, err := h.Budget.Usage(c.Request.Context(), bot.ID, bot.EpicIssueID.Int64)
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, usage)
}

// --- metrics / engagements / calendar ---

func (h *OpsBotHandler) GetMetrics(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	bot, _, ok := h.botForAccess(c, false)
	if !ok {
		return
	}
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -29)
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			BadRequest(c, "from must be YYYY-MM-DD")
			return
		}
		from = parsed
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			BadRequest(c, "to must be YYYY-MM-DD")
			return
		}
		to = parsed
	}
	fromDay, toDay := from.Format("2006-01-02"), to.Format("2006-01-02")
	if fromDay > toDay {
		BadRequest(c, "from must not be after to")
		return
	}
	rows, err := h.Queries.ListOpsMetricsDaily(c.Request.Context(), store.ListOpsMetricsDailyParams{
		BotID: bot.ID, Date: fromDay, Date_2: toDay,
	})
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	items := make([]opsMetricsItemDTO, 0, len(rows))
	for _, r := range rows {
		items = append(items, opsMetricsItemDTO{Date: r.Date, Metrics: opsJSON(r.MetricsJson), Source: r.Source})
	}
	c.JSON(http.StatusOK, gin.H{"bot_id": bot.ID, "from": fromDay, "to": toDay, "items": items})
}

func (h *OpsBotHandler) ListEngagements(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	bot, _, ok := h.botForAccess(c, false)
	if !ok {
		return
	}
	status := strings.TrimSpace(c.DefaultQuery("status", "new"))
	limit := int64(50)
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			BadRequest(c, "limit must be a positive integer")
			return
		}
		if n > 200 {
			n = 200
		}
		limit = n
	}
	rows, err := h.Queries.ListOpsEngagementsByStatus(c.Request.Context(), store.ListOpsEngagementsByStatusParams{
		BotID: bot.ID, Status: status, Limit: limit,
	})
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "count": len(rows)})
}

// GetCalendar returns the bot's cadence plus the scheduled work under its
// control Epic. It is the read model the calendar widget and the
// ops_calendar tool share.
func (h *OpsBotHandler) GetCalendar(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	bot, _, ok := h.botForAccess(c, false)
	if !ok {
		return
	}
	items := []opsCalendarItemDTO{}
	if bot.EpicIssueID.Valid {
		children, err := h.Queries.ListIssuesByParent(c.Request.Context(), sql.NullInt64{Int64: bot.EpicIssueID.Int64, Valid: true})
		if err != nil {
			h.respondOpsError(c, err)
			return
		}
		for _, issue := range children {
			item := opsCalendarItemDTO{
				IssueID:   issue.ID,
				Title:     issue.Title,
				IssueType: issue.IssueType,
				Status:    issue.LifecycleStatus,
			}
			if issue.DueDate.Valid {
				item.DueDate = issue.DueDate.String
			}
			if issue.StartDate.Valid {
				item.StartDate = issue.StartDate.String
			}
			items = append(items, item)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"bot_id":  bot.ID,
		"cadence": opsJSON(bot.CadenceJson),
		"items":   items,
		"count":   len(items),
	})
}

// --- publish / engagement reply / browser login / credential link ---

type publishBody struct {
	IssueID        int64  `json:"issue_id"`
	ContentVersion int64  `json:"content_version"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Publish records the publish intent idempotently. Actual dispatch is the
// publish orchestrator's job (Task 18); this endpoint is the durable record the
// orchestrator and the receipt/retry logic key on.
func (h *OpsBotHandler) Publish(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	bot, _, ok := h.botForAccess(c, true)
	if !ok {
		return
	}
	var body publishBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if body.IssueID <= 0 {
		BadRequest(c, "issue_id is required")
		return
	}
	if !h.authorizeIssueOwner(c, bot, body.IssueID) {
		return
	}
	key := strings.TrimSpace(body.IdempotencyKey)
	if key == "" {
		BadRequest(c, "idempotency_key is required")
		return
	}
	if body.ContentVersion <= 0 {
		body.ContentVersion = 1
	}
	existing, err := h.Queries.GetOpsPublishRecord(c.Request.Context(), store.GetOpsPublishRecordParams{
		BotID: bot.ID, IdempotencyKey: key,
	})
	if err == nil {
		c.JSON(http.StatusOK, publishDTO(existing))
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		h.respondOpsError(c, err)
		return
	}
	row, err := h.Queries.CreateOpsPublishRecord(c.Request.Context(), store.CreateOpsPublishRecordParams{
		IssueID:        body.IssueID,
		BotID:          bot.ID,
		PackID:         bot.PackID,
		AccountRef:     bot.AccountRef,
		ContentVersion: body.ContentVersion,
		IdempotencyKey: key,
		Status:         "pending",
	})
	if err != nil {
		// A concurrent request won the unique insert; return that record.
		if again, gerr := h.Queries.GetOpsPublishRecord(c.Request.Context(), store.GetOpsPublishRecordParams{
			BotID: bot.ID, IdempotencyKey: key,
		}); gerr == nil {
			c.JSON(http.StatusOK, publishDTO(again))
			return
		}
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusCreated, publishDTO(row))
}

type engagementReplyBody struct {
	ReplyText         string `json:"reply_text"`
	ApprovalRequestID int64  `json:"approval_request_id"`
}

// ReplyEngagement records an approved reply and moves the engagement into the
// dispatch queue. The channel send itself belongs to the engagement service
// (Task 23); status stays 'approved' until that dispatcher reports a receipt.
func (h *OpsBotHandler) ReplyEngagement(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	engagementID, err := parseIDParamNamed(c, "id")
	if err != nil {
		BadRequest(c, "bad id")
		return
	}
	cur, err := h.Queries.GetOpsEngagement(c.Request.Context(), engagementID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			RespondError(c, http.StatusNotFound, "OPS_ENGAGEMENT_NOT_FOUND", "engagement not found")
			return
		}
		h.respondOpsError(c, err)
		return
	}
	_, uid, ok := h.botForID(c, cur.BotID, true)
	if !ok {
		return
	}
	var body engagementReplyBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	reply := strings.TrimSpace(body.ReplyText)
	if reply == "" {
		reply = strings.TrimSpace(cur.SuggestedReply)
	}
	if reply == "" {
		BadRequest(c, "reply_text is required")
		return
	}
	next := cur.Status
	switch next {
	case "new", "classified", "suggested":
		next = "approved"
	}
	updated, err := h.Queries.UpdateOpsEngagementStatus(c.Request.Context(), store.UpdateOpsEngagementStatusParams{
		Status:            next,
		Category:          cur.Category,
		RiskLevel:         cur.RiskLevel,
		SuggestedReply:    cur.SuggestedReply,
		ApprovalRequestID: sql.NullInt64{Int64: body.ApprovalRequestID, Valid: body.ApprovalRequestID > 0},
		ReplyText:         reply,
		Receipt:           cur.Receipt,
		DecidedBy:         sql.NullInt64{Int64: uid, Valid: uid != 0},
		DecidedAt:         sql.NullTime{Time: time.Now().UTC(), Valid: true},
		ID:                cur.ID,
		Status_2:          cur.Status,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			RespondError(c, http.StatusConflict, "OPS_ENGAGEMENT_STATE_CHANGED", "engagement status changed concurrently")
			return
		}
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

type browserLoginBody struct {
	Backend string `json:"backend"`
}

// BrowserLogin registers (or returns) the bot's browser profile for a backend.
// The actual login happens in the browser worker; this records where the
// profile lives so the worker and the operator agree on one directory.
func (h *OpsBotHandler) BrowserLogin(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	bot, _, ok := h.botForAccess(c, true)
	if !ok {
		return
	}
	var body browserLoginBody
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			BadRequest(c, err.Error())
			return
		}
	}
	backend := strings.TrimSpace(body.Backend)
	if backend == "" {
		backend = "local"
	}
	// backend becomes a path segment below; a free-form value would let a caller
	// escape the profile root via ../ traversal.
	if backend != "local" && backend != "cloud" {
		BadRequest(c, "backend must be local or cloud")
		return
	}
	profileDir := filepath.Join(h.DataDir, "ops", "browser", strconv.FormatInt(bot.ID, 10), backend)
	session, err := h.Queries.UpsertOpsBrowserSession(c.Request.Context(), store.UpsertOpsBrowserSessionParams{
		BotID: bot.ID, Backend: backend, ProfileDir: profileDir,
	})
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"session":   session,
		"backend":   backend,
		"next_step": "run the browser worker login against this profile directory",
	})
}

type credentialLinkBody struct {
	ProjectID int64    `json:"project_id"`
	Title     string   `json:"title"`
	Fields    []string `json:"fields"`
	AuthMode  string   `json:"auth_mode"`
}

// CredentialLink mints a one-time secure link for the bot's platform
// credential. The secret never enters the conversation: the user fills it in on
// the standalone page.
func (h *OpsBotHandler) CredentialLink(c *gin.Context) {
	if !h.requireReady(c) {
		return
	}
	if h.CredLinks == nil {
		RespondError(c, http.StatusServiceUnavailable, "OPS_CREDENTIAL_LINK_UNAVAILABLE", "credential link service is not configured")
		return
	}
	bot, uid, ok := h.botForAccess(c, true)
	if !ok {
		return
	}
	var body credentialLinkBody
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			BadRequest(c, err.Error())
			return
		}
	}
	projectID := body.ProjectID
	if projectID <= 0 {
		var err error
		projectID, err = h.projectForBot(c.Request.Context(), bot)
		if err != nil {
			BadRequest(c, "project_id is required for a bot without a control Epic")
			return
		}
	} else if !h.authorizeProjectOwner(c, bot, projectID) {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = fmt.Sprintf("%s 凭据录入 / %s credential", bot.DisplayName, bot.PackID)
	}
	fields := body.Fields
	if len(fields) == 0 {
		fields = []string{"app_id", "app_secret"}
	}
	token, err := h.CredLinks.Issue(c.Request.Context(), projectID, uid,
		service.CredentialKindProviderCredential, title, fields,
		map[string]any{
			"provider":  bot.PackID,
			"alias":     bot.AccountRef,
			"auth_mode": body.AuthMode,
		})
	if err != nil {
		h.respondOpsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"url":     "/api/credential-onboarding/" + token,
		"token":   token,
		"expires": "15m",
	})
}

// authorizeProjectOwner rejects a request whose project belongs to a different
// owner than the bot. Handlers that accept a client-supplied project id must
// gate it here: a foreign id is an IDOR even though the bot itself is owned.
func (h *OpsBotHandler) authorizeProjectOwner(c *gin.Context, bot store.OpsBot, projectID int64) bool {
	project, err := h.Queries.GetProject(c.Request.Context(), projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			RespondError(c, http.StatusNotFound, "OPS_PROJECT_NOT_FOUND", "project not found")
			return false
		}
		h.respondOpsError(c, err)
		return false
	}
	if project.OwnerType != bot.OwnerType || project.OwnerID != bot.OwnerID {
		RespondError(c, http.StatusForbidden, "OPS_FORBIDDEN", "project does not belong to this bot's owner")
		return false
	}
	return true
}

// authorizeIssueOwner rejects a request whose issue belongs to a different
// owner than the bot.
func (h *OpsBotHandler) authorizeIssueOwner(c *gin.Context, bot store.OpsBot, issueID int64) bool {
	ctx := c.Request.Context()
	issue, err := h.Queries.GetIssue(ctx, issueID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			RespondError(c, http.StatusNotFound, "OPS_ISSUE_NOT_FOUND", "issue not found")
			return false
		}
		h.respondOpsError(c, err)
		return false
	}
	column, err := h.Queries.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		h.respondOpsError(c, err)
		return false
	}
	return h.authorizeProjectOwner(c, bot, column.ProjectID)
}

// projectForBot resolves the project that owns the bot's control Epic.
func (h *OpsBotHandler) projectForBot(ctx context.Context, bot store.OpsBot) (int64, error) {
	if !bot.EpicIssueID.Valid {
		return 0, fmt.Errorf("bot %d has no control epic", bot.ID)
	}
	issue, err := h.Queries.GetIssue(ctx, bot.EpicIssueID.Int64)
	if err != nil {
		return 0, err
	}
	column, err := h.Queries.GetColumn(ctx, issue.ColumnID)
	if err != nil {
		return 0, err
	}
	return column.ProjectID, nil
}
