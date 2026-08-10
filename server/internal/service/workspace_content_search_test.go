package service

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
)

func TestWorkspaceService_SearchWorkspaceIDsByUserContent(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		testWorkspaceServiceSearchByUserContent(t, drv)
	})
}

func testWorkspaceServiceSearchByUserContent(t *testing.T, drv string) {
	svc, db := newWorkspaceServiceForTestDriver(t, drv)
	authz := NewAuthz(svc.q, db)
	svc.authz = authz

	ctx := context.Background()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}

	// alice + bob both members of org 10.
	mustExec(`INSERT INTO users (id, username, password_hash, display_name) VALUES
		(1, 'alice', 'x', 'Alice'),
		(2, 'bob',   'x', 'Bob')`)
	mustExec(`INSERT INTO organizations (id, slug, name, created_by) VALUES (10, 'acme', 'Acme', 1)`)
	mustExec(`INSERT INTO org_members (org_id, user_id, role) VALUES (10, 1, 'admin'), (10, 2, 'member')`)

	// 101,102 org-owned; 103 alice personal; 104 bob personal; 105 alice personal
	// but archived; 106 alice personal.
	mustExec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by) VALUES
		(101, 'w1', '/w1', 'org',  10, 1),
		(102, 'w2', '/w2', 'org',  10, 2),
		(103, 'w3', '/w3', 'user', 1, 1),
		(104, 'w4', '/w4', 'user', 2, 2),
		(106, 'w6', '/w6', 'user', 1, 1)`)
	mustExec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id, created_by, is_archived) VALUES
		(105, 'w5', '/w5', 'user', 1, 1, 1)`)

	// Messages. role/event_type drive what counts as searchable user content.
	insMsg := func(id string, wsID int64, role, eventType, content string) {
		mustExec(`INSERT INTO agent_messages (id, workspace_id, role, content, message_id, event_type)
			VALUES (?, ?, ?, ?, ?, ?)`, id, wsID, role, content, id, eventType)
	}
	insMsg("m1", 101, "user", "text", "fix the login bug")         // match
	insMsg("m2", 102, "assistant", "text", "login bug is fixed")   // assistant -> no match
	insMsg("m3", 103, "user", "text", "deploy the pipeline now")   // matches "deploy"
	insMsg("m4", 104, "user", "text", "the login screen is broken") // bob personal -> alice can't see
	insMsg("m5", 105, "user", "text", "login regression in prod")  // archived -> no match
	insMsg("m6", 106, "user", "tool_use", "login tool invocation") // non-text event -> no match

	cases := []struct {
		name   string
		userID int64
		query  string
		want   []int64
	}{
		{"alice login -> only her visible user-text match", 1, "login", []int64{101}},
		{"case-insensitive", 1, "LOGIN", []int64{101}},
		{"alice deploy -> personal workspace", 1, "deploy", []int64{103}},
		{"bob login -> org + his personal", 2, "login", []int64{101, 104}},
		{"blank query -> empty", 1, "   ", []int64{}},
		{"no match -> empty", 1, "nonexistent-keyword", []int64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.SearchWorkspaceIDsByUserContent(ctx, tc.userID, tc.query)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !setEqInt64(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
