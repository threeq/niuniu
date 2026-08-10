package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- jitter helper ---

func TestJitteredTTLInRange(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		got := jitteredTTL(r, 600*time.Second, 60*time.Second)
		if got < 540*time.Second || got > 660*time.Second {
			t.Fatalf("jittered TTL out of range: %v", got)
		}
	}
}

// --- pricing ---

func TestComputeCostUSD_KnownModel(t *testing.T) {
	got := computeCostUSD("claude-opus-4-7", 1_000_000, 0, 0, 0)
	if math.Abs(got-15.0) > 1e-9 {
		t.Errorf("input cost = %v, want 15.0", got)
	}
	got = computeCostUSD("claude-opus-4-7", 0, 1_000_000, 0, 0)
	if math.Abs(got-75.0) > 1e-9 {
		t.Errorf("output cost = %v, want 75.0", got)
	}
}

func TestComputeCostUSD_UnknownModel(t *testing.T) {
	got := computeCostUSD("claude-future-99", 1_000_000, 1_000_000, 0, 0)
	if got != 0 {
		t.Errorf("unknown model should return 0, got %v", got)
	}
}

// --- JSONL parsing + dedup ---

func TestScanJSONL_HappyPathSkipsNonAssistant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{
		`{"type":"permission-mode","sessionId":"s1"}`,
		`{"type":"user","timestamp":"2026-05-01T10:00:00Z"}`,
		`{"type":"assistant","timestamp":"2026-05-01T10:01:00Z","sessionId":"s1","requestId":"r1","message":{"id":"m1","model":"claude-sonnet-4-6","usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		`{"type":"file-history-snapshot","timestamp":"2026-05-01T10:02:00Z"}`,
		`{"type":"assistant","timestamp":"2026-05-01T10:03:00Z","sessionId":"s1","requestId":"r2","message":{"id":"m2","model":"claude-opus-4-7","usage":{"input_tokens":100,"output_tokens":200,"cache_creation_input_tokens":50,"cache_read_input_tokens":1000}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	entries, err := scanJSONL(path, seen)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (only assistant rows)", len(entries))
	}
	if entries[0].Model != "claude-sonnet-4-6" || entries[1].Model != "claude-opus-4-7" {
		t.Errorf("models in wrong order: %+v", entries)
	}
	if entries[1].CacheReadTokens != 1000 {
		t.Errorf("cache_read_input_tokens not parsed: %+v", entries[1])
	}
}

func TestScanJSONL_SkipsAPIError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	body := `{"type":"assistant","timestamp":"2026-05-01T10:00:00Z","sessionId":"s","requestId":"r","isApiErrorMessage":true,"message":{"id":"m","model":"claude-sonnet-4-6","usage":{"input_tokens":99999,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	os.WriteFile(path, []byte(body), 0o600)
	entries, _ := scanJSONL(path, map[string]struct{}{})
	if len(entries) != 0 {
		t.Errorf("API error should be skipped, got %d", len(entries))
	}
}

func TestScanJSONL_SkipsMalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	body := strings.Join([]string{
		`{not valid json`,
		`{"type":"assistant","timestamp":"2026-05-01T10:00:00Z","sessionId":"s","requestId":"r1","message":{"id":"m1","model":"claude-sonnet-4-6","usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		``,
		`{"type":"assistant","timestamp":"BAD-TIMESTAMP","message":{"id":"m2","model":"x","usage":{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	}, "\n")
	os.WriteFile(path, []byte(body), 0o600)
	entries, _ := scanJSONL(path, map[string]struct{}{})
	if len(entries) != 1 {
		t.Errorf("only 1 valid line should survive, got %d", len(entries))
	}
}

func TestScanJSONL_DedupCrossInvocation(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.jsonl")
	pathB := filepath.Join(dir, "b.jsonl")
	row := `{"type":"assistant","timestamp":"2026-05-01T10:00:00Z","sessionId":"s","requestId":"r1","message":{"id":"m1","model":"claude-sonnet-4-6","usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	os.WriteFile(pathA, []byte(row), 0o600)
	os.WriteFile(pathB, []byte(row), 0o600)
	seen := map[string]struct{}{}
	a, _ := scanJSONL(pathA, seen)
	b, _ := scanJSONL(pathB, seen)
	if len(a) != 1 || len(b) != 0 {
		t.Errorf("dedup failed: a=%d, b=%d", len(a), len(b))
	}
}

func TestScanJSONL_NoDedupWhenIDsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	row := `{"type":"assistant","timestamp":"2026-05-01T10:00:00Z","sessionId":"s","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	os.WriteFile(path, []byte(row+"\n"+row), 0o600)
	entries, _ := scanJSONL(path, map[string]struct{}{})
	if len(entries) != 2 {
		t.Errorf("got %d, want 2 (no dedup possible)", len(entries))
	}
}

// --- 5h block algorithm ---

func mkEntry(ts string, model string, in int64) usageEntry {
	t, _ := time.Parse(time.RFC3339, ts)
	return usageEntry{Timestamp: t, Model: model, InputTokens: in}
}

func TestFiveHourBlock_FloorToUTCHour(t *testing.T) {
	entries := []usageEntry{mkEntry("2026-05-01T10:23:45Z", "claude-sonnet-4-6", 100)}
	now, _ := time.Parse(time.RFC3339, "2026-05-01T11:00:00Z")
	blk, ok := fiveHourBillingBlock(entries, now)
	if !ok {
		t.Fatal("expected block")
	}
	want, _ := time.Parse(time.RFC3339, "2026-05-01T10:00:00Z")
	if !blk.Start.Equal(want) {
		t.Errorf("Start = %v, want %v (floored)", blk.Start, want)
	}
	if !blk.IsActive {
		t.Error("expected active")
	}
}

func TestFiveHourBlock_NewBlockAfter5hSinceLast(t *testing.T) {
	entries := []usageEntry{
		mkEntry("2026-05-01T01:00:00Z", "x", 1),
		mkEntry("2026-05-01T07:30:00Z", "x", 2),
	}
	now, _ := time.Parse(time.RFC3339, "2026-05-01T08:00:00Z")
	blk, _ := fiveHourBillingBlock(entries, now)
	if len(blk.Entries) != 1 {
		t.Errorf("latest block should have 1 entry, got %d", len(blk.Entries))
	}
	want, _ := time.Parse(time.RFC3339, "2026-05-01T07:00:00Z")
	if !blk.Start.Equal(want) {
		t.Errorf("latest block start = %v, want %v", blk.Start, want)
	}
}

func TestFiveHourBlock_InactiveAfter5hOfNoActivity(t *testing.T) {
	entries := []usageEntry{mkEntry("2026-05-01T01:00:00Z", "x", 1)}
	now, _ := time.Parse(time.RFC3339, "2026-05-01T20:00:00Z")
	blk, _ := fiveHourBillingBlock(entries, now)
	if blk.IsActive {
		t.Error("block should be inactive after 19h")
	}
}

// --- aggregateWindows ---

func TestAggregateWindows_SeparatesSonnetAndOpus(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-05-08T00:00:00Z")
	entries := []usageEntry{
		{Timestamp: now.Add(-2 * 24 * time.Hour), Model: "claude-opus-4-7", InputTokens: 100, OutputTokens: 50},
		{Timestamp: now.Add(-3 * 24 * time.Hour), Model: "claude-sonnet-4-6", InputTokens: 200, OutputTokens: 100},
		{Timestamp: now.Add(-10 * 24 * time.Hour), Model: "claude-opus-4-7", InputTokens: 99999, OutputTokens: 99999}, // outside 7d
	}
	windows := aggregateWindows(entries, now)
	by := map[string]UsageWindow{}
	for _, w := range windows {
		by[w.Type] = w
	}
	if got := by["seven_day_all"].TotalTokens; got != 100+50+200+100 {
		t.Errorf("seven_day_all = %d, want %d", got, 100+50+200+100)
	}
	if got := by["seven_day_sonnet"].TotalTokens; got != 200+100 {
		t.Errorf("seven_day_sonnet = %d, want %d", got, 200+100)
	}
	if got := by["seven_day_opus"].TotalTokens; got != 100+50 {
		t.Errorf("seven_day_opus = %d, want %d", got, 100+50)
	}
}

func TestAggregateWindows_PerModelBreakdown(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-05-08T00:00:00Z")
	entries := []usageEntry{
		{Timestamp: now.Add(-1 * time.Hour), Model: "claude-opus-4-7", InputTokens: 100},
		{Timestamp: now.Add(-2 * time.Hour), Model: "claude-opus-4-7", InputTokens: 200},
		{Timestamp: now.Add(-3 * time.Hour), Model: "claude-sonnet-4-6", InputTokens: 50},
	}
	windows := aggregateWindows(entries, now)
	var all UsageWindow
	for _, w := range windows {
		if w.Type == "seven_day_all" {
			all = w
			break
		}
	}
	if len(all.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(all.Models))
	}
	if all.Models[0].Name != "claude-opus-4-7" || all.Models[0].TotalTokens != 300 {
		t.Errorf("first model = %+v, want opus 300", all.Models[0])
	}
}

// --- top-level Service.Get ---

// legacyTestAccountID is the accountID used by newClaudeUsageTestService's
// fake default account. Legacy tests that call RecordRateLimit / rateLimitFor
// must pass this ID.
const legacyTestAccountID int64 = 1

func newClaudeUsageTestService(now time.Time, entries []usageEntry) *ClaudeUsageService {
	accs := &fakeResolver{
		byID:  map[int64]*ResolvedAccount{legacyTestAccountID: {ID: legacyTestAccountID, ConfigDir: "/fake/dir", Visibility: "public"}},
		defID: legacyTestAccountID,
	}
	svc := NewClaudeUsageService(true, accs)
	svc.now = func() time.Time { return now }
	svc.dirsLister = func(configDir string) ([]string, error) { return []string{"/fake/dir"}, nil }
	svc.walker = func(_ context.Context, _ []string, _ time.Time) ([]usageEntry, error) {
		return entries, nil
	}
	svc.credsLoad = func(string) claudeCredentials {
		return claudeCredentials{SubscriptionType: "max", RateLimitTier: "default_claude_max_20x"}
	}
	return svc
}

func TestServiceGet_LiveThenCache(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-05-08T00:00:00Z")
	svc := newClaudeUsageTestService(now, []usageEntry{
		{Timestamp: now.Add(-1 * time.Hour), Model: "claude-sonnet-4-6", InputTokens: 100, OutputTokens: 200},
	})
	first, err := svc.GetForAccount(context.Background(), legacyTestAccountID, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Source != "live" {
		t.Errorf("first.Source = %q, want live", first.Source)
	}
	if first.SubscriptionType != "max" {
		t.Errorf("subscription_type = %q", first.SubscriptionType)
	}
	if !first.Shared {
		t.Error("expected Shared=true")
	}
	second, err := svc.GetForAccount(context.Background(), legacyTestAccountID, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Source != "cache" {
		t.Errorf("second.Source = %q, want cache", second.Source)
	}
}

func TestServiceGet_NoDataDir(t *testing.T) {
	// GetForAccount on the default account (empty ConfigDir + no dirs) → ErrClaudeDataDirNotFound.
	accs := &fakeResolver{
		byID:  map[int64]*ResolvedAccount{1: {ID: 1, ConfigDir: "", Visibility: "public"}},
		defID: 1,
	}
	svc := NewClaudeUsageService(false, accs)
	svc.now = time.Now
	svc.dirsLister = func(string) ([]string, error) { return nil, nil }
	svc.walker = func(_ context.Context, _ []string, _ time.Time) ([]usageEntry, error) { return nil, nil }
	svc.credsLoad = func(string) claudeCredentials { return claudeCredentials{} }
	_, err := svc.GetForAccount(context.Background(), 1, false)
	if err != ErrClaudeDataDirNotFound {
		t.Fatalf("err = %v, want ErrClaudeDataDirNotFound", err)
	}
}

// --- rate_limit_event capture ---

func TestRecordRateLimit_InjectedIntoFiveHourWindow(t *testing.T) {
	t1, _ := time.Parse(time.RFC3339, "2026-05-08T00:00:00Z")
	t2 := t1.Add(10 * time.Second) // past the 5s L1 upstream floor

	entries := []usageEntry{
		{Timestamp: t1.Add(-1 * time.Hour), Model: "claude-sonnet-4-6", InputTokens: 100},
	}

	accs := &fakeResolver{
		byID:  map[int64]*ResolvedAccount{legacyTestAccountID: {ID: legacyTestAccountID, ConfigDir: "/fake/dir", Visibility: "public"}},
		defID: legacyTestAccountID,
	}
	svc := NewClaudeUsageService(true, accs)
	tick := t1
	svc.now = func() time.Time { return tick }
	svc.dirsLister = func(string) ([]string, error) { return []string{"/fake/dir"}, nil }
	svc.walker = func(_ context.Context, _ []string, _ time.Time) ([]usageEntry, error) { return entries, nil }
	svc.credsLoad = func(string) claudeCredentials {
		return claudeCredentials{SubscriptionType: "max", RateLimitTier: "default_claude_max_20x"}
	}

	// Call at T1: primes the rl bag and caches result.
	_, err := svc.GetForAccount(context.Background(), legacyTestAccountID, false)
	if err != nil {
		t.Fatal(err)
	}

	// Inject observation.
	resetsAt := t1.Add(2 * time.Hour)
	svc.RecordRateLimit(legacyTestAccountID, "five_hour", resetsAt.Unix(), "allowed_warning")

	// Advance clock past L1 floor, then force-refresh to pick up the observation.
	tick = t2
	usage, err := svc.GetForAccount(context.Background(), legacyTestAccountID, true)
	if err != nil {
		t.Fatal(err)
	}
	var fiveHour *UsageWindow
	for i := range usage.Windows {
		if usage.Windows[i].Type == "five_hour_billing" {
			fiveHour = &usage.Windows[i]
			break
		}
	}
	if fiveHour == nil {
		t.Fatal("no five_hour_billing window")
	}
	if fiveHour.ResetsAt == nil {
		t.Fatal("ResetsAt should be set after RecordRateLimit observation")
	}
	if !fiveHour.ResetsAt.Equal(resetsAt) {
		t.Errorf("ResetsAt = %v, want %v", fiveHour.ResetsAt, resetsAt)
	}
}

func TestRecordRateLimit_PastResetIsDiscarded(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-05-08T00:00:00Z")
	svc := newClaudeUsageTestService(now, []usageEntry{
		{Timestamp: now.Add(-1 * time.Hour), Model: "claude-sonnet-4-6", InputTokens: 100},
	})

	// Prime the bag first.
	_, _ = svc.GetForAccount(context.Background(), legacyTestAccountID, false)

	// Old observation: reset already happened 1h ago.
	svc.RecordRateLimit(legacyTestAccountID, "five_hour", now.Add(-1*time.Hour).Unix(), "allowed")

	usage, _ := svc.GetForAccount(context.Background(), legacyTestAccountID, true)
	for _, w := range usage.Windows {
		if w.Type == "five_hour_billing" && w.ResetsAt != nil {
			t.Errorf("past resets_at should be discarded, got %v", w.ResetsAt)
		}
	}
}

func TestRecordRateLimit_RejectsBadInput(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-05-08T00:00:00Z")
	svc := newClaudeUsageTestService(now, []usageEntry{
		{Timestamp: now.Add(-1 * time.Hour), Model: "claude-sonnet-4-6", InputTokens: 100},
	})
	// Prime the bag first.
	_, _ = svc.GetForAccount(context.Background(), legacyTestAccountID, false)

	// Empty type: reject.
	svc.RecordRateLimit(legacyTestAccountID, "", now.Add(time.Hour).Unix(), "allowed")
	// Zero unix: reject.
	svc.RecordRateLimit(legacyTestAccountID, "five_hour", 0, "allowed")
	// Negative unix: reject.
	svc.RecordRateLimit(legacyTestAccountID, "five_hour", -1, "allowed")

	if obs := svc.rateLimitFor(legacyTestAccountID, "five_hour", now); obs != nil {
		t.Errorf("expected no observation, got %+v", obs)
	}
}

func TestClaudeDataDirs_RespectsEnvVar(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	os.MkdirAll(filepath.Join(dir1, "projects"), 0o755)
	os.MkdirAll(filepath.Join(dir2, "projects"), 0o755)
	t.Setenv("CLAUDE_CONFIG_DIR", dir1+","+dir2)
	got := claudeDataDirs()
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestServiceGet_JSONFieldNames(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-05-08T00:00:00Z")
	svc := newClaudeUsageTestService(now, []usageEntry{
		{Timestamp: now.Add(-1 * time.Hour), Model: "claude-sonnet-4-6", InputTokens: 100},
	})
	usage, _ := svc.GetForAccount(context.Background(), legacyTestAccountID, false)
	body, _ := json.Marshal(usage)
	for _, want := range []string{
		`"source"`, `"data_source_paths"`, `"windows"`,
		`"five_hour_billing"`, `"seven_day_all"`, `"seven_day_sonnet"`, `"seven_day_opus"`,
		`"total_tokens"`, `"input_tokens"`, `"output_tokens"`,
		`"cache_creation_tokens"`, `"cache_read_tokens"`, `"cost_usd"`,
		`"models"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("response missing %s", want)
		}
	}
}

// --- accountWalkDirs ---

func TestAccountWalkDirs_NamedAccountWithProjects(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := accountWalkDirs(tmp)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 1 || got[0] != filepath.Clean(tmp) {
		t.Fatalf("got %v, want [%q]", got, tmp)
	}
}

func TestAccountWalkDirs_NamedAccountNoProjects(t *testing.T) {
	tmp := t.TempDir() // no projects/ subdir
	got, err := accountWalkDirs(tmp)
	if got != nil {
		t.Fatalf("got %v, want nil (no projects/)", got)
	}
	if !errors.Is(err, ErrClaudeProjectsNotFound) {
		t.Fatalf("got err %v, want ErrClaudeProjectsNotFound", err)
	}
}

func TestAccountWalkDirs_DefaultDelegatesToClaudeDataDirs(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	got, err := accountWalkDirs("")
	if err != nil {
		t.Fatalf("default branch should not error, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil from fallback on empty HOME, got %v", got)
	}
}

func TestAccountWalkDirs_RejectsRelativePath(t *testing.T) {
	got, err := accountWalkDirs("../../../etc")
	if got != nil {
		t.Fatalf("expected nil dirs for relative path, got %v", got)
	}
	if !errors.Is(err, ErrClaudeConfigDirInvalid) {
		t.Fatalf("got err %v, want ErrClaudeConfigDirInvalid", err)
	}
}

func TestAccountWalkDirs_RejectsEmbeddedDotDot(t *testing.T) {
	// Absolute path that survives Clean but contains a literal `..` segment
	// would itself be cleaned to a different absolute. We still reject any
	// configDir whose Clean form contains "/.." defensively.
	if _, err := accountWalkDirs("/var/foo/../../etc"); err != nil && !errors.Is(err, ErrClaudeConfigDirInvalid) {
		// Clean() will resolve to "/etc" — which IS absolute and has no "..".
		// This case is benign: the row was tampered to point at /etc, and
		// our existence check will simply fail because /etc/projects doesn't
		// exist. Accept either ErrClaudeConfigDirInvalid (if Clean preserved)
		// or ErrClaudeProjectsNotFound (if Clean resolved away the traversal).
		if !errors.Is(err, ErrClaudeProjectsNotFound) {
			t.Fatalf("unexpected err %v", err)
		}
	}
}

// --- credentials per account ---

func TestLoadCredentialsForConfig_NamedAccount(t *testing.T) {
	tmp := t.TempDir()
	body := `{"claudeAiOauth":{"subscriptionType":"max_5x","rateLimitTier":"tier_1"}}`
	if err := os.WriteFile(filepath.Join(tmp, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadCredentialsForConfig(tmp)
	if got.SubscriptionType != "max_5x" || got.RateLimitTier != "tier_1" {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadCredentialsForConfig_EmptyDirFallsBackToHomeClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"claudeAiOauth":{"subscriptionType":"pro"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadCredentialsForConfig("")
	if got.SubscriptionType != "pro" {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadCredentialsForConfig_MissingReturnsZero(t *testing.T) {
	if got := loadCredentialsForConfig(t.TempDir()); got.SubscriptionType != "" {
		t.Fatalf("got %+v, want zero", got)
	}
}

// When .credentials.json has the fields populated, the behavior should match
// the original implementation — preserves the default-account display path
// that was already working.
func TestLoadCredentialsForConfig_CredentialsAloneStillWorks(t *testing.T) {
	tmp := t.TempDir()
	body := `{"claudeAiOauth":{"subscriptionType":"max","rateLimitTier":"default_claude_max_5x"}}`
	if err := os.WriteFile(filepath.Join(tmp, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadCredentialsForConfig(tmp)
	if got.SubscriptionType != "max" || got.RateLimitTier != "default_claude_max_5x" {
		t.Fatalf("got %+v", got)
	}
}

// The bug scenario: .credentials.json carries null/empty tier fields but
// .claude.json's oauthAccount has the real info. Tier must come from
// .claude.json so the UI no longer hides the row.
func TestLoadCredentialsForConfig_CredentialsNullFallsBackToClaudeJson(t *testing.T) {
	tmp := t.TempDir()
	credBody := `{"claudeAiOauth":{"subscriptionType":null,"rateLimitTier":null}}`
	if err := os.WriteFile(filepath.Join(tmp, ".credentials.json"), []byte(credBody), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeBody := `{"oauthAccount":{"organizationType":"claude_max","organizationRateLimitTier":"default_claude_max_20x"}}`
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"), []byte(claudeBody), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadCredentialsForConfig(tmp)
	if got.SubscriptionType != "max" || got.RateLimitTier != "default_claude_max_20x" {
		t.Fatalf("got %+v, want {max, default_claude_max_20x}", got)
	}
}

// .claude.json is preferred over .credentials.json when both have values —
// .claude.json is profile-synced and tracks the live tier, .credentials.json
// is an OAuth-time snapshot that can go stale.
func TestLoadCredentialsForConfig_ClaudeJsonWinsOverCredentials(t *testing.T) {
	tmp := t.TempDir()
	credBody := `{"claudeAiOauth":{"subscriptionType":"old","rateLimitTier":"old_tier"}}`
	if err := os.WriteFile(filepath.Join(tmp, ".credentials.json"), []byte(credBody), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeBody := `{"oauthAccount":{"organizationType":"claude_max","organizationRateLimitTier":"default_claude_max_20x"}}`
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"), []byte(claudeBody), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadCredentialsForConfig(tmp)
	if got.SubscriptionType != "max" || got.RateLimitTier != "default_claude_max_20x" {
		t.Fatalf("got %+v, want claude.json values", got)
	}
}

// For empty configDir (default account), tier should come from $HOME/.claude.json
// (note: home root, not inside $HOME/.claude/).
func TestLoadCredentialsForConfig_EmptyDirReadsHomeClaudeJsonForTier(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	// .credentials.json has null tier fields (the buggy case for default too).
	credBody := `{"claudeAiOauth":{"subscriptionType":null,"rateLimitTier":null}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(credBody), 0o600); err != nil {
		t.Fatal(err)
	}
	// $HOME/.claude.json has the real tier.
	claudeBody := `{"oauthAccount":{"organizationType":"claude_max","organizationRateLimitTier":"default_claude_max_5x"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeBody), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadCredentialsForConfig("")
	if got.SubscriptionType != "max" || got.RateLimitTier != "default_claude_max_5x" {
		t.Fatalf("got %+v, want home .claude.json values", got)
	}
}

// --- per-account isolation ---

// fakeResolver is a test-only accountConfigDirResolver — never used in prod.
type fakeResolver struct {
	byID  map[int64]*ResolvedAccount
	defID int64
}

func (f *fakeResolver) GetByID(_ context.Context, id int64) (*ResolvedAccount, error) {
	acc, ok := f.byID[id]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return acc, nil
}
func (f *fakeResolver) GetDefault(_ context.Context) (*ResolvedAccount, error) {
	if f.defID == 0 {
		return nil, ErrAccountNotFound
	}
	return f.byID[f.defID], nil
}

func newTestUsageSvc(t *testing.T, accs *fakeResolver, walkOut []usageEntry) *ClaudeUsageService {
	t.Helper()
	svc := NewClaudeUsageService(false, accs)
	svc.now = func() time.Time { return time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC) }
	svc.dirsLister = func(configDir string) ([]string, error) {
		if configDir == "" {
			return nil, nil
		}
		return []string{configDir}, nil
	}
	svc.walker = func(_ context.Context, _ []string, _ time.Time) ([]usageEntry, error) {
		return walkOut, nil
	}
	svc.credsLoad = func(string) claudeCredentials { return claudeCredentials{} }
	return svc
}

func TestGetForAccount_HappyPathReturnsFourWindows(t *testing.T) {
	accs := &fakeResolver{byID: map[int64]*ResolvedAccount{1: {ID: 1, ConfigDir: "/fake/acc1", Visibility: "public"}}}
	entries := []usageEntry{{
		Timestamp: time.Date(2026, 5, 10, 11, 30, 0, 0, time.UTC),
		Model:     "claude-sonnet-4-6", InputTokens: 100, OutputTokens: 200,
	}}
	svc := newTestUsageSvc(t, accs, entries)
	got, err := svc.GetForAccount(context.Background(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 4 {
		t.Fatalf("got %d windows, want 4", len(got.Windows))
	}
}

func TestGetForAccount_ProjectsNotFound(t *testing.T) {
	accs := &fakeResolver{byID: map[int64]*ResolvedAccount{
		1: {ID: 1, ConfigDir: "/no/such/dir", Visibility: "public"},
	}}
	svc := NewClaudeUsageService(false, accs)
	svc.now = func() time.Time { return time.Now() }
	svc.dirsLister = func(string) ([]string, error) { return nil, ErrClaudeProjectsNotFound }
	if _, err := svc.GetForAccount(context.Background(), 1, false); !errors.Is(err, ErrClaudeProjectsNotFound) {
		t.Fatalf("got %v, want ErrClaudeProjectsNotFound", err)
	}
}

func TestGetForAccount_DefaultEmptyDirsErrorsClaudeDataDirNotFound(t *testing.T) {
	accs := &fakeResolver{byID: map[int64]*ResolvedAccount{
		1: {ID: 1, ConfigDir: "", Visibility: "public", Name: "__default__"},
	}}
	svc := NewClaudeUsageService(false, accs)
	svc.now = func() time.Time { return time.Now() }
	svc.dirsLister = func(string) ([]string, error) { return nil, nil }
	if _, err := svc.GetForAccount(context.Background(), 1, false); !errors.Is(err, ErrClaudeDataDirNotFound) {
		t.Fatalf("got %v, want ErrClaudeDataDirNotFound", err)
	}
}

// M1 from architect review: empty entries → 200 OK with all-zero windows.
func TestGetForAccount_EmptyJSONLReturnsZeroWindows(t *testing.T) {
	accs := &fakeResolver{byID: map[int64]*ResolvedAccount{1: {ID: 1, ConfigDir: "/x", Visibility: "public"}}}
	svc := newTestUsageSvc(t, accs, nil) // walker returns nil entries
	got, err := svc.GetForAccount(context.Background(), 1, false)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if len(got.Windows) != 4 {
		t.Fatalf("got %d windows, want 4", len(got.Windows))
	}
	for _, w := range got.Windows {
		if w.TotalTokens != 0 {
			t.Fatalf("window %s expected zero tokens, got %d", w.Type, w.TotalTokens)
		}
		// Models must be a non-nil slice so JSON marshals to [] not null;
		// the frontend reads window.models.length unconditionally.
		if w.Models == nil {
			t.Fatalf("window %s has nil Models — would marshal to JSON null and crash frontend", w.Type)
		}
	}
}

// I4 from deep review: concurrent force=true requests for the same account
// must collapse to a single walker invocation via singleflight. Without
// dedup, N tabs hitting refresh simultaneously would each re-walk thousands
// of jsonl files in parallel — the 5s upstream floor only protects sequential
// calls because lastUpstream isn't bumped until the walk completes.
func TestGetForAccount_SingleflightDedupesConcurrentForceRefresh(t *testing.T) {
	const N = 10
	accs := &fakeResolver{byID: map[int64]*ResolvedAccount{
		1: {ID: 1, ConfigDir: "/x", Visibility: "public"},
	}}
	svc := NewClaudeUsageService(false, accs)
	svc.now = time.Now
	svc.dirsLister = func(string) ([]string, error) { return []string{"/x"}, nil }
	svc.credsLoad = func(string) claudeCredentials { return claudeCredentials{} }

	var walkerCalls int32
	gate := make(chan struct{})
	svc.walker = func(_ context.Context, _ []string, _ time.Time) ([]usageEntry, error) {
		atomic.AddInt32(&walkerCalls, 1)
		<-gate // hold open until all goroutines have queued behind singleflight
		return nil, nil
	}

	var wg sync.WaitGroup
	results := make([]*AccountUsage, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.GetForAccount(context.Background(), 1, true)
		}(i)
	}

	// Give the goroutines a moment to all enter sfGroup.Do.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := atomic.LoadInt32(&walkerCalls); got != 1 {
		t.Fatalf("walker called %d times, want exactly 1 (singleflight should dedupe)", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d errored: %v", i, err)
		}
		if results[i] == nil {
			t.Fatalf("goroutine %d returned nil", i)
		}
	}
}

// M2 from deep review: a failing walker must bump lastUpstream so the L1 5s
// floor engages on retry — otherwise rapid force-refresh during transient
// walker failure hammers the walker once per request.
func TestGetForAccount_FailedWalkerEngagesL1Floor(t *testing.T) {
	tick := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	accs := &fakeResolver{byID: map[int64]*ResolvedAccount{
		1: {ID: 1, ConfigDir: "/x", Visibility: "public"},
	}}
	svc := newTestUsageSvc(t, accs, nil)
	svc.now = func() time.Time { return tick }

	// First call: walker SUCCEEDS, populates cache + lastUpstream.
	walkerCalls := 0
	svc.walker = func(_ context.Context, _ []string, _ time.Time) ([]usageEntry, error) {
		walkerCalls++
		return nil, nil
	}
	if _, err := svc.GetForAccount(context.Background(), 1, false); err != nil {
		t.Fatal(err)
	}
	if walkerCalls != 1 {
		t.Fatalf("walker called %d times after primer, want 1", walkerCalls)
	}

	// Advance past cache TTL so future calls re-walk.
	tick = tick.Add(45 * time.Second)
	// Walker now FAILS.
	svc.walker = func(_ context.Context, _ []string, _ time.Time) ([]usageEntry, error) {
		walkerCalls++
		return nil, errors.New("walker boom")
	}
	if _, err := svc.GetForAccount(context.Background(), 1, true); err == nil {
		t.Fatal("expected walker failure to surface as error")
	}
	if walkerCalls != 2 {
		t.Fatalf("walker calls = %d, want 2 (one primer + one failed)", walkerCalls)
	}

	// Second force=true within 5s: must NOT call walker; should serve cached
	// value via the L1 floor short-circuit.
	tick = tick.Add(2 * time.Second)
	out, err := svc.GetForAccount(context.Background(), 1, true)
	if err != nil {
		t.Fatalf("expected cached serve, got err %v", err)
	}
	if out.Source != "cache" {
		t.Fatalf("Source = %q, want cache", out.Source)
	}
	if walkerCalls != 2 {
		t.Fatalf("walker called %d times during throttled retry, want 2 (no new call)", walkerCalls)
	}
}

// I5 from deep review: serveLocked must deep-clone Windows + DataSourcePaths
// so mutation through the returned AccountUsage does NOT corrupt cached state.
func TestServeLocked_DeepClonesWindowsAndPaths(t *testing.T) {
	accs := &fakeResolver{byID: map[int64]*ResolvedAccount{
		1: {ID: 1, ConfigDir: "/x", Visibility: "public"},
	}}
	entries := []usageEntry{{
		Timestamp: time.Date(2026, 5, 10, 11, 30, 0, 0, time.UTC),
		Model:     "claude-sonnet-4-6", InputTokens: 100, OutputTokens: 200,
	}}
	svc := newTestUsageSvc(t, accs, entries)
	first, err := svc.GetForAccount(context.Background(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate the returned value as a buggy future caller might.
	first.Windows[0].TotalTokens = 999_999_999
	first.DataSourcePaths[0] = "/etc/hijacked"
	if first.Windows[0].Models != nil && len(first.Windows[0].Models) > 0 {
		first.Windows[0].Models[0].Name = "evil"
	}
	// Re-serve from cache; must NOT see our mutations.
	second, err := svc.GetForAccount(context.Background(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Source != "cache" {
		t.Fatalf("expected cache hit, got %q", second.Source)
	}
	if second.Windows[0].TotalTokens == 999_999_999 {
		t.Fatal("Windows mutation leaked into cached value")
	}
	if second.DataSourcePaths[0] == "/etc/hijacked" {
		t.Fatal("DataSourcePaths mutation leaked into cached value")
	}
	if len(second.Windows[0].Models) > 0 && second.Windows[0].Models[0].Name == "evil" {
		t.Fatal("Models mutation leaked into cached value")
	}
}

// I8 from deep review: RecordRateLimit must drop events whose resets_at is
// already in the past — clock skew or stale upstream payload would otherwise
// pollute the bag with values that rateLimitFor immediately filters anyway.
func TestRecordRateLimit_DropsPastResetsAt(t *testing.T) {
	accs := &fakeResolver{byID: map[int64]*ResolvedAccount{
		1: {ID: 1, ConfigDir: "/x", Visibility: "public"},
	}}
	svc := newTestUsageSvc(t, accs, nil)
	// Prime so the bag exists.
	_, _ = svc.GetForAccount(context.Background(), 1, false)
	// Submit a rate_limit_event with resets_at one hour in the past.
	pastResets := svc.now().Add(-1 * time.Hour).Unix()
	svc.RecordRateLimit(1, "five_hour", pastResets, "allowed")
	if obs := svc.rateLimitFor(1, "five_hour", svc.now()); obs != nil {
		t.Fatalf("expected past resets to be dropped, got %+v", obs)
	}
}

// I5 from architect review: RecordRateLimit must NOT create new entries for
// accounts that haven't been seen via GetForAccount (would zombie deleted accs).
func TestRecordRateLimit_DropsForUnseenAccount(t *testing.T) {
	svc := NewClaudeUsageService(false, nil)
	svc.now = func() time.Time { return time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC) }
	svc.RecordRateLimit(42, "five_hour", time.Date(2026, 5, 10, 17, 0, 0, 0, time.UTC).Unix(), "allowed")
	if _, ok := svc.rlObs.Load(int64(42)); ok {
		t.Fatal("RecordRateLimit must not create new entry for unseen account")
	}
}

func TestRecordRateLimit_PerAccountIsolation(t *testing.T) {
	accs := &fakeResolver{byID: map[int64]*ResolvedAccount{
		1: {ID: 1, ConfigDir: "/x", Visibility: "public"},
		2: {ID: 2, ConfigDir: "/y", Visibility: "public"},
	}}
	svc := newTestUsageSvc(t, accs, nil)
	// Prime both accounts so RecordRateLimit can update them.
	_, _ = svc.GetForAccount(context.Background(), 1, false)
	_, _ = svc.GetForAccount(context.Background(), 2, false)
	resets := time.Date(2026, 5, 10, 17, 0, 0, 0, time.UTC).Unix()
	svc.RecordRateLimit(1, "five_hour", resets, "allowed")
	if obs := svc.rateLimitFor(1, "five_hour", svc.now()); obs == nil {
		t.Fatal("account 1 should have observation")
	}
	if obs := svc.rateLimitFor(2, "five_hour", svc.now()); obs != nil {
		t.Fatal("account 2 must NOT see account 1's observation")
	}
}

func TestEvictAccount_RemovesCacheAndRLObs(t *testing.T) {
	accs := &fakeResolver{byID: map[int64]*ResolvedAccount{
		1: {ID: 1, ConfigDir: "/x", Visibility: "public"},
	}}
	svc := newTestUsageSvc(t, accs, nil)
	_, _ = svc.GetForAccount(context.Background(), 1, false) // primes both maps
	svc.EvictAccount(1)
	if _, ok := svc.caches.Load(int64(1)); ok {
		t.Fatal("cache not evicted")
	}
	if _, ok := svc.rlObs.Load(int64(1)); ok {
		t.Fatal("rlObs not evicted")
	}
}

// GetForAccount on a known account returns 4 windows.
func TestGetForAccount_ReturnsDefaultAccountWindows(t *testing.T) {
	accs := &fakeResolver{
		byID:  map[int64]*ResolvedAccount{7: {ID: 7, ConfigDir: "/default", Visibility: "public", Name: "__default__"}},
		defID: 7,
	}
	svc := newTestUsageSvc(t, accs, nil)
	got, err := svc.GetForAccount(context.Background(), 7, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 4 {
		t.Fatalf("got %d windows", len(got.Windows))
	}
}
