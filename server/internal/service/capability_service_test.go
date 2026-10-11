package service_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// storedCapability reads the cached doc digest for a subject straight from the
// table, so the tests assert what was actually persisted (not just the
// in-memory return value).
func storedCapability(t *testing.T, db *sql.DB, kind string, id int64) (digest string, count int) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT source_digest FROM integration_capabilities WHERE subject_kind = ? AND subject_id = ?`,
		kind, id).Scan(&digest); err != nil && err != sql.ErrNoRows {
		t.Fatalf("read capability row: %v", err)
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM integration_capabilities WHERE subject_kind = ? AND subject_id = ?`,
		kind, id).Scan(&count); err != nil {
		t.Fatalf("count capability rows: %v", err)
	}
	return digest, count
}

func TestCapabilityEnsureProvider_PersistsAndRefreshes(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	cap := service.NewCapabilityService(env.Queries(), env.DB, nil, nil, nil)
	ctx := context.Background()

	p := providerWithProfile(capabilityContractJSON) // ID 7
	doc, err := cap.EnsureProvider(ctx, p)
	if err != nil {
		t.Fatalf("EnsureProvider: %v", err)
	}
	if doc == nil {
		t.Fatal("nil doc")
	}
	digest, n := storedCapability(t, env.DB, service.CapabilityTargetProvider, p.ID)
	if n != 1 {
		t.Fatalf("want exactly 1 cached row, got %d", n)
	}
	if digest != p.Contract().Digest {
		t.Fatalf("persisted digest %q != contract digest %q", digest, p.Contract().Digest)
	}

	// Idempotent: an unchanged contract does not add a row.
	if _, err := cap.EnsureProvider(ctx, p); err != nil {
		t.Fatalf("EnsureProvider (2nd): %v", err)
	}
	if _, n := storedCapability(t, env.DB, service.CapabilityTargetProvider, p.ID); n != 1 {
		t.Fatalf("second ensure should not duplicate rows, got %d", n)
	}

	// Contract change → digest moves → the cached row is refreshed in place.
	changed := strings.Replace(capabilityContractJSON, `"operations": [`,
		`"operations": [{"name":"ping","method":"GET","path":"/ping","read_only":true},`, 1)
	p2 := providerWithProfile(changed)
	doc2, err := cap.EnsureProvider(ctx, p2)
	if err != nil {
		t.Fatalf("EnsureProvider (changed): %v", err)
	}
	if doc2.SourceDigest != p2.Contract().Digest {
		t.Fatalf("refreshed doc not bound to new digest: %s", doc2.SourceDigest)
	}
	digest, n = storedCapability(t, env.DB, service.CapabilityTargetProvider, p.ID)
	if n != 1 || digest != p2.Contract().Digest {
		t.Fatalf("cache not refreshed in place: n=%d digest=%s want=%s", n, digest, p2.Contract().Digest)
	}
}

func TestCapabilityEnsureDataSource_PersistsAndRefreshes(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	cap := service.NewCapabilityService(env.Queries(), env.DB, nil, nil, nil)
	ctx := context.Background()

	dto := &service.DataSourceDTO{
		ID: 42, Name: "analytics", Kind: "postgres",
		Config:            map[string]any{"host": "db", "database": "analytics"},
		ScopeConfig:       map[string]any{"databases": []any{"analytics"}, "tables_allow": []any{"orders"}},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	}
	doc, err := cap.EnsureDataSource(ctx, dto)
	if err != nil || doc == nil {
		t.Fatalf("EnsureDataSource: %v (doc=%v)", err, doc)
	}
	digest, n := storedCapability(t, env.DB, service.CapabilityTargetDataSource, dto.ID)
	if n != 1 || digest != doc.SourceDigest {
		t.Fatalf("data source doc not persisted: n=%d digest=%s want=%s", n, digest, doc.SourceDigest)
	}

	// Widening the scope changes the digest → the cached doc is refreshed.
	widened := *dto
	widened.ScopeConfig = map[string]any{"databases": []any{"analytics"}, "tables_allow": []any{"orders", "customers"}}
	if _, err := cap.EnsureDataSource(ctx, &widened); err != nil {
		t.Fatalf("EnsureDataSource (widened): %v", err)
	}
	digest2, n2 := storedCapability(t, env.DB, service.CapabilityTargetDataSource, dto.ID)
	if n2 != 1 {
		t.Fatalf("want 1 row after refresh, got %d", n2)
	}
	if digest2 == digest {
		t.Fatal("scope widening did not invalidate the cached description")
	}
	if digest2 != service.DataSourceCapabilityDigest(&widened) {
		t.Fatal("refreshed digest does not match the new metadata digest")
	}
}

func TestCapabilityDelete(t *testing.T) {
	env := niutest.NewIsolationEnv(t)
	cap := service.NewCapabilityService(env.Queries(), env.DB, nil, nil, nil)
	ctx := context.Background()
	p := providerWithProfile(capabilityContractJSON)
	if _, err := cap.EnsureProvider(ctx, p); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := cap.Delete(ctx, service.CapabilityTargetProvider, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, n := storedCapability(t, env.DB, service.CapabilityTargetProvider, p.ID); n != 0 {
		t.Fatalf("row not deleted, n=%d", n)
	}
}
