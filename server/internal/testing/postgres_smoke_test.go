package testing_test

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	apptesting "github.com/niuniu-dev/niuniu/internal/testing"
)

// TestPGSetupSmoke verifies that SetupPGDB starts a container, applies the
// schema, and returns a working *Queries. Auto-skips when Docker is
// unavailable.
func TestPGSetupSmoke(t *testing.T) {
	raw, q := pgtest.SetupPGDB(t)
	if raw == nil || q == nil {
		t.Fatal("SetupPGDB returned nil")
	}
	// Confirm the schema applied — query a known table.
	var n int
	if err := raw.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	// Confirm sqlc generated method dispatches through the wrapper.
	if _, err := q.ListWorkspaces(context.Background()); err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
}

// TestPGIsolationEnvSmoke verifies the multi-tenant fixture works on PG.
func TestPGIsolationEnvSmoke(t *testing.T) {
	env := apptesting.NewPGIsolationEnv(t)
	if env.UserA == 0 || env.UserB == 0 || env.OrgA == 0 || env.OrgB == 0 {
		t.Fatalf("expected non-zero ids, got: UserA=%d UserB=%d OrgA=%d OrgB=%d",
			env.UserA, env.UserB, env.OrgA, env.OrgB)
	}
	if env.UserA == env.UserB || env.OrgA == env.OrgB {
		t.Fatalf("expected distinct ids, got UserA=%d UserB=%d OrgA=%d OrgB=%d",
			env.UserA, env.UserB, env.OrgA, env.OrgB)
	}
}
