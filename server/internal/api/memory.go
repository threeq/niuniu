package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/robfig/cron/v3"
)

// MemoryHandler serves the owner-scoped white-box memory REST + MCP surface.
type MemoryHandler struct {
	svc        *service.MemoryService
	authz      *service.Authz
	db         *sql.DB
	notifyHub  *notify.NotificationHub
	maintBoard service.MaintBoard // optional: enables the "run once" maintenance trigger
}

func NewMemoryHandler(svc *service.MemoryService, authz *service.Authz, db *sql.DB, hub *notify.NotificationHub) *MemoryHandler {
	return &MemoryHandler{svc: svc, authz: authz, db: db, notifyHub: hub}
}

// WithMaintBoard wires the kanban board used by the manual "run once" memory
// maintenance trigger. Without it the trigger reports unavailable.
func (h *MemoryHandler) WithMaintBoard(b service.MaintBoard) *MemoryHandler {
	h.maintBoard = b
	return h
}

// MemoryResponse serializes a memory with sql.Null* fields as plain values/null.
type MemoryResponse struct {
	ID            int64   `json:"id"`
	OwnerType     string  `json:"owner_type"`
	OwnerID       int64   `json:"owner_id"`
	ProjectID     *int64  `json:"project_id"`
	WorkspaceID   *int64  `json:"workspace_id"`
	MemType       string  `json:"mem_type"`
	Title         string  `json:"title"`
	Content       string  `json:"content"`
	Source        string  `json:"source"`
	SourcePath    string  `json:"source_path"`
	Version       int64   `json:"version"`
	PendingReview bool    `json:"pending_review"`
	StaleReason   string  `json:"stale_reason"`
	DeletedAt     *string `json:"deleted_at"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// MemorySweepRunResponse serializes one automatic staleness-sweep execution.
type MemorySweepRunResponse struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	Trigger     string `json:"trigger"`
	Scanned     int64  `json:"scanned"`
	AutoDeleted int64  `json:"auto_deleted"`
	Queued      int64  `json:"queued"`
	Detail      string `json:"detail"`
	CreatedAt   string `json:"created_at"`
}

// MemoryVersionResponse serializes a single version snapshot.
type MemoryVersionResponse struct {
	ID         int64  `json:"id"`
	MemoryID   int64  `json:"memory_id"`
	Version    int64  `json:"version"`
	MemType    string `json:"mem_type"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	Source     string `json:"source"`
	SourcePath string `json:"source_path"`
	CreatedAt  string `json:"created_at"`
}

const tsLayout = "2006-01-02T15:04:05Z"

func nullInt64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func nullTimePtr(n sql.NullTime) *string {
	if !n.Valid {
		return nil
	}
	s := n.Time.UTC().Format(tsLayout)
	return &s
}

func toMemoryResponse(m store.Memory) MemoryResponse {
	return MemoryResponse{
		ID:            m.ID,
		OwnerType:     m.OwnerType,
		OwnerID:       m.OwnerID,
		ProjectID:     nullInt64Ptr(m.ProjectID),
		WorkspaceID:   nullInt64Ptr(m.WorkspaceID),
		MemType:       m.MemType,
		Title:         m.Title,
		Content:       m.Content,
		Source:        m.Source,
		SourcePath:    m.SourcePath,
		Version:       m.Version,
		PendingReview: m.PendingReview != 0,
		StaleReason:   m.StaleReason,
		DeletedAt:     nullTimePtr(m.DeletedAt),
		CreatedAt:     m.CreatedAt.UTC().Format(tsLayout),
		UpdatedAt:     m.UpdatedAt.UTC().Format(tsLayout),
	}
}

func toSweepRunResponse(r store.MemorySweepRun) MemorySweepRunResponse {
	return MemorySweepRunResponse{
		ID:          r.ID,
		ProjectID:   r.ProjectID,
		Trigger:     r.Trigger,
		Scanned:     r.Scanned,
		AutoDeleted: r.AutoDeleted,
		Queued:      r.Queued,
		Detail:      r.Detail,
		CreatedAt:   r.CreatedAt.UTC().Format(tsLayout),
	}
}

