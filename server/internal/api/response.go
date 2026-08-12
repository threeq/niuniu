package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Response DTOs that properly serialize sql.Null* types as plain values or null.

// OwnerDTO represents resource owner information for list and detail responses.
// The frontend OwnerBadge accepts {type, id, name?, slug?}.
// NOTE: name and slug are populated on a best-effort basis (N+1 per item when
// handler has access to store.Queries; otherwise type/id are sufficient for
// the badge to render a fallback).
type OwnerDTO struct {
	Type string  `json:"type"`
	ID   int64   `json:"id"`
	Name *string `json:"name,omitempty"`
	Slug *string `json:"slug,omitempty"`
}

// ownerDTOFromRef builds an OwnerDTO from type+id without a DB lookup.
// Handlers that have access to store.Queries can enrich with name/slug via enrichOwnerDTO.
func ownerDTOFromRef(ownerType string, ownerID int64) OwnerDTO {
	return OwnerDTO{Type: ownerType, ID: ownerID}
}

// ownerRef is a (type, id) pair collected from a list of rows.
type ownerRef struct {
	ownerType string
	ownerID   int64
}

// ownerLookup holds pre-fetched owner names/slugs for a page of list rows.
// Build two batch queries (one for users, one for orgs) instead of N per-row queries.
type ownerLookup struct {
	users map[int64]string                      // user_id  -> display_name or username
	orgs  map[int64]struct{ Name, Slug string } // org_id -> {name, slug}
}

// newOwnerLookup collects (ownerType, ownerID) pairs from refs, then issues at most
// two batch SELECT queries — one for users, one for organizations — and returns a
// lookup table that Build can query in O(1) per row.
//
// The db parameter is the raw *sql.DB; placeholder conversion for PostgreSQL is
// applied inline via store.ConvertPlaceholders so the helper works for both drivers.
//
// On any DB error the function falls back gracefully: it returns a non-nil lookup
// with empty maps so callers still get type+id in the DTO without panicking.
func newOwnerLookup(ctx context.Context, db *sql.DB, refs []ownerRef) (*ownerLookup, error) {
	lookup := &ownerLookup{
		users: make(map[int64]string),
		orgs:  make(map[int64]struct{ Name, Slug string }),
	}
	if len(refs) == 0 {
		return lookup, nil
	}

	// Deduplicate by type.
	var userIDs, orgIDs []int64
	seenUser := make(map[int64]bool)
	seenOrg := make(map[int64]bool)
	for _, r := range refs {
		switch r.ownerType {
		case "user":
			if !seenUser[r.ownerID] {
				seenUser[r.ownerID] = true
				userIDs = append(userIDs, r.ownerID)
			}
		case "org":
			if !seenOrg[r.ownerID] {
				seenOrg[r.ownerID] = true
				orgIDs = append(orgIDs, r.ownerID)
			}
		}
	}

	// Batch query: users.
	if len(userIDs) > 0 {
		placeholders := make([]string, len(userIDs))
		args := make([]interface{}, len(userIDs))
		for i, id := range userIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		query := fmt.Sprintf(
			"SELECT id, COALESCE(NULLIF(display_name, ''), username) AS name FROM users WHERE id IN (%s)",
			strings.Join(placeholders, ","),
		)
		if store.Driver == "postgres" {
			query = store.ConvertPlaceholders(query)
		}
		rows, err := db.QueryContext(ctx, query, args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id int64
				var name string
				if scanErr := rows.Scan(&id, &name); scanErr == nil {
					lookup.users[id] = name
				}
			}
		}
	}

	// Batch query: organizations.
	if len(orgIDs) > 0 {
		placeholders := make([]string, len(orgIDs))
		args := make([]interface{}, len(orgIDs))
		for i, id := range orgIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		query := fmt.Sprintf(
			"SELECT id, name, slug FROM organizations WHERE id IN (%s)",
			strings.Join(placeholders, ","),
		)
		if store.Driver == "postgres" {
			query = store.ConvertPlaceholders(query)
		}
		rows, err := db.QueryContext(ctx, query, args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id int64
				var name, slug string
				if scanErr := rows.Scan(&id, &name, &slug); scanErr == nil {
					lookup.orgs[id] = struct{ Name, Slug string }{name, slug}
				}
			}
		}
	}

	return lookup, nil
}

