// server/internal/service/claude_usage.go
//
// Local-JSONL aggregation backend for the "账户用量" settings panel.
// Replaces the old Anthropic /api/oauth/usage upstream call (which was
// blocked at Anthropic's WAF for non-CLI clients — see spec §10).
//
// Data source: ~/.claude/projects/**/*.jsonl (and XDG variant). Walks files,
// dedupes by (messageId, requestId), aggregates into 5h billing block + three
// 7-day windows. 0 network dependency. Methodology adapted from ryoppippi/ccusage.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/claudehome"
	"golang.org/x/sync/singleflight"
)

// AccountUsage is the response shape returned to API consumers.
type AccountUsage struct {
	Source           string        `json:"source"`             // "live" | "cache"
	FetchedAt        time.Time     `json:"fetched_at"`
	NextRefreshAfter time.Time     `json:"next_refresh_after"`
	Shared           bool          `json:"shared"`                       // true in team mode
	SubscriptionType string        `json:"subscription_type,omitempty"`  // optional, from credentials
	RateLimitTier    string        `json:"rate_limit_tier,omitempty"`    // optional, from credentials
	DataSourcePaths  []string      `json:"data_source_paths"`            // dirs we actually walked
	Windows          []UsageWindow `json:"windows"`
}

// UsageWindow is one aggregation bucket. Token counts are absolute (no quota %)
// because we don't have authoritative subscription tier limits.
type UsageWindow struct {
	Type                string       `json:"type"` // "five_hour_billing" | "seven_day_all" | "seven_day_sonnet" | "seven_day_opus"
	TotalTokens         int64        `json:"total_tokens"`
	InputTokens         int64        `json:"input_tokens"`
	OutputTokens        int64        `json:"output_tokens"`
	CacheCreationTokens int64        `json:"cache_creation_tokens"`
	CacheReadTokens     int64        `json:"cache_read_tokens"`
	CostUSD             float64      `json:"cost_usd"`
	Models              []ModelUsage `json:"models"`

	// Only populated for "five_hour_billing":
	BlockStart *time.Time `json:"block_start,omitempty"`
	BlockEnd   *time.Time `json:"block_end,omitempty"`
	IsActive   bool       `json:"is_active,omitempty"`

	// ResetsAt is the authoritative window-reset time from Anthropic, captured
	// passively from the agentproxy rate_limit_event stream. Only present
	// after at least one such event has been observed for this rate-limit type
	// (typically: the user's usage approaches a threshold). When nil, the
	// frontend simply doesn't display "Resets …".
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// ResetsAtCapturedAt records when the agentproxy observed this resets_at.
	// Stale-after-reset values are filtered out at Get() time.
	ResetsAtCapturedAt *time.Time `json:"resets_at_captured_at,omitempty"`
}

