package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type ProjectService struct {
	q         *store.Queries
	db        *store.DB
	notifyHub *notify.NotificationHub
	authz     *Authz
}

func NewProjectService(q *store.Queries, db *sql.DB, notifyHub *notify.NotificationHub, authz *Authz) *ProjectService {
	return &ProjectService{q: q, db: store.Wrap(db), notifyHub: notifyHub, authz: authz}
}

// Q returns the underlying store.Queries, used by handlers that need direct
// query access (e.g. existence pre-checks).
func (s *ProjectService) Q() *store.Queries { return s.q }

func (s *ProjectService) List(ctx context.Context) ([]store.Project, error) {
	return s.ListByStatuses(ctx, []string{"active"})
}

// ListForUser returns active projects accessible to userID (personal + org memberships).
func (s *ProjectService) ListForUser(ctx context.Context, userID int64) ([]store.Project, error) {
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	return s.q.ListProjectsForOwners(ctx, store.ListProjectsForOwnersParams{
		OwnerID: owners.UserID,
		OrgIds:  orgIDs,
	})
}

// ListByStatuses returns projects matching any of the given statuses.
func (s *ProjectService) ListByStatuses(ctx context.Context, statuses []string) ([]store.Project, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	if len(statuses) == 1 {
		return s.q.ListProjects(ctx, statuses[0])
	}
	// Dynamic query for multiple statuses
	placeholders := make([]string, len(statuses))
	args := make([]interface{}, len(statuses))
	for i, st := range statuses {
		placeholders[i] = "?"
		args[i] = st
	}
	query := "SELECT id, name, description, status, owner_type, owner_id, color, created_at, updated_at FROM projects WHERE status IN (" +
		strings.Join(placeholders, ",") + ") ORDER BY created_at DESC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []store.Project
	for rows.Next() {
		var p store.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Status, &p.OwnerType, &p.OwnerID, &p.Color, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

// UpdateStatus changes a project's status (active, hidden).
func (s *ProjectService) UpdateStatus(ctx context.Context, id int64, status string) (store.Project, error) {
	if status != "active" && status != "hidden" {
		return store.Project{}, fmt.Errorf("invalid project status: %s", status)
	}
	project, err := s.q.UpdateProjectStatus(ctx, store.UpdateProjectStatusParams{
		Status: status,
		ID:     id,
	})
	if err != nil {
		return project, err
	}
	appendResourceAudit(ctx, s.db, nil, project.OwnerType, project.OwnerID, 0, "project.status_changed", "project", id,
		resourceAuditPayload(project.Name, "status", status))
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicProject, Action: "updated", ID: id})
	}
	return project, nil
}

func (s *ProjectService) Get(ctx context.Context, id int64) (store.Project, error) {
	return s.q.GetProject(ctx, id)
}

// CreateProjectParams are the inputs for ProjectService.Create. Color is
// optional: nil or empty string means "no color assigned" (stored as NULL).
type CreateProjectParams struct {
	Name        string
	Description string
	OwnerType   string
	OwnerID     int64
	Color       *string // nil or "" → NULL
	// DefaultCliType is the project's default agent CLI. Empty → 'claude' (the
	// SQL layer defaults it). Validated against ValidCliTypes.
	DefaultCliType string
}

// Create persists a new project. Audit + notify are fired only on success.
// Color is optional and follows the "nil or empty = NULL" convention so the
// HTTP layer doesn't have to distinguish between an absent JSON field and an
// empty string.
func (s *ProjectService) Create(ctx context.Context, params CreateProjectParams) (store.Project, error) {
	color := sql.NullString{}
	if params.Color != nil && *params.Color != "" {
		color = sql.NullString{String: *params.Color, Valid: true}
	}
	if _, ok := ValidCliTypes[params.DefaultCliType]; !ok {
		return store.Project{}, fmt.Errorf("%w: got %q", ErrInvalidCliType, params.DefaultCliType)
	}
	project, err := s.q.CreateProject(ctx, store.CreateProjectParams{
		Name:           params.Name,
		Description:    toNullString(params.Description),
		OwnerType:      params.OwnerType,
		OwnerID:        params.OwnerID,
		Color:          color,
		DefaultCliType: params.DefaultCliType,
	})
	if err != nil {
		return project, err
	}
	appendResourceAudit(ctx, s.db, nil, params.OwnerType, params.OwnerID, 0, "project.created", "project", project.ID,
		resourceAuditPayload(params.Name))
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicProject, Action: "updated", ID: project.ID})
	}
	return project, nil
}

