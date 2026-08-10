// Round-trip test for the IsolationEnv external fixture helpers.
//
// Unlocked by M0.3 (issues table now carries external_source / external_id /
// external_url columns added by addIssueExternalColumns). The test reads
// those columns back through raw SQL via SeedExternalIssue's GetIssue call —
// sqlc regen for the matching struct fields lands in M0.7, so we skip the
// struct-field assertions until then but exercise the migration path now.
package testing_test

import (
	"context"
	"testing"

	nt "github.com/niuniu-dev/niuniu/internal/testing"
)

func TestFixtures_RoundTrip(t *testing.T) {
	env := nt.NewIsolationEnv(t)
	proj := env.NewProject(t, env.UserA, "fixt-test")
	col := env.NewColumn(t, proj.ID, "Todo", "created")
	issue := env.SeedExternalIssue(t, col.ID, "github", "acme/foo#1")

	// Struct fields ExternalSource / ExternalID land via M0.7 sqlc regen.
	// Until then, verify the columns persisted by reading them back with
	// raw SQL — proves the migration column-add path works end-to-end.
	var gotSource, gotExtID string
	if err := env.DB.QueryRowContext(context.Background(),
		`SELECT external_source, external_id FROM issues WHERE id = ?`, issue.ID).
		Scan(&gotSource, &gotExtID); err != nil {
		t.Fatalf("read external_* columns: %v", err)
	}
	if gotSource != "github" {
		t.Fatalf("external_source mismatch: got %q want %q", gotSource, "github")
	}
	if gotExtID != "acme/foo#1" {
		t.Fatalf("external_id mismatch: got %q want %q", gotExtID, "acme/foo#1")
	}

	cmnt := env.SeedComment(t, issue.ID, "alice", "hello")
	if cmnt.Content != "hello" {
		t.Fatal("content mismatch")
	}
}
