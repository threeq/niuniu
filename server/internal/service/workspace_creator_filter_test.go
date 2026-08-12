package service

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
)

func TestWorkspaceService_ListForUser_CreatorFilter(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		testWorkspaceServiceListForUserCreatorFilter(t, drv)
	})
}

func testWorkspaceServiceListForUserCreatorFilter(t *testing.T, drv string) {
	svc, db := newWorkspaceServiceForTestDriver(t, drv)
	// Wire real authz so ListForUser can resolve org memberships.
	authz := NewAuthz(svc.q, db)
	svc.authz = authz

	ctx := context.Background()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}

	// Two users in one org, both with workspaces.
	mustExec(`INSERT INTO users (id, username, password_hash, display_name) VALUES
		(1, 'alice', 'x', 'Alice'),
		(2, 'bob',   'x', 'Bob')`)
	mustExec(`INSERT INTO organizations (id, slug, name, created_by) VALUES (10, 'acme', 'Acme', 1)`)
	mustExec(`INSERT INTO org_members (org_id, user_id, role) VALUES (10, 1, 'admin'), (10, 2, 'member')`)
	// Org workspaces: W1 created by alice, W2 created by bob.
	mustExec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by) VALUES
		(101, 'w1', '/w1', 'org', 10, 1),
		(102, 'w2', '/w2', 'org', 10, 2)`)
	// Personal workspace W3 in alice's space, NULL created_by (legacy).
	mustExec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by) VALUES
		(103, 'w3', '/w3', 'user', 1, NULL)`)
	// Personal workspace W4 in bob's space — alice cannot see.
	mustExec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by) VALUES
		(104, 'w4', '/w4', 'user', 2, 2)`)
	// Bob's personal-space workspace W6 created by bob — alice must NOT see this
	// even when she filters by creator=bob (Authz boundary).
	mustExec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by) VALUES
		(106, 'w6', '/w6', 'user', 2, 2)`)
	// Org workspace W5 created by alice but NULL — should NOT match alice's "me" filter
	// (NULL fallback only applies to personal-space rows).
	mustExec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by) VALUES
		(105, 'w5', '/w5', 'org', 10, NULL)`)

	cases := []struct {
		name   string
		userID int64
		filter WorkspaceListFilter
		want   []int64 // expected workspace IDs
	}{
		{
			name:   "no filter returns all accessible",
			userID: 1,
			filter: WorkspaceListFilter{},
			want:   []int64{101, 102, 103, 105},
		},
		{
			name:   "creator=alice (me) returns alice's org + her personal NULL",
			userID: 1,
			filter: WorkspaceListFilter{CreatorID: int64Ptr(1)},
			want:   []int64{101, 103},
		},
		{
			name:   "creator=bob filters to bob's org-visible only -- Authz boundary excludes W6",
			userID: 1,
			filter: WorkspaceListFilter{CreatorID: int64Ptr(2)},
			want:   []int64{102}, // NOT 104 (alice can't see bob's personal) and NOT 106
		},
		{
			name:   "creator=999 (unknown) returns empty",
			userID: 1,
			filter: WorkspaceListFilter{CreatorID: int64Ptr(999)},
			want:   []int64{},
		},
		{
			name:   "bob creator=me sees own personal + own org",
			userID: 2,
			filter: WorkspaceListFilter{CreatorID: int64Ptr(2)},
			want:   []int64{102, 104, 106},
		},
		{
			name:   "alice creator=me does NOT match org NULL w5",
			userID: 1,
			filter: WorkspaceListFilter{CreatorID: int64Ptr(1)},
			want:   []int64{101, 103}, // explicitly excludes 105
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.ListForUser(ctx, tc.userID, tc.filter)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			gotIDs := make([]int64, len(got))
			for i, w := range got {
				gotIDs[i] = w.ID
			}
			if !setEqInt64(gotIDs, tc.want) {
				t.Errorf("got %v, want %v", gotIDs, tc.want)
			}
		})
	}
}

func setEqInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[int64]bool{}
	for _, v := range a {
		m[v] = true
	}
	for _, v := range b {
		if !m[v] {
			return false
		}
	}
	return true
}

func int64Ptr(i int64) *int64 { return &i }
