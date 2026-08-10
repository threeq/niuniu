package api

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// KBHandler serves the knowledge-base MCP surface (kb_search / kb_list) consumed
// by niuniu-mcp over /mcp/workspaces/:id/kb/*. Like the memory MCP routes it is
// keyed by workspace id: owner + project are derived server-side from the
// workspace, so an agent can only reach KBs its workspace's owner owns AND that
// are bound to the workspace's project (tenant isolation + binding visibility).
type KBHandler struct {
	svc *service.KBService
	db  *sql.DB
}

// NewKBHandler constructs a KBHandler.
func NewKBHandler(svc *service.KBService, db *sql.DB) *KBHandler {
	return &KBHandler{svc: svc, db: db}
}

// mcpWorkspaceContext resolves the owner and (optional) project for a workspace,
// mirroring MemoryHandler.mcpWorkspaceContext. On any failure it writes the
// response and returns ok=false.
func (h *KBHandler) mcpWorkspaceContext(c *gin.Context) (service.OwnerRef, *int64, bool) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return service.OwnerRef{}, nil, false
	}
	q := store.Wrap(h.db).Queries()
	ws, err := q.GetWorkspace(c.Request.Context(), wsID)
	if err != nil {
		NotFound(c, "WORKSPACE")
		return service.OwnerRef{}, nil, false
	}
	owner := service.OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}
	var projectID *int64
	if pid, err := q.GetProjectIDForWorkspace(c.Request.Context(), wsID); err == nil && pid > 0 {
		projectID = &pid
	}
	return owner, projectID, true
}

// kbListItem is the JSON shape returned by kb_list: the metadata an agent needs
// to decide which KB to search (it never exposes cross-owner KBs).
type kbListItem struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SourceKind  string `json:"source_kind"`
}

// MCPList lists the knowledge bases visible to the workspace (owner-owned +
// project-bound). Powers the kb_list tool.
func (h *KBHandler) MCPList(c *gin.Context) {
	owner, projectID, ok := h.mcpWorkspaceContext(c)
	if !ok {
		return
	}
	kbs, err := h.svc.ListVisibleKBs(c.Request.Context(), owner, projectID)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]kbListItem, 0, len(kbs))
	for _, kb := range kbs {
		out = append(out, kbListItem{
			ID:          kb.ID,
			Name:        kb.Name,
			Description: kb.Description,
			SourceKind:  kb.SourceKind,
		})
	}
	c.JSON(http.StatusOK, out)
}

// MCPSearch runs a keyword FTS search across the KBs visible to the workspace
// and returns ranked hits (each tagged with its source KB). Powers kb_search.
func (h *KBHandler) MCPSearch(c *gin.Context) {
	owner, projectID, ok := h.mcpWorkspaceContext(c)
	if !ok {
		return
	}
	query := c.Query("q")
	limit := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	hits, err := h.svc.SearchVisible(c.Request.Context(), owner, projectID, query, limit)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	if hits == nil {
		hits = []service.VisibleSearchHit{}
	}
	c.JSON(http.StatusOK, hits)
}
