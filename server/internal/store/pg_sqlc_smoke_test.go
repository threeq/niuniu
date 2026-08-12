package store_test

// PG sqlc smoke tests. These exercise every generated sqlc method for the
// six domains listed in the ws-326 brief (workspaces, workspaces_owner_filter,
// organizations, permission, labels, claude_accounts) against a real
// PostgreSQL backend. The point is to catch PG-only SQL errors (SQLSTATE
// 42P18 untyped parameter, 42601 syntax error, ON CONFLICT rewrite edge
// cases) at PARSE/PLAN time — these don't surface under SQLite's loose
// typing.
//
// The tests do NOT re-assert business behavior; that's covered by the
// existing SQLite test suite. Failure mode here is "PG rejected the SQL",
// not "wrong row was returned".
//
// Auto-skips when Docker is unavailable and NIUNIU_TEST_PG_DSN is unset.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// pgSmokeSeed installs a minimum row graph required to exercise the smoke
// tests: 1 user, 1 project + column + issue, 1 workspace, 1 org.
type pgSmokeSeed struct {
	UserID      int64
	OrgID       int64
	ProjectID   int64
	ColumnID    int64
	IssueID     int64
	WorkspaceID int64
}

func seedPGSmoke(t *testing.T, raw *sql.DB) pgSmokeSeed {
	t.Helper()
	var s pgSmokeSeed
	if err := raw.QueryRow(
		`INSERT INTO users (username, password_hash, role) VALUES ('smoke', 'x', 'admin') RETURNING id`,
	).Scan(&s.UserID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := raw.QueryRow(
		`INSERT INTO organizations (slug, name, created_by) VALUES ('smoke-org', 'Smoke', $1) RETURNING id`,
		s.UserID,
	).Scan(&s.OrgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if err := raw.QueryRow(
		`INSERT INTO projects (name) VALUES ('smoke') RETURNING id`,
	).Scan(&s.ProjectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := raw.QueryRow(
		`INSERT INTO columns (project_id, name, position) VALUES ($1, 'Backlog', 0) RETURNING id`,
		s.ProjectID,
	).Scan(&s.ColumnID); err != nil {
		t.Fatalf("seed column: %v", err)
	}
	if err := raw.QueryRow(
		`INSERT INTO issues (column_id, title) VALUES ($1, 'smoke') RETURNING id`,
		s.ColumnID,
	).Scan(&s.IssueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if err := raw.QueryRow(
		`INSERT INTO workspaces (issue_id, path, status, owner_type, owner_id, created_by) VALUES ($1, '/tmp/smoke', 'created', 'user', $2, $2) RETURNING id`,
		s.IssueID, s.UserID,
	).Scan(&s.WorkspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return s
}

func TestPGSmokeWorkspaces(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	// Read-side
	mustNoErr(t, "GetWorkspace", func() error { _, err := q.GetWorkspace(ctx, seed.WorkspaceID); return err })
	mustNoErr(t, "ListWorkspaces", func() error { _, err := q.ListWorkspaces(ctx); return err })
	mustNoErr(t, "GetWorkspacesByIssue", func() error {
		_, err := q.GetWorkspacesByIssue(ctx, sql.NullInt64{Int64: seed.IssueID, Valid: true})
		return err
	})
	mustNoErr(t, "GetProjectIDForWorkspace", func() error {
		_, err := q.GetProjectIDForWorkspace(ctx, seed.WorkspaceID)
		return err
	})
	mustNoErr(t, "CountSchedulesByWorkspace", func() error {
		_, err := q.CountSchedulesByWorkspace(ctx)
		return err
	})
	mustNoErr(t, "ListArchivedWorkspaces", func() error { _, err := q.ListArchivedWorkspaces(ctx); return err })
	mustNoErr(t, "ListArchivedWorkspacesWithMeta", func() error {
		_, err := q.ListArchivedWorkspacesWithMeta(ctx)
		return err
	})
	mustNoErr(t, "GetWorkspaceAlertableUserIDs", func() error {
		_, err := q.GetWorkspaceAlertableUserIDs(ctx, seed.WorkspaceID)
		return err
	})

	// Write-side
	mustNoErr(t, "CreateWorkspace", func() error {
		_, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
			IssueID:   sql.NullInt64{Int64: seed.IssueID, Valid: true},
			Name:      "two",
			Path:      "/tmp/smoke-2",
			Status:    "created",
			OwnerType: "user",
			OwnerID:   seed.UserID,
			CreatedBy: sql.NullInt64{Int64: seed.UserID, Valid: true},
		})
		return err
	})
	// CliType branch coverage. The SQL uses COALESCE(NULLIF(CAST(sqlc.arg(cli_type)
	// AS TEXT), ''), 'claude') with a 7-positional + 1-named placeholder shape
	// that exercises store.ConvertPlaceholders's ?N handling on PG (regression
	// guard for the $88 bug fixed 2026-05-19; see pgplaceholder_test.go).
	// Empty CliType normalizes to 'claude'; explicit 'codex' must round-trip.
	mustNoErr(t, "CreateWorkspace CliType=codex", func() error {
		ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
			Name:      "codex-ws",
			Path:      "/tmp/smoke-codex",
			Status:    "created",
			OwnerType: "user",
			OwnerID:   seed.UserID,
			CreatedBy: sql.NullInt64{Int64: seed.UserID, Valid: true},
			CliType:   "codex",
		})
		if err != nil {
			return err
		}
		if ws.CliType != "codex" {
			t.Fatalf("CliType round-trip: got %q, want %q", ws.CliType, "codex")
		}
		return nil
	})
	mustNoErr(t, "CreateWorkspace CliType='' defaults to claude", func() error {
		ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
			Name:      "default-ws",
			Path:      "/tmp/smoke-default",
			Status:    "created",
			OwnerType: "user",
			OwnerID:   seed.UserID,
			CreatedBy: sql.NullInt64{Int64: seed.UserID, Valid: true},
			// CliType deliberately empty — SQL COALESCE should fill 'claude'.
		})
		if err != nil {
			return err
		}
		if ws.CliType != "claude" {
			t.Fatalf("CliType default: got %q, want %q", ws.CliType, "claude")
		}
		return nil
	})
	mustNoErr(t, "UpdateWorkspaceName", func() error {
		return q.UpdateWorkspaceName(ctx, store.UpdateWorkspaceNameParams{Name: "renamed", ID: seed.WorkspaceID})
	})
	mustNoErr(t, "UpdateWorkspaceStatus", func() error {
		return q.UpdateWorkspaceStatus(ctx, store.UpdateWorkspaceStatusParams{Status: "running", ID: seed.WorkspaceID})
	})
	mustNoErr(t, "UpdateAgentStatus", func() error {
		return q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{
			AgentPid:    sql.NullInt64{Int64: 1234, Valid: true},
			AgentStatus: sql.NullString{String: "running", Valid: true},
			ID:          seed.WorkspaceID,
		})
	})
	mustNoErr(t, "UpdateSessionColumns", func() error {
		return q.UpdateSessionColumns(ctx, store.UpdateSessionColumnsParams{
			SessionID:     sql.NullString{String: "sess-1", Valid: true},
			SessionStatus: sql.NullString{String: "idle", Valid: true},
			ID:            seed.WorkspaceID,
		})
	})
	mustNoErr(t, "UpdateWorkspaceSessionUser", func() error {
		return q.UpdateWorkspaceSessionUser(ctx, store.UpdateWorkspaceSessionUserParams{
			CurrentSessionUserID: sql.NullInt64{Int64: seed.UserID, Valid: true},
			ID:                   seed.WorkspaceID,
		})
	})
	mustNoErr(t, "UpdateWorkspacePath", func() error {
		_, err := q.UpdateWorkspacePath(ctx, store.UpdateWorkspacePathParams{Path: "/tmp/new", ID: seed.WorkspaceID})
		return err
	})
	mustNoErr(t, "UpdateWorkspaceStatusConditional", func() error {
		return q.UpdateWorkspaceStatusConditional(ctx, store.UpdateWorkspaceStatusConditionalParams{
			Status: "stopped", ID: seed.WorkspaceID, Status_2: "running",
		})
	})
	mustNoErr(t, "ArchiveWorkspace", func() error { return q.ArchiveWorkspace(ctx, seed.WorkspaceID) })
	mustNoErr(t, "DeleteWorkspace", func() error { return q.DeleteWorkspace(ctx, seed.WorkspaceID) })
}

