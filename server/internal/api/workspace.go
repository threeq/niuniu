package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type WorkspaceHandler struct {
	Svc      *service.WorkspaceService
	Proxy    *agentproxy.AgentProxy
	AgentMgr *service.AgentManager
	Q        *store.Queries
	DB       *sql.DB // used for batch owner-name lookup on list endpoints
	Authz    *service.Authz
	// RepoSvc + Perm power the studio "from local directory" flow (issues
	// #232/#235): auto-init/bind the directory as a repo and preset the git Bash
	// allowlist. Optional; nil makes CreateFromDirectory 500 (mis-wired server).
	RepoSvc *service.RepositoryService
	Perm    *service.PermissionService
}

func NewWorkspaceHandler(svc *service.WorkspaceService) *WorkspaceHandler {
	return &WorkspaceHandler{Svc: svc}
}

type CreateWorkspaceOwnerRequest struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

type CreateWorkspaceRequest struct {
	IssueID *int64 `json:"issue_id"` // nullable int64
	// ProjectID lets the SPA create a project-scoped workspace without binding
	// it to a specific issue. When set, used to prefill any project-default
	// scenes (see WorkspaceService.Create scene-layer prefill). Ignored if
	// IssueID is also set — issue's project takes precedence.
	ProjectID       *int64                       `json:"project_id,omitempty"`
	Name            string                       `json:"name"`
	Repos           []RepoBranchRequest          `json:"repos"`
	Owner           *CreateWorkspaceOwnerRequest `json:"owner"`
	MCPServers     []string `json:"mcp_servers,omitempty"` // workspace-scoped MCP server names
	// CliType picks the agent CLI for the workspace. "claude" (default) or
	// "codex". Empty string normalizes to "claude" in the SQL layer.
	// Immutable after create: PATCH /api/workspaces/:id rejects this field.
	CliType string `json:"cli_type,omitempty"`
	// NoRepo creates a plain owner-isolated directory with no git worktrees
	// (office / non-code tasks). When true, Repos must be empty; when false,
	// at least one repo is required. See docs/architecture/workspace-model.md.
	NoRepo bool `json:"no_repo,omitempty"`
}

type RepoBranchRequest struct {
	RepoID int64 `json:"repo_id" binding:"required"` // int64
	// Branch is the base branch the new worktree forks from. Required so the
	// server never silently falls back to a guessed default (e.g. "main") that
	// may not exist in the repo — that fallback used to leave the workspace
	// shell in place with no worktree on disk and the failure swallowed.
	Branch string `json:"branch" binding:"required"`
}

