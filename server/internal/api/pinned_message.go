package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// PinnedMessageHandler serves the chat-side REST endpoints that back the
// pin-message panel:
//   - List pinned messages for a workspace
//   - Pin a message (idempotent upsert by (workspace_id, message_id))
//   - Unpin a message
//
// Pins are workspace-scoped; ownership is inherited via the workspace, so
// every entry point gates on Authz.CanAccessWorkspace (same pattern as
// PermissionHandler). Spec:
// docs/superpowers/specs/2026-06-04-chat-message-pin-design.md
type PinnedMessageHandler struct {
	DB    *store.DB
	Authz *service.Authz
}

// pinnedMessageDTO is the wire shape for a single pinned message.
type pinnedMessageDTO struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	MessageID   string `json:"message_id"`
	Role        string `json:"role"`
	Preview     string `json:"preview"`
	CreatedAt   int64  `json:"created_at"`
}

func toPinnedMessageDTO(r store.PinnedMessage) pinnedMessageDTO {
	return pinnedMessageDTO{
		ID:          r.ID,
		WorkspaceID: r.WorkspaceID,
		MessageID:   r.MessageID,
		Role:        r.Role,
		Preview:     r.Preview,
		CreatedAt:   r.CreatedAt.UnixMilli(),
	}
}

// previewMaxLen bounds the stored preview so a giant message doesn't bloat the
// row; the panel only needs a short snippet.
const previewMaxLen = 280

// authorizeWorkspace parses the :id param and gates access, writing the
// appropriate error response on failure. Returns (wsID, ok).
func (h *PinnedMessageHandler) authorizeWorkspace(c *gin.Context) (int64, bool) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace id")
		return 0, false
	}
	userID := c.GetInt64("auth_user_id")
	if h.Authz != nil && userID > 0 {
		if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); aerr != nil {
			writeAuthzError(c, aerr)
			return 0, false
		}
	}
	return wsID, true
}

// List handles GET /api/workspaces/:id/pinned-messages
func (h *PinnedMessageHandler) List(c *gin.Context) {
	wsID, ok := h.authorizeWorkspace(c)
	if !ok {
		return
	}
	rows, err := store.New(h.DB).ListPinnedMessages(c.Request.Context(), wsID)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]pinnedMessageDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toPinnedMessageDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{"pins": out})
}

type createPinBody struct {
	MessageID string `json:"message_id" binding:"required"`
	Role      string `json:"role"`
	Preview   string `json:"preview"`
}

// Create handles POST /api/workspaces/:id/pinned-messages
func (h *PinnedMessageHandler) Create(c *gin.Context) {
	wsID, ok := h.authorizeWorkspace(c)
	if !ok {
		return
	}
	var b createPinBody
	if err := c.ShouldBindJSON(&b); err != nil {
		BadRequest(c, err.Error())
		return
	}
	preview := b.Preview
	if len(preview) > previewMaxLen {
		preview = preview[:previewMaxLen]
	}
	row, err := store.New(h.DB).CreatePinnedMessage(c.Request.Context(), store.CreatePinnedMessageParams{
		WorkspaceID: wsID,
		MessageID:   b.MessageID,
		Role:        b.Role,
		Preview:     preview,
	})
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"pin": toPinnedMessageDTO(row)})
}

// Delete handles DELETE /api/workspaces/:id/pinned-messages/:pinId
func (h *PinnedMessageHandler) Delete(c *gin.Context) {
	wsID, ok := h.authorizeWorkspace(c)
	if !ok {
		return
	}
	pinID, err := strconv.ParseInt(c.Param("pinId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid pin id")
		return
	}
	// Scoped by workspace_id so a pin id from another workspace can't be
	// deleted even though the caller passed an authorized :id.
	if err := store.New(h.DB).DeletePinnedMessage(c.Request.Context(), store.DeletePinnedMessageParams{
		ID:          pinID,
		WorkspaceID: wsID,
	}); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