func TestPGSmokeWorkspacesOwnerFilter(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	// The point of these tests: prior to the ws-324 fix, ListWorkspacesForOwners
	// with a populated CreatedBy filter raised SQLSTATE 42P18 on PG because of
	// bare `? = ?` operands. Verify the fixed query passes PARSE.

	// (a) no creator filter — exercises the ? = 0 sentinel branch.
	// Column3 = 0 means ? = 0 → TRUE, bypassing the filter entirely.
	mustNoErr(t, "ListWorkspacesForOwners/no-creator-filter", func() error {
		_, err := q.ListWorkspacesForOwners(ctx, store.ListWorkspacesForOwnersParams{
			OwnerID:   seed.UserID,
			OrgIds:    []int64{seed.OrgID},
			Column3:   0,
			CreatedBy: sql.NullInt64{},
			OwnerID_2: seed.UserID,
		})
		return err
	})
	// (b) created_by=me filter — exercises the typed-column-anchored branch
	mustNoErr(t, "ListWorkspacesForOwners/created-by-me", func() error {
		_, err := q.ListWorkspacesForOwners(ctx, store.ListWorkspacesForOwnersParams{
			OwnerID:   seed.UserID,
			OrgIds:    []int64{seed.OrgID},
			Column3:   seed.UserID,
			CreatedBy: sql.NullInt64{Int64: seed.UserID, Valid: true},
			OwnerID_2: seed.UserID,
		})
		return err
	})
	// (c) empty OrgIds slice — exercises the NULL replacement path in generated code
	mustNoErr(t, "ListWorkspacesForOwners/empty-org-ids", func() error {
		_, err := q.ListWorkspacesForOwners(ctx, store.ListWorkspacesForOwnersParams{
			OwnerID:   seed.UserID,
			OrgIds:    nil,
			Column3:   0,
			CreatedBy: sql.NullInt64{},
			OwnerID_2: seed.UserID,
		})
		return err
	})
	mustNoErr(t, "ListWorkspaceCreatorsForOwners", func() error {
		_, err := q.ListWorkspaceCreatorsForOwners(ctx, store.ListWorkspaceCreatorsForOwnersParams{
			OwnerID: seed.UserID,
			OrgIds:  []int64{seed.OrgID},
		})
		return err
	})
}

