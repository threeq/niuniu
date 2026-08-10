package service

// Per-service isolation tests for all 9 top-level resource services.
//
// Import cycle note: internal/testing imports internal/service (for OwnerRef),
// so service tests cannot import internal/testing. The isolationEnv helper
// already defined in authz_test.go is reused here — both files are in package
// "service" and compiled together.
//
// Pattern:
//  1. Build isolationEnv (two users, two orgs, disjoint memberships).
//  2. Insert a resource owned by OrgA and one owned by OrgB.
//  3. Call ListForUser(userA) — expect OrgA resource visible, OrgB invisible.
//  4. Call ListForUser(userB) — expect OrgB resource visible, OrgA invisible.

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// runIsolationTest is a shared parameterised harness. It sets up a two-org
// environment, inserts one resource per org via insert(db, orgID), then calls
// list(service, userID) for each user and validates cross-org invisibility.
//
// insert must return the row count visible for a given user (to let list be a
// closure over the constructed service). For simplicity callers pass a
// list closure that returns (count, error).
func runIsolationTest(
	t *testing.T,
	name string,
	makeService func(env *isolationEnv) any,
	insertOrgA func(env *isolationEnv),
	insertOrgB func(env *isolationEnv),
	list func(svc any, userID int64) (int, error),
) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		env := newIsolationEnv(t)
		insertOrgA(env)
		insertOrgB(env)
		svc := makeService(env)

		// UserA should see OrgA's resource, not OrgB's.
		countA, err := list(svc, env.UserA)
		if err != nil {
			t.Fatalf("ListForUser(userA): %v", err)
		}
		if countA != 1 {
			t.Errorf("ListForUser(userA): got %d results, want 1 (only OrgA resource)", countA)
		}

		// UserB should see OrgB's resource, not OrgA's.
		countB, err := list(svc, env.UserB)
		if err != nil {
			t.Fatalf("ListForUser(userB): %v", err)
		}
		if countB != 1 {
			t.Errorf("ListForUser(userB): got %d results, want 1 (only OrgB resource)", countB)
		}
	})
}

// TestProjectIsolation verifies that projects owned by OrgA are invisible to
// UserB (who belongs only to OrgB) and vice versa.
func TestProjectIsolation(t *testing.T) {
	runIsolationTest(t, "project",
		func(env *isolationEnv) any {
			authz := NewAuthz(store.New(env.DB), env.DB)
			return NewProjectService(store.New(env.DB), env.DB, nil, authz)
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO projects (name, status, owner_type, owner_id) VALUES ('proj-a', 'active', 'org', ?)`,
				env.OrgA,
			)
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO projects (name, status, owner_type, owner_id) VALUES ('proj-b', 'active', 'org', ?)`,
				env.OrgB,
			)
		},
		func(svc any, userID int64) (int, error) {
			items, err := svc.(*ProjectService).ListForUser(context.Background(), userID)
			return len(items), err
		},
	)
}

// TestWorkspaceIsolation verifies workspace cross-org invisibility.
func TestWorkspaceIsolation(t *testing.T) {
	runIsolationTest(t, "workspace",
		func(env *isolationEnv) any {
			authz := NewAuthz(store.New(env.DB), env.DB)
			return NewWorkspaceService(store.New(env.DB), env.DB, nil, t.TempDir(), nil, authz)
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO workspaces (name, path, status, owner_type, owner_id) VALUES ('ws-a', '/tmp/a', 'created', 'org', ?)`,
				env.OrgA,
			)
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO workspaces (name, path, status, owner_type, owner_id) VALUES ('ws-b', '/tmp/b', 'created', 'org', ?)`,
				env.OrgB,
			)
		},
		func(svc any, userID int64) (int, error) {
			items, err := svc.(*WorkspaceService).ListForUser(context.Background(), userID, WorkspaceListFilter{})
			return len(items), err
		},
	)
}

// TestRepositoryIsolation verifies repository cross-org invisibility.
func TestRepositoryIsolation(t *testing.T) {
	runIsolationTest(t, "repository",
		func(env *isolationEnv) any {
			authz := NewAuthz(store.New(env.DB), env.DB)
			return NewRepositoryService(store.New(env.DB), env.DB, authz, t.TempDir())
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO repositories (name, path, owner_type, owner_id) VALUES ('repo-a', '/tmp/ra', 'org', ?)`,
				env.OrgA,
			)
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO repositories (name, path, owner_type, owner_id) VALUES ('repo-b', '/tmp/rb', 'org', ?)`,
				env.OrgB,
			)
		},
		func(svc any, userID int64) (int, error) {
			items, err := svc.(*RepositoryService).ListForUser(context.Background(), userID)
			return len(items), err
		},
	)
}

// TestEnvPresetIsolation verifies env preset cross-org invisibility.
func TestEnvPresetIsolation(t *testing.T) {
	runIsolationTest(t, "env_preset",
		func(env *isolationEnv) any {
			authz := NewAuthz(store.New(env.DB), env.DB)
			return NewEnvPresetService(store.New(env.DB), env.DB, authz)
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO env_presets (name, description, env, owner_type, owner_id) VALUES ('preset-a', '', '{}', 'org', ?)`,
				env.OrgA,
			)
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO env_presets (name, description, env, owner_type, owner_id) VALUES ('preset-b', '', '{}', 'org', ?)`,
				env.OrgB,
			)
		},
		func(svc any, userID int64) (int, error) {
			items, err := svc.(*EnvPresetService).ListForUser(context.Background(), userID)
			return len(items), err
		},
	)
}

// TestQuickActionIsolation verifies quick action cross-org invisibility.
func TestQuickActionIsolation(t *testing.T) {
	runIsolationTest(t, "quick_action",
		func(env *isolationEnv) any {
			authz := NewAuthz(store.New(env.DB), env.DB)
			return NewQuickActionService(store.New(env.DB), env.DB, authz)
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO quick_actions (label, content, auto_send, position, owner_type, owner_id) VALUES ('qa-a', 'x', 0, 0, 'org', ?)`,
				env.OrgA,
			)
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO quick_actions (label, content, auto_send, position, owner_type, owner_id) VALUES ('qa-b', 'x', 0, 1, 'org', ?)`,
				env.OrgB,
			)
		},
		func(svc any, userID int64) (int, error) {
			items, err := svc.(*QuickActionService).ListForUser(context.Background(), userID)
			return len(items), err
		},
	)
}

// TestAgentIsolation verifies file-based agent cross-org invisibility.
// AgentService.ListForUser is called without an agentDir since no filesystem
// access happens during a list-from-DB operation.
func TestAgentIsolation(t *testing.T) {
	runIsolationTest(t, "agent",
		func(env *isolationEnv) any {
			authz := NewAuthz(store.New(env.DB), env.DB)
			// Construct AgentService directly — agentDir is only used for file
			// read/write, not for the DB-driven ListForUser query.
			return &AgentService{q: store.New(env.DB), agentDir: t.TempDir(), authz: authz}
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO agents (name, dir_path, owner_type, owner_id) VALUES ('agent-a', '/tmp/a.md', 'org', ?)`,
				env.OrgA,
			)
		},
		func(env *isolationEnv) {
			env.DB.Exec(
				`INSERT INTO agents (name, dir_path, owner_type, owner_id) VALUES ('agent-b', '/tmp/b.md', 'org', ?)`,
				env.OrgB,
			)
		},
		func(svc any, userID int64) (int, error) {
			items, err := svc.(*AgentService).ListForUser(context.Background(), userID)
			return len(items), err
		},
	)
}