func toMemoryResponses(rows []store.Memory) []MemoryResponse {
	out := make([]MemoryResponse, 0, len(rows))
	for _, m := range rows {
		out = append(out, toMemoryResponse(m))
	}
	return out
}

func toMemoryVersionResponses(rows []store.MemoryVersion) []MemoryVersionResponse {
	out := make([]MemoryVersionResponse, 0, len(rows))
	for _, v := range rows {
		out = append(out, MemoryVersionResponse{
			ID:         v.ID,
			MemoryID:   v.MemoryID,
			Version:    v.Version,
			MemType:    v.MemType,
			Title:      v.Title,
			Content:    v.Content,
			Source:     v.Source,
			SourcePath: v.SourcePath,
			CreatedAt:  v.CreatedAt.UTC().Format(tsLayout),
		})
	}
	return out
}

// List returns alive memories. Scoping precedence:
//   - ?project_id=N  -> the project's memories (gated by project access)
//   - ?owner=...     -> that owner's memories (gated by owner read access)
//   - default        -> the caller's personal memories
//
// Optional ?type= and ?q= further filter the result.
func (h *MemoryHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	memType := c.Query("type")
	query := c.Query("q")

	if pid := c.Query("project_id"); pid != "" {
		projectID, err := strconv.ParseInt(pid, 10, 64)
		if err != nil {
			BadRequest(c, "invalid project_id")
			return
		}
		if h.authz != nil && userID > 0 {
			if _, err := h.authz.CanAccessProject(c.Request.Context(), userID, projectID); err != nil {
				writeAuthzError(c, err)
				return
			}
		}
		rows, err := h.svc.ListForProject(c.Request.Context(), projectID, memType)
		if err != nil {
			InternalError(c, err)
			return
		}
		if query != "" {
			rows = filterByQuery(rows, query)
		}
		c.JSON(http.StatusOK, toMemoryResponses(rows))
		return
	}

	owner, err := h.resolveListOwner(c, userID)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	if h.authz != nil && userID > 0 {
		if err := h.authz.EnsureOwnerReadable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	var rows []store.Memory
	if query != "" {
		rows, err = h.svc.Search(c.Request.Context(), owner, query)
		if err == nil {
			rows = filterRespByType(rows, memType)
		}
	} else {
		rows, err = h.svc.ListForOwner(c.Request.Context(), owner, memType)
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toMemoryResponses(rows))
}

func filterRespByType(rows []store.Memory, memType string) []store.Memory {
	if memType == "" {
		return rows
	}
	out := make([]store.Memory, 0, len(rows))
	for _, m := range rows {
		if m.MemType == memType {
			out = append(out, m)
		}
	}
	return out
}

// filterByQuery narrows rows to a literal, case-insensitive substring match of
// q against title or content. Used by the project-scoped list branch (the
// owner branch goes through MemoryService.Search). Allocates a fresh slice.
func filterByQuery(rows []store.Memory, q string) []store.Memory {
	needle := strings.ToLower(strings.TrimSpace(q))
	if needle == "" {
		return rows
	}
	out := make([]store.Memory, 0, len(rows))
	for _, m := range rows {
		if strings.Contains(strings.ToLower(m.Title), needle) ||
			strings.Contains(strings.ToLower(m.Content), needle) {
			out = append(out, m)
		}
	}
	return out
}

func (h *MemoryHandler) resolveListOwner(c *gin.Context, userID int64) (service.OwnerRef, error) {
	f, err := ParseOwnerFilter(c, h.db)
	if err != nil {
		return service.OwnerRef{}, err
	}
	if f.Type == "" {
		return service.OwnerRef{Type: "user", ID: userID}, nil
	}
	return service.OwnerRef{Type: f.Type, ID: f.ID}, nil
}

type createMemoryRequest struct {
	Owner       *OwnerDTO `json:"owner,omitempty"`
	ProjectID   *int64    `json:"project_id,omitempty"`
	WorkspaceID *int64    `json:"workspace_id,omitempty"`
	MemType     string    `json:"mem_type"`
	Title       string    `json:"title" binding:"required"`
	Content     string    `json:"content"`
	Source      string    `json:"source"`
	SourcePath  string    `json:"source_path"`
}

func (h *MemoryHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req createMemoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	owner := resolveOwnerForCreate(req.Owner, userID)
	if h.authz != nil && userID > 0 {
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	m, err := h.svc.Create(c.Request.Context(), service.CreateMemoryInput{
		Owner:       owner,
		ProjectID:   req.ProjectID,
		WorkspaceID: req.WorkspaceID,
		MemType:     req.MemType,
		Title:       req.Title,
		Content:     req.Content,
		Source:      req.Source,
		SourcePath:  req.SourcePath,
	})
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusCreated, toMemoryResponse(m))
}

// memoryDetailResponse bundles a memory with its version timeline.
type memoryDetailResponse struct {
	MemoryResponse
	Versions []MemoryVersionResponse `json:"versions"`
}

func (h *MemoryHandler) Get(c *gin.Context) {
	id, ok := h.accessMemory(c)
	if !ok {
		return
	}
	m, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		NotFound(c, "MEMORY")
		return
	}
	vers, err := h.svc.ListVersions(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, memoryDetailResponse{
		MemoryResponse: toMemoryResponse(m),
		Versions:       toMemoryVersionResponses(vers),
	})
}