func TestPGSmokeOrganizations(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	ctx := context.Background()
	var creatorID int64
	if err := raw.QueryRow(`INSERT INTO users (username, password_hash, role) VALUES ('orgsmoke', 'x', 'admin') RETURNING id`).Scan(&creatorID); err != nil {
		t.Fatalf("seed creator: %v", err)
	}

	var orgID int64
	mustNoErr(t, "CreateOrganization", func() error {
		o, err := q.CreateOrganization(ctx, store.CreateOrganizationParams{
			Slug: "o1", Name: "Org1", Description: "d", CreatedBy: creatorID,
		})
		orgID = o.ID
		return err
	})
	mustNoErr(t, "GetOrganization", func() error { _, err := q.GetOrganization(ctx, orgID); return err })
	mustNoErr(t, "GetOrganizationBySlug", func() error { _, err := q.GetOrganizationBySlug(ctx, "o1"); return err })
	mustNoErr(t, "ListOrganizationsForUser", func() error {
		_, err := q.ListOrganizationsForUser(ctx, creatorID)
		return err
	})
	mustNoErr(t, "UpdateOrganization", func() error {
		return q.UpdateOrganization(ctx, store.UpdateOrganizationParams{
			Name: "Org1b", Description: "dd", Slug: "o1b", ID: orgID,
		})
	})
	mustNoErr(t, "DeleteOrganization", func() error { return q.DeleteOrganization(ctx, orgID) })
}