// List returns all workspaces
// @Summary      List workspaces
// @Description  Get all workspaces
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Success      200  {array}   WorkspaceResponse
// @Failure      500  {object}  Error
// @Router       /workspaces [get]
func (h *WorkspaceHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	// Optional ?owner=user:<id>|org:<slug> filter — used by the org detail
	// "资源" tab to count workspaces belonging to a specific org.
	ownerF, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	creatorID, err := parseCreatorParam(c, userID)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	var metas []service.WorkspaceSidebarMeta
	if userID > 0 {
		metas, err = h.Svc.ListWithSidebarMetaForUser(ctx, userID, service.WorkspaceListFilter{
			CreatorID: creatorID,
		})
	} else {
		metas, err = h.Svc.ListWithSidebarMeta(ctx)
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	if ownerF.Type != "" {
		filtered := metas[:0]
		for _, m := range metas {
			if ownerF.Match(m.Workspace.OwnerType, m.Workspace.OwnerID) {
				filtered = append(filtered, m)
			}
		}
		metas = filtered
	}

	// Build owner lookup once for the whole page (two batch queries max).
	// Include workspace owners, project owners, and workspace creators
	// (always users); newOwnerLookup deduplicates.
	var lk *ownerLookup
	if h.DB != nil {
		refs := make([]ownerRef, 0, len(metas)*3)
		for _, m := range metas {
			refs = append(refs, ownerRef{m.Workspace.OwnerType, m.Workspace.OwnerID})
			if m.ProjectOwnerType != "" {
				refs = append(refs, ownerRef{m.ProjectOwnerType, m.ProjectOwnerID})
			}
			if m.Workspace.CreatedBy.Valid {
				refs = append(refs, ownerRef{"user", m.Workspace.CreatedBy.Int64})
			}
		}
		lk, _ = newOwnerLookup(ctx, h.DB, refs)
	}

	responses := make([]WorkspaceResponse, len(metas))
	for i, m := range metas {
		var projName *string
		if m.ProjectName != "" {
			projName = &m.ProjectName
		}
		var lcStatus *string
		if m.LifecycleStatus != "" {
			lcStatus = &m.LifecycleStatus
		}
		// Only surface issue_type for Epics; regular tasks stay omitted so the
		// JSON contract matches the SPA's "render marker only when 'epic'" gate.
		var issueType *string
		if m.IssueType == "epic" {
			it := m.IssueType
			issueType = &it
		}
		// Surface parent_issue_id only for sub-issues (linked issue has a parent)
		// so the SPA renders the sub-issue marker; top-level issues stay omitted.
		var parentIssueID *int64
		if m.ParentIssueID != 0 {
			pid := m.ParentIssueID
			parentIssueID = &pid
		}
		var taskStats *TaskStatsDTO
		if m.TaskTotal > 0 {
			ts := TaskStatsDTO{
				Total:     m.TaskTotal,
				Completed: m.TaskDone,
			}
			if m.TaskCurrent != "" {
				ts.CurrentTask = &m.TaskCurrent
			}
			taskStats = &ts
		}
		var wtDTOs []WorktreeSidebarDTO
		for _, wt := range m.Worktrees {
			wtDTOs = append(wtDTOs, WorktreeSidebarDTO{
				Name:         wt.Name,
				RepoName:     wt.RepoName,
				Branch:       wt.Branch,
				BaseBranch:   wt.BaseBranch,
				ChangesCount: wt.ChangesCount,
				AheadCount:   wt.AheadCount,
			})
		}
		var schedCount *int
		if m.ScheduleCount > 0 {
			schedCount = &m.ScheduleCount
		}
		var bgAgg *BgTaskAggregateDTO
		hasAny := m.BgAgentBusy ||
			m.BgBashCount > 0 || m.BgWakeupCount > 0 || m.BgSubagentCount > 0 ||
			m.EnabledCronCount > 0
		if hasAny {
			bgAgg = &BgTaskAggregateDTO{
				AgentBusy:     m.BgAgentBusy,
				BashCount:     m.BgBashCount,
				WakeupCount:   m.BgWakeupCount,
				SubagentCount: m.BgSubagentCount,
				CronCount:     m.EnabledCronCount,
			}
			if m.BgHighlight != nil {
				h := &BgTaskHighlight{
					Kind:  m.BgHighlight.Kind,
					Title: m.BgHighlight.Title,
				}
				if !m.BgHighlight.StartedAt.IsZero() {
					h.StartedAt = m.BgHighlight.StartedAt.Format(time.RFC3339)
				}
				if !m.BgHighlight.ScheduledFor.IsZero() {
					h.ScheduledFor = m.BgHighlight.ScheduledFor.Format(time.RFC3339)
				}
				bgAgg.Highlight = h
			}
		}
		var projOwnerType *string
		var projOwnerID *int64
		var projOwnerName *string
		if m.ProjectOwnerType != "" {
			pt := m.ProjectOwnerType
			pid := m.ProjectOwnerID
			projOwnerType = &pt
			projOwnerID = &pid
			if lk != nil {
				name := lk.Build(m.ProjectOwnerType, m.ProjectOwnerID).Name
				if name != nil && *name != "" {
					projOwnerName = name
				}
			}
		}
		resp := toWorkspaceResponseWithMeta(m.Workspace, SidebarMeta{
			ChangesCount:    m.ChangesCount,
			AheadCount:      m.AheadCount,
			MessageCount:    &m.MessageCount,
			LastMessageAt:   m.LastMessageAt,
			ProjectName:     projName,
			LifecycleStatus: lcStatus,
			IssueType:       issueType,
			ParentIssueID:   parentIssueID,
			TaskStats:       taskStats,
			Worktrees:       wtDTOs,
			ScheduleCount:   schedCount,
			BgTasks:         bgAgg,
		})
		if lk != nil {
			resp.Owner = lk.Build(m.Workspace.OwnerType, m.Workspace.OwnerID)
			if m.Workspace.CreatedBy.Valid {
				co := lk.Build("user", m.Workspace.CreatedBy.Int64)
				resp.CreatorOwner = &co
			}
		} else if m.Workspace.CreatedBy.Valid {
			co := OwnerDTO{Type: "user", ID: m.Workspace.CreatedBy.Int64}
			resp.CreatorOwner = &co
		}
		resp.ProjectOwnerType = projOwnerType
		resp.ProjectOwnerID = projOwnerID
		resp.ProjectOwnerName = projOwnerName
		responses[i] = resp
	}
	c.JSON(http.StatusOK, responses)
}

// WorktreeGitStatusDTO is the lazily-loaded git badge for one worktree.
type WorktreeGitStatusDTO struct {
	Name         string `json:"name"`
	ChangesCount int    `json:"changes_count"`
	AheadCount   int    `json:"ahead_count"`
}

// WorkspaceGitStatusDTO is the lazily-loaded git badge for one workspace,
// returned by GET /api/workspaces/sidebar-git. workspace_id is a string to
// match WorkspaceResponse.id so the SPA can merge it into the list cache.
type WorkspaceGitStatusDTO struct {
	WorkspaceID  string                 `json:"workspace_id"`
	ChangesCount int                    `json:"changes_count"`
	AheadCount   int                    `json:"ahead_count"`
	Worktrees    []WorktreeGitStatusDTO `json:"worktrees,omitempty"`
}

// SidebarGitStatus godoc
// @Summary      Sidebar git status (lazy)
// @Description  Per-workspace git change/ahead counts for the caller's accessible
// @Description  workspaces. The sidebar list (GET /workspaces) returns instantly
// @Description  without git; the SPA calls this afterwards and merges the badges.
// @Tags         Workspaces
// @Produce      json
// @Param        created_by  query     string  false  "me | all | <user id>"
// @Success      200  {array}   WorkspaceGitStatusDTO
// @Failure      500  {object}  Error
// @Router       /workspaces/sidebar-git [get]
func (h *WorkspaceHandler) SidebarGitStatus(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	creatorID, err := parseCreatorParam(c, userID)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	var statuses []service.WorkspaceGitStatus
	if userID > 0 {
		statuses, err = h.Svc.SidebarGitStatusForUser(ctx, userID, service.WorkspaceListFilter{
			CreatorID: creatorID,
		})
	} else {
		statuses, err = h.Svc.SidebarGitStatus(ctx)
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]WorkspaceGitStatusDTO, 0, len(statuses))
	for _, st := range statuses {
		dto := WorkspaceGitStatusDTO{
			WorkspaceID:  strconv.FormatInt(st.WorkspaceID, 10),
			ChangesCount: st.ChangesCount,
			AheadCount:   st.AheadCount,
		}
		for _, wt := range st.Worktrees {
			dto.Worktrees = append(dto.Worktrees, WorktreeGitStatusDTO{
				Name:         wt.Name,
				ChangesCount: wt.ChangesCount,
				AheadCount:   wt.AheadCount,
			})
		}
		out = append(out, dto)
	}
	c.JSON(http.StatusOK, out)
}

// SearchContent returns the IDs of workspaces accessible to the caller whose
// user-authored chat messages match the keyword q. The sidebar uses these IDs
// to extend its instant name/id filter with conversation-content matching.
// @Summary      Search workspaces by conversation content
// @Description  Workspace IDs whose user-sent chat messages contain the keyword.
// @Tags         Workspaces
// @Produce      json
// @Param        q  query  string  true  "search keyword"
// @Success      200  {object}  map[string][]int64
// @Failure      500  {object}  Error
// @Router       /workspaces/search [get]
func (h *WorkspaceHandler) SearchContent(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	q := c.Query("q")
	if userID <= 0 || strings.TrimSpace(q) == "" {
		c.JSON(http.StatusOK, gin.H{"workspace_ids": []int64{}})
		return
	}
	ids, err := h.Svc.SearchWorkspaceIDsByUserContent(c.Request.Context(), userID, q)
	if err != nil {
		InternalError(c, err)
		return
	}
	if ids == nil {
		ids = []int64{}
	}
	c.JSON(http.StatusOK, gin.H{"workspace_ids": ids})
}

