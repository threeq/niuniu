package service_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

func newSettingsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	store.Migrate(db)
	return db
}

func TestOrchestrationSettings_FallbackThenOverride(t *testing.T) {
	ctx := context.Background()
	ss := service.NewServerSettingsService(store.Wrap(newSettingsDB(t)))
	ss.SetCacheTTL(0) // no cache so the override is read immediately
	o := service.NewOrchestrationSettings(ss, 50, 5, 20, 80)

	// Fallback to config defaults when nothing is written.
	if got := o.BudgetUSD(ctx); got != 50 {
		t.Fatalf("BudgetUSD default = %v, want 50", got)
	}
	if got := o.WarnRatio(ctx); got != 0.8 {
		t.Fatalf("WarnRatio default = %v, want 0.8", got)
	}

	// Override via the store; accessor reflects it.
	if err := ss.Put(ctx, service.KeyOrchBudgetUSD, "30", 0); err != nil {
		t.Fatal(err)
	}
	if got := o.BudgetUSD(ctx); got != 30 {
		t.Fatalf("BudgetUSD after override = %v, want 30", got)
	}
}

func TestServerSettings_SeedIfAbsent(t *testing.T) {
	ctx := context.Background()
	ss := service.NewServerSettingsService(store.Wrap(newSettingsDB(t)))
	ss.SetCacheTTL(0)

	if err := ss.SeedIfAbsent(ctx, service.KeyOrchBudgetUSD, "50"); err != nil {
		t.Fatal(err)
	}
	if got := ss.GetInt(ctx, service.KeyOrchBudgetUSD, 0); got != 50 {
		t.Fatalf("after seed = %d, want 50", got)
	}
	// Second seed must not clobber an existing value.
	if err := ss.SeedIfAbsent(ctx, service.KeyOrchBudgetUSD, "99"); err != nil {
		t.Fatal(err)
	}
	if got := ss.GetInt(ctx, service.KeyOrchBudgetUSD, 0); got != 50 {
		t.Fatalf("seed clobbered existing: = %d, want 50", got)
	}
}
