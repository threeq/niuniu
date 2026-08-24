package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestEnvProviderGroupAndCooldownColumns verifies the fresh-schema env_providers
// carries group_name + cooldown_until and the cooldown queries round-trip.
func TestEnvProviderGroupAndCooldownColumns(t *testing.T) {
	db := openMem(t)
	defer db.Close()
	q := NewQueries(db)
	ctx := context.Background()

	prov, err := q.CreateEnvProvider(ctx, CreateEnvProviderParams{
		Name: "智谱-1", Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey: "${ACCOUNT:智谱-1}", Model: "glm-5.1", GroupName: "智谱",
		GroupPosition: 2, Enabled: 1,
		OwnerType: "user", OwnerID: 0,
	})
	if err != nil {
		t.Fatalf("CreateEnvProvider: %v", err)
	}
	if prov.GroupName != "智谱" {
		t.Errorf("group_name not persisted: %q", prov.GroupName)
	}
	if prov.GroupPosition != 2 {
		t.Errorf("group_position not persisted: %d", prov.GroupPosition)
	}
	if prov.Enabled != 1 {
		t.Errorf("enabled not persisted: %d", prov.Enabled)
	}
	if prov.CooldownUntil.Valid {
		t.Error("cooldown_until should start NULL")
	}

	until := time.Date(2026, 8, 24, 0, 27, 12, 0, time.UTC)
	if err := q.SetProviderCooldown(ctx, SetProviderCooldownParams{ID: prov.ID, CooldownUntil: sql.NullTime{Time: until, Valid: true}}); err != nil {
		t.Fatalf("SetProviderCooldown: %v", err)
	}
	got, err := q.GetEnvProvider(ctx, prov.ID)
	if err != nil {
		t.Fatalf("GetEnvProvider: %v", err)
	}
	if !got.CooldownUntil.Valid || !got.CooldownUntil.Time.Equal(until) {
		t.Errorf("cooldown_until not persisted: %+v", got.CooldownUntil)
	}

	if err := q.ClearProviderCooldown(ctx, prov.ID); err != nil {
		t.Fatalf("ClearProviderCooldown: %v", err)
	}
	got, err = q.GetEnvProvider(ctx, prov.ID)
	if err != nil {
		t.Fatalf("GetEnvProvider after clear: %v", err)
	}
	if got.CooldownUntil.Valid {
		t.Error("cooldown_until should be NULL after clear")
	}

	// Update carries group_name/position/enabled through without touching cooldown.
	if err := q.UpdateEnvProvider(ctx, UpdateEnvProviderParams{
		ID: prov.ID, Name: "智谱-1", Platform: "zhipu", BaseUrls: prov.BaseUrls,
		ApiKey: prov.ApiKey, Model: "glm-5.1", GroupName: "智谱-2",
		GroupPosition: 1, Enabled: 0, Slug: prov.Slug,
	}); err != nil {
		t.Fatalf("UpdateEnvProvider: %v", err)
	}
	got, _ = q.GetEnvProvider(ctx, prov.ID)
	if got.GroupName != "智谱-2" {
		t.Errorf("update did not persist group_name: %q", got.GroupName)
	}
	if got.GroupPosition != 1 || got.Enabled != 0 {
		t.Errorf("update did not persist group_position/enabled: pos=%d enabled=%d", got.GroupPosition, got.Enabled)
	}

	// Dedicated toggle + reorder queries.
	if err := q.SetProviderEnabled(ctx, SetProviderEnabledParams{ID: prov.ID, Enabled: 1}); err != nil {
		t.Fatalf("SetProviderEnabled: %v", err)
	}
	if err := q.SetProviderGroupPosition(ctx, SetProviderGroupPositionParams{ID: prov.ID, GroupPosition: 3}); err != nil {
		t.Fatalf("SetProviderGroupPosition: %v", err)
	}
	got, _ = q.GetEnvProvider(ctx, prov.ID)
	if got.Enabled != 1 || got.GroupPosition != 3 {
		t.Errorf("toggle/reorder not applied: enabled=%d pos=%d", got.Enabled, got.GroupPosition)
	}
}