func TestPGSmokePermission(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	exp := time.Now().Add(2 * time.Hour)

	var reqID int64
	mustNoErr(t, "InsertPermissionRequest", func() error {
		id, err := q.InsertPermissionRequest(ctx, store.InsertPermissionRequestParams{
			WorkspaceID: seed.WorkspaceID,
			OwnerType:   "user",
			OwnerID:     seed.UserID,
			SessionID:   "s1",
			ToolName:    "Bash",
			ToolInput:   "{}",
			ExpiresAt:   exp,
		})
		reqID = id
		return err
	})
	mustNoErr(t, "GetPermissionRequest", func() error { _, err := q.GetPermissionRequest(ctx, reqID); return err })
	mustNoErr(t, "ListPendingPermissionRequestsByWorkspace", func() error {
		_, err := q.ListPendingPermissionRequestsByWorkspace(ctx, seed.WorkspaceID)
		return err
	})
	mustNoErr(t, "MarkPermissionRequestAllowed", func() error {
		_, err := q.MarkPermissionRequestAllowed(ctx, store.MarkPermissionRequestAllowedParams{
			DecisionSource: sql.NullString{String: "user", Valid: true},
			DecidedBy:      sql.NullInt64{Int64: seed.UserID, Valid: true},
			MatcherUsed:    sql.NullString{String: "exact:ls", Valid: true},
			ID:             reqID,
		})
		return err
	})

	// insert another for denied path
	var reqID2 int64
	{
		id, err := q.InsertPermissionRequest(ctx, store.InsertPermissionRequestParams{
			WorkspaceID: seed.WorkspaceID, OwnerType: "user", OwnerID: seed.UserID,
			SessionID: "s2", ToolName: "Bash", ToolInput: "{}", ExpiresAt: exp,
		})
		if err != nil {
			t.Fatalf("seed req2: %v", err)
		}
		reqID2 = id
	}
	mustNoErr(t, "MarkPermissionRequestDenied", func() error {
		_, err := q.MarkPermissionRequestDenied(ctx, store.MarkPermissionRequestDeniedParams{
			DecisionSource: sql.NullString{String: "user", Valid: true},
			DecidedBy:      sql.NullInt64{Int64: seed.UserID, Valid: true},
			DenyMessage:    sql.NullString{String: "nope", Valid: true},
			ID:             reqID2,
		})
		return err
	})

	// timeout path
	var reqID3 int64
	{
		id, _ := q.InsertPermissionRequest(ctx, store.InsertPermissionRequestParams{
			WorkspaceID: seed.WorkspaceID, OwnerType: "user", OwnerID: seed.UserID,
			SessionID: "s3", ToolName: "Bash", ToolInput: "{}", ExpiresAt: exp,
		})
		reqID3 = id
	}
	mustNoErr(t, "MarkPermissionRequestTimeout", func() error {
		_, err := q.MarkPermissionRequestTimeout(ctx, reqID3)
		return err
	})

	// cancellation paths require a pending row
	var reqID4 int64
	{
		id, _ := q.InsertPermissionRequest(ctx, store.InsertPermissionRequestParams{
			WorkspaceID: seed.WorkspaceID, OwnerType: "user", OwnerID: seed.UserID,
			SessionID: "s4", ToolName: "Bash", ToolInput: "{}", ExpiresAt: exp,
		})
		reqID4 = id
	}
	mustNoErr(t, "MarkPermissionRequestCancelled", func() error {
		_, err := q.MarkPermissionRequestCancelled(ctx, store.MarkPermissionRequestCancelledParams{
			DecisionSource: sql.NullString{String: "user", Valid: true},
			ID:             reqID4,
		})
		return err
	})

	mustNoErr(t, "CancelPendingPermissionRequestsByWorkspace", func() error {
		_, err := q.CancelPendingPermissionRequestsByWorkspace(ctx, store.CancelPendingPermissionRequestsByWorkspaceParams{
			DecisionSource: sql.NullString{String: "session_end", Valid: true},
			WorkspaceID:    seed.WorkspaceID,
		})
		return err
	})
	mustNoErr(t, "CancelAllPendingPermissionRequests", func() error {
		_, err := q.CancelAllPendingPermissionRequests(ctx)
		return err
	})

	// Allowlist
	mustNoErr(t, "InsertPermissionAllowlist", func() error {
		return q.InsertPermissionAllowlist(ctx, store.InsertPermissionAllowlistParams{
			WorkspaceID:  seed.WorkspaceID,
			ToolName:     "Bash",
			MatcherKind:  "exact",
			MatcherValue: "ls",
			CreatedBy:    sql.NullInt64{Int64: seed.UserID, Valid: true},
		})
	})
	// re-insert same row exercises ON CONFLICT DO NOTHING rewrite
	mustNoErr(t, "InsertPermissionAllowlist/idempotent", func() error {
		return q.InsertPermissionAllowlist(ctx, store.InsertPermissionAllowlistParams{
			WorkspaceID: seed.WorkspaceID, ToolName: "Bash", MatcherKind: "exact",
			MatcherValue: "ls", CreatedBy: sql.NullInt64{Int64: seed.UserID, Valid: true},
		})
	})
	mustNoErr(t, "ListPermissionAllowlistByWorkspace", func() error {
		_, err := q.ListPermissionAllowlistByWorkspace(ctx, seed.WorkspaceID)
		return err
	})
	mustNoErr(t, "ListPermissionAllowlistByWorkspaceAndTool", func() error {
		_, err := q.ListPermissionAllowlistByWorkspaceAndTool(ctx, store.ListPermissionAllowlistByWorkspaceAndToolParams{
			WorkspaceID: seed.WorkspaceID, ToolName: "Bash",
		})
		return err
	})

	var allowID int64
	if err := raw.QueryRow(`SELECT id FROM agent_permission_allowlist WHERE workspace_id=$1 LIMIT 1`, seed.WorkspaceID).Scan(&allowID); err != nil {
		t.Fatalf("look up allowlist id: %v", err)
	}
	mustNoErr(t, "GetPermissionAllowlistEntryWorkspaceID", func() error {
		_, err := q.GetPermissionAllowlistEntryWorkspaceID(ctx, allowID)
		return err
	})
	mustNoErr(t, "DeletePermissionAllowlist", func() error {
		_, err := q.DeletePermissionAllowlist(ctx, allowID)
		return err
	})
	mustNoErr(t, "InsertPermissionRequestAllowed", func() error {
		_, err := q.InsertPermissionRequestAllowed(ctx, store.InsertPermissionRequestAllowedParams{
			WorkspaceID: seed.WorkspaceID,
			OwnerType:   "user",
			OwnerID:     seed.UserID,
			SessionID:   "s5",
			ToolName:    "Bash",
			ToolInput:   "{}",
			MatcherUsed: sql.NullString{String: "exact:ls", Valid: true},
			ExpiresAt:   exp,
		})
		return err
	})
}

