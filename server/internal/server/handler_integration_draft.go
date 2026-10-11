// handler_integration_draft.go — HTTP handlers for the integration draft
// pipeline (Epic #726 / #734).
//
//	draft -> lint -> impact confirmation -> publish -> verify / rollback
//
// Two surfaces, deliberately different:
//
//   - The SPA surface (/api/me/integration-drafts) is the HUMAN one. It may
//     confirm a draft and publish it.
//   - The agent surface (/mcp/integration-drafts) may create and read drafts,
//     and nothing else. Publish/rollback are registered there too, so the
//     refusal is a real 403 with a reason the agent can act on rather than a
//     bare 404 — but the handler always marks the request as agent-originated,
//     and the service refuses.
//
// The origin is hardcoded per route group, never read from the request body, so
// an agent cannot claim to be the user's UI.
package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// integrationDraftBody is the create payload for both lines.
type integrationDraftBody struct {
	Kind       string `json:"kind"`
	TargetName string `json:"target_name"`
	OwnerType  string `json:"owner_type"`
	OwnerID    int64  `json:"owner_id"`

	Provider   *service.ProviderDraftPayload   `json:"provider"`
	DataSource *service.DataSourceDraftPayload `json:"data_source"`

	// Provenance of the source document, when the draft came from one. DocOps
	// must come from the document's STRUCTURED sections (OpenAPI paths), never
	// from its prose.
	Origin   *service.UntrustedOrigin   `json:"origin"`
	Advisory []service.QuarantinedChunk `json:"advisory"`
	DocOps   []service.OperationKey     `json:"doc_operations"`
	DocHost  string                     `json:"doc_host"`
}

type integrationDraftConfirmBody struct {
	Decision           string   `json:"decision"` // approve (default) | reject
	Acknowledged       []string `json:"acknowledged"`
	SecondConfirmation bool     `json:"second_confirmation"`
}

type integrationRollbackBody struct {
	Confirmed    bool     `json:"confirmed"`
	Acknowledged []string `json:"acknowledged"`
}

// --- SPA (human) surface --------------------------------------------------

func (s *Server) draftCreate(c *gin.Context) {
	s.handleDraftCreate(c, false)
}

func (s *Server) draftList(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid == 0 {
		writeDraftError(c, service.ErrDraftNotFound)
		return
	}
	ownerType, ownerID, ok := s.resolveDraftOwner(c, uid, c.Query("owner_type"), queryInt64(c, "owner_id"))
	if !ok {
		return
	}
	views, err := s.integrationDraftSvc.ListDrafts(c.Request.Context(), ownerType, ownerID, queryInt64(c, "limit"))
	if err != nil {
		writeDraftError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": views})
}

func (s *Server) draftGet(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid == 0 {
		writeDraftError(c, service.ErrDraftNotFound)
		return
	}
	id, ok := draftIDParam(c)
	if !ok {
		return
	}
	ownerType, ownerID, ok := s.resolveDraftOwner(c, uid, c.Query("owner_type"), queryInt64(c, "owner_id"))
	if !ok {
		return
	}
	view, err := s.integrationDraftSvc.GetDraft(c.Request.Context(), id, ownerType, ownerID)
	if err != nil {
		writeDraftError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": view})
}

func (s *Server) draftConfirm(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid == 0 {
		writeDraftError(c, service.ErrDraftNotFound)
		return
	}
	id, ok := draftIDParam(c)
	if !ok {
		return
	}
	var body integrationDraftConfirmBody
	_ = c.ShouldBindJSON(&body)
	ownerType, ownerID, ok := s.resolveDraftOwner(c, uid, c.Query("owner_type"), queryInt64(c, "owner_id"))
	if !ok {
		return
	}
	view, err := s.integrationDraftSvc.ConfirmDraft(c.Request.Context(), service.ConfirmImpactInput{
		DraftID:            id,
		OwnerType:          ownerType,
		OwnerID:            ownerID,
		AckKeys:            body.Acknowledged,
		SecondConfirmation: body.SecondConfirmation,
		Reject:             body.Decision == "reject",
	})
	if err != nil {
		writeDraftError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": view})
}

func (s *Server) draftPublish(c *gin.Context) {
	s.handleDraftPublish(c, false)
}

func (s *Server) draftDelete(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid == 0 {
		writeDraftError(c, service.ErrDraftNotFound)
		return
	}
	id, ok := draftIDParam(c)
	if !ok {
		return
	}
	ownerType, ownerID, ok := s.resolveDraftOwner(c, uid, c.Query("owner_type"), queryInt64(c, "owner_id"))
	if !ok {
		return
	}
	if err := s.integrationDraftSvc.DeleteDraft(c.Request.Context(), id, ownerType, ownerID); err != nil {
		writeDraftError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"deleted": true}})
}

func (s *Server) draftVersions(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid == 0 {
		writeDraftError(c, service.ErrDraftNotFound)
		return
	}
	ownerType, ownerID, ok := s.resolveDraftOwner(c, uid, c.Query("owner_type"), queryInt64(c, "owner_id"))
	if !ok {
		return
	}
	kind := c.Query("kind")
	target := c.Query("target_name")
	if kind == "" || target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "kind and target_name are required"}})
		return
	}
	versions, err := s.integrationDraftSvc.ListVersions(c.Request.Context(), ownerType, ownerID, kind, target, queryInt64(c, "limit"))
	if err != nil {
		writeDraftError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": versions})
}