// TestEnvProviderGroupPositionAppend verifies MaxEnvProviderGroupPosition so a
// provider JOINING a group is appended at the end (max+1) rather than position
// 0, and that self-exclusion keeps a member's own position out of the max.
func TestEnvProviderGroupPositionAppend(t *testing.T) {
	db := openMem(t)
	defer db.Close()
	q := NewQueries(db)
	ctx := context.Background()

	mk := func(name, group string, pos int64) int64 {
		p, err := q.CreateEnvProvider(ctx, CreateEnvProviderParams{
			Name: name, Platform: "zhipu", BaseUrls: `{"anthropic":"x"}`,
			ApiKey: "${ACCOUNT:" + name + "}", GroupName: group,
			GroupPosition: pos, Enabled: 1, OwnerType: "user", OwnerID: 0,
		})
		if err != nil {
			t.Fatalf("CreateEnvProvider %s: %v", name, err)
		}
		return p.ID
	}
	a := mk("a", "g", 1)
	mk("b", "g", 2)
	mk("standalone", "", 0)

	// Newcomer to group g → appended after position 2.
	max, err := q.MaxEnvProviderGroupPosition(ctx, MaxEnvProviderGroupPositionParams{GroupName: "g", ID: 0})
	if err != nil {
		t.Fatalf("MaxEnvProviderGroupPosition: %v", err)
	}
	if max != 2 {
		t.Fatalf("max position = %d, want 2", max)
	}
	// Self-excluded (moving a within its own group) → its own position 1 does
	// not count, so a would re-land after b.
	maxSelf, _ := q.MaxEnvProviderGroupPosition(ctx, MaxEnvProviderGroupPositionParams{GroupName: "g", ID: a})
	if maxSelf != 2 {
		t.Fatalf("self-excluded max = %d, want 2", maxSelf)
	}
	// Empty/unknown group → 0 (first member lands at 1).
	maxNone, _ := q.MaxEnvProviderGroupPosition(ctx, MaxEnvProviderGroupPositionParams{GroupName: "", ID: 0})
	if maxNone != 0 {
		t.Fatalf("ungrouped max = %d, want 0", maxNone)
	}
}

// TestEnvProviderCooldownMigration verifies the open-time migration adds
// group_name + cooldown_until to a legacy env_providers table that predates
// them (the addColumnIfNotExists path in migrate.go), mirroring the fresh-schema
// round-trip above.
func TestEnvProviderCooldownMigration(t *testing.T) {
	db := openMem(t)
	defer db.Close()
	ctx := context.Background()
	// Simulate a legacy table: drop the new columns from the freshly-applied
	// schema so the migration must re-add them.
	if _, err := db.Exec("ALTER TABLE env_providers DROP COLUMN group_name"); err != nil {
		t.Fatalf("drop group_name: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE env_providers DROP COLUMN cooldown_until"); err != nil {
		t.Fatalf("drop cooldown_until: %v", err)
	}

	// migrate.go's addColumnIfNotExists runs through ApplySchema only for the
	// driver in use; exercise the same helper directly (it is what the
	// migration block calls on startup).
	addColumnIfNotExists(db, "env_providers", "group_name", "TEXT NOT NULL DEFAULT ''")
	addColumnIfNotExists(db, "env_providers", "cooldown_until", "TIMESTAMP")

	for _, col := range []string{"group_name", "cooldown_until"} {
		if !columnExistsForTest(t, db, "env_providers", col) {
			t.Errorf("migration did not add %s", col)
		}
	}

	q := NewQueries(db)
	prov, err := q.CreateEnvProvider(ctx, CreateEnvProviderParams{
		Name: "智谱-1", Platform: "zhipu", BaseUrls: `{"anthropic":"x"}`,
		ApiKey: "${ACCOUNT:智谱-1}", GroupName: "智谱", OwnerType: "user", OwnerID: 0,
	})
	if err != nil {
		t.Fatalf("CreateEnvProvider on migrated table: %v", err)
	}
	until := time.Now().Add(time.Hour)
	if err := q.SetProviderCooldown(ctx, SetProviderCooldownParams{ID: prov.ID, CooldownUntil: sql.NullTime{Time: until, Valid: true}}); err != nil {
		t.Fatalf("SetProviderCooldown on migrated table: %v", err)
	}
}