// UpdateColor sets or clears the project's color token. color == nil (or "")
// clears the column to NULL; a non-empty string sets it. Use this method
// instead of Update for color edits — Update intentionally does not take a
// color parameter to avoid signature bloat.
func (s *ProjectService) UpdateColor(ctx context.Context, id int64, color *string) (store.Project, error) {
	c := sql.NullString{}
	if color != nil && *color != "" {
		c = sql.NullString{String: *color, Valid: true}
	}
	project, err := s.q.UpdateProjectColor(ctx, store.UpdateProjectColorParams{
		Color: c,
		ID:    id,
	})
	if err != nil {
		return project, err
	}
	appendResourceAudit(ctx, s.db, nil, project.OwnerType, project.OwnerID, 0, "project.updated", "project", id,
		resourceAuditPayload(project.Name, "color", c.String))
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicProject, Action: "updated", ID: id})
	}
	return project, nil
}

// UpdateDefaultCliType sets the project's default agent CLI. Mirrors UpdateColor:
// a dedicated single-field update the HTTP layer calls right after create (and
// can reuse for edits). Validated against the closed set; empty is rejected
// since an explicit update should name a concrete engine.
func (s *ProjectService) UpdateDefaultCliType(ctx context.Context, id int64, cliType string) (store.Project, error) {
	if _, ok := ValidCliTypes[cliType]; !ok || cliType == "" {
		return store.Project{}, fmt.Errorf("%w: got %q", ErrInvalidCliType, cliType)
	}
	project, err := s.q.UpdateProjectDefaultCliType(ctx, store.UpdateProjectDefaultCliTypeParams{
		DefaultCliType: cliType,
		ID:             id,
	})
	if err != nil {
		return project, err
	}
	appendResourceAudit(ctx, s.db, nil, project.OwnerType, project.OwnerID, 0, "project.updated", "project", id,
		resourceAuditPayload(project.Name, "default_cli_type", cliType))
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicProject, Action: "updated", ID: id})
	}
	return project, nil
}

// UpdateEnvProvider binds (or unbinds, when providerID=0) a subscription-platform
// provider as the project default. New workspaces created from issues under this
// project inherit it.
func (s *ProjectService) UpdateEnvProvider(ctx context.Context, id, providerID int64) (store.Project, error) {
	var v sql.NullInt64
	if providerID > 0 {
		v = sql.NullInt64{Int64: providerID, Valid: true}
	}
	if err := s.q.SetProjectEnvProvider(ctx, store.SetProjectEnvProviderParams{
		EnvProviderID: v,
		ID:            id,
	}); err != nil {
		return store.Project{}, err
	}
	project, err := s.q.GetProject(ctx, id)
	if err != nil {
		return project, err
	}
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicProject, Action: "updated", ID: id})
	}
	return project, nil
}

func (s *ProjectService) Update(ctx context.Context, id int64, name, description string) (store.Project, error) {
	project, err := s.q.UpdateProject(ctx, store.UpdateProjectParams{
		ID:          id,
		Name:        name,
		Description: toNullString(description),
	})
	if err != nil {
		return project, err
	}
	appendResourceAudit(ctx, s.db, nil, project.OwnerType, project.OwnerID, 0, "project.updated", "project", id,
		resourceAuditPayload(name))
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicProject, Action: "updated", ID: id})
	}
	return project, nil
}

