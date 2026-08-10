package testing

import (
	"context"
	"testing"
)

func TestSetupDB(t *testing.T) {
	q := SetupDB(t)
	if q == nil {
		t.Fatal("queries is nil")
	}
	// Verify the schema is complete by querying a stable kept table.
	if _, err := q.ListAgents(context.Background()); err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}
}
