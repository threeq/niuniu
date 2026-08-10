package service_test

import (
	"database/sql"
	"testing"
)

// kanbanCreateUser inserts a user row and returns its ID.
func kanbanCreateUser(t *testing.T, db *sql.DB, username string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO users (username, password_hash, display_name, role) VALUES (?, '', ?, 'member')`,
		username, username)
	if err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// kanbanCreateOrg inserts an org and seeds the given user as owner.
func kanbanCreateOrg(t *testing.T, db *sql.DB, slug string, ownerUserID int64) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO organizations (slug, name, created_by) VALUES (?, ?, ?)`,
		slug, slug, ownerUserID)
	if err != nil {
		t.Fatalf("create org %q: %v", slug, err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')`,
		id, ownerUserID); err != nil {
		t.Fatalf("seed owner for org %q: %v", slug, err)
	}
	return id
}

// kanbanAddOrgMember adds a member to an org with the given role.
func kanbanAddOrgMember(t *testing.T, db *sql.DB, orgID, userID int64, role string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, ?)`,
		orgID, userID, role); err != nil {
		t.Fatalf("add member (org=%d user=%d role=%s): %v", orgID, userID, role, err)
	}
}

// kanbanCreateProject inserts a project row and returns its ID.
func kanbanCreateProject(t *testing.T, db *sql.DB, ownerType string, ownerID int64, name string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO projects (name, status, owner_type, owner_id) VALUES (?, 'active', ?, ?)`,
		name, ownerType, ownerID)
	if err != nil {
		t.Fatalf("create project %q: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// kanbanCreateColumn inserts a column row and returns its ID.
func kanbanCreateColumn(t *testing.T, db *sql.DB, projectID int64, name string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO columns (project_id, name, position) VALUES (?, ?, 0)`,
		projectID, name)
	if err != nil {
		t.Fatalf("create column %q: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// kanbanCreateIssue inserts an issue row and returns its ID.
func kanbanCreateIssue(t *testing.T, db *sql.DB, columnID int64, title string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO issues (column_id, title, position) VALUES (?, ?, 0)`,
		columnID, title)
	if err != nil {
		t.Fatalf("create issue %q: %v", title, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// kanbanCreateLabel inserts a label row and returns its ID.
func kanbanCreateLabel(t *testing.T, db *sql.DB, projectID, createdBy int64, name, color string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO labels (project_id, name, color, description, created_by) VALUES (?, ?, ?, '', ?)`,
		projectID, name, color, createdBy)
	if err != nil {
		t.Fatalf("create label %q: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// kanbanAddOrgMember is referenced to suppress unused-variable warnings in
// packages that import only a subset of these helpers.
var _ = kanbanAddOrgMember