func (s *ProjectService) Delete(ctx context.Context, id int64) error {
	// Load project owner before deletion for the audit entry.
	proj, projErr := s.q.GetProject(ctx, id)
	if err := s.q.DeleteProject(ctx, id); err != nil {
		return err
	}
	if projErr == nil {
		appendResourceAudit(ctx, s.db, nil, proj.OwnerType, proj.OwnerID, 0, "project.deleted", "project", id,
			resourceAuditPayload(proj.Name))
	}
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicProject, Action: "updated", ID: id})
	}
	return nil
}

// ProjectIssueStats represents issue count per kanban column for a project.
type ProjectIssueStats struct {
	ColumnName string
	Count      int64
}

// ProjectWsStats represents workspace count per status for a project.
type ProjectWsStats struct {
	Status string
	Count  int64
}

// ProjectWithStats combines a project with its aggregated stats.
type ProjectWithStats struct {
	Project store.Project
	Issues  []ProjectIssueStats
	Ws      []ProjectWsStats
}

// AttachStats enriches a list of projects with their aggregated issue and
// workspace stats. The two underlying queries are global (no per-project
// filter) — group-by-project_id maps make the join O(1) per project. Used
// by both ListWithStats (auth-disabled list) and the auth-enabled list
// branch in api/project.go (which goes through ListForUser for accessible
// project filtering).
func (s *ProjectService) AttachStats(ctx context.Context, projects []store.Project) ([]ProjectWithStats, error) {
	issueRows, err := s.q.GetIssueStatsByProject(ctx)
	if err != nil {
		return nil, err
	}

	wsRows, err := s.q.GetWorkspaceStatsByProject(ctx)
	if err != nil {
		return nil, err
	}

	// Group issue stats by project_id
	issueMap := make(map[int64][]ProjectIssueStats)
	for _, row := range issueRows {
		issueMap[row.ProjectID] = append(issueMap[row.ProjectID], ProjectIssueStats{
			ColumnName: row.ColumnName,
			Count:      row.IssueCount,
		})
	}

	// Group workspace stats by project_id
	wsMap := make(map[int64][]ProjectWsStats)
	for _, row := range wsRows {
		wsMap[row.ProjectID] = append(wsMap[row.ProjectID], ProjectWsStats{
			Status: row.Status,
			Count:  row.WsCount,
		})
	}

	// Assemble results
	result := make([]ProjectWithStats, len(projects))
	for i, p := range projects {
		result[i] = ProjectWithStats{
			Project: p,
			Issues:  issueMap[p.ID],
			Ws:      wsMap[p.ID],
		}
	}
	return result, nil
}

// ListWithStats returns projects enriched with issue and workspace stats.
// statuses filters which projects to include (e.g. ["active"], ["active","hidden"]).
func (s *ProjectService) ListWithStats(ctx context.Context, statuses []string) ([]ProjectWithStats, error) {
	projects, err := s.ListByStatuses(ctx, statuses)
	if err != nil {
		return nil, err
	}
	return s.AttachStats(ctx, projects)
}

// ProjectRepoBinding pairs a Repository with the project-specific default branch.
type ProjectRepoBinding struct {
	Repository    store.Repository
	DefaultBranch string // from project_repositories.default_branch
}

// ListRepositories returns all repositories associated with projectID, with
// the project-specific default_branch on each binding.
func (s *ProjectService) ListRepositories(ctx context.Context, projectID int64) ([]ProjectRepoBinding, error) {
	rows, err := s.q.ListProjectRepositories(ctx, projectID)
	if err != nil {
		return nil, err
	}
	bindings := make([]ProjectRepoBinding, 0, len(rows))
	for _, r := range rows {
		bindings = append(bindings, ProjectRepoBinding{
			Repository: store.Repository{
				ID:            r.ID,
				Name:          r.Name,
				Path:          r.Path,
				GitRemote:     r.GitRemote,
				DefaultBranch: r.RepoDefaultBranch,
				OwnerType:     r.OwnerType,
				OwnerID:       r.OwnerID,
				CreatedAt:     r.CreatedAt,
				UpdatedAt:     r.UpdatedAt,
			},
			DefaultBranch: r.ProjectDefaultBranch,
		})
	}
	return bindings, nil
}