// Overview returns the cross-workspace dashboard payload (cost roll-ups,
// stuck-detection, activity timestamps) restricted to the caller's accessible
// owners. Optional ?owner=user:<id>|org:<slug> narrows further.
// @Summary      Cross-workspace overview
// @Description  Aggregated cost / activity / stuck-detection across workspaces accessible to the caller.
// @Tags         Workspaces
// @Produce      json
// @Param        owner  query  string  false  "owner filter (user:<id> or org:<slug>)"
// @Success      200    {object}  service.WorkspaceOverview
// @Failure      400    {object}  Error
// @Failure      500    {object}  Error
// @Router       /workspaces/overview [get]
func (h *WorkspaceHandler) Overview(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	ownerF, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	creatorID, err := parseCreatorParam(c, userID)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	var metas []service.WorkspaceSidebarMeta
	if userID > 0 {
		metas, err = h.Svc.ListWithSidebarMetaForUser(ctx, userID, service.WorkspaceListFilter{
			CreatorID: creatorID,
		})
	} else {
		metas, err = h.Svc.ListWithSidebarMeta(ctx)
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	if ownerF.Type != "" {
		filtered := metas[:0]
		for _, m := range metas {
			if ownerF.Match(m.Workspace.OwnerType, m.Workspace.OwnerID) {
				filtered = append(filtered, m)
			}
		}
		metas = filtered
	}
	overview, err := h.Svc.BuildOverview(ctx, metas)
	if err != nil {
		InternalError(c, err)
		return
	}
	// Always wrap items into a DTO that includes `owner` — the SPA's
	// WorkspaceOverviewItem type marks owner as required, and OwnerBadge
	// would crash on undefined. When the lookup fails or DB is nil, fill
	// with a synthetic OwnerDTO carrying type+id only (no display name).
	// Build the workspace owner lookup AND a creator lookup. Creators are
	// always users, so we extend the same ownerLookup with user refs.
	var lk *ownerLookup
	if h.DB != nil && len(overview.Workspaces) > 0 {
		refs := make([]ownerRef, 0, len(overview.Workspaces)*2)
		for _, item := range overview.Workspaces {
			refs = append(refs, ownerRef{item.OwnerType, item.OwnerID})
			if item.CreatedBy != nil {
				refs = append(refs, ownerRef{"user", *item.CreatedBy})
			}
		}
		lk, _ = newOwnerLookup(ctx, h.DB, refs)
	}
	enriched := make([]workspaceOverviewItemDTO, len(overview.Workspaces))
	for i, it := range overview.Workspaces {
		var owner OwnerDTO
		if lk != nil {
			owner = lk.Build(it.OwnerType, it.OwnerID)
		} else {
			owner = OwnerDTO{Type: it.OwnerType, ID: it.OwnerID}
		}
		dto := workspaceOverviewItemDTO{
			WorkspaceOverviewItem: it,
			Owner:                 owner,
		}
		if it.CreatedBy != nil {
			var co OwnerDTO
			if lk != nil {
				co = lk.Build("user", *it.CreatedBy)
			} else {
				co = OwnerDTO{Type: "user", ID: *it.CreatedBy}
			}
			dto.CreatorOwner = &co
		}
		enriched[i] = dto
	}
	c.JSON(http.StatusOK, gin.H{
		"summary":    overview.Summary,
		"workspaces": enriched,
	})
}

// workspaceOverviewItemDTO embeds the service item and adds owner display
// info (mirrors the OwnerDTO pattern used by WorkspaceResponse).
type workspaceOverviewItemDTO struct {
	service.WorkspaceOverviewItem
	Owner        OwnerDTO  `json:"owner"`
	CreatorOwner *OwnerDTO `json:"creator_owner,omitempty"`
}

// Get retrieves a workspace by ID
// @Summary      Get a workspace
// @Description  Get a workspace by ID
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Workspace ID"
// @Success      200  {object}  WorkspaceResponse
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id} [get]
func (h *WorkspaceHandler) Get(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	workspace, err := h.Svc.Get(c.Request.Context(), id)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("GetWorkspace failed", "id", id, "error", err)
		}
		NotFound(c, "WORKSPACE")
		return
	}
	resp := toWorkspaceResponse(workspace)
	c.JSON(http.StatusOK, resp)
}

// Create creates a new workspace
// @Summary      Create a workspace
// @Description  Create a new workspace with repositories
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        request  body      CreateWorkspaceRequest  true  "Workspace details"
// @Success      201     {object}  CreateWorkspaceResponse
// @Failure      400     {object}  Error
// @Failure      500     {object}  Error
// @Router       /workspaces [post]
func (h *WorkspaceHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req CreateWorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	// no_repo is the explicit opt-in for a plain-directory workspace. Enforce a
	// clean either/or contract so an empty-repos request is never an accident:
	// no_repo => no repos; otherwise at least one repo is required.
	if req.NoRepo && len(req.Repos) > 0 {
		BadRequest(c, "no_repo workspaces cannot have repositories")
		return
	}
	if !req.NoRepo && len(req.Repos) == 0 {
		BadRequest(c, "select at least one repository, or set no_repo=true for a plain-directory workspace")
		return
	}

	// Resolve owner: explicit body wins, but treat both nil and the SPA
	// no-currentUser fallback {type:"user", id:0} as "default to caller".
	// See project.go Create for context.
	owner := service.OwnerRef{Type: "user", ID: userID}
	if req.Owner != nil && req.Owner.Type != "" && !(req.Owner.Type == "user" && req.Owner.ID == 0) {
		owner = service.OwnerRef{Type: req.Owner.Type, ID: req.Owner.ID}
	}

	// If the workspace is linked to an issue, validate that owner matches the issue's project owner.
	if req.IssueID != nil && h.Q != nil {
		issue, err := h.Q.GetIssue(c.Request.Context(), *req.IssueID)
		if err != nil {
			BadRequest(c, "issue not found")
			return
		}
		col, err := h.Q.GetColumn(c.Request.Context(), issue.ColumnID)
		if err != nil {
			BadRequest(c, "issue column not found")
			return
		}
		proj, err := h.Q.GetProject(c.Request.Context(), col.ProjectID)
		if err != nil {
			BadRequest(c, "issue project not found")
			return
		}
		if owner.Type != proj.OwnerType || owner.ID != proj.OwnerID {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf(
				"workspace owner must match the issue's project owner (type=%s id=%d)",
				proj.OwnerType, proj.OwnerID)})
			return
		}
	}

	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	// Convert request repos to service format
	repos := make([]service.RepoBranch, len(req.Repos))
	for i, r := range req.Repos {
		repos[i] = service.RepoBranch{
			RepoID: r.RepoID,
			Branch: r.Branch,
		}
	}

	var createdByPtr *int64
	if userID > 0 {
		createdByPtr = &userID
	}
	result, err := h.Svc.Create(c.Request.Context(), service.CreateWorkspaceInput{
		IssueID:         req.IssueID,
		ProjectID:       req.ProjectID,
		Name:            req.Name,
		Repos:           repos,
		OwnerType:       owner.Type,
		OwnerID:         owner.ID,
		CreatedBy:       createdByPtr,
		MCPServers:      req.MCPServers,
		CliType:         req.CliType,
		NoRepo:          req.NoRepo,
		Language:        c.GetHeader("X-Niuniu-Language"),
	})
	if err != nil {
		// Invalid cli_type is user-input, not a server failure. Mapping to
		// 400 (instead of the generic 500) avoids paging monitoring and
		// gives clients an actionable response.
		if errors.Is(err, service.ErrInvalidCliType) {
			BadRequest(c, err.Error())
			return
		}
		InternalError(c, err)
		return
	}

	var warnings []string

	// Build response
	successRepos := make([]CreateWorkspaceSuccessRepo, len(result.Repos))
	for i, r := range result.Repos {
		successRepos[i] = CreateWorkspaceSuccessRepo{
			ID:           fmt.Sprintf("%d-repo-%d", result.Workspace.ID, r.RepositoryID),
			RepositoryID: strconv.FormatInt(r.RepositoryID, 10),
			WorktreePath: r.WorktreePath,
			Branch:       r.Branch,
		}
	}
	errors := make([]CreateWorkspaceErrorEntry, len(result.Errors))
	for i, e := range result.Errors {
		errors[i] = CreateWorkspaceErrorEntry{
			RepositoryID: e.RepositoryID,
			Error:        e.Error,
		}
	}

	var respIssueID *string
	if result.Workspace.IssueID.Valid {
		idStr := strconv.FormatInt(result.Workspace.IssueID.Int64, 10)
		respIssueID = &idStr
	}

	c.JSON(http.StatusCreated, CreateWorkspaceResponse{
		ID:       strconv.FormatInt(result.Workspace.ID, 10),
		IssueID:  respIssueID,
		Path:     result.Workspace.Path,
		Status:   result.Workspace.Status,
		Repos:    successRepos,
		Errors:   errors,
		Warnings: warnings,
	})
}

