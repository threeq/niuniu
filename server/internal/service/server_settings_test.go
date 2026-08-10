package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

func TestServerSettings_GetInt_DefaultWhenMissing(t *testing.T) {
	db, _ := niutest.SetupDBRaw(t)
	svc := service.NewServerSettingsService(store.Wrap(db))
	ctx := context.Background()
	got := svc.GetInt(ctx, "test.sample_ratio", 7)
	if got != 7 {
		t.Fatalf("expected default 7, got %d", got)
	}
}

func TestServerSettings_PutThenGet(t *testing.T) {
	db, _ := niutest.SetupDBRaw(t)
	svc := service.NewServerSettingsService(store.Wrap(db))
	ctx := context.Background()
	if err := svc.Put(ctx, "test.sample_ratio", "30", 1); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got := svc.GetInt(ctx, "test.sample_ratio", 0)
	if got != 30 {
		t.Fatalf("got %d", got)
	}
}

func TestServerSettings_Cache5sLRU(t *testing.T) {
	db, _ := niutest.SetupDBRaw(t)
	svc := service.NewServerSettingsService(store.Wrap(db))
	svc.SetCacheTTL(50 * time.Millisecond) // accelerate for test
	ctx := context.Background()
	_ = svc.Put(ctx, "k", "1", 0)
	_ = svc.GetInt(ctx, "k", 0) // populates cache
	// Mutate via raw SQL to bypass cache invalidation
	if _, err := db.ExecContext(ctx,
		`UPDATE server_settings SET value = '99' WHERE key = ?`, "k"); err != nil {
		t.Fatal(err)
	}
	if v := svc.GetInt(ctx, "k", 0); v != 1 {
		t.Fatalf("expected cached value 1, got %d", v)
	}
	time.Sleep(60 * time.Millisecond)
	if v := svc.GetInt(ctx, "k", 0); v != 99 {
		t.Fatalf("expected refreshed 99, got %d", v)
	}
}

func TestServerSettings_PutInvalidatesCache(t *testing.T) {
	db, _ := niutest.SetupDBRaw(t)
	svc := service.NewServerSettingsService(store.Wrap(db))
	ctx := context.Background()
	_ = svc.Put(ctx, "k", "1", 0)
	_ = svc.GetInt(ctx, "k", 0)
	_ = svc.Put(ctx, "k", "2", 0)
	if v := svc.GetInt(ctx, "k", 0); v != 2 {
		t.Fatalf("expected 2 after Put invalidation, got %d", v)
	}
}