func TestPGSmokeLabels(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	var lbl store.Label
	mustNoErr(t, "CreateLabel", func() error {
		l, err := q.CreateLabel(ctx, store.CreateLabelParams{
			ProjectID:   seed.ProjectID,
			Name:        "bug",
			Color:       "#ff0000",
			Description: "",
			CreatedBy:   seed.UserID,
		})
		lbl = l
		return err
	})
	mustNoErr(t, "GetLabel", func() error { _, err := q.GetLabel(ctx, lbl.ID); return err })
	mustNoErr(t, "GetLabelByProjectAndName", func() error {
		_, err := q.GetLabelByProjectAndName(ctx, store.GetLabelByProjectAndNameParams{
			ProjectID: seed.ProjectID, Name: "bug",
		})
		return err
	})
	mustNoErr(t, "ListLabelsByProject", func() error {
		_, err := q.ListLabelsByProject(ctx, seed.ProjectID)
		return err
	})
	mustNoErr(t, "ListLabelsByProjectWithUsage", func() error {
		_, err := q.ListLabelsByProjectWithUsage(ctx, seed.ProjectID)
		return err
	})
	mustNoErr(t, "UpdateLabel", func() error {
		_, err := q.UpdateLabel(ctx, store.UpdateLabelParams{
			Name: "bug2", Color: "#00ff00", Description: "d", ID: lbl.ID,
		})
		return err
	})
	mustNoErr(t, "ListLabelsByIssue", func() error { _, err := q.ListLabelsByIssue(ctx, seed.IssueID); return err })
	// Empty + non-empty IN slice exercises the /*SLICE:*/ rewrite path
	mustNoErr(t, "ListLabelsByIssueIDs/empty", func() error {
		_, err := q.ListLabelsByIssueIDs(ctx, nil)
		return err
	})
	mustNoErr(t, "ListLabelsByIssueIDs/populated", func() error {
		_, err := q.ListLabelsByIssueIDs(ctx, []int64{seed.IssueID})
		return err
	})
	mustNoErr(t, "DeleteLabel", func() error { return q.DeleteLabel(ctx, lbl.ID) })
}

func TestPGSmokeExternalCredentials(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	var credID int64
	mustNoErr(t, "CreateExternalCredential", func() error {
		c, err := q.CreateExternalCredential(ctx, store.CreateExternalCredentialParams{
			OwnerType:      "user",
			OwnerID:        seed.UserID,
			UserID:         seed.UserID,
			Provider:       "github",
			Alias:          "smoke",
			Config:         `{"token":"ghp_smoke"}`,
			LastVerifiedAt: sql.NullTime{Time: time.Now(), Valid: true},
		})
		credID = c.ID
		return err
	})
	mustNoErr(t, "GetExternalCredentialByID", func() error {
		_, err := q.GetExternalCredentialByID(ctx, store.GetExternalCredentialByIDParams{
			ID: credID, OwnerType: "user", OwnerID: seed.UserID, UserID: seed.UserID,
		})
		return err
	})
	mustNoErr(t, "ListExternalCredentialsForUser", func() error {
		_, err := q.ListExternalCredentialsForUser(ctx, store.ListExternalCredentialsForUserParams{
			OwnerType: "user", OwnerID: seed.UserID, UserID: seed.UserID,
		})
		return err
	})
	mustNoErr(t, "TouchCredentialVerifiedAtByID", func() error {
		return q.TouchCredentialVerifiedAtByID(ctx, store.TouchCredentialVerifiedAtByIDParams{
			ID: credID, OwnerType: "user", OwnerID: seed.UserID, UserID: seed.UserID,
		})
	})
	mustNoErr(t, "DeleteExternalCredentialByID", func() error {
		return q.DeleteExternalCredentialByID(ctx, store.DeleteExternalCredentialByIDParams{
			ID: credID, OwnerType: "user", OwnerID: seed.UserID, UserID: seed.UserID,
		})
	})
}

func TestPGSmokeProjectExternalSources(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	var srcID int64
	mustNoErr(t, "AddProjectExternalSource/insert", func() error {
		s, err := q.AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
			ProjectID: seed.ProjectID,
			Provider:  "github",
			SourceKey: "acme/foo",
			Config:    `{}`,
		})
		srcID = s.ID
		return err
	})
	mustNoErr(t, "AddProjectExternalSource/upsert", func() error {
		_, err := q.AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
			ProjectID: seed.ProjectID,
			Provider:  "github",
			SourceKey: "acme/foo",
			Config:    `{"reaffirm":true}`,
		})
		return err
	})
	mustNoErr(t, "ListProjectExternalSources", func() error {
		_, err := q.ListProjectExternalSources(ctx, seed.ProjectID)
		return err
	})
	mustNoErr(t, "GetProjectExternalSource", func() error {
		_, err := q.GetProjectExternalSource(ctx, srcID)
		return err
	})
	mustNoErr(t, "UpdateProjectExternalSourceConfig", func() error {
		_, err := q.UpdateProjectExternalSourceConfig(ctx, store.UpdateProjectExternalSourceConfigParams{
			Config: `{"updated":true}`, ID: srcID,
		})
		return err
	})
	mustNoErr(t, "DeleteProjectExternalSource", func() error {
		return q.DeleteProjectExternalSource(ctx, srcID)
	})
}