// Helper function to convert string to sql.NullString
func toNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

var (
	// ErrOwnerMismatch is returned when adding a repository whose owner
	// differs from the project's owner.
	ErrOwnerMismatch = errors.New("repository owner does not match project owner")
	// ErrAlreadyAssociated is returned when AddRepository is called for a
	// repo already linked to the project.
	ErrAlreadyAssociated = errors.New("repository already associated with project")
)

// AddRepository associates a repository with a project. Validates owner
// consistency; rejects duplicates with ErrAlreadyAssociated.
func (s *ProjectService) AddRepository(ctx context.Context, projectID, repoID int64, defaultBranch string) error {
	project, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	owner, err := s.q.GetRepositoryOwner(ctx, repoID)
	if err != nil {
		return err
	}
	if owner.OwnerType != project.OwnerType || owner.OwnerID != project.OwnerID {
		return fmt.Errorf("%w: repository %d", ErrOwnerMismatch, repoID)
	}
	if err := s.q.InsertProjectRepository(ctx, store.InsertProjectRepositoryParams{
		ProjectID:     projectID,
		RepositoryID:  repoID,
		DefaultBranch: defaultBranch,
	}); err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: repository %d", ErrAlreadyAssociated, repoID)
		}
		return err
	}
	return nil
}

// RemoveRepository deletes the binding. Absent binding = no-op.
func (s *ProjectService) RemoveRepository(ctx context.Context, projectID, repoID int64) error {
	return s.q.DeleteProjectRepository(ctx, store.DeleteProjectRepositoryParams{
		ProjectID:    projectID,
		RepositoryID: repoID,
	})
}

// UpdateRepositoryBranch changes default_branch of an existing binding.
// If the binding doesn't exist the UPDATE affects 0 rows and returns nil.
func (s *ProjectService) UpdateRepositoryBranch(ctx context.Context, projectID, repoID int64, branch string) error {
	return s.q.UpdateProjectRepositoryBranch(ctx, store.UpdateProjectRepositoryBranchParams{
		DefaultBranch: branch,
		ProjectID:     projectID,
		RepositoryID:  repoID,
	})
}

// CleanupCrossOwnerRepositoriesByProject deletes any project_repositories
// row whose joined repository's owner does not match (newOwnerType, newOwnerID).
// Returns the number of rows removed (used for audit logging).
//
// Called by transfer-resource after a project's owner is updated.
func (s *ProjectService) CleanupCrossOwnerRepositoriesByProject(
	ctx context.Context, projectID int64, newOwnerType string, newOwnerID int64,
) (int64, error) {
	return s.q.DeleteCrossOwnerProjectRepositoriesByProject(ctx, store.DeleteCrossOwnerProjectRepositoriesByProjectParams{
		ProjectID: projectID, OwnerType: newOwnerType, OwnerID: newOwnerID,
	})
}

// CleanupCrossOwnerRepositoriesByRepo handles the reverse case: when a
// repository's owner changes, every project_repository row pointing at it
// whose project owner doesn't match is dropped.
func (s *ProjectService) CleanupCrossOwnerRepositoriesByRepo(
	ctx context.Context, repoID int64, newOwnerType string, newOwnerID int64,
) (int64, error) {
	return s.q.DeleteCrossOwnerProjectRepositoriesByRepo(ctx, store.DeleteCrossOwnerProjectRepositoriesByRepoParams{
		RepositoryID: repoID, OwnerType: newOwnerType, OwnerID: newOwnerID,
	})
}

// isUniqueViolation detects sqlite + pgx unique-key errors.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint") ||
		strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key value")
}