// CreateFromDirectoryRequest is the body for POST /api/workspaces/from-directory.
type CreateFromDirectoryRequest struct {
	// Dir is the local directory the user picked. Already a git repo => bound
	// in place; otherwise auto-init'd (with default .gitignore + Git LFS, #233).
	Dir   string                       `json:"dir" binding:"required"`
	Owner *CreateWorkspaceOwnerRequest `json:"owner"`
	Name  string                       `json:"name"`
	// CliType picks the agent CLI ("claude" default / "codex").
	CliType string `json:"cli_type,omitempty"`
	// WorkflowTemplateID optionally pre-selects a workflow (project_templates).
	// Reserved for the create wizard (#236); not yet consumed server-side.
	WorkflowTemplateID *int64 `json:"workflow_template_id,omitempty"`
}

// CreateFromDirectory builds a studio workspace from a local directory in one
// step (issue #232). Orchestration reuses existing services end to end:
//  1. RepositoryService.Create (AutoInit) — auto-init a plain folder (with
//     .gitignore + Git LFS, #233) or bind an existing repo in place (plan A:
//     the user's directory stays the main working tree, untouched).
//  2. WorkspaceService.Create — one worktree forked off the repo's base branch
//     onto ws-<id>/<branch>; the workspace is flagged is_studio.
//  3. PermissionService.PresetGitBashAllowlist (#235) — so save/deliver quick
//     actions run git unattended.
//
// @Summary      Create a workspace from a local directory
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        request  body      CreateFromDirectoryRequest  true  "Directory + owner"
// @Success      201     {object}  CreateWorkspaceResponse
// @Failure      400     {object}  Error
// @Failure      500     {object}  Error
// @Router       /workspaces/from-directory [post]
func (h *WorkspaceHandler) CreateFromDirectory(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req CreateFromDirectoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if h.RepoSvc == nil {
		InternalError(c, fmt.Errorf("repository service not configured"))
		return
	}

	// Resolve owner: explicit body wins, nil / {user,0} default to caller.
	owner := service.OwnerRef{Type: "user", ID: userID}
	if req.Owner != nil && req.Owner.Type != "" && !(req.Owner.Type == "user" && req.Owner.ID == 0) {
		owner = service.OwnerRef{Type: req.Owner.Type, ID: req.Owner.ID}
	}
	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	// 1) Register the directory as a repository (auto-init if it is not yet a
	//    git repo). finishCreate preserves the dirCreated invariant: a
	//    pre-existing user directory is never relocated.
	//
	//    First reuse an already-registered repository when the selected
	//    directory is one we already track for this owner. Without this,
	//    re-selecting a directory that is already a repo fails the whole studio
	//    create with REPO_NAME_EXISTS (the base name collides on
	//    RepositoryService.Create) — the user just wants a new workspace on the
	//    existing repo.
	var (
		repo         store.Repository
		repoWarnings []string
		err          error
	)
	if existing, ok, ferr := h.RepoSvc.FindByOwnerPath(c.Request.Context(), owner.Type, owner.ID, req.Dir); ferr != nil {
		InternalError(c, ferr)
		return
	} else if ok {
		repo = existing
	} else {
		repo, repoWarnings, err = h.RepoSvc.Create(c.Request.Context(), service.CreateRepositoryInput{
			Path:      req.Dir,
			AutoInit:  true,
			OwnerType: owner.Type,
			OwnerID:   owner.ID,
		})
		if err != nil {
			writeRepoCreateError(c, err)
			return
		}
	}

	// 2) Create the studio workspace. Branch:"" lets resolveBaseBranch pick the
	//    repo's current/default branch (e.g. the branch produced by git init).
	var createdByPtr *int64
	if userID > 0 {
		createdByPtr = &userID
	}
	result, err := h.Svc.Create(c.Request.Context(), service.CreateWorkspaceInput{
		Name:            req.Name,
		Repos:           []service.RepoBranch{{RepoID: repo.ID, Branch: ""}},
		OwnerType:       owner.Type,
		OwnerID:         owner.ID,
		CreatedBy:       createdByPtr,
		CliType:         req.CliType,
		IsStudio:        true,
		Language:        c.GetHeader("X-Niuniu-Language"),
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidCliType) {
			BadRequest(c, err.Error())
			return
		}
		InternalError(c, err)
		return
	}

	// 3) Preset the git Bash allowlist (#235). Best-effort: a failure here only
	//    means the user sees permission cards on git; it must not fail the build.
	if h.Perm != nil {
		if err := h.Perm.PresetGitBashAllowlist(c.Request.Context(), result.Workspace.ID, userID); err != nil {
			slog.Warn("workspace.CreateFromDirectory: preset git allowlist failed",
				"workspace_id", result.Workspace.ID, "error", err)
		}
	}

	// Build response: surface repo warnings (e.g. WARN_GIT_LFS_MISSING) + the
	// per-repo worktree result.
	successRepos := make([]CreateWorkspaceSuccessRepo, len(result.Repos))
	for i, r := range result.Repos {
		successRepos[i] = CreateWorkspaceSuccessRepo{
			ID:           fmt.Sprintf("%d-repo-%d", result.Workspace.ID, r.RepositoryID),
			RepositoryID: strconv.FormatInt(r.RepositoryID, 10),
			WorktreePath: r.WorktreePath,
			Branch:       r.Branch,
		}
	}
	repoErrors := make([]CreateWorkspaceErrorEntry, len(result.Errors))
	for i, e := range result.Errors {
		repoErrors[i] = CreateWorkspaceErrorEntry{RepositoryID: e.RepositoryID, Error: e.Error}
	}

	c.JSON(http.StatusCreated, CreateWorkspaceResponse{
		ID:           strconv.FormatInt(result.Workspace.ID, 10),
		Path:         result.Workspace.Path,
		Status:       result.Workspace.Status,
		Repos:        successRepos,
		Errors:       repoErrors,
		Warnings:     repoWarnings,
		RepositoryID: strconv.FormatInt(repo.ID, 10),
		IsStudio:     true,
	})
}