func TestPGSmokeExternalIssues(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	mustNoErr(t, "InsertImportedIssue", func() error {
		_, err := q.InsertImportedIssue(ctx, store.InsertImportedIssueParams{
			ColumnID:       seed.ColumnID,
			Title:          "imported smoke",
			Description:    sql.NullString{String: "snap", Valid: true},
			ExternalSource: sql.NullString{String: "github", Valid: true},
			ExternalID:     sql.NullString{String: "acme/foo#42", Valid: true},
			ExternalUrl:    sql.NullString{String: "https://github.com/acme/foo/issues/42", Valid: true},
		})
		// INSERT OR IGNORE may return ErrNoRows on conflict — that's PG-accepted SQL.
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	})
	// Second call should hit the unique index → ErrNoRows; PG must still PARSE.
	mustNoErr(t, "InsertImportedIssue/duplicate", func() error {
		_, err := q.InsertImportedIssue(ctx, store.InsertImportedIssueParams{
			ColumnID:       seed.ColumnID,
			Title:          "imported smoke",
			Description:    sql.NullString{String: "snap", Valid: true},
			ExternalSource: sql.NullString{String: "github", Valid: true},
			ExternalID:     sql.NullString{String: "acme/foo#42", Valid: true},
			ExternalUrl:    sql.NullString{String: "", Valid: true},
		})
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	})
	mustNoErr(t, "FindIssueByExternalKey", func() error {
		_, err := q.FindIssueByExternalKey(ctx, store.FindIssueByExternalKeyParams{
			ProjectID:      seed.ProjectID,
			ExternalSource: sql.NullString{String: "github", Valid: true},
			ExternalID:     sql.NullString{String: "acme/foo#42", Valid: true},
		})
		return err
	})
	// Exercise the sqlc.slice IN-list expansion through the PG ?→$N rewriter.
	mustNoErr(t, "BatchFindImportedExternalIDs/non-empty", func() error {
		_, err := q.BatchFindImportedExternalIDs(ctx, store.BatchFindImportedExternalIDsParams{
			ProjectID:      seed.ProjectID,
			ExternalSource: sql.NullString{String: "github", Valid: true},
			ExternalIds: []sql.NullString{
				{String: "acme/foo#42", Valid: true},
				{String: "acme/foo#99", Valid: true},
			},
		})
		return err
	})
	// Empty slice path expands to `IN (NULL)` — separate PARSE.
	mustNoErr(t, "BatchFindImportedExternalIDs/empty", func() error {
		_, err := q.BatchFindImportedExternalIDs(ctx, store.BatchFindImportedExternalIDsParams{
			ProjectID:      seed.ProjectID,
			ExternalSource: sql.NullString{String: "github", Valid: true},
			ExternalIds:    nil,
		})
		return err
	})
	mustNoErr(t, "RefreshIssueSnapshot", func() error {
		_, err := q.RefreshIssueSnapshot(ctx, store.RefreshIssueSnapshotParams{
			Title:       "refreshed",
			Description: sql.NullString{String: "snap-v2", Valid: true},
			ID:          seed.IssueID, // niuniu-only row → guard returns ErrNoRows
		})
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	})
}

// --- server settings + issue goal_condition (generic KV round-trip) ---
// Catches PG-only failures (42P18 untyped params, 42601 syntax).

func TestPGSmoke_ServerSettings(t *testing.T) {
	_, q := pgtest.SetupPGDB(t)
	ctx := context.Background()

	mustNoErr(t, "UpsertServerSetting", func() error {
		return q.UpsertServerSetting(ctx, store.UpsertServerSettingParams{
			Key:       "test.sample_ratio",
			Value:     "30",
			UpdatedBy: sql.NullInt64{Int64: 1, Valid: true},
		})
	})
	mustNoErr(t, "GetServerSetting", func() error {
		v, err := q.GetServerSetting(ctx, "test.sample_ratio")
		if err != nil {
			return err
		}
		if v != "30" {
			t.Fatalf("GetServerSetting: expected %q got %q", "30", v)
		}
		return nil
	})
}

func TestPGSmoke_IssueGoalCondition(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	mustNoErr(t, "UpdateIssueGoalCondition", func() error {
		return q.UpdateIssueGoalCondition(ctx, store.UpdateIssueGoalConditionParams{
			GoalCondition: "all tests pass",
			ID:            seed.IssueID,
		})
	})
	mustNoErr(t, "GetIssueGoalCondition", func() error {
		v, err := q.GetIssueGoalCondition(ctx, seed.IssueID)
		if err != nil {
			return err
		}
		if v != "all tests pass" {
			t.Fatalf("GetIssueGoalCondition: expected %q got %q", "all tests pass", v)
		}
		return nil
	})
}