func (s *Server) draftRollback(c *gin.Context) {
	s.handleDraftRollback(c, false)
}

// --- agent (MCP) surface --------------------------------------------------

// draftCreateAgent creates a draft on behalf of an agent. The request is marked
// agent-originated, which is what later bars it from being published by an
// agent — the draft a human publishes is exactly this one.
func (s *Server) draftCreateAgent(c *gin.Context) { s.handleDraftCreate(c, true) }

// draftGetAgent lets an agent read back a draft it created (its lint verdict and
// the system-rendered card), so it can explain to the user what needs approving.
func (s *Server) draftGetAgent(c *gin.Context) { s.draftGet(c) }

// draftPublishAgent exists so an agent's attempt to publish gets a clear refusal
// instead of a 404. It always marks the request agent-originated.
func (s *Server) draftPublishAgent(c *gin.Context) { s.handleDraftPublish(c, true) }

// draftRollbackAgent is the rollback counterpart of draftPublishAgent.
func (s *Server) draftRollbackAgent(c *gin.Context) { s.handleDraftRollback(c, true) }

// --- shared handlers ------------------------------------------------------

func (s *Server) handleDraftCreate(c *gin.Context, fromAgent bool) {
	uid := c.GetInt64("auth_user_id")
	if uid == 0 {
		writeDraftError(c, service.ErrDraftNotFound)
		return
	}
	var body integrationDraftBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()}})
		return
	}
	ownerType, ownerID := body.OwnerType, body.OwnerID
	// An agent inside a workspace drafts for that workspace's owner — the same
	// rule data-source creation follows, so the draft lands where the agent's
	// project binding can actually use it. The owner is read from the workspace
	// row, never taken from the request.
	if fromAgent && ownerType == "" {
		if wsID := c.GetInt64("mcp_workspace_id"); wsID > 0 {
			if ws, err := s.queries.GetWorkspace(c.Request.Context(), wsID); err == nil {
				ownerType, ownerID = ws.OwnerType, ws.OwnerID
			}
		}
	}
	resolvedType, resolvedID, ok := s.resolveDraftOwner(c, uid, ownerType, ownerID)
	if !ok {
		return
	}
	ownerType, ownerID = resolvedType, resolvedID
	in := service.CreateDraftInput{
		OwnerType:     ownerType,
		OwnerID:       ownerID,
		CreatedBy:     uid,
		Kind:          body.Kind,
		TargetName:    body.TargetName,
		AgentAuthored: fromAgent,
		Provider:      body.Provider,
		DataSource:    body.DataSource,
		Advisory:      body.Advisory,
		DocOps:        body.DocOps,
		DocHost:       body.DocHost,
	}
	if body.Origin != nil {
		in.Origin = *body.Origin
	} else {
		// Content without a recognised origin is still untrusted: default to the
		// weakest provenance rather than silently implying trust.
		in.Origin = service.NewUntrustedOrigin(service.UntrustedKindNote, "")
	}
	view, err := s.integrationDraftSvc.CreateDraft(c.Request.Context(), in)
	if err != nil {
		writeDraftError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": view})
}