// writeRepoCreateError maps the prefixed error codes from
// RepositoryService.Create to HTTP responses (mirrors RepositoryHandler.Create).
func writeRepoCreateError(c *gin.Context, err error) {
	msg := err.Error()
	switch {
	case containsPrefix(msg, "CLONE_FAILED"),
		containsPrefix(msg, "PATH_CREATION_FAILED"),
		containsPrefix(msg, "PATH_DOES_NOT_EXIST"),
		containsPrefix(msg, "PATH_ACCESS_ERROR"),
		containsPrefix(msg, "NOT_A_DIRECTORY"),
		containsPrefix(msg, "NOT_A_GIT_REPO"):
		BadRequest(c, msg)
	case containsPrefix(msg, "REPO_NAME_EXISTS"):
		RespondError(c, http.StatusConflict, "REPO_NAME_EXISTS", msg[len("REPO_NAME_EXISTS:"):])
	case containsPrefix(msg, "GIT_IDENTITY_MISSING"):
		RespondError(c, http.StatusBadRequest, "GIT_IDENTITY_MISSING", msg)
	default:
		InternalError(c, err)
	}
}

// CreateForIssue creates a new workspace for an issue
// @Summary      Create a workspace for an issue
// @Description  Create a new workspace for a specific issue
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        id       path      string                  true  "Issue ID"
// @Param        request  body      CreateWorkspaceRequest  true  "Workspace details"
// @Success      201     {object}  CreateWorkspaceResponse
// @Failure      400     {object}  Error
// @Failure      500     {object}  Error
// @Router       /issues/{id}/workspace [post]
func (h *WorkspaceHandler) CreateForIssue(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req CreateWorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	// Override issue_id with URL param (convert string to int64 pointer)
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}

	// Resolve owner from the issue's project (issues inherit owner from their project).
	// Falls back to caller's personal space when h.Q is unavailable (test wiring).
	owner := service.OwnerRef{Type: "user", ID: userID}
	if h.Q != nil {
		issue, err := h.Q.GetIssue(c.Request.Context(), issueID)
		if err != nil {
			BadRequest(c, "issue not found")
			return
		}
		col, err := h.Q.GetColumn(c.Request.Context(), issue.ColumnID)
		if err != nil {
			BadRequest(c, "issue column not found")
			return
		}
		proj, err := h.Q.GetProject(c.Request.Context(), col.ProjectID)
		if err != nil {
			BadRequest(c, "issue project not found")
			return
		}
		owner = service.OwnerRef{Type: proj.OwnerType, ID: proj.OwnerID}
	}

	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	// Convert request repos to service format
	repos := make([]service.RepoBranch, len(req.Repos))
	for i, r := range req.Repos {
		repos[i] = service.RepoBranch{
			RepoID: r.RepoID,
			Branch: r.Branch,
		}
	}

	var createdByPtrForIssue *int64
	if userID > 0 {
		createdByPtrForIssue = &userID
	}
	result, err := h.Svc.Create(c.Request.Context(), service.CreateWorkspaceInput{
		IssueID:         &issueID,
		Name:            req.Name,
		Repos:           repos,
		OwnerType:       owner.Type,
		OwnerID:         owner.ID,
		CreatedBy:       createdByPtrForIssue,
		MCPServers:      req.MCPServers,
		CliType:         req.CliType,
		Language:        c.GetHeader("X-Niuniu-Language"),
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidCliType) {
			BadRequest(c, err.Error())
			return
		}
		InternalError(c, err)
		return
	}

	// Build response
	successRepos := make([]CreateWorkspaceSuccessRepo, len(result.Repos))
	for i, r := range result.Repos {
		successRepos[i] = CreateWorkspaceSuccessRepo{
			ID:           fmt.Sprintf("%d-repo-%d", result.Workspace.ID, r.RepositoryID),
			RepositoryID: strconv.FormatInt(r.RepositoryID, 10),
			WorktreePath: r.WorktreePath,
			Branch:       r.Branch,
		}
	}
	errors := make([]CreateWorkspaceErrorEntry, len(result.Errors))
	for i, e := range result.Errors {
		errors[i] = CreateWorkspaceErrorEntry{
			RepositoryID: e.RepositoryID,
			Error:        e.Error,
		}
	}

	var respIssueID *string
	if result.Workspace.IssueID.Valid {
		idStr := strconv.FormatInt(result.Workspace.IssueID.Int64, 10)
		respIssueID = &idStr
	}

	c.JSON(http.StatusCreated, CreateWorkspaceResponse{
		ID:      strconv.FormatInt(result.Workspace.ID, 10),
		IssueID: respIssueID,
		Path:    result.Workspace.Path,
		Status:  result.Workspace.Status,
		Repos:   successRepos,
		Errors:  errors,
	})
}

