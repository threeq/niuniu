package sceneenv

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

func cooldown(t time.Time) sql.NullTime {
	return sql.NullTime{Time: t, Valid: true}
}

func TestParseRateLimitReset_ZhipuFullDateTime(t *testing.T) {
	line := "✗ Error: API Error: Request rejected (429) · You have exceeded the 5-hour usage quota. It will reset at 2026-08-23 11:21:17 +0800 CST. We recommend upgrading your plan for more quota, or waiting for the reset. Request id: 02178744691941588db556329240132465f0386a4fcfe59b9f55a"
	got, ok := ParseRateLimitReset(line)
	if !ok {
		t.Fatal("expected reset time parsed")
	}
	want := time.Date(2026, 8, 23, 11, 21, 17, 0, time.FixedZone("", 8*3600))
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseRateLimitReset_ZhipuChinese(t *testing.T) {
	line := "✗ Error: API Error: Request rejected (429) · [1308][已达到 5 小时的使用上限。您的限额将在 2026-08-24 00:27:12 重置。][20260824002313ee87e5b2aed14fa1]"
	got, ok := ParseRateLimitReset(line)
	if !ok {
		t.Fatal("expected reset time parsed")
	}
	want := time.Date(2026, 8, 24, 0, 27, 12, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseRateLimitReset_BailianMonthDayUTC(t *testing.T) {
	line := "✗ Error: API Error: Request rejected (429) · Your token-plan 1-week quota has been exhausted. The quota will reset at 08-29 03:09:00 UTC."
	got, ok := ParseRateLimitReset(line)
	if !ok {
		t.Fatal("expected reset time parsed")
	}
	want := time.Date(time.Now().Year(), 8, 29, 3, 9, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseRateLimitReset_NotARateLimitLine(t *testing.T) {
	cases := []string{
		"",
		"✗ Error: API Error: Request rejected (401) · invalid api key",
		"git: fatal: repository not found",
		"ordinary assistant output about the weather",
	}
	for _, c := range cases {
		if _, ok := ParseRateLimitReset(c); ok {
			t.Errorf("expected no parse for %q", c)
		}
	}
}

func TestParseRateLimitReset_429WithoutResetTime(t *testing.T) {
	// A 429 whose message does not carry a parseable reset time must not
	// fabricate one.
	line := "✗ Error: API Error: Request rejected (429) · too many requests"
	if _, ok := ParseRateLimitReset(line); ok {
		t.Error("expected no parse for a 429 without a reset time")
	}
}

// zhipuProvider builds a 智谱-style provider for fallback tests. Enabled
// defaults to 1 (in rotation) to match a freshly-created DB row.
func zhipuProvider(id int64, name, group string, cooldownAt sql.NullTime) store.EnvProvider {
	return store.EnvProvider{
		ID: id, Name: name, GroupName: group,
		BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey:   "${ACCOUNT:" + name + "}", Model: "glm-5.1",
		Enabled: 1,
		CooldownUntil: cooldownAt,
	}
}

func TestActiveProvider_NoBoundProvider(t *testing.T) {
	q := fakeQuerier{}
	if _, ok := ActiveProvider(context.Background(), q, 7); ok {
		t.Fatal("expected ok=false when no provider is bound")
	}
}

func TestActiveProvider_BoundHealthy(t *testing.T) {
	prov := zhipuProvider(1, "智谱-1", "", sql.NullTime{})
	q := fakeQuerier{boundProvider: &prov, cliType: "claude"}
	got, ok := ActiveProvider(context.Background(), q, 7)
	if !ok || got.ID != 1 {
		t.Fatalf("expected bound provider returned, got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_CooldownNoGroupKeepsBound(t *testing.T) {
	prov := zhipuProvider(1, "智谱-1", "", cooldown(time.Now().Add(time.Hour)))
	q := fakeQuerier{boundProvider: &prov, cliType: "claude"}
	got, ok := ActiveProvider(context.Background(), q, 7)
	if !ok || got.ID != 1 {
		t.Fatalf("expected bound provider kept (no group), got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_CooldownFallsBackToGroupMember(t *testing.T) {
	bound := zhipuProvider(1, "智谱-1", "智谱", cooldown(time.Now().Add(5*time.Hour)))
	fallback := zhipuProvider(2, "智谱-2", "智谱", sql.NullTime{})
	q := fakeQuerier{
		boundProvider: &bound,
		providers:     []store.EnvProvider{bound, fallback},
		cliType:       "claude",
	}
	got, ok := ActiveProvider(context.Background(), q, 7)
	if !ok || got.ID != 2 {
		t.Fatalf("expected fallback provider 2, got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_CooldownAllGroupMembersCooldown(t *testing.T) {
	bound := zhipuProvider(1, "智谱-1", "智谱", cooldown(time.Now().Add(time.Hour)))
	other := zhipuProvider(2, "智谱-2", "智谱", cooldown(time.Now().Add(2*time.Hour)))
	q := fakeQuerier{
		boundProvider: &bound,
		providers:     []store.EnvProvider{bound, other},
		cliType:       "claude",
	}
	got, ok := ActiveProvider(context.Background(), q, 7)
	// No healthy member — degrade to the bound provider rather than erroring.
	if !ok || got.ID != 1 {
		t.Fatalf("expected bound provider kept when no healthy member, got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_FallbackMustServeProtocol(t *testing.T) {
	// The fallback candidate only carries an openai endpoint while the workspace
	// runs Claude (anthropic protocol) — it is NOT interchangeable here.
	bound := zhipuProvider(1, "智谱-1", "智谱", cooldown(time.Now().Add(time.Hour)))
	openaiOnly := store.EnvProvider{
		ID: 2, Name: "智谱-2", GroupName: "智谱",
		BaseUrls: `{"openai":"https://open.bigmodel.cn/api/openai"}`,
		ApiKey:   "${ACCOUNT:智谱-2}",
	}
	q := fakeQuerier{
		boundProvider: &bound,
		providers:     []store.EnvProvider{bound, openaiOnly},
		cliType:       "claude",
	}
	got, ok := ActiveProvider(context.Background(), q, 7)
	if !ok || got.ID != 1 {
		t.Fatalf("expected bound provider kept (candidate cannot serve protocol), got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_DeterministicLowestID(t *testing.T) {
	bound := zhipuProvider(5, "智谱-5", "智谱", cooldown(time.Now().Add(time.Hour)))
	a := zhipuProvider(9, "智谱-9", "智谱", sql.NullTime{})
	b := zhipuProvider(3, "智谱-3", "智谱", sql.NullTime{})
	q := fakeQuerier{
		boundProvider: &bound,
		providers:     []store.EnvProvider{bound, a, b},
		cliType:       "claude",
	}
	got, _ := ActiveProvider(context.Background(), q, 7)
	if got.ID != 3 {
		t.Fatalf("expected lowest-id fallback (3), got %d", got.ID)
	}
}

func TestResolve_CooldownFallsBackToGroupMember(t *testing.T) {
	// End-to-end through Resolve: a bound provider in cooldown must expand the
	// healthy group member's env (base_url + account key) at spawn.
	bound := zhipuProvider(1, "智谱-1", "智谱", cooldown(time.Now().Add(5*time.Hour)))
	fallback := zhipuProvider(2, "智谱-2", "智谱", sql.NullTime{})
	q := fakeQuerier{
		boundProvider: &bound,
		providers:     []store.EnvProvider{bound, fallback},
		accounts:      []store.EnvAccount{{Name: "智谱-2", ApiKey: "sk-zhipu-2"}},
		cliType:       "claude",
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	got := envMap(rows)
	if got["ANTHROPIC_BASE_URL"] != "https://open.bigmodel.cn/api/anthropic" {
		t.Errorf("fallback base_url not expanded: %q", got["ANTHROPIC_BASE_URL"])
	}
	if got["ANTHROPIC_AUTH_TOKEN"] != "sk-zhipu-2" {
		t.Errorf("fallback account key not substituted: %q", got["ANTHROPIC_AUTH_TOKEN"])
	}
	if got["ANTHROPIC_MODEL"] != "glm-5.1" {
		t.Errorf("fallback model not expanded: %q", got["ANTHROPIC_MODEL"])
	}
}

func TestResolve_CooldownNoGroupStillExpandsBound(t *testing.T) {
	// A bound provider in cooldown with no group still expands itself (nothing
	// to fall back to), so the workspace at least keeps the config.
	bound := zhipuProvider(1, "智谱-1", "", cooldown(time.Now().Add(time.Hour)))
	q := fakeQuerier{
		boundProvider: &bound,
		accounts:      []store.EnvAccount{{Name: "智谱-1", ApiKey: "sk-zhipu-1"}},
		cliType:       "claude",
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if got := envMap(rows)["ANTHROPIC_AUTH_TOKEN"]; got != "sk-zhipu-1" {
		t.Errorf("bound provider should still expand: %q", got)
	}
}

func TestActiveProvider_FallbackRespectsGroupPosition(t *testing.T) {
	// Manual order decides which group member is tried first.
	bound := zhipuProvider(1, "智谱-1", "智谱", cooldown(time.Now().Add(time.Hour)))
	pos2 := zhipuProvider(2, "智谱-2", "智谱", sql.NullTime{})
	pos2.GroupPosition = 2
	pos1 := zhipuProvider(3, "智谱-3", "智谱", sql.NullTime{})
	pos1.GroupPosition = 1
	q := fakeQuerier{
		boundProvider: &bound,
		providers:     []store.EnvProvider{bound, pos2, pos1},
		cliType:       "claude",
	}
	got, ok := ActiveProvider(context.Background(), q, 7)
	if !ok || got.ID != 3 {
		t.Fatalf("expected position-1 fallback (id 3), got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_EnabledBoundWinsOverGroupOrder(t *testing.T) {
	// The explicitly-bound provider wins even when a group member has a lower
	// group_position — ordering only decides FALLBACK priority.
	bound := zhipuProvider(1, "智谱-1", "智谱", sql.NullTime{})
	bound.GroupPosition = 5
	lower := zhipuProvider(2, "智谱-2", "智谱", sql.NullTime{})
	lower.GroupPosition = 1
	q := fakeQuerier{
		boundProvider: &bound,
		providers:     []store.EnvProvider{bound, lower},
		cliType:       "claude",
	}
	got, ok := ActiveProvider(context.Background(), q, 7)
	if !ok || got.ID != 1 {
		t.Fatalf("expected bound provider (id 1) despite higher position, got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_DisabledBoundFallsBack(t *testing.T) {
	bound := zhipuProvider(1, "智谱-1", "智谱", sql.NullTime{})
	bound.Enabled = 0 // manually disabled
	fallback := zhipuProvider(2, "智谱-2", "智谱", sql.NullTime{})
	q := fakeQuerier{
		boundProvider: &bound,
		providers:     []store.EnvProvider{bound, fallback},
		cliType:       "claude",
	}
	got, ok := ActiveProvider(context.Background(), q, 7)
	if !ok || got.ID != 2 {
		t.Fatalf("expected fallback for disabled bound, got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_DisabledBoundNoGroupIsNotUsable(t *testing.T) {
	bound := zhipuProvider(1, "智谱-1", "", sql.NullTime{})
	bound.Enabled = 0 // standalone + manually disabled → nothing to use
	q := fakeQuerier{boundProvider: &bound, cliType: "claude"}
	if _, ok := ActiveProvider(context.Background(), q, 7); ok {
		t.Fatal("expected ok=false when the only (standalone) provider is disabled")
	}
}

func TestActiveProvider_DisabledGroupMemberSkipped(t *testing.T) {
	bound := zhipuProvider(1, "智谱-1", "智谱", cooldown(time.Now().Add(time.Hour)))
	disabled := zhipuProvider(2, "智谱-2", "智谱", sql.NullTime{})
	disabled.Enabled = 0
	healthy := zhipuProvider(3, "智谱-3", "智谱", sql.NullTime{})
	q := fakeQuerier{
		boundProvider: &bound,
		providers:     []store.EnvProvider{bound, disabled, healthy},
		cliType:       "claude",
	}
	got, ok := ActiveProvider(context.Background(), q, 7)
	if !ok || got.ID != 3 {
		t.Fatalf("expected healthy (id 3) to be picked, disabled skipped, got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_GroupBindingPicksBestUsable(t *testing.T) {
	// Workspace bound to a GROUP (workspaces.env_provider_group) — the group's
	// best usable member by manual order is used, not any specific provider.
	pos3 := zhipuProvider(1, "智谱-3", "智谱", sql.NullTime{})
	pos3.GroupPosition = 3
	pos1 := zhipuProvider(2, "智谱-1", "智谱", sql.NullTime{})
	pos1.GroupPosition = 1
	pos2 := zhipuProvider(3, "智谱-2", "智谱", sql.NullTime{})
	pos2.GroupPosition = 2
	q := fakeQuerier{
		providers:    []store.EnvProvider{pos3, pos1, pos2},
		groupBinding: "智谱",
		cliType:      "claude",
	}
	got, ok := ActiveProvider(context.Background(), q, 7)
	if !ok || got.ID != 2 {
		t.Fatalf("expected lowest-position member (id 2), got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_GroupBindingSkipsDisabledAndCooldown(t *testing.T) {
	disabled := zhipuProvider(1, "智谱-1", "智谱", sql.NullTime{})
	disabled.GroupPosition = 1
	disabled.Enabled = 0
	cooldownP := zhipuProvider(2, "智谱-2", "智谱", cooldown(time.Now().Add(time.Hour)))
	cooldownP.GroupPosition = 2
	healthy := zhipuProvider(3, "智谱-3", "智谱", sql.NullTime{})
	healthy.GroupPosition = 3
	q := fakeQuerier{
		providers:    []store.EnvProvider{disabled, cooldownP, healthy},
		groupBinding: "智谱",
		cliType:      "claude",
	}
	got, ok := ActiveProvider(context.Background(), q, 7)
	if !ok || got.ID != 3 {
		t.Fatalf("expected the only healthy member (id 3), got %+v ok=%v", got, ok)
	}
}

func TestActiveProvider_GroupBindingAllDownNotUsable(t *testing.T) {
	a := zhipuProvider(1, "智谱-1", "智谱", cooldown(time.Now().Add(time.Hour)))
	b := zhipuProvider(2, "智谱-2", "智谱", cooldown(time.Now().Add(time.Hour)))
	q := fakeQuerier{
		providers:    []store.EnvProvider{a, b},
		groupBinding: "智谱",
		cliType:      "claude",
	}
	if _, ok := ActiveProvider(context.Background(), q, 7); ok {
		t.Fatal("expected ok=false when every group member is unusable")
	}
}

func TestActiveProvider_GroupBindingUnknownGroupNotUsable(t *testing.T) {
	q := fakeQuerier{providers: []store.EnvProvider{zhipuProvider(1, "智谱-1", "智谱", sql.NullTime{})}, groupBinding: "不存在", cliType: "claude"}
	if _, ok := ActiveProvider(context.Background(), q, 7); ok {
		t.Fatal("expected ok=false for an unknown group")
	}
}

func TestResolve_GroupBindingExpandsBestMember(t *testing.T) {
	first := zhipuProvider(1, "智谱-1", "智谱", sql.NullTime{})
	first.GroupPosition = 1
	second := zhipuProvider(2, "智谱-2", "智谱", sql.NullTime{})
	second.GroupPosition = 2
	q := fakeQuerier{
		providers:    []store.EnvProvider{first, second},
		groupBinding: "智谱",
		accounts:     []store.EnvAccount{{Name: "智谱-1", ApiKey: "sk-1"}},
		cliType:      "claude",
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	got := envMap(rows)
	if got["ANTHROPIC_AUTH_TOKEN"] != "sk-1" {
		t.Errorf("expected group's best member key expanded, got %q", got["ANTHROPIC_AUTH_TOKEN"])
	}
}

func TestMarkProviderCooldown_RoundTrip(t *testing.T) {
	prov := zhipuProvider(1, "智谱-1", "智谱", sql.NullTime{})
	q := fakeQuerier{providers: []store.EnvProvider{prov}}
	until := time.Now().Add(2 * time.Hour)
	if err := MarkProviderCooldown(context.Background(), q, 1, until); err != nil {
		t.Fatalf("MarkProviderCooldown error: %v", err)
	}
	if !ProviderInCooldown(q.providers[0], time.Now()) {
		t.Error("provider should be in cooldown after marking")
	}
	if err := ClearProviderCooldown(context.Background(), q, 1); err != nil {
		t.Fatalf("ClearProviderCooldown error: %v", err)
	}
	if ProviderInCooldown(q.providers[0], time.Now()) {
		t.Error("provider should be healthy after clearing")
	}
}

func TestProviderInCooldown_ExpiredByTime(t *testing.T) {
	now := time.Now()
	p := zhipuProvider(1, "智谱-1", "", cooldown(now.Add(-time.Minute)))
	if ProviderInCooldown(p, now) {
		t.Error("expired cooldown should count as healthy")
	}
	p = zhipuProvider(1, "智谱-1", "", sql.NullTime{})
	if ProviderInCooldown(p, now) {
		t.Error("NULL cooldown should count as healthy")
	}
}