type updateMemoryRequest struct {
	MemType    string `json:"mem_type"`
	Title      string `json:"title" binding:"required"`
	Content    string `json:"content"`
	Source     string `json:"source"`
	SourcePath string `json:"source_path"`
}

func (h *MemoryHandler) Update(c *gin.Context) {
	id, ok := h.accessMemory(c)
	if !ok {
		return
	}
	var req updateMemoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	m, err := h.svc.Update(c.Request.Context(), id, service.UpdateMemoryInput{
		MemType:    req.MemType,
		Title:      req.Title,
		Content:    req.Content,
		Source:     req.Source,
		SourcePath: req.SourcePath,
	})
	if err == service.ErrMemoryNotFound {
		NotFound(c, "MEMORY")
		return
	}
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, toMemoryResponse(m))
}

func (h *MemoryHandler) Delete(c *gin.Context) {
	id, ok := h.accessMemory(c)
	if !ok {
		return
	}
	if c.Query("hard") == "1" {
		if err := h.svc.HardDelete(c.Request.Context(), id); err != nil {
			InternalError(c, err)
			return
		}
	} else if err := h.svc.SoftDelete(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *MemoryHandler) Restore(c *gin.Context) {
	id, ok := h.accessMemory(c)
	if !ok {
		return
	}
	if err := h.svc.Restore(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}
	m, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		NotFound(c, "MEMORY")
		return
	}
	c.JSON(http.StatusOK, toMemoryResponse(m))
}

func (h *MemoryHandler) ListVersions(c *gin.Context) {
	id, ok := h.accessMemory(c)
	if !ok {
		return
	}
	vers, err := h.svc.ListVersions(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toMemoryVersionResponses(vers))
}

type rollbackMemoryRequest struct {
	Version int64 `json:"version" binding:"required"`
}

func (h *MemoryHandler) Rollback(c *gin.Context) {
	id, ok := h.accessMemory(c)
	if !ok {
		return
	}
	var req rollbackMemoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	m, err := h.svc.Rollback(c.Request.Context(), id, req.Version)
	if err == service.ErrMemoryNotFound {
		NotFound(c, "MEMORY")
		return
	}
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, toMemoryResponse(m))
}

// --- MCP surface (called by niuniu-mcp over /mcp/*) ---
//
// The MCP routes are keyed by workspace id (:id). Owner + project are derived
// from the workspace so memories land under the workspace's owner with full
// provenance, mirroring how the blackboard tools key off the workspace.