// Build returns an OwnerDTO for the given (ownerType, ownerID), enriched with
// name/slug from the pre-fetched maps if available.
func (l *ownerLookup) Build(ownerType string, ownerID int64) OwnerDTO {
	dto := OwnerDTO{Type: ownerType, ID: ownerID}
	if l == nil {
		return dto
	}
	switch ownerType {
	case "user":
		if name, ok := l.users[ownerID]; ok {
			dto.Name = &name
		}
	case "org":
		if org, ok := l.orgs[ownerID]; ok {
			dto.Name = &org.Name
			dto.Slug = &org.Slug
		}
	}
	return dto
}

// ProjectResponse represents a project in the system
type ProjectResponse struct {
	ID           int64                      `json:"id"`
	Name         string                     `json:"name" example:"My Project"`
	Description  *string                    `json:"description,omitempty" example:"A sample project"`
	Status       string                     `json:"status" example:"active"`
	Owner        OwnerDTO                   `json:"owner"`
	Color        *string                    `json:"color" example:"emerald"`
	DefaultCliType string                   `json:"default_cli_type" example:"claude"`
	EnvProviderID  *int64                    `json:"env_provider_id,omitempty"`
	IssueStats   []ColumnIssueStat          `json:"issue_stats,omitempty"`
	WsStats      []WsStatusStat             `json:"ws_stats,omitempty"`
	Repositories []ProjectRepositoryBinding `json:"repositories,omitempty"`
	CreatedAt    time.Time                  `json:"created_at" example:"2026-03-17T12:00:00Z"`
	UpdatedAt    time.Time                  `json:"updated_at" example:"2026-03-17T12:00:00Z"`
}

// ColumnIssueStat represents issue count for a kanban column.
type ColumnIssueStat struct {
	ColumnName string `json:"column_name"`
	Count      int64  `json:"count"`
}

// WsStatusStat represents workspace count for a status.
type WsStatusStat struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

// ProjectRepositoryBinding is one entry in the project's associated-repository list.
type ProjectRepositoryBinding struct {
	RepositoryID         int64  `json:"repository_id"`
	Name                 string `json:"name"`
	Path                 string `json:"path"`
	RepoDefaultBranch    string `json:"repo_default_branch"`
	ProjectDefaultBranch string `json:"project_default_branch"`
}

func toProjectRepositoryBindings(bs []service.ProjectRepoBinding) []ProjectRepositoryBinding {
	out := make([]ProjectRepositoryBinding, len(bs))
	for i, b := range bs {
		repoBranch := ""
		if b.Repository.DefaultBranch.Valid {
			repoBranch = b.Repository.DefaultBranch.String
		}
		out[i] = ProjectRepositoryBinding{
			RepositoryID:         b.Repository.ID,
			Name:                 b.Repository.Name,
			Path:                 b.Repository.Path,
			RepoDefaultBranch:    repoBranch,
			ProjectDefaultBranch: b.DefaultBranch,
		}
	}
	return out
}