// @Summary      Delete a workspace
// @Description  Delete a workspace and all its worktrees. Use ?force=true to skip change checks.
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        id     path      string  true   "Workspace ID"
// @Param        force  query     bool    false  "Force delete without change check"
// @Success      204
// @Failure      404  {object}  Error
// @Failure      409  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id} [delete]
func (h *WorkspaceHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	ctx := c.Request.Context()

	force := c.Query("force") == "true"

	// If not forced, check for uncommitted changes first
	if !force {
		changes, err := h.Svc.CheckWorkspaceChanges(ctx, id)
		if err != nil {
			InternalError(c, err)
			return
		}
		if len(changes) > 0 {
			RespondErrorWithDetails(c, http.StatusConflict, "WORKSPACE_HAS_CHANGES",
				"工作空间有未提交或未合并的变更", changes)
			return
		}
	}

	// Stop PTY terminal process (shell) before deleting
	if h.AgentMgr != nil {
		_ = h.AgentMgr.Stop(ctx, id)
	}

	// Stop agent proxy session and clean up hub resources
	if h.Proxy != nil {
		h.Proxy.RemoveSession(ctx, id)
	}

	if err := h.Svc.Delete(ctx, id); err != nil {
		slog.Warn("DeleteWorkspace failed", "id", id, "error", err)
		NotFound(c, "WORKSPACE")
		return
	}
	c.Status(http.StatusNoContent)
}

type batchDeleteWorkspacesRequest struct {
	WorkspaceIDs []int64 `json:"workspace_ids" binding:"required"`
	// Force skips the per-workspace uncommitted/unmerged change check. When
	// false, a workspace with changes is reported in `skipped` (reason
	// "has_changes") instead of being destroyed.
	Force bool `json:"force"`
}

// BatchDelete asynchronously deletes multiple workspaces. Each accepted
// workspace is marked 'deleting' and cleaned up in a background goroutine; the
// response returns immediately with the accepted ids and a per-id skip list.
// The 'deleting' marker is also the dedup gate, so re-submitting an id that is
// already being deleted is skipped (reason "already_deleting"), never run twice.
// @Summary      Batch delete workspaces (async)
// @Description  Mark workspaces 'deleting' and clean them up in the background. Idempotent: ids already deleting are skipped.
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        body  body      batchDeleteWorkspacesRequest  true  "workspace ids + force"
// @Success      200   {object}  service.BatchDeleteResult
// @Failure      400   {object}  Error
// @Router       /workspaces/batch-delete [post]
func (h *WorkspaceHandler) BatchDelete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req batchDeleteWorkspacesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	ctx := c.Request.Context()

	// Authorize each id up front; unauthorized (or non-existent for this caller)
	// ids are reported as skipped without leaking which case it was. Dedup the
	// input so a repeated id can't spawn two cleanup attempts.
	authorized := make([]int64, 0, len(req.WorkspaceIDs))
	skipped := make([]service.BatchSkippedItem, 0)
	seen := make(map[int64]bool, len(req.WorkspaceIDs))
	for _, id := range req.WorkspaceIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if userID > 0 && h.Authz != nil {
			if _, err := h.Authz.CanAccessWorkspace(ctx, userID, id); err != nil {
				skipped = append(skipped, service.BatchSkippedItem{ID: id, Reason: "forbidden"})
				continue
			}
		}
		authorized = append(authorized, id)
	}

	// stop terminates the PTY agent + proxy session before the on-disk cleanup
	// runs. Invoked inside the cleanup goroutine, so it uses the passed ctx.
	stop := func(ctx context.Context, id int64) {
		if h.AgentMgr != nil {
			_ = h.AgentMgr.Stop(ctx, id)
		}
		if h.Proxy != nil {
			h.Proxy.RemoveSession(ctx, id)
		}
	}

	result := h.Svc.BatchDelete(ctx, authorized, req.Force, stop)
	result.Skipped = append(result.Skipped, skipped...)
	c.JSON(http.StatusOK, result)
}

// GetTree retrieves the directory tree of a workspace
// @Summary      Get workspace directory tree
// @Description  Get the directory tree structure of a workspace
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Workspace ID"
// @Success      200  {object}  interface{}
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/tree [get]
func (h *WorkspaceHandler) GetTree(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	tree, err := h.Svc.GetTree(c.Request.Context(), id)
	if err != nil {
		slog.Warn("GetTree failed", "id", id, "error", err)
		NotFound(c, "WORKSPACE")
		return
	}
	c.JSON(http.StatusOK, tree)
}

// GetMainTree returns the main workspace tree (excluding worktrees/)
func (h *WorkspaceHandler) GetMainTree(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	subPath := c.Query("path")

	tree, err := h.Svc.GetMainTree(c.Request.Context(), id, subPath)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, tree)
}

// GetWorktreeTree returns the tree for a specific worktree group
func (h *WorkspaceHandler) GetWorktreeTree(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	name := c.Param("name")
	subPath := c.Query("path")
	treeType := c.Query("type")

	// If type=git-status, return git status instead of file tree
	if treeType == "git-status" {
		status, err := h.Svc.GetWorktreeGitStatus(c.Request.Context(), id, name)
		if err != nil {
			slog.Warn("GetWorktreeTree(git-status) failed", "id", id, "name", name, "error", err)
			NotFound(c, "WORKTREE")
			return
		}
		c.JSON(http.StatusOK, status)
		return
	}

	tree, err := h.Svc.GetWorktreeTree(c.Request.Context(), id, name, subPath)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, tree)
}

// ListWorktreeGroups returns the list of worktree subdirectory names
func (h *WorkspaceHandler) ListWorktreeGroups(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	dirs, err := h.Svc.ListWorktreeGroups(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			NotFound(c, "WORKSPACE")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, dirs)
}

// GetWorktreeGitStatus returns the git status for a worktree
func (h *WorkspaceHandler) GetWorktreeGitStatus(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	name := c.Param("name")

	status, err := h.Svc.GetWorktreeGitStatus(c.Request.Context(), id, name)
	if err != nil {
		slog.Warn("GetWorktreeGitStatus failed", "id", id, "name", name, "error", err)
		NotFound(c, "WORKTREE")
		return
	}
	c.JSON(http.StatusOK, status)
}