// mcpWorkspaceContext resolves the owner and (optional) project for a workspace.
func (h *MemoryHandler) mcpWorkspaceContext(c *gin.Context) (service.OwnerRef, *int64, int64, bool) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return service.OwnerRef{}, nil, 0, false
	}
	q := store.Wrap(h.db).Queries()
	ws, err := q.GetWorkspace(c.Request.Context(), wsID)
	if err != nil {
		NotFound(c, "WORKSPACE")
		return service.OwnerRef{}, nil, 0, false
	}
	owner := service.OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}
	var projectID *int64
	if pid, err := q.GetProjectIDForWorkspace(c.Request.Context(), wsID); err == nil && pid > 0 {
		projectID = &pid
	}
	return owner, projectID, wsID, true
}

// MCPList lists/searches memories for the workspace's owner.
func (h *MemoryHandler) MCPList(c *gin.Context) {
	owner, _, _, ok := h.mcpWorkspaceContext(c)
	if !ok {
		return
	}
	memType := c.Query("type")
	if q := c.Query("q"); q != "" {
		rows, err := h.svc.Search(c.Request.Context(), owner, q)
		if err != nil {
			InternalError(c, err)
			return
		}
		c.JSON(http.StatusOK, toMemoryResponses(filterRespByType(rows, memType)))
		return
	}
	rows, err := h.svc.ListForOwner(c.Request.Context(), owner, memType)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toMemoryResponses(rows))
}

type mcpMemoryWriteRequest struct {
	MemType    string `json:"mem_type"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	SourcePath string `json:"source_path"`
}

// mcpWrite is shared by generate (source) and extract (source) paths.
func (h *MemoryHandler) mcpWrite(c *gin.Context, source string) {
	owner, projectID, wsID, ok := h.mcpWorkspaceContext(c)
	if !ok {
		return
	}
	var req mcpMemoryWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	wsCopy := wsID
	m, err := h.svc.Create(c.Request.Context(), service.CreateMemoryInput{
		Owner:       owner,
		ProjectID:   projectID,
		WorkspaceID: &wsCopy,
		MemType:     req.MemType,
		Title:       req.Title,
		Content:     req.Content,
		Source:      source,
		SourcePath:  req.SourcePath,
	})
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusCreated, toMemoryResponse(m))
}

// MCPGenerate records a memory distilled from the session (source=generate).
func (h *MemoryHandler) MCPGenerate(c *gin.Context) { h.mcpWrite(c, "generate") }

// MCPExtract records a memory extracted from a deliverable/diff (source=extract).
func (h *MemoryHandler) MCPExtract(c *gin.Context) { h.mcpWrite(c, "extract") }

// MCPConsolidate runs an idle dedup/tidy pass over the owner's memories.
func (h *MemoryHandler) MCPConsolidate(c *gin.Context) {
	owner, _, _, ok := h.mcpWorkspaceContext(c)
	if !ok {
		return
	}
	res, err := h.svc.ConsolidateForOwner(c.Request.Context(), owner)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// mcpResolveOwnedMemory fetches a memory by id and verifies it belongs to the
// workspace's owner, so an agent can only mutate its own owner's memories. On
// failure it writes the response (404, not leaking cross-tenant existence) and
// returns ok=false.
func (h *MemoryHandler) mcpResolveOwnedMemory(c *gin.Context, owner service.OwnerRef, id int64) (store.Memory, bool) {
	m, err := h.svc.Get(c.Request.Context(), id)
	if err != nil || m.OwnerType != owner.Type || m.OwnerID != owner.ID {
		NotFound(c, "MEMORY")
		return store.Memory{}, false
	}
	return m, true
}

type mcpMemoryUpdateRequest struct {
	ID      int64  `json:"id"`
	MemType string `json:"mem_type"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// MCPUpdateMemory edits an existing owner-scoped memory. Reversible: every update
// snapshots the prior version (rollback/restore stay available). Empty fields
// keep their current value.
func (h *MemoryHandler) MCPUpdateMemory(c *gin.Context) {
	owner, _, _, ok := h.mcpWorkspaceContext(c)
	if !ok {
		return
	}
	var req mcpMemoryUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	prev, ok := h.mcpResolveOwnedMemory(c, owner, req.ID)
	if !ok {
		return
	}
	memType := req.MemType
	if strings.TrimSpace(memType) == "" {
		memType = prev.MemType
	}
	title := req.Title
	if strings.TrimSpace(title) == "" {
		title = prev.Title
	}
	content := req.Content
	if strings.TrimSpace(content) == "" {
		content = prev.Content
	}
	m, err := h.svc.Update(c.Request.Context(), req.ID, service.UpdateMemoryInput{
		MemType: memType, Title: title, Content: content,
		Source: prev.Source, SourcePath: prev.SourcePath,
	})
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, toMemoryResponse(m))
}

