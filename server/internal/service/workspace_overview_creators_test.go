package service

import (
	"context"
	"errors"
	"testing"
)

func TestWorkspaceService_ListOverviewCreators(t *testing.T) {
	svc, db := newWorkspaceServiceForTest(t)
	ctx := context.Background()

	// Wire real authz so ListOverviewCreators can resolve org memberships.
	authz := NewAuthz(svc.q, db)
	svc.authz = authz

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}

	// alice in org 10 (acme) and org 20 (beta); bob only in org 10; dave only in org 20.
	mustExec(`INSERT INTO users (id, username, password_hash, display_name) VALUES
		(1, 'alice', 'x', 'Alice'),
		(2, 'bob',   'x', 'Bob'),
		(3, 'carol', 'x', 'Carol'),
		(4, 'dave',  'x', 'Dave')`)
	// organizations.created_by is NOT NULL — provide it.
	mustExec(`INSERT INTO organizations (id, slug, name, created_by) VALUES
		(10, 'acme', 'Acme', 1),
		(20, 'beta', 'Beta', 1),
		(30, 'other', 'Other', 1)`)
	mustExec(`INSERT INTO org_members (org_id, user_id, role) VALUES
		(10, 1, 'admin'), (10, 2, 'member'),
		(20, 1, 'member'), (20, 4, 'member')`)
	// alice & bob in acme; alice & dave in beta; alice's personal space.
	mustExec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by) VALUES
		(101, 'w1', '/w1', 'org',  10, 1),
		(102, 'w2', '/w2', 'org',  10, 2),
		(103, 'w3', '/w3', 'org',  20, 1),
		(104, 'w4', '/w4', 'org',  20, 4),
		(105, 'w5', '/w5', 'user',  1, 1),
		(106, 'w6', '/w6', 'org',  10, NULL)`) // NULL creator excluded

	t.Run("no ownerFilter returns full Authz set", func(t *testing.T) {
		got, err := svc.ListOverviewCreators(ctx, 1, OwnerScope{})
		if err != nil {
			t.Fatal(err)
		}
		gotIDs := make([]int64, len(got))
		for i, u := range got {
			gotIDs[i] = u.ID
		}
		// alice (self/personal+acme+beta), bob (acme), dave (beta).
		if !setEqInt64(gotIDs, []int64{1, 2, 4}) {
			t.Errorf("got %v, want [1 2 4]", gotIDs)
		}
	})

	t.Run("ownerFilter=org:acme narrows to acme creators only — beta colleague dave excluded", func(t *testing.T) {
		got, err := svc.ListOverviewCreators(ctx, 1, OwnerScope{Type: "org", ID: 10})
		if err != nil {
			t.Fatal(err)
		}
		gotIDs := make([]int64, len(got))
		for i, u := range got {
			gotIDs[i] = u.ID
		}
		if !setEqInt64(gotIDs, []int64{1, 2}) {
			t.Errorf("got %v, want [1 2]", gotIDs)
		}
	})

	t.Run("ownerFilter=user:self returns own personal creators", func(t *testing.T) {
		got, err := svc.ListOverviewCreators(ctx, 1, OwnerScope{Type: "user", ID: 1})
		if err != nil {
			t.Fatal(err)
		}
		gotIDs := make([]int64, len(got))
		for i, u := range got {
			gotIDs[i] = u.ID
		}
		if !setEqInt64(gotIDs, []int64{1}) {
			t.Errorf("got %v, want [1]", gotIDs)
		}
	})

	t.Run("ownerFilter=org:other (not member) returns 403-equivalent error", func(t *testing.T) {
		_, err := svc.ListOverviewCreators(ctx, 1, OwnerScope{Type: "org", ID: 30})
		if err == nil {
			t.Fatalf("expected forbidden error, got nil")
		}
		if !errors.Is(err, ErrForbiddenOwnerScope) {
			t.Errorf("expected ErrForbiddenOwnerScope, got %v", err)
		}
	})

	t.Run("ownerFilter=user:other returns 403-equivalent error", func(t *testing.T) {
		_, err := svc.ListOverviewCreators(ctx, 1, OwnerScope{Type: "user", ID: 2})
		if err == nil {
			t.Fatalf("expected forbidden error, got nil")
		}
		if !errors.Is(err, ErrForbiddenOwnerScope) {
			t.Errorf("expected ErrForbiddenOwnerScope, got %v", err)
		}
	})
}