// ListRepositories returns all repositories bound to a workspace
// @Summary      List workspace repositories
// @Description  Get all repositories bound to a workspace
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Workspace ID"
// @Success      200  {array}   interface{}
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories [get]
func (h *WorkspaceHandler) ListRepositories(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	repos, err := h.Svc.ListWorkspaceRepositories(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, repos)
}

// AddRepositoryRequest is the request body for adding a repository to a workspace
type AddRepositoryRequest struct {
	RepositoryID string `json:"repository_id" binding:"required"`
	Branch       string `json:"branch"`
}

// AddRepository adds a repository to a workspace by creating a worktree
// @Summary      Add repository to workspace
// @Description  Add a repository to a workspace by creating a git worktree
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        id   path      string               true  "Workspace ID"
// @Param        request  body    AddRepositoryRequest true  "Repository details"
// @Success      201  {object}  interface{}
// @Failure      400  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/repositories [post]
func (h *WorkspaceHandler) AddRepository(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.Svc.CheckNotArchived(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var req AddRepositoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	repoID, err := strconv.ParseInt(req.RepositoryID, 10, 64)
	if err != nil {
		BadRequest(c, "invalid repository_id")
		return
	}

	wsRepo, err := h.Svc.AddRepositoryToWorkspace(c.Request.Context(), id, repoID, req.Branch)
	if err != nil {
		slog.Warn("AddRepositoryToWorkspace failed", "workspace_id", id, "repo_id", repoID, "error", err)
		BadRequest(c, err.Error())
		return
	}

	c.JSON(http.StatusCreated, wsRepo)
}

// UpdateName renames a workspace
func (h *WorkspaceHandler) UpdateName(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.Svc.CheckNotArchived(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	if err := h.Svc.Rename(c.Request.Context(), id, req.Name); err != nil {
		InternalError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"name": req.Name})
}

// GetEnv returns the environment variables for a workspace
// @Summary      Get workspace env vars
// @Description  Get all environment variables for a workspace
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Workspace ID"
// @Success      200  {object}  map[string]string
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/env [get]
func (h *WorkspaceHandler) GetEnv(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	env, err := h.Svc.GetWorkspaceEnv(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"env": env})
}

// SetEnvRequest is the request body for setting workspace env vars
type SetEnvRequest struct {
	Env map[string]string `json:"env"`
}

// SetEnv updates the environment variables for a workspace
// @Summary      Set workspace env vars
// @Description  Replace all environment variables for a workspace
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        id   path      string       true  "Workspace ID"
// @Param        request  body    SetEnvRequest true  "Environment variables"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  Error
// @Failure      500  {object}  Error
// @Router       /workspaces/{id}/env [put]
func (h *WorkspaceHandler) SetEnv(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	var req SetEnvRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	if err := h.Svc.CheckNotArchived(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	if err := h.Svc.SetWorkspaceEnvVars(c.Request.Context(), id, req.Env); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"env": req.Env})
}