type mcpMemoryIDRequest struct {
	ID int64 `json:"id"`
}

// MCPDeleteMemory soft-deletes an owner-scoped memory (reversible: archived, not
// erased; restorable via memory_restore). Used to clear stale/outdated memories.
func (h *MemoryHandler) MCPDeleteMemory(c *gin.Context) {
	owner, _, _, ok := h.mcpWorkspaceContext(c)
	if !ok {
		return
	}
	var req mcpMemoryIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if _, ok := h.mcpResolveOwnedMemory(c, owner, req.ID); !ok {
		return
	}
	if err := h.svc.SoftDelete(c.Request.Context(), req.ID); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": req.ID, "deleted": true})
}

// MCPRestoreMemory restores a previously soft-deleted owner-scoped memory.
func (h *MemoryHandler) MCPRestoreMemory(c *gin.Context) {
	owner, _, _, ok := h.mcpWorkspaceContext(c)
	if !ok {
		return
	}
	var req mcpMemoryIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if _, ok := h.mcpResolveOwnedMemory(c, owner, req.ID); !ok {
		return
	}
	if err := h.svc.Restore(c.Request.Context(), req.ID); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": req.ID, "restored": true})
}

// accessMemory parses :id, enforces caller access, and returns (id, true) on
// success. On any failure it writes the response and returns ok=false.
func (h *MemoryHandler) accessMemory(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid memory ID")
		return 0, false
	}
	userID := c.GetInt64("auth_user_id")
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessMemory(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return 0, false
		}
	}
	return id, true
}

// accessProject enforces caller access to a project (path :id). On failure it
// writes the response and returns (0, false).
func (h *MemoryHandler) accessProject(c *gin.Context) (int64, bool) {
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || pid <= 0 {
		BadRequest(c, "invalid project ID")
		return 0, false
	}
	userID := c.GetInt64("auth_user_id")
	if h.authz != nil && userID > 0 {
		if _, err := h.authz.CanAccessProject(c.Request.Context(), userID, pid); err != nil {
			writeAuthzError(c, err)
			return 0, false
		}
	}
	return pid, true
}

// ListReviewQueue returns the staleness review queue for a project (memories
// soft-deleted by detection but awaiting a human keep/delete decision).
func (h *MemoryHandler) ListReviewQueue(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	rows, err := h.svc.ListPendingReview(c.Request.Context(), pid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toMemoryResponses(rows))
}

// GetProjectMemorySchedule returns a project's staleness-sweep cron schedule.
func (h *MemoryHandler) GetProjectMemorySchedule(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	cronExpr, err := h.svc.GetProjectSweepCron(c.Request.Context(), pid)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"cron": cronExpr})
}

type memoryScheduleRequest struct {
	Cron string `json:"cron"`
}

// SetProjectMemorySchedule validates and stores a project's staleness-sweep cron.
func (h *MemoryHandler) SetProjectMemorySchedule(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	var req memoryScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	// An empty expression disables automatic maintenance (the default). A
	// non-empty expression must be a valid 5-field cron.
	expr := strings.TrimSpace(req.Cron)
	if expr != "" {
		if _, err := cron.ParseStandard(expr); err != nil {
			BadRequest(c, "invalid cron expression (expect 5 fields, e.g. '33 3 3 * *')")
			return
		}
	}
	if err := h.svc.SetProjectSweepCron(c.Request.Context(), pid, expr); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"cron": expr, "enabled": expr != ""})
}

