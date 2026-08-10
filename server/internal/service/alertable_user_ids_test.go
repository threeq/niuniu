package service

import (
	"context"
	"testing"
)

func TestWorkspaceService_AlertableUserIDs(t *testing.T) {
	svc, db := newWorkspaceServiceForTest(t) // reuse from workspace_test.go; returns svc and matching db
	ctx := context.Background()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}

	mustExec(`INSERT INTO users (id, username, password_hash, display_name) VALUES
		(1, 'alice', 'x', 'Alice'),
		(2, 'bob',   'x', 'Bob'),
		(3, 'carol', 'x', 'Carol')`)
	mustExec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (10, 'p', 'org', 5)`)
	mustExec(`INSERT INTO columns (id, project_id, name, position) VALUES (20, 10, 'todo', 0)`)
	mustExec(`INSERT INTO issues (id, column_id, title, position) VALUES (30, 20, 't', 0)`)
	mustExec(`INSERT INTO issue_assignees (issue_id, user_id) VALUES (30, 2), (30, 3)`)

	// W1: creator only (no issue)
	mustExec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by) VALUES
		(101, 'w1', '/w1', 'org', 5, 1)`)
	// W2: assignees only (NULL creator), linked to issue 30
	mustExec(`INSERT INTO workspaces (id, issue_id, name, path, owner_type, owner_id, created_by) VALUES
		(102, 30, 'w2', '/w2', 'org', 5, NULL)`)
	// W3: both creator (alice) and assignees (bob, carol)
	mustExec(`INSERT INTO workspaces (id, issue_id, name, path, owner_type, owner_id, created_by) VALUES
		(103, 30, 'w3', '/w3', 'org', 5, 1)`)
	// W4: creator IS also an assignee (bob both) — must dedup
	mustExec(`INSERT INTO workspaces (id, issue_id, name, path, owner_type, owner_id, created_by) VALUES
		(104, 30, 'w4', '/w4', 'org', 5, 2)`)
	// W5: NULL creator, no issue
	mustExec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by) VALUES
		(105, 'w5', '/w5', 'org', 5, NULL)`)
	// W6: workspaceID <= 0 returns empty
	// (no row needed — call AlertableUserIDs(ctx, 0))

	cases := []struct {
		name string
		wsID int64
		want []int64
	}{
		{"creator only", 101, []int64{1}},
		{"assignees only", 102, []int64{2, 3}},
		{"creator + assignees", 103, []int64{1, 2, 3}},
		{"creator-is-assignee dedup", 104, []int64{2, 3}},
		{"no creator no issue", 105, []int64{}},
		{"workspaceID <= 0", 0, []int64{}},
		{"unknown workspace", 999, []int64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.AlertableUserIDs(ctx, tc.wsID)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got == nil {
				t.Errorf("got nil, want []int64{} (use empty slice not nil)")
				got = []int64{}
			}
			if !setEq(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func setEq(a, b []int64) bool {
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