// SetEnvProvider binds (or unbinds, when 0/null) a subscription-platform
// provider directly to the workspace, so its base_url/models/account reach the
// agent at spawn without mounting a scene.
func (h *WorkspaceHandler) SetEnvProvider(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req struct {
		EnvProviderID *int64 `json:"env_provider_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	var pid int64
	if req.EnvProviderID != nil {
		pid = *req.EnvProviderID
	}
	if err := h.Svc.SetEnvProvider(c.Request.Context(), id, pid); err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"env_provider_id": pid})
}

// GetByIssue returns the workspace linked to an issue
// @Summary      Get workspace by issue
// @Description  Get workspace linked to a specific issue
// @Tags         Workspaces
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Issue ID"
// @Success      200  {object}  WorkspaceResponse
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /issues/{id}/workspace [get]
func (h *WorkspaceHandler) GetByIssue(c *gin.Context) {
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	workspaces, err := h.Svc.GetWorkspacesByIssue(c.Request.Context(), issueID)
	if err != nil {
		slog.Warn("GetByIssue failed", "issueID", issueID, "error", err)
		InternalError(c, err)
		return
	}
	if len(workspaces) == 0 {
		c.JSON(http.StatusOK, nil)
		return
	}
	responses := toWorkspaceResponses(workspaces)
	// Enrich archived workspaces with worktree info from DB
	for i, ws := range workspaces {
		if ws.IsArchived == 1 {
			wts, err := h.Svc.ListWorktreesWithRepository(c.Request.Context(), ws.ID)
			if err == nil {
				dtos := make([]WorktreeSidebarDTO, len(wts))
				for j, wt := range wts {
					dtos[j] = WorktreeSidebarDTO{
						Name:       wt.RName,
						RepoName:   wt.RName,
						Branch:     wt.Branch,
						BaseBranch: wt.BaseBranch,
					}
				}
				responses[i].Worktrees = dtos
			}
		}
	}
	c.JSON(http.StatusOK, responses)
}

type AvailableIssueResponse struct {
	ID              int64  `json:"id"`
	Title           string `json:"title"`
	ProjectID       int64  `json:"project_id"`
	ProjectName     string `json:"project_name"`
	LifecycleStatus string `json:"lifecycle_status"`
}

// ListAvailableIssues returns unfinished issues not already linked to a workspace
func (h *WorkspaceHandler) ListAvailableIssues(c *gin.Context) {
	rows, err := h.Svc.ListAvailableIssuesForWorkspace(c.Request.Context())
	if err != nil {
		InternalError(c, err)
		return
	}
	out := make([]AvailableIssueResponse, len(rows))
	for i, r := range rows {
		out[i] = AvailableIssueResponse{
			ID:              r.ID,
			Title:           r.Title,
			ProjectID:       r.ProjectID,
			ProjectName:     r.ProjectName,
			LifecycleStatus: r.LifecycleStatus,
		}
	}
	c.JSON(http.StatusOK, out)
}

// GetIssueDefaults returns the default repositories+branches to pre-select in
// the workspace creation dialog when opened from an issue.
//
// GET /api/workspaces/issue-defaults?issue_id=N
func (h *WorkspaceHandler) GetIssueDefaults(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Query("issue_id"), 10, 64)
	if err != nil || issueID == 0 {
		BadRequest(c, "invalid issue_id")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessIssue(c.Request.Context(), userID, issueID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	repos, err := h.Svc.GetIssueDefaultRepos(c.Request.Context(), issueID)
	if err != nil {
		slog.Warn("GetIssueDefaults failed", "issueID", issueID, "error", err)
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"repos":                   repos,
		"project_default_cli_type": h.Svc.GetIssueDefaultCliType(c.Request.Context(), issueID),
	})
}

// GetChangesSummary returns aggregated git change counts across all worktrees
func (h *WorkspaceHandler) GetChangesSummary(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	summary, err := h.Svc.GetChangesSummary(c.Request.Context(), id)
	if err != nil {
		slog.Warn("GetChangesSummary failed", "id", id, "error", err)
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": summary})
}

// GetWorktreeCommits returns commits not yet merged into the base branch
func (h *WorkspaceHandler) GetWorktreeCommits(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	name := c.Param("name")

	commits, err := h.Svc.GetWorktreeCommits(c.Request.Context(), id, name)
	if err != nil {
		slog.Warn("GetWorktreeCommits failed", "id", id, "name", name, "error", err)
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, commits)
}

// GetWorktreeCommitDetail returns detail info for a single commit in a worktree
func (h *WorkspaceHandler) GetWorktreeCommitDetail(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	name := c.Param("name")
	hash := c.Param("hash")

	// Validate hash is a hex string to prevent argument injection
	for _, ch := range hash {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			BadRequest(c, "invalid commit hash")
			return
		}
	}

	detail, err := h.Svc.GetWorktreeCommitDetail(c.Request.Context(), id, name, hash)
	if err != nil {
		slog.Warn("GetWorktreeCommitDetail failed", "id", id, "name", name, "hash", hash, "error", err)
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (h *WorkspaceHandler) Archive(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	// Stop PTY terminal process before archiving
	if h.AgentMgr != nil {
		_ = h.AgentMgr.Stop(c.Request.Context(), id)
	}

	// Stop agent proxy session
	if h.Proxy != nil {
		h.Proxy.RemoveSession(c.Request.Context(), id)
	}

	if err := h.Svc.Archive(c.Request.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			NotFound(c, "WORKSPACE")
			return
		}
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusConflict, "WORKSPACE_ALREADY_ARCHIVED", "工作空间已归档", nil)
			return
		}
		InternalError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// ListPins returns the caller's pinned workspace ids, most-recently-pinned
// first. Drives the sidebar pinned zone (per-user, server-backed).
// @Summary      List pinned workspaces
// @Tags         Workspaces
// @Produce      json
// @Success      200  {object}  map[string][]int64
// @Router       /workspaces/pins [get]
func (h *WorkspaceHandler) ListPins(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ids, err := h.Svc.ListPinnedWorkspaceIDs(c.Request.Context(), userID)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"workspace_ids": ids})
}

// Pin pins a workspace to the top of the caller's sidebar list (per-user).
// Idempotent. Requires workspace access.
// @Summary      Pin a workspace
// @Tags         Workspaces
// @Param        id   path  string  true  "Workspace ID"
// @Success      204
// @Failure      400  {object}  Error
// @Failure      403  {object}  Error
// @Router       /workspaces/{id}/pin [put]
func (h *WorkspaceHandler) Pin(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	// Pins are per-user; without an authenticated user there is nothing to
	// scope the pin to. Treat as an inert no-op (keeps no-auth / test paths sane).
	if userID <= 0 {
		c.Status(http.StatusNoContent)
		return
	}
	if h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if err := h.Svc.PinWorkspace(c.Request.Context(), userID, id); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Unpin removes the caller's pin for a workspace. Idempotent.
// @Summary      Unpin a workspace
// @Tags         Workspaces
// @Param        id   path  string  true  "Workspace ID"
// @Success      204
// @Failure      400  {object}  Error
// @Failure      403  {object}  Error
// @Router       /workspaces/{id}/pin [delete]
func (h *WorkspaceHandler) Unpin(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID <= 0 {
		c.Status(http.StatusNoContent)
		return
	}
	if h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if err := h.Svc.UnpinWorkspace(c.Request.Context(), userID, id); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *WorkspaceHandler) ListArchived(c *gin.Context) {
	results, err := h.Svc.ListArchivedWorkspaces(c.Request.Context())
	if err != nil {
		InternalError(c, err)
		return
	}

	out := make([]ArchivedWorkspaceResponse, len(results))
	for i, r := range results {
		row := r.Row
		resp := ArchivedWorkspaceResponse{
			ID:        strconv.FormatInt(row.ID, 10),
			Name:      row.Name,
			Status:    row.Status,
			CreatedAt: row.CreatedAt,
		}
		if row.IssueID.Valid {
			idStr := strconv.FormatInt(row.IssueID.Int64, 10)
			resp.IssueID = &idStr
		}
		if row.IssueTitle != "" {
			resp.IssueTitle = &row.IssueTitle
		}
		if row.ProjectName != "" {
			resp.ProjectName = &row.ProjectName
		}
		if row.ArchivedAt.Valid {
			t := row.ArchivedAt.Time
			resp.ArchivedAt = &t
		}

		wts := make([]ArchivedWorktreeDTO, len(r.Worktrees))
		for j, wt := range r.Worktrees {
			wts[j] = ArchivedWorktreeDTO{
				RepoName:   wt.RepoName,
				Branch:     wt.Branch,
				BaseBranch: wt.BaseBranch,
			}
		}
		resp.Worktrees = wts
		out[i] = resp
	}
	c.JSON(http.StatusOK, out)
}

// OverviewCreators returns the distinct list of users who created at
// least one workspace inside the requested ?owner= scope (defaulting
// to the caller's full Authz set). Powers the creator-picker dropdown
// on the workspace overview page.
//
// Returns 403 when ?owner= references an org/user outside the caller's
// access -- this prevents cross-org name leakage in the picker.
//
// @Summary      Overview creator list
// @Description  Distinct creators of workspaces inside the requested owner scope.
// @Tags         Workspaces
// @Produce      json
// @Param        owner  query  string  false  "owner filter (user:<id> or org:<slug>)"
// @Success      200    {object}  overviewCreatorsResponse
// @Failure      400    {object}  Error
// @Failure      403    {object}  Error
// @Failure      500    {object}  Error
// @Router       /workspaces/overview/creators [get]
func (h *WorkspaceHandler) OverviewCreators(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()

	ownerF, err := ParseOwnerFilter(c, h.DB)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}

	creators, err := h.Svc.ListOverviewCreators(ctx, userID, service.OwnerScope{
		Type: ownerF.Type,
		ID:   ownerF.ID,
	})
	if err != nil {
		if errors.Is(err, service.ErrForbiddenOwnerScope) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden owner scope"})
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": creators})
}

type overviewCreatorsResponse struct {
	Data []service.CreatorBrief `json:"data"`
}