// ModelUsage is per-model breakdown within a single window.
type ModelUsage struct {
	Name        string  `json:"name"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

// jitteredTTL returns base ± uniform(-jitter, +jitter). Used by the cache to
// avoid synchronized refreshes across multiple panels.
func jitteredTTL(r *rand.Rand, base, jitter time.Duration) time.Duration {
	delta := time.Duration(r.Int63n(int64(2*jitter+1))) - jitter
	return base + delta
}

// ----- credentials (optional, for tier display) -----

type claudeCredentialsFile struct {
	ClaudeAiOauth claudeCredentials `json:"claudeAiOauth"`
}

type claudeCredentials struct {
	SubscriptionType string `json:"subscriptionType"`
	RateLimitTier    string `json:"rateLimitTier"`
}

// loadCredentialsForConfig assembles tier info for the given account by
// consulting two on-disk sources, in this order of preference:
//
//  1. .claude.json's oauthAccount block (profile-synced; tracks the live
//     organization tier and is filled in for every logged-in account).
//  2. .credentials.json's claudeAiOauth block (OAuth-time snapshot; may
//     carry null values for these fields depending on the OAuth response,
//     so we use it only when source 1 doesn't provide a value).
//
// For configDir == "" (default account), source 1 reads $HOME/.claude.json
// and source 2 reads $HOME/.claude/.credentials.json. Both sources return
// zero on any failure; an account with neither populated yields zero.
//
// Why two sources: historically the loader only read .credentials.json, and
// the UI hid the tier row when those fields were empty. Some accounts log in
// with an OAuth response that omits subscription info (it's left as null in
// .credentials.json), but the same info IS available in .claude.json. See
// account-usage-card.tsx's conditional render.
func loadCredentialsForConfig(configDir string) claudeCredentials {
	var sub, rate string
	if configDir == "" {
		sub, rate = claudehome.ReadTierFromHomeJSON()
	} else {
		sub, rate = claudehome.ReadTierFromConfigDir(configDir)
	}
	if sub != "" && rate != "" {
		return claudeCredentials{SubscriptionType: sub, RateLimitTier: rate}
	}

	dir := configDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return claudeCredentials{SubscriptionType: sub, RateLimitTier: rate}
		}
		dir = filepath.Join(home, ".claude")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return claudeCredentials{SubscriptionType: sub, RateLimitTier: rate}
	}
	var f claudeCredentialsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return claudeCredentials{SubscriptionType: sub, RateLimitTier: rate}
	}
	if sub == "" {
		sub = f.ClaudeAiOauth.SubscriptionType
	}
	if rate == "" {
		rate = f.ClaudeAiOauth.RateLimitTier
	}
	return claudeCredentials{SubscriptionType: sub, RateLimitTier: rate}
}

// ----- cache + rate limit -----

const (
	// cacheBaseTTL — how long a successful aggregation stays valid before we
	// re-walk the JSONL files. Walking is cheap (~50ms) but not free for
	// users with thousands of historical sessions, and the data only changes
	// when claude writes new lines to jsonl. 30s is generous.
	cacheBaseTTL = 30 * time.Second
	cacheJitter  = 5 * time.Second
	// upstreamMinInterval (L1) — minimum interval between actual aggregation
	// runs, even with force=1. Protects against runaway force-refresh from
	// frontend bugs.
	upstreamMinInterval = 5 * time.Second
)

var errCacheMiss = errors.New("cache miss")

type usageCache struct {
	mu           sync.Mutex
	rng          *rand.Rand
	value        *AccountUsage
	fetchedAt    time.Time
	ttl          time.Duration
	lastUpstream time.Time
}

func newUsageCache(r *rand.Rand) *usageCache {
	return &usageCache{rng: r}
}

func (c *usageCache) putWithJitter(value *AccountUsage, fetchedAt time.Time, base, jitter time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = value
	c.fetchedAt = fetchedAt
	c.ttl = jitteredTTL(c.rng, base, jitter)
	c.lastUpstream = fetchedAt
}

// tryServe returns:
//   - a usable cached AccountUsage (with Source set), OR
//   - errCacheMiss when caller should re-aggregate.
func (c *usageCache) tryServe(now time.Time, force bool) (*AccountUsage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	hasCache := c.value != nil
	cacheFresh := hasCache && now.Sub(c.fetchedAt) < c.ttl

	if force {
		// L1: even forced, don't re-walk more than once per upstreamMinInterval.
		if !c.lastUpstream.IsZero() && now.Sub(c.lastUpstream) < upstreamMinInterval {
			if hasCache {
				return c.serveLocked(now, "cache"), nil
			}
			return nil, errCacheMiss
		}
		return nil, errCacheMiss
	}

	if cacheFresh {
		return c.serveLocked(now, "cache"), nil
	}
	return nil, errCacheMiss
}

func (c *usageCache) serveLocked(now time.Time, source string) *AccountUsage {
	out := *c.value // top-level shallow copy
	// Deep-copy the slice headers and inner Models so callers can mutate the
	// returned AccountUsage without scribbling on cached state. Without this,
	// any caller writing through out.Windows[i] or out.Windows[i].Models would
	// corrupt every concurrent and future served snapshot.
	out.Windows = cloneWindows(c.value.Windows)
	out.DataSourcePaths = append([]string(nil), c.value.DataSourcePaths...)
	out.Source = source
	out.FetchedAt = c.fetchedAt
	out.NextRefreshAfter = c.lastUpstream.Add(upstreamMinInterval)
	if out.NextRefreshAfter.Before(now) {
		out.NextRefreshAfter = now
	}
	return &out
}

// cloneWindows returns a deep copy of a Windows slice, including the inner
// Models slice on each entry. Time pointers (ResetsAt, ResetsAtCapturedAt,
// BlockStart, BlockEnd) are intentionally NOT cloned: time.Time is immutable
// once constructed, so sharing the pointer is safe.
func cloneWindows(src []UsageWindow) []UsageWindow {
	if src == nil {
		return nil
	}
	out := make([]UsageWindow, len(src))
	for i, w := range src {
		out[i] = w
		// Use make+copy rather than `append(nil, w.Models...)` because the
		// latter returns nil when w.Models is empty-but-non-nil — and nil
		// would marshal to JSON null, crashing the frontend's
		// window.models.length read.
		if w.Models != nil {
			out[i].Models = make([]ModelUsage, len(w.Models))
			copy(out[i].Models, w.Models)
		}
	}
	return out
}

// markAttempt records the time of an upstream walker invocation, regardless
// of success/failure. Called on walker error so the L1 5s floor engages even
// when no fresh value was produced — without this, repeated force-refresh
// requests during a transient walker outage would hammer the walker
// once per request rather than once per 5s.
func (c *usageCache) markAttempt(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastUpstream = now
}

func (c *usageCache) nextRefreshAfter(now time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.lastUpstream.Add(upstreamMinInterval)
	if t.Before(now) {
		return now
	}
	return t
}

// rateLimitObservation is the latest rate_limit_event seen for a given
// rate-limit type (currently only "five_hour" is emitted by Claude CLI as of
// version 2.1.x). The Anthropic-authoritative ResetsAt timestamp is the
// single source of truth for "when does my 5h window reset" — there is no
// other way to obtain this without calling Anthropic's blocked /usage endpoint.
type rateLimitObservation struct {
	ResetsAt    time.Time
	CapturedAt  time.Time
	Status      string // "allowed" | "allowed_warning" | "rejected"
}

// ----- top-level Service -----

// ClaudeUsageService composes per-account JSONL aggregation + cache + tier
// credentials + agentproxy rate_limit_event capture.
type ClaudeUsageService struct {
	shared   bool
	now      func() time.Time

	caches sync.Map // accountID(int64) -> *usageCache
	rlObs  sync.Map // accountID(int64) -> *accountRateLimitObservations

	// sfGroup deduplicates concurrent walker invocations for the same account.
	// Without this, two browser tabs hitting force-refresh simultaneously each
	// re-walk thousands of jsonl files in parallel; with it, one walker call
	// services every concurrent waiter.
	sfGroup singleflight.Group

	dirsLister func(configDir string) ([]string, error)
	walker     func(ctx context.Context, dirs []string, cutoff time.Time) ([]usageEntry, error)
	credsLoad  func(configDir string) claudeCredentials
}

// accountRateLimitObservations: per-account map keyed by rateLimitType.
type accountRateLimitObservations struct {
	mu sync.RWMutex
	m  map[string]*rateLimitObservation
}

// NewClaudeUsageService constructs the service. Multi-account switching was
// removed; usage is aggregated from the host's global ~/.claude/ only.
func NewClaudeUsageService(authEnabled bool) *ClaudeUsageService {
	return &ClaudeUsageService{
		shared:     authEnabled,
		now:        time.Now,
		dirsLister: accountWalkDirs,
		walker:     walkAndParse,
		credsLoad:  loadCredentialsForConfig,
	}
}

func (s *ClaudeUsageService) cacheFor(accountID int64) *usageCache {
	if v, ok := s.caches.Load(accountID); ok {
		return v.(*usageCache)
	}
	// Each cache gets its own *rand.Rand. *rand.Rand is not safe for concurrent
	// use, and multiple accounts hitting putWithJitter() concurrently would race
	// on a shared service-level rng. The race detector catches this on Linux CI.
	//
	// Seed combines current nanos with a golden-ratio multiple of accountID so
	// near-simultaneous cold-start cache creation across accounts produces
	// well-separated seeds (XOR alone with small accountIDs and similar nanos
	// gave correlated first draws, defeating the de-sync intent of jittered TTL).
	r := rand.New(rand.NewSource(s.now().UnixNano() + int64(uint64(accountID)*0x9E3779B97F4A7C15)))
	c := newUsageCache(r)
	actual, _ := s.caches.LoadOrStore(accountID, c)
	return actual.(*usageCache)
}

// rlBagFor returns the observation bag for an account, creating it on first
// touch (called only from GetForAccount path — not from RecordRateLimit, to
// avoid zombie state for deleted accounts; see spec §"RecordRateLimit 与账号删除的竞态").
func (s *ClaudeUsageService) rlBagFor(accountID int64) *accountRateLimitObservations {
	if v, ok := s.rlObs.Load(accountID); ok {
		return v.(*accountRateLimitObservations)
	}
	bag := &accountRateLimitObservations{m: map[string]*rateLimitObservation{}}
	actual, _ := s.rlObs.LoadOrStore(accountID, bag)
	return actual.(*accountRateLimitObservations)
}

// EvictAccount drops all per-account state. Called by ClaudeAccountService.Delete.
func (s *ClaudeUsageService) EvictAccount(accountID int64) {
	s.caches.Delete(accountID)
	s.rlObs.Delete(accountID)
}

// RecordRateLimit forwards an Anthropic rate_limit_event observation. Drops
// the event if the account has no existing observation bag — that means
// either the account was never read via GetForAccount (no consumer) or it's
// been deleted (EvictAccount removed the bag). Either way: no zombie state.
// Also drops events whose resets_at is already in the past (clock skew /
// stale upstream payload) to keep the bag from accumulating dead entries.
func (s *ClaudeUsageService) RecordRateLimit(accountID int64, rateLimitType string, resetsAtUnix int64, status string) {
	if rateLimitType == "" || resetsAtUnix <= 0 {
		return
	}
	resetsAt := time.Unix(resetsAtUnix, 0)
	if resetsAt.Before(s.now()) {
		return
	}
	v, ok := s.rlObs.Load(accountID)
	if !ok {
		return
	}
	bag := v.(*accountRateLimitObservations)
	bag.mu.Lock()
	defer bag.mu.Unlock()
	bag.m[rateLimitType] = &rateLimitObservation{
		ResetsAt:   resetsAt,
		CapturedAt: s.now(),
		Status:     status,
	}
}

func (s *ClaudeUsageService) rateLimitFor(accountID int64, rateLimitType string, now time.Time) *rateLimitObservation {
	v, ok := s.rlObs.Load(accountID)
	if !ok {
		return nil
	}
	bag := v.(*accountRateLimitObservations)
	bag.mu.RLock()
	defer bag.mu.RUnlock()
	obs := bag.m[rateLimitType]
	if obs == nil || obs.ResetsAt.Before(now) {
		return nil
	}
	return obs
}

// GetForAccount returns cached or freshly aggregated usage for one account.
// `force=true` bypasses cache TTL but still respects the L1 5s upstream floor.
// Service-internal: NO authz check — caller must gate via CanAccessClaudeAccount.
//
// Concurrent calls for the same account are deduplicated via singleflight:
// only one walker invocation runs at a time per account, regardless of how
// many tabs / users hit force-refresh simultaneously.
func (s *ClaudeUsageService) GetForAccount(ctx context.Context, accountID int64, force bool) (*AccountUsage, error) {
	now := s.now()
	cache := s.cacheFor(accountID)

	if cached, err := cache.tryServe(now, force); err == nil {
		cached.Shared = s.shared
		return cached, nil
	} else if !errors.Is(err, errCacheMiss) {
		return nil, err
	}

	// Slow path: at most one walker call per accountID at any moment.
	// Followers wait for the leader's call to complete, then read from cache.
	_, sfErr, shared := s.sfGroup.Do(strconv.FormatInt(accountID, 10), func() (interface{}, error) {
		// Re-check cache after acquiring sf lock — leader might have populated
		// it between our cache.tryServe above and entering the sf'd function.
		// Use a fresh `now` for the freshness check (clock advanced while we waited).
		nowSf := s.now()
		if _, err := cache.tryServe(nowSf, force); err == nil {
			return nil, nil
		} else if !errors.Is(err, errCacheMiss) {
			return nil, err
		}
		return nil, s.refreshLocked(ctx, accountID, cache, nowSf)
	})
	if sfErr != nil {
		return nil, sfErr
	}

	source := "live"
	if shared {
		// Some other concurrent caller did the walk; we're serving the cache
		// they populated.
		source = "cache"
	}
	cache.mu.Lock()
	out := cache.serveLocked(s.now(), source)
	cache.mu.Unlock()
	out.Shared = s.shared
	return out, nil
}

// refreshLocked is the singleflight-protected slow path: resolve account,
// walk jsonl, aggregate windows, populate cache. Returns the walker error if
// any (callers in the singleflight group all see the same error).
func (s *ClaudeUsageService) refreshLocked(ctx context.Context, accountID int64, cache *usageCache, now time.Time) error {
	// Multi-account switching removed: always aggregate the host ~/.claude/.
	_ = accountID
	configDir := ""
	dirs, err := s.dirsLister(configDir)
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		// Default account: claudeDataDirs() returned empty. Named accounts
		// would have surfaced ErrClaudeProjectsNotFound from accountWalkDirs.
		return ErrClaudeDataDirNotFound
	}

	cutoff := now.Add(-7 * 24 * time.Hour)
	entries, err := s.walker(ctx, dirs, cutoff)
	if err != nil {
		// Bump lastUpstream so a follow-up force-refresh within 5s falls
		// back to the cached value (if any) instead of hitting the walker
		// again. Without this, transient walker outages cause retry storms.
		cache.markAttempt(now)
		slog.Warn("claude-usage walk failed", "err", err, "accountID", accountID)
		return err
	}

	// Prime the rate-limit bag so subsequent RecordRateLimit calls have
	// somewhere to write. This is the ONLY path that creates new bags.
	_ = s.rlBagFor(accountID)

	windows := aggregateWindows(entries, now)
	if obs := s.rateLimitFor(accountID, "five_hour", now); obs != nil {
		for i := range windows {
			if windows[i].Type == "five_hour_billing" {
				resets := obs.ResetsAt
				captured := obs.CapturedAt
				windows[i].ResetsAt = &resets
				windows[i].ResetsAtCapturedAt = &captured
				break
			}
		}
	}

	usage := &AccountUsage{
		DataSourcePaths: dirs,
		Windows:         windows,
	}
	creds := s.credsLoad(configDir)
	usage.SubscriptionType = creds.SubscriptionType
	usage.RateLimitTier = creds.RateLimitTier

	cache.putWithJitter(usage, now, cacheBaseTTL, cacheJitter)
	return nil
}