// ============================================================
// Scene-based MCP/plugin management smoke tests (M1, 2026-05-18)
//
// Catches PG-only PARSE/PLAN errors (SQLSTATE 42P18 / 42601) for every
// sqlc-generated method in the scenes-family domain:
//   - scenes (CRUD + UpsertBuiltinScene partial-conflict)
//   - scenes_owner_filter (ListScenesAccessible, ListAttachedSceneIDs)
//   - scene_layers (base layer InsertBaseLayer/UpdateBaseLayer split,
//     AttachLayer/DetachLayer, position ops)
//   - scene_asset_imports (CRUD)
//   - project_default_scenes (CRUD + JOIN-with-scenes)
// ============================================================

func TestScenesSqlcSmoke(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	var sceneID int64
	mustNoErr(t, "CreateScene", func() error {
		s, err := q.CreateScene(ctx, store.CreateSceneParams{
			OwnerType:   "user",
			OwnerID:     seed.UserID,
			Slug:        "smoke-scene",
			DisplayName: "Smoke",
			Description: "smoke test",
			Tags:        "[]",
			Source:      "user",
			SourceSlug:  "",
			Definition:  "{}",
			ContentHash: "deadbeef",
			Enabled:     1,
		})
		sceneID = s.ID
		return err
	})
	mustNoErr(t, "GetScene", func() error { _, err := q.GetScene(ctx, sceneID); return err })
	mustNoErr(t, "GetSceneByOwnerSlug", func() error {
		_, err := q.GetSceneByOwnerSlug(ctx, store.GetSceneByOwnerSlugParams{
			OwnerType: "user", OwnerID: seed.UserID, Slug: "smoke-scene",
		})
		return err
	})
	mustNoErr(t, "CountScenesByOwner", func() error {
		_, err := q.CountScenesByOwner(ctx, store.CountScenesByOwnerParams{
			OwnerType: "user", OwnerID: seed.UserID,
		})
		return err
	})
	mustNoErr(t, "UpdateScene", func() error {
		return q.UpdateScene(ctx, store.UpdateSceneParams{
			ID: sceneID, DisplayName: "Smoke 2",
			Description: "", Tags: "[]", Definition: "{}",
			ContentHash: "cafef00d", Enabled: 1,
		})
	})
	mustNoErr(t, "UpsertBuiltinScene", func() error {
		return q.UpsertBuiltinScene(ctx, store.UpsertBuiltinSceneParams{
			Slug: "builtin-smoke", DisplayName: "Builtin Smoke", Description: "",
			Tags: "[]", SourceSlug: "builtin-smoke",
			Definition: "{}", ContentHash: "feedface",
		})
	})
	mustNoErr(t, "ListBuiltinScenes", func() error {
		_, err := q.ListBuiltinScenes(ctx)
		return err
	})
	mustNoErr(t, "ListScenesAccessible", func() error {
		_, err := q.ListScenesAccessible(ctx, store.ListScenesAccessibleParams{
			OwnerType: "user", OwnerID: seed.UserID, UserID: seed.UserID,
		})
		return err
	})
	mustNoErr(t, "DeleteScene", func() error { return q.DeleteScene(ctx, sceneID) })
}

func TestSceneLayersSqlcSmoke(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	// Seed a scene so AttachLayer has a real scene_id to reference.
	scene, err := q.CreateScene(ctx, store.CreateSceneParams{
		OwnerType: "user", OwnerID: seed.UserID, Slug: "layer-smoke-scene",
		DisplayName: "S", Description: "", Tags: "[]",
		Source: "user", SourceSlug: "", Definition: "{}",
		ContentHash: "00", Enabled: 1,
	})
	if err != nil {
		t.Fatalf("seed scene: %v", err)
	}

	// Migrate may have already promoted a base layer for the seeded workspace.
	// Skip base inserts here and exercise the non-base flow instead.
	mustNoErr(t, "ListLayersForWorkspace", func() error {
		_, err := q.ListLayersForWorkspace(ctx, seed.WorkspaceID)
		return err
	})
	mustNoErr(t, "GetBaseLayer-no-row-ok-on-error", func() error {
		_, err := q.GetBaseLayer(ctx, seed.WorkspaceID)
		// sql.ErrNoRows is acceptable — this workspace was created via raw
		// INSERT (not WorkspaceService.Create) so it has no base layer yet
		// unless migration promoted it.
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	})
	// Use an unused workspace_id to avoid colliding with migration-promoted base.
	// Insert a fresh workspace to own the base layer test.
	var freshWS int64
	if err := raw.QueryRow(
		`INSERT INTO workspaces (issue_id, path, status, owner_type, owner_id, created_by) VALUES ($1, '/tmp/layer-smoke', 'created', 'user', $2, $2) RETURNING id`,
		seed.IssueID, seed.UserID,
	).Scan(&freshWS); err != nil {
		t.Fatalf("seed fresh workspace: %v", err)
	}
	mustNoErr(t, "InsertBaseLayer", func() error {
		return q.InsertBaseLayer(ctx, store.InsertBaseLayerParams{
			WorkspaceID: freshWS, BaseDefinition: "{}",
		})
	})
	mustNoErr(t, "UpdateBaseLayer", func() error {
		return q.UpdateBaseLayer(ctx, store.UpdateBaseLayerParams{
			BaseDefinition: `{"mcp":[]}`, WorkspaceID: freshWS,
		})
	})
	mustNoErr(t, "NextLayerPosition", func() error {
		_, err := q.NextLayerPosition(ctx, freshWS)
		return err
	})
	var layerID int64
	mustNoErr(t, "AttachLayer", func() error {
		layer, err := q.AttachLayer(ctx, store.AttachLayerParams{
			WorkspaceID: freshWS,
			SceneID:     sql.NullInt64{Int64: scene.ID, Valid: true},
			Position:    1,
		})
		layerID = layer.ID
		return err
	})
	mustNoErr(t, "UpdateLayerPosition", func() error {
		return q.UpdateLayerPosition(ctx, store.UpdateLayerPositionParams{
			Position: 2, ID: layerID,
		})
	})
	mustNoErr(t, "DetachLayer", func() error { return q.DetachLayer(ctx, layerID) })
	mustNoErr(t, "ListAttachedSceneIDs", func() error {
		_, err := q.ListAttachedSceneIDs(ctx, freshWS)
		return err
	})
}