func toProjectResponse(p store.Project) ProjectResponse {
	var envProviderID *int64
	if p.EnvProviderID.Valid {
		v := p.EnvProviderID.Int64
		envProviderID = &v
	}
	var desc *string
	if p.Description.Valid {
		desc = &p.Description.String
	}
	var color *string
	if p.Color.Valid {
		s := p.Color.String
		color = &s
	}
	return ProjectResponse{
		ID:             p.ID,
		Name:           p.Name,
		Description:    desc,
		Status:         p.Status,
		Owner:          ownerDTOFromRef(p.OwnerType, p.OwnerID),
		Color:          color,
		DefaultCliType: normalizeCliType(p.DefaultCliType),
		EnvProviderID:  envProviderID,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

func toProjectResponses(ps []store.Project) []ProjectResponse {
	out := make([]ProjectResponse, len(ps))
	for i, p := range ps {
		out[i] = toProjectResponse(p)
	}
	return out
}

// toProjectResponsesWithLookup builds responses for a page of projects using a
// pre-fetched ownerLookup so owner name/slug are filled with two batch queries
// instead of N per-row queries.
func toProjectResponsesWithLookup(ps []store.Project, lk *ownerLookup) []ProjectResponse {
	out := make([]ProjectResponse, len(ps))
	for i, p := range ps {
		resp := toProjectResponse(p)
		resp.Owner = lk.Build(p.OwnerType, p.OwnerID)
		out[i] = resp
	}
	return out
}

func toProjectResponseWithStats(pw service.ProjectWithStats) ProjectResponse {
	resp := toProjectResponse(pw.Project)
	if len(pw.Issues) > 0 {
		resp.IssueStats = make([]ColumnIssueStat, len(pw.Issues))
		for i, is := range pw.Issues {
			resp.IssueStats[i] = ColumnIssueStat{ColumnName: is.ColumnName, Count: is.Count}
		}
	}
	if len(pw.Ws) > 0 {
		resp.WsStats = make([]WsStatusStat, len(pw.Ws))
		for i, ws := range pw.Ws {
			resp.WsStats[i] = WsStatusStat{Status: ws.Status, Count: ws.Count}
		}
	}
	return resp
}

func toProjectResponsesWithStats(pws []service.ProjectWithStats) []ProjectResponse {
	out := make([]ProjectResponse, len(pws))
	for i, pw := range pws {
		out[i] = toProjectResponseWithStats(pw)
	}
	return out
}

// toProjectResponsesWithStatsAndLookup builds responses for a page using a
// pre-fetched ownerLookup to avoid N+1 owner name queries.
func toProjectResponsesWithStatsAndLookup(pws []service.ProjectWithStats, lk *ownerLookup) []ProjectResponse {
	out := make([]ProjectResponse, len(pws))
	for i, pw := range pws {
		resp := toProjectResponseWithStats(pw)
		resp.Owner = lk.Build(pw.Project.OwnerType, pw.Project.OwnerID)
		out[i] = resp
	}
	return out
}

// LabelResponse represents a label attached to an issue.
type LabelResponse struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	CreatedBy   int64  `json:"created_by"`
}

// IssueAssigneeResponse represents one assignee on an issue.
type IssueAssigneeResponse struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// IssueResponse represents an issue in the system
type IssueResponse struct {
	ID                int64                   `json:"id"`
	ColumnID          int64                   `json:"column_id"`
	ProjectID         int64                   `json:"project_id,omitempty"`
	Title             string                  `json:"title" example:"Implement user authentication"`
	Description       *string                 `json:"description,omitempty" example:"Add JWT-based user authentication"`
	Priority          int64                   `json:"priority" example:"1"`
	Labels            []LabelResponse         `json:"labels"`
	Position          int64                   `json:"position" example:"0"`
	LifecycleStatus   string                  `json:"lifecycle_status" example:"created"`
	Assignees         []IssueAssigneeResponse `json:"assignees"`
	StartDate         string                  `json:"start_date"`
	DueDate           string                  `json:"due_date"`
	EstimateType      string                  `json:"estimate_type"`
	Estimate          float64                 `json:"estimate"`
	ActualTime        float64                 `json:"actual_time"`
	// GoalCondition is the per-issue autohost LLM-judge stop condition.
	// Empty string means "use the workspace-level fallback (or no judge)".
	GoalCondition string `json:"goal_condition"`
	// Executable Epic: ParentIssueID is the parent Epic (nil = top-level),
	// IssueType is 'task' | 'epic', ExecWave is the child execution wave,
	// ExecStatus is the Epic execution lifecycle (idle/running/done/failed/paused).
	ParentIssueID  *int64             `json:"parent_issue_id"`
	IssueType      string             `json:"issue_type"`
	ExecWave       int64              `json:"exec_wave"`
	ExecStatus     string             `json:"exec_status"`
	// ExecStatusReason is the human-readable reason behind a terminal exec_status
	// (blocked-needs-human / abandoned-with-reason, spec §19). Nil when empty.
	ExecStatusReason *string            `json:"exec_status_reason,omitempty"`
	ChecklistStats   *ChecklistStatsDTO `json:"checklist_stats,omitempty"`
	// External tracker linkage. Empty when the issue is not linked to any
	// external source. ExternalSource is the provider name (e.g. "github");
	// ExternalID is canonical ("owner/repo#123"); ExternalURL is the
	// upstream permalink.
	ExternalSource             string                        `json:"external_source,omitempty"`
	ExternalID                 string                        `json:"external_id,omitempty"`
	ExternalURL                string                        `json:"external_url,omitempty"`
	ExternalCommentsSnapshot   []integration.ExternalComment `json:"external_comments_snapshot,omitempty"`
	ExternalCommentsSnapshotAt *time.Time                    `json:"external_comments_snapshot_at,omitempty"`
	CreatedAt                  time.Time                     `json:"created_at" example:"2026-03-17T12:00:00Z"`
	UpdatedAt                  time.Time                     `json:"updated_at" example:"2026-03-17T12:00:00Z"`
}

type ChecklistStatsDTO struct {
	Total     int64 `json:"total"`
	Completed int64 `json:"completed"`
}

type TaskStatsDTO struct {
	Total       int     `json:"total"`
	Completed   int     `json:"completed"`
	CurrentTask *string `json:"current_task,omitempty"`
}

type BgTaskHighlight struct {
	Kind         string `json:"kind"` // "bash" | "subagent" | "wakeup"
	Title        string `json:"title"`
	StartedAt    string `json:"started_at,omitempty"`    // RFC3339, set for bash/subagent
	ScheduledFor string `json:"scheduled_for,omitempty"` // RFC3339, set for wakeup
}

type BgTaskAggregateDTO struct {
	AgentBusy     bool             `json:"agent_busy"`
	BashCount     int              `json:"bash_count"`
	WakeupCount   int              `json:"wakeup_count"`
	SubagentCount int              `json:"subagent_count"`
	CronCount     int              `json:"cron_count"`
	Highlight     *BgTaskHighlight `json:"highlight,omitempty"`
}

func toIssueResponse(d service.IssueDetail) IssueResponse {
	var desc *string
	if d.Description.Valid {
		desc = &d.Description.String
	}
	labels := make([]LabelResponse, 0, len(d.Labels))
	for _, l := range d.Labels {
		labels = append(labels, LabelResponse{
			ID:          l.ID,
			ProjectID:   l.ProjectID,
			Name:        l.Name,
			Color:       l.Color,
			Description: l.Description,
			CreatedAt:   l.CreatedAt.Format(time.RFC3339),
			CreatedBy:   l.CreatedBy,
		})
	}
	assignees := make([]IssueAssigneeResponse, 0, len(d.Assignees))
	for _, a := range d.Assignees {
		assignees = append(assignees, IssueAssigneeResponse{
			ID:          a.ID,
			Username:    a.Username,
			DisplayName: a.DisplayName,
		})
	}
	var parentIssueID *int64
	if d.ParentIssueID.Valid {
		v := d.ParentIssueID.Int64
		parentIssueID = &v
	}
	var execStatusReason *string
	if d.ExecStatusReason.Valid && d.ExecStatusReason.String != "" {
		v := d.ExecStatusReason.String
		execStatusReason = &v
	}
	return IssueResponse{
		ID:                         d.ID,
		ColumnID:                   d.ColumnID,
		Title:                      d.Title,
		Description:                desc,
		Priority:                   nullInt64ValResp(d.Priority),
		Labels:                     labels,
		Position:                   d.Position,
		LifecycleStatus:            d.LifecycleStatus,
		Assignees:                  assignees,
		StartDate:                  nullStringValResp(d.StartDate),
		DueDate:                    nullStringValResp(d.DueDate),
		EstimateType:               nullStringValResp(d.EstimateType),
		Estimate:                   nullFloat64ValResp(d.Estimate),
		ActualTime:                 nullFloat64ValResp(d.ActualTime),
		GoalCondition:              d.GoalCondition,
		ParentIssueID:              parentIssueID,
		IssueType:                  d.IssueType,
		ExecWave:                   d.ExecWave,
		ExecStatus:                 d.ExecStatus,
		ExecStatusReason:           execStatusReason,
		ExternalSource:             nullStringValResp(d.ExternalSource),
		ExternalID:                 nullStringValResp(d.ExternalID),
		ExternalURL:                nullStringValResp(d.ExternalUrl),
		ExternalCommentsSnapshot:   parseExternalCommentsSnapshot(d.ExternalCommentsSnapshot),
		ExternalCommentsSnapshotAt: nullTimeToPtr(d.ExternalCommentsSnapshotAt),
		CreatedAt:                  d.CreatedAt,
		UpdatedAt:                  d.UpdatedAt,
	}
}

// parseExternalCommentsSnapshot decodes the JSON array stored in
// issues.external_comments_snapshot. Empty / NULL / malformed input
// returns nil — the SPA renders a "never synced" placeholder via the
// snapshot_at timestamp gate, so silently dropping a malformed payload
// is preferable to surfacing a 500 from issue-detail load.
func parseExternalCommentsSnapshot(s sql.NullString) []integration.ExternalComment {
	if !s.Valid || s.String == "" {
		return nil
	}
	var out []integration.ExternalComment
	if err := json.Unmarshal([]byte(s.String), &out); err != nil {
		return nil
	}
	return out
}

func nullStringValResp(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func nullInt64ValResp(ni sql.NullInt64) int64 {
	if ni.Valid {
		return ni.Int64
	}
	return 0
}

func nullFloat64ValResp(nf sql.NullFloat64) float64 {
	if nf.Valid {
		return nf.Float64
	}
	return 0
}

func toIssueResponses(issues []service.IssueDetail) []IssueResponse {
	out := make([]IssueResponse, len(issues))
	for i, issue := range issues {
		out[i] = toIssueResponse(issue)
	}
	return out
}

// RepositoryResponse represents a repository in the system
type RepositoryResponse struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name" example:"My Repository"`
	Path          string    `json:"path" example:"/path/to/repo"`
	GitRemote     *string   `json:"git_remote,omitempty" example:"https://github.com/niuniu-dev/niuniu.git"`
	DefaultBranch *string   `json:"default_branch,omitempty" example:"main"`
	TotalBranches int       `json:"total_branches" example:"3"`
	Owner         OwnerDTO  `json:"owner"`
	CreatedAt     time.Time `json:"created_at" example:"2026-03-17T12:00:00Z"`
	UpdatedAt     time.Time `json:"updated_at" example:"2026-03-17T12:00:00Z"`
	// Warnings carries non-fatal notices from create (e.g. WARN_GIT_LFS_MISSING
	// when auto-init could not enable Git LFS). Omitted when empty.
	Warnings []string `json:"warnings,omitempty"`
}

func toRepositoryResponse(r store.Repository) RepositoryResponse {
	var remote, branch *string
	if r.GitRemote.Valid {
		remote = &r.GitRemote.String
	}
	if r.DefaultBranch.Valid {
		branch = &r.DefaultBranch.String
	}
	return RepositoryResponse{
		ID:            r.ID,
		Name:          r.Name,
		Path:          r.Path,
		GitRemote:     remote,
		DefaultBranch: branch,
		Owner:         ownerDTOFromRef(r.OwnerType, r.OwnerID),
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}

func ToRepositoryResponses(rs []store.Repository) []RepositoryResponse {
	out := make([]RepositoryResponse, len(rs))
	for i, r := range rs {
		out[i] = toRepositoryResponse(r)
	}
	return out
}

// ToRepositoryResponsesWithLookup builds responses for a page of repositories
// using a pre-fetched ownerLookup to avoid N+1 owner name queries.
func ToRepositoryResponsesWithLookup(rs []store.Repository, lk *ownerLookup) []RepositoryResponse {
	out := make([]RepositoryResponse, len(rs))
	for i, r := range rs {
		resp := toRepositoryResponse(r)
		resp.Owner = lk.Build(r.OwnerType, r.OwnerID)
		out[i] = resp
	}
	return out
}

// WorktreeSidebarDTO holds per-worktree git status for sidebar display.
type WorktreeSidebarDTO struct {
	Name         string `json:"name"`
	RepoName     string `json:"repo_name"`
	Branch       string `json:"branch"`
	BaseBranch   string `json:"base_branch"`
	ChangesCount int    `json:"changes_count"`
	AheadCount   int    `json:"ahead_count"`
}

// WorkspaceResponse represents a workspace in the system
type WorkspaceResponse struct {
	ID               string               `json:"id"`
	Name             string               `json:"name" example:"My Workspace"`
	IssueID          *string              `json:"issue_id,omitempty"`
	Path             string               `json:"path" example:"/path/to/workspace"`
	Status           string               `json:"status" example:"active"`
	AgentPid         *int64               `json:"agent_pid,omitempty" example:"12345"`
	AgentStatus      string               `json:"agent_status" example:"idle"`
	ChangesCount     int                  `json:"changes_count"`
	AheadCount       int                  `json:"ahead_count"`
	MessageCount     *int64               `json:"message_count,omitempty"`
	LastMessageAt    *time.Time           `json:"last_message_at,omitempty"`
	Owner            OwnerDTO             `json:"owner"`
	ProjectName      *string              `json:"project_name,omitempty"`
	ProjectOwnerType *string              `json:"project_owner_type,omitempty"`
	ProjectOwnerID   *int64               `json:"project_owner_id,omitempty"`
	ProjectOwnerName *string              `json:"project_owner_name,omitempty"`
	LifecycleStatus  *string              `json:"lifecycle_status,omitempty"`
	// IssueType is the linked issue's type ('task'|'epic'). Omitted for regular
	// tasks and workspaces with no linked issue; the SPA renders an Epic marker
	// in the sidebar only when this is "epic".
	IssueType        *string              `json:"issue_type,omitempty"`
	// ParentIssueID is the linked issue's parent (the Epic it belongs to).
	// Present only when the linked issue is a sub-issue; the SPA renders a
	// sub-issue marker in the sidebar when this is set.
	ParentIssueID    *int64               `json:"parent_issue_id,omitempty"`
	TaskStats        *TaskStatsDTO        `json:"task_stats,omitempty"`
	Worktrees        []WorktreeSidebarDTO `json:"worktrees,omitempty"`
	ScheduleCount    *int                 `json:"schedule_count,omitempty"`
	IsArchived       int64                `json:"is_archived"`
	// IsStudio marks workspaces created via the studio "from local directory"
	// flow (#232). The SPA shows the delivery hint (#238) when true.
	IsStudio         int64                `json:"is_studio"`
	ArchivedAt       *time.Time           `json:"archived_at,omitempty"`
	BgTasks          *BgTaskAggregateDTO  `json:"bg_tasks,omitempty"`
	CreatedBy        *int64               `json:"created_by,omitempty"`
	CreatorOwner     *OwnerDTO            `json:"creator_owner,omitempty"`
	CliType          string               `json:"cli_type" example:"claude"`
	// Codex-only fields. Always populated for codex workspaces (defaults
	// applied if column is empty). Ignored by the SPA for claude workspaces.
	CodexSandboxMode    string    `json:"codex_sandbox_mode,omitempty"`
	CodexApprovalPolicy string    `json:"codex_approval_policy,omitempty"`
	// EnvProviderID is the directly-bound subscription-platform provider (issue
	// #653). nil = no direct binding (env comes from scene/ explicit workspace_env).
	EnvProviderID       *int64    `json:"env_provider_id,omitempty"`
	CreatedAt           time.Time `json:"created_at" example:"2026-03-17T12:00:00Z"`
	UpdatedAt           time.Time `json:"updated_at" example:"2026-03-17T12:00:00Z"`
}

func toWorkspaceResponse(w store.Workspace) WorkspaceResponse {
	var pid *int64
	if w.AgentPid.Valid {
		pid = &w.AgentPid.Int64
	}
	var agentStatus string
	if w.AgentStatus.Valid {
		agentStatus = w.AgentStatus.String
	}
	var issueID *string
	if w.IssueID.Valid {
		idStr := strconv.FormatInt(w.IssueID.Int64, 10)
		issueID = &idStr
	}
	var archivedAt *time.Time
	if w.ArchivedAt.Valid {
		t := w.ArchivedAt.Time
		archivedAt = &t
	}
	var createdBy *int64
	if w.CreatedBy.Valid {
		v := w.CreatedBy.Int64
		createdBy = &v
	}
	var envProviderID *int64
	if w.EnvProviderID.Valid {
		v := w.EnvProviderID.Int64
		envProviderID = &v
	}
	return WorkspaceResponse{
		ID:                  strconv.FormatInt(w.ID, 10),
		Name:                w.Name,
		IssueID:             issueID,
		Path:                w.Path,
		Status:              w.Status,
		AgentPid:            pid,
		AgentStatus:         agentStatus,
		Owner:               ownerDTOFromRef(w.OwnerType, w.OwnerID),
		IsArchived:          w.IsArchived,
		IsStudio:            w.IsStudio,
		ArchivedAt:          archivedAt,
		CreatedBy:           createdBy,
			CliType:             normalizeCliType(w.CliType),
			CodexSandboxMode:    normalizeCodexSandbox(w.CodexSandboxMode),
		CodexApprovalPolicy: normalizeCodexApproval(w.CodexApprovalPolicy),
		EnvProviderID:       envProviderID,
		CreatedAt:           w.CreatedAt,
		UpdatedAt:           w.UpdatedAt,
	}
}

// normalizeCodexSandbox / normalizeCodexApproval mirror normalizeCliType:
// fall back to niuniu's full-bypass Codex default when the DB row predates
// the column.
func normalizeCodexSandbox(v string) string {
	switch v {
	case "read-only", "workspace-write", "danger-full-access":
		return v
	default:
		return "danger-full-access"
	}
}
func normalizeCodexApproval(v string) string {
	switch v {
	case "untrusted", "on-failure", "on-request", "never":
		return v
	default:
		return "never"
	}
}

// normalizeCliType collapses empty / unknown DB values to "claude". Legacy
// rows in an upgraded DB may have NULL or empty cli_type until the
// addWorkspaceCliTypeColumn migration backfills them, and a defensive
// fallback here keeps the API contract tight (cli_type is never empty in
// responses).
func normalizeCliType(v string) string {
	switch v {
	case "claude", "codex", "qwen":
		return v
	default:
		return "claude"
	}
}

func toWorkspaceResponses(ws []store.Workspace) []WorkspaceResponse {
	out := make([]WorkspaceResponse, len(ws))
	for i, w := range ws {
		out[i] = toWorkspaceResponse(w)
	}
	return out
}

// toWorkspaceResponsesWithLookup builds responses for a page of workspaces
// using a pre-fetched ownerLookup to avoid N+1 owner name queries.
func toWorkspaceResponsesWithLookup(ws []store.Workspace, lk *ownerLookup) []WorkspaceResponse {
	out := make([]WorkspaceResponse, len(ws))
	for i, w := range ws {
		resp := toWorkspaceResponse(w)
		resp.Owner = lk.Build(w.OwnerType, w.OwnerID)
		out[i] = resp
	}
	return out
}

// SidebarMeta holds enriched data computed outside the DB row.
type SidebarMeta struct {
	ChangesCount    int
	AheadCount      int
	MessageCount    *int64
	LastMessageAt   *time.Time
	ProjectName     *string
	LifecycleStatus *string
	IssueType       *string
	ParentIssueID   *int64
	TaskStats       *TaskStatsDTO
	Worktrees       []WorktreeSidebarDTO
	ScheduleCount   *int
	BgTasks         *BgTaskAggregateDTO
}

func toWorkspaceResponseWithMeta(w store.Workspace, meta SidebarMeta) WorkspaceResponse {
	resp := toWorkspaceResponse(w)
	resp.ChangesCount = meta.ChangesCount
	resp.AheadCount = meta.AheadCount
	resp.MessageCount = meta.MessageCount
	resp.LastMessageAt = meta.LastMessageAt
	resp.ProjectName = meta.ProjectName
	resp.LifecycleStatus = meta.LifecycleStatus
	resp.IssueType = meta.IssueType
	resp.ParentIssueID = meta.ParentIssueID
	resp.TaskStats = meta.TaskStats
	resp.Worktrees = meta.Worktrees
	resp.ScheduleCount = meta.ScheduleCount
	resp.BgTasks = meta.BgTasks
	return resp
}

type ArchivedWorkspaceResponse struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	IssueID     *string               `json:"issue_id,omitempty"`
	IssueTitle  *string               `json:"issue_title,omitempty"`
	ProjectName *string               `json:"project_name,omitempty"`
	Status      string                `json:"status"`
	Worktrees   []ArchivedWorktreeDTO `json:"worktrees"`
	ArchivedAt  *time.Time            `json:"archived_at"`
	CreatedAt   time.Time             `json:"created_at"`
}

type ArchivedWorktreeDTO struct {
	RepoName   string `json:"repo_name"`
	Branch     string `json:"branch"`
	BaseBranch string `json:"base_branch"`
}

// CommentResponse represents a comment in the system
type CommentResponse struct {
	ID          int64     `json:"id"`
	WorkspaceID int64     `json:"workspace_id"`
	Repo        string    `json:"repo" example:"niuniu"`
	FilePath    string    `json:"file_path" example:"/path/to/file.go"`
	LineNumber  *int64    `json:"line_number,omitempty" example:"42"`
	Content     string    `json:"content" example:"This is a great comment!"`
	SentToAgent *bool     `json:"sent_to_agent,omitempty" example:"true"`
	CreatedAt   time.Time `json:"created_at" example:"2026-03-17T12:00:00Z"`
}

func toCommentResponse(c store.Comment) CommentResponse {
	var lineNum *int64
	if c.LineNumber.Valid {
		lineNum = &c.LineNumber.Int64
	}
	var sent *bool
	if c.SentToAgent.Valid {
		sent = &c.SentToAgent.Bool
	}
	return CommentResponse{
		ID:          c.ID,
		WorkspaceID: c.WorkspaceID,
		Repo:        c.Repo,
		FilePath:    c.FilePath,
		LineNumber:  lineNum,
		Content:     c.Content,
		SentToAgent: sent,
		CreatedAt:   c.CreatedAt,
	}
}

func toCommentResponses(cs []store.Comment) []CommentResponse {
	out := make([]CommentResponse, len(cs))
	for i, c := range cs {
		out[i] = toCommentResponse(c)
	}
	return out
}

// CreateWorkspaceSuccessRepo represents a successfully created worktree in a workspace
type CreateWorkspaceSuccessRepo struct {
	ID           string `json:"id"`
	RepositoryID string `json:"repository_id"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
}

// CreateWorkspaceErrorEntry represents an error for a specific repository during workspace creation
type CreateWorkspaceErrorEntry struct {
	RepositoryID string `json:"repository_id"`
	Error        string `json:"error"`
}

// CreateWorkspaceResponse represents the response after creating a workspace
type CreateWorkspaceResponse struct {
	ID       string                       `json:"id"`
	IssueID  *string                      `json:"issue_id,omitempty"`
	Path     string                       `json:"path"`
	Status   string                       `json:"status"`
	Repos    []CreateWorkspaceSuccessRepo `json:"repositories"`
	Errors   []CreateWorkspaceErrorEntry  `json:"errors"`
	Warnings []string                     `json:"warnings,omitempty"`
	// RepositoryID is set by the studio from-directory flow (#232) so the client
	// can reference the auto-created/bound repo. Omitted by other create paths.
	RepositoryID string `json:"repository_id,omitempty"`
	// IsStudio marks a studio workspace (#232) so the SPA can show the delivery
	// hint (#238). Omitted (false) for regular workspaces.
	IsStudio bool `json:"is_studio,omitempty"`
}

// Helper to convert int64 to string for JSON serialization
func int64ToString(i int64) string {
	return strconv.FormatInt(i, 10)
}

// HarnessCheckResponse represents a harness check result with properly serialized nullable fields.
type HarnessCheckResponse struct {
	ID          int64     `json:"id"`
	WorkspaceID int64     `json:"workspace_id"`
	RunID       *int64    `json:"run_id"`
	SpecID      int64     `json:"spec_id"`
	PhaseName   string    `json:"phase_name"`
	Status      string    `json:"status"`
	Message     string    `json:"message"`
	Details     string    `json:"details"`
	DurationMs  int64     `json:"duration_ms"`
	CreatedAt   time.Time `json:"created_at"`
	Category    string    `json:"category"`
	SpecName    string    `json:"spec_name"`
	Severity    string    `json:"severity"`
}

func toHarnessCheckResponse(c store.ListHarnessChecksByWorkspaceRow) HarnessCheckResponse {
	var runID *int64
	if c.RunID.Valid {
		runID = &c.RunID.Int64
	}
	return HarnessCheckResponse{
		ID:          c.ID,
		WorkspaceID: c.WorkspaceID,
		RunID:       runID,
		SpecID:      c.SpecID,
		PhaseName:   c.PhaseName,
		Status:      c.Status,
		Message:     c.Message,
		Details:     c.Details,
		DurationMs:  c.DurationMs,
		CreatedAt:   c.CreatedAt,
		Category:    c.Category,
		SpecName:    c.SpecName,
		Severity:    c.Severity,
	}
}

func toHarnessCheckResponses(checks []store.ListHarnessChecksByWorkspaceRow) []HarnessCheckResponse {
	out := make([]HarnessCheckResponse, len(checks))
	for i, c := range checks {
		out[i] = toHarnessCheckResponse(c)
	}
	return out
}

// QuickActionResponse represents a quick action
type QuickActionResponse struct {
	ID        int64     `json:"id"`
	Label     string    `json:"label"`
	Content   string    `json:"content"`
	AutoSend  bool      `json:"auto_send"`
	Position  int64     `json:"position"`
	Owner     OwnerDTO  `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toQuickActionResponse(qa store.QuickAction) QuickActionResponse {
	return QuickActionResponse{
		ID:        qa.ID,
		Label:     qa.Label,
		Content:   qa.Content,
		AutoSend:  qa.AutoSend != 0,
		Position:  qa.Position,
		Owner:     ownerDTOFromRef(qa.OwnerType, qa.OwnerID),
		CreatedAt: qa.CreatedAt,
		UpdatedAt: qa.UpdatedAt,
	}
}

func toQuickActionResponses(qas []store.QuickAction) []QuickActionResponse {
	out := make([]QuickActionResponse, len(qas))
	for i, qa := range qas {
		out[i] = toQuickActionResponse(qa)
	}
	return out
}

// toQuickActionResponsesWithLookup builds responses for a page of quick actions
// using a pre-fetched ownerLookup to avoid N+1 owner name queries.
func toQuickActionResponsesWithLookup(qas []store.QuickAction, lk *ownerLookup) []QuickActionResponse {
	out := make([]QuickActionResponse, len(qas))
	for i, qa := range qas {
		resp := toQuickActionResponse(qa)
		resp.Owner = lk.Build(qa.OwnerType, qa.OwnerID)
		out[i] = resp
	}
	return out
}
