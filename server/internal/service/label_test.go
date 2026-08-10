package service_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// openLabelTestDB opens an in-memory SQLite DB with foreign-keys enabled,
// applies the embedded schema, and runs the feature migrations. Mirrors
// setupAuthzTest in authz_test.go.
func openLabelTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store.Migrate(db)
	return db
}

// labelCreateUser inserts a users row and returns the new id. Mirrors the
// authzCreateUser helper in authz_test.go.
func labelCreateUser(t *testing.T, db *sql.DB, username string) int64 {
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

// labelCreateOrg creates an organization owned by ownerUserID and seeds the
// owner row in org_members. Mirrors authzCreateOrg.
func labelCreateOrg(t *testing.T, db *sql.DB, slug string, ownerUserID int64) int64 {
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

// labelAddOrgMember inserts an org_members row with the given role.
// Mirrors authzAddOrgMember.
func labelAddOrgMember(t *testing.T, db *sql.DB, orgID, userID int64, role string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, ?)`,
		orgID, userID, role); err != nil {
		t.Fatalf("add member (org=%d user=%d role=%s): %v", orgID, userID, role, err)
	}
}

// labelCreateProject inserts a projects row with the given owner and returns
// the new project id. Mirrors authzCreateProject.
func labelCreateProject(t *testing.T, db *sql.DB, ownerType string, ownerID int64, name string) int64 {
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

// setupLabelTest provisions an in-memory DB + LabelService + a personal
// project owned by the returned user. The returned (svc, db, ctx, projectID,
// ownerUserID) tuple is the standard fixture for label-service tests.
func setupLabelTest(t *testing.T) (*service.LabelService, *sql.DB, context.Context, int64, int64) {
	t.Helper()
	db := openLabelTestDB(t)
	authz := service.NewAuthz(store.New(db), db)
	svc := service.NewLabelService(db, authz)
	owner := labelCreateUser(t, db, "owner-"+t.Name())
	proj := labelCreateProject(t, db, "user", owner, "p-"+t.Name())
	return svc, db, context.Background(), proj, owner
}

func TestLabelService_Create_Happy(t *testing.T) {
	svc, _, ctx, projID, userID := setupLabelTest(t)
	lbl, err := svc.Create(ctx, userID, projID, service.LabelCreateInput{
		Name: "bug", Color: "#D73A4A", Description: "broken",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if lbl.Name != "bug" || lbl.Color != "#d73a4a" {
		t.Errorf("got %+v", lbl)
	}
}

func TestLabelService_Create_DefaultColor(t *testing.T) {
	svc, _, ctx, projID, userID := setupLabelTest(t)
	lbl, err := svc.Create(ctx, userID, projID, service.LabelCreateInput{Name: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !service.IsPresetColor(lbl.Color) {
		t.Errorf("expected preset, got %q", lbl.Color)
	}
}

func TestLabelService_Create_InvalidColor(t *testing.T) {
	svc, _, ctx, projID, userID := setupLabelTest(t)
	for _, c := range []string{"red", "#fff", "#FFFFFFFF", "FFFFFF"} {
		_, err := svc.Create(ctx, userID, projID, service.LabelCreateInput{Name: "x", Color: c})
		if !errors.Is(err, service.ErrInvalidLabelColor) {
			t.Errorf("color %q: got %v, want ErrInvalidLabelColor", c, err)
		}
	}
}

func TestLabelService_Create_NameConflict(t *testing.T) {
	svc, _, ctx, projID, userID := setupLabelTest(t)
	if _, err := svc.Create(ctx, userID, projID, service.LabelCreateInput{Name: "bug", Color: "#d73a4a"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := svc.Create(ctx, userID, projID, service.LabelCreateInput{Name: "bug", Color: "#000000"})
	var conflict *service.LabelNameConflictError
	if !errors.As(err, &conflict) {
		t.Errorf("got %v, want LabelNameConflictError", err)
	} else if conflict.Existing.Name != "bug" {
		t.Errorf("conflict.Existing.Name = %q", conflict.Existing.Name)
	}
}

func TestLabelService_List_PerProject(t *testing.T) {
	svc, db, ctx, projA, userID := setupLabelTest(t)
	projB := labelCreateProject(t, db, "user", userID, "proj-b")

	if _, err := svc.Create(ctx, userID, projA, service.LabelCreateInput{Name: "a", Color: "#000000"}); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := svc.Create(ctx, userID, projB, service.LabelCreateInput{Name: "b", Color: "#000000"}); err != nil {
		t.Fatalf("create b: %v", err)
	}

	labelsA, err := svc.List(ctx, userID, projA, "")
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	if len(labelsA) != 1 || labelsA[0].Name != "a" {
		t.Errorf("project A leaked B's labels: %+v", labelsA)
	}
}

func TestLabelService_Update_NonAdminDenied(t *testing.T) {
	svc, db, ctx, _, _ := setupLabelTest(t)
	owner := labelCreateUser(t, db, "owner-l")
	member := labelCreateUser(t, db, "member-l")
	org := labelCreateOrg(t, db, "o-l", owner)
	labelAddOrgMember(t, db, org, member, "member")
	orgProj := labelCreateProject(t, db, "org", org, "p-org-l")

	lbl, err := svc.Create(ctx, owner, orgProj, service.LabelCreateInput{Name: "x", Color: "#000000"})
	if err != nil {
		t.Fatalf("create label: %v", err)
	}
	name := "y"
	_, err = svc.Update(ctx, member, lbl.ID, service.LabelUpdateInput{Name: &name})
	if !errors.Is(err, service.ErrLabelForbidden) {
		t.Errorf("got %v, want ErrLabelForbidden", err)
	}
}

func TestLabelService_Delete_NonAdminDenied(t *testing.T) {
	svc, db, ctx, _, _ := setupLabelTest(t)
	owner := labelCreateUser(t, db, "owner-d")
	member := labelCreateUser(t, db, "member-d")
	org := labelCreateOrg(t, db, "o-d", owner)
	labelAddOrgMember(t, db, org, member, "member")
	orgProj := labelCreateProject(t, db, "org", org, "p-org-d")

	lbl, err := svc.Create(ctx, owner, orgProj, service.LabelCreateInput{Name: "x", Color: "#000000"})
	if err != nil {
		t.Fatalf("create label: %v", err)
	}
	if err := svc.Delete(ctx, member, lbl.ID); !errors.Is(err, service.ErrLabelForbidden) {
		t.Errorf("got %v, want ErrLabelForbidden", err)
	}
}