func (s *Server) handleDraftPublish(c *gin.Context, fromAgent bool) {
	uid := c.GetInt64("auth_user_id")
	if uid == 0 {
		writeDraftError(c, service.ErrDraftNotFound)
		return
	}
	id, ok := draftIDParam(c)
	if !ok {
		return
	}
	var body struct {
		OwnerType string `json:"owner_type"`
		OwnerID   int64  `json:"owner_id"`
	}
	_ = c.ShouldBindJSON(&body)
	ownerType, ownerID, ok := s.resolveDraftOwner(c, uid, body.OwnerType, body.OwnerID)
	if !ok {
		return
	}
	res, err := s.integrationDraftSvc.PublishDraft(c.Request.Context(), service.PublishDraftInput{
		DraftID:   id,
		OwnerType: ownerType,
		OwnerID:   ownerID,
		ByUser:    uid,
		FromAgent: fromAgent,
	})
	if err != nil {
		writeDraftError(c, err)
		return
	}
	// A publish whose probe failed is a conflict (the caller must act), not a
	// server error.
	status := http.StatusOK
	if !res.Published {
		status = http.StatusConflict
	}
	c.JSON(status, gin.H{"data": res})
}

func (s *Server) handleDraftRollback(c *gin.Context, fromAgent bool) {
	uid := c.GetInt64("auth_user_id")
	if uid == 0 {
		writeDraftError(c, service.ErrDraftNotFound)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid version id"}})
		return
	}
	var body integrationRollbackBody
	_ = c.ShouldBindJSON(&body)
	ownerType, ownerID, ok := s.resolveDraftOwner(c, uid, c.Query("owner_type"), queryInt64(c, "owner_id"))
	if !ok {
		return
	}
	res, err := s.integrationDraftSvc.Rollback(c.Request.Context(), service.RollbackInput{
		VersionID: id,
		OwnerType: ownerType,
		OwnerID:   ownerID,
		ByUser:    uid,
		FromAgent: fromAgent,
		Confirmed: body.Confirmed,
		AckKeys:   body.Acknowledged,
	})
	if err != nil {
		writeDraftError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

// resolveDraftOwner validates the owner fields against the caller's accessible
// set. Omitted -> the caller's personal owner; org requires membership.
func (s *Server) resolveDraftOwner(c *gin.Context, uid int64, ownerType string, ownerID int64) (string, int64, bool) {
	switch ownerType {
	case "", "user":
		if ownerID != 0 && ownerID != uid {
			c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN", "message": "cannot act on another user's integrations"}})
			return "", 0, false
		}
		return "user", uid, true
	case "org":
		if ownerID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "owner_id is required for an org owner"}})
			return "", 0, false
		}
		if s.authzSvc != nil {
			owners, err := s.authzSvc.Accessible(c.Request.Context(), uid)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
				return "", 0, false
			}
			for _, id := range owners.OrgIDs {
				if id == ownerID {
					return "org", ownerID, true
				}
			}
		}
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN", "message": "not a member of that organization"}})
		return "", 0, false
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "owner_type must be user or org"}})
		return "", 0, false
	}
}

func draftIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": "invalid draft id"}})
		return 0, false
	}
	return id, true
}

func queryInt64(c *gin.Context, key string) int64 {
	v, err := strconv.ParseInt(c.Query(key), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// writeDraftError maps the pipeline's sentinel errors onto HTTP statuses.
func writeDraftError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAgentCannotAuthorize):
		// 403, not 404: the agent should learn the action is reserved for the
		// user, not that the route is missing.
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
			"code":    "AGENT_CANNOT_AUTHORIZE",
			"message": err.Error(),
		}})
	case errors.Is(err, service.ErrDraftNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "NOT_FOUND", "message": err.Error()}})
	case errors.Is(err, service.ErrDraftStale):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "DRAFT_STALE", "message": err.Error()}})
	case errors.Is(err, service.ErrDraftNotConfirmed),
		errors.Is(err, service.ErrDraftAckIncomplete),
		errors.Is(err, service.ErrDraftInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "BAD_REQUEST", "message": err.Error()}})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL_ERROR", "message": err.Error()}})
	}
}