// RunMemoryMaintenanceOnce triggers a single maintenance run for a project,
// regardless of its schedule (the manual "整理记忆" button). The prelude (repo
// check + issue creation) runs synchronously so a misconfigured project returns a
// real error and the created issue is confirmed; the workspace start + completion
// poll continue in the background. Returns the created issue id.
func (h *MemoryHandler) RunMemoryMaintenanceOnce(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	if h.maintBoard == nil {
		RespondError(c, http.StatusServiceUnavailable, "UNAVAILABLE", "记忆整理功能在此服务器上不可用")
		return
	}
	userID := c.GetInt64("auth_user_id")
	res, err := h.svc.StartMemoryMaintenanceOnce(c.Request.Context(), h.maintBoard, pid, userID)
	if err != nil {
		if errors.Is(err, service.ErrNoRepository) {
			Conflict(c, "项目未关联仓库，无法整理记忆")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"started": true, "issue_id": res.IssueID})
}

// ListSweepRuns returns the automatic-sweep execution log for a project.
func (h *MemoryHandler) ListSweepRuns(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	rows, err := h.svc.ListSweepRuns(c.Request.Context(), pid, 50)
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]MemorySweepRunResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, toSweepRunResponse(r))
	}
	c.JSON(http.StatusOK, out)
}

// ClearSweepRuns deletes a project's automatic-maintenance run log.
func (h *MemoryHandler) ClearSweepRuns(c *gin.Context) {
	pid, ok := h.accessProject(c)
	if !ok {
		return
	}
	if err := h.svc.ClearSweepRuns(c.Request.Context(), pid); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"cleared": true})
}

// KeepReviewed resolves a queued memory as "keep": clears the review flag and
// restores it.
func (h *MemoryHandler) KeepReviewed(c *gin.Context) {
	id, ok := h.accessMemory(c)
	if !ok {
		return
	}
	if err := h.svc.KeepReviewedMemory(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": id, "kept": true}})
}

// ConfirmReviewedDelete resolves a queued memory as "delete": clears the review
// flag, leaving it soft-deleted (still restorable).
func (h *MemoryHandler) ConfirmReviewedDelete(c *gin.Context) {
	id, ok := h.accessMemory(c)
	if !ok {
		return
	}
	if err := h.svc.ConfirmReviewedDeletion(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"id": id, "deleted": true}})
}

// --- Session-facing memory operations (workspace-keyed) ---
//
// Relocated from the removed learnings layer (#256): the project-id lookup an
// agent/client uses to resolve a workspace's project, and the server-side AI
// extraction that distills durable memories from a finished work session.

func (h *MemoryHandler) broadcastProjectMemory(projectID int64) {
	if h.notifyHub != nil {
		h.notifyHub.Broadcast(notify.Notification{
			Topic:  "project",
			Action: "learnings_updated",
			ID:     projectID,
		})
	}
}

// GetWorkspaceProjectID resolves the project a workspace belongs to.
func (h *MemoryHandler) GetWorkspaceProjectID(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	q := store.Wrap(h.db).Queries()
	projectID, err := q.GetProjectIDForWorkspace(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{"code": "NO_PROJECT", "message": "workspace is not linked to a project"},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"project_id": projectID}})
}

// ExtractSession runs server-side AI extraction over the workspace's recent
// session messages and writes the distilled memories to the unified store.
func (h *MemoryHandler) ExtractSession(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	q := store.Wrap(h.db).Queries()
	projectID, err := q.GetProjectIDForWorkspace(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{"code": "NO_PROJECT", "message": "workspace is not linked to a project"},
		})
		return
	}
	userID := c.GetInt64("auth_user_id")
	// Extraction runs the claude CLI (seconds), so it's dispatched to a detached
	// goroutine; the client polls /extract-learnings/status and the button shows a
	// spinner while running. State is kept in memory by the service.
	started := h.svc.StartExtractionAsync(wsID, projectID, userID, func(pid int64) {
		h.broadcastProjectMemory(pid)
	})
	if !started {
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{"code": "EXTRACTION_IN_PROGRESS", "message": "extraction is already running for this workspace"},
		})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"data": gin.H{"running": true}})
}

// ExtractSessionStatus reports the in-memory progress of async session extraction
// so the client can render a spinner that survives reloads and surface the result.
func (h *MemoryHandler) ExtractSessionStatus(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": h.svc.GetExtractStatus(wsID)})
}