func TestSceneAssetImportsSqlcSmoke(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()

	scene, err := q.CreateScene(ctx, store.CreateSceneParams{
		OwnerType: "user", OwnerID: seed.UserID, Slug: "asset-smoke-scene",
		DisplayName: "A", Description: "", Tags: "[]",
		Source: "user", SourceSlug: "", Definition: "{}",
		ContentHash: "11", Enabled: 1,
	})
	if err != nil {
		t.Fatalf("seed scene: %v", err)
	}

	// Use raw asset_id (no FK) — the scene_asset_imports table intentionally
	// doesn't FK to asset tables because asset_kind is polymorphic.
	const fakeAssetID = int64(42)

	mustNoErr(t, "CreateImport", func() error {
		return q.CreateImport(ctx, store.CreateImportParams{
			WorkspaceID: seed.WorkspaceID, SceneID: scene.ID,
			AssetKind: "env_preset", AssetID: fakeAssetID,
		})
	})
	mustNoErr(t, "ListImportsForWorkspace", func() error {
		_, err := q.ListImportsForWorkspace(ctx, seed.WorkspaceID)
		return err
	})
	mustNoErr(t, "ListImportsForScene", func() error {
		_, err := q.ListImportsForScene(ctx, scene.ID)
		return err
	})
	mustNoErr(t, "GetImportByAsset", func() error {
		_, err := q.GetImportByAsset(ctx, store.GetImportByAssetParams{
			WorkspaceID: seed.WorkspaceID, SceneID: scene.ID,
			AssetKind: "env_preset", AssetID: fakeAssetID,
		})
		return err
	})
	mustNoErr(t, "DeleteImportsForWorkspaceScene", func() error {
		return q.DeleteImportsForWorkspaceScene(ctx, store.DeleteImportsForWorkspaceSceneParams{
			WorkspaceID: seed.WorkspaceID, SceneID: scene.ID,
		})
	})
}

func TestProjectDefaultScenesSqlcSmoke(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	seed := seedPGSmoke(t, raw)
	ctx := context.Background()
	_ = raw

	scene, err := q.CreateScene(ctx, store.CreateSceneParams{
		OwnerType: "user", OwnerID: seed.UserID, Slug: "project-default-smoke",
		DisplayName: "P", Description: "", Tags: "[]",
		Source: "user", SourceSlug: "", Definition: "{}",
		ContentHash: "22", Enabled: 1,
	})
	if err != nil {
		t.Fatalf("seed scene: %v", err)
	}

	mustNoErr(t, "NextProjectDefaultPosition", func() error {
		_, err := q.NextProjectDefaultPosition(ctx, seed.ProjectID)
		return err
	})
	mustNoErr(t, "AttachProjectDefault", func() error {
		_, err := q.AttachProjectDefault(ctx, store.AttachProjectDefaultParams{
			ProjectID: seed.ProjectID, SceneID: scene.ID, Position: 0,
		})
		return err
	})
	mustNoErr(t, "ListProjectDefaults", func() error {
		_, err := q.ListProjectDefaults(ctx, seed.ProjectID)
		return err
	})
	mustNoErr(t, "ReorderProjectDefault", func() error {
		return q.ReorderProjectDefault(ctx, store.ReorderProjectDefaultParams{
			Position: 1, ProjectID: seed.ProjectID, SceneID: scene.ID,
		})
	})
	mustNoErr(t, "DetachProjectDefault", func() error {
		return q.DetachProjectDefault(ctx, store.DetachProjectDefaultParams{
			ProjectID: seed.ProjectID, SceneID: scene.ID,
		})
	})
}

func mustNoErr(t *testing.T, name string, fn func() error) {
	t.Helper()
	if err := fn(); err != nil {
		t.Errorf("%s: %v", name, err)
	}
}
