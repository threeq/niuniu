package service

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// The settings view renders the schema default for a missing row; the policy
// loader must resolve the same mode, or the UI promises observation the runtime
// never performs.
func TestIntakeDefaultsMatchPolicyLoader(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()

	if err := EnsureIntakeProjectDefaults(ctx, store.Wrap(f.db), f.projectID); err != nil {
		t.Fatalf("seed intake defaults: %v", err)
	}

	pol, err := (imbotPolicy{q: f.q}).LoadPolicy(ctx, f.projectID, intake.SourceKindIMBot)
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	if pol.Mode != intake.ModeObserve {
		t.Fatalf("policy mode = %q, want %q", pol.Mode, intake.ModeObserve)
	}
	if pol.DailyCap != defaultIntakeDailyCap {
		t.Fatalf("daily cap = %d, want %d", pol.DailyCap, defaultIntakeDailyCap)
	}
}
