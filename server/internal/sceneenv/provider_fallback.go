// Provider grouping + rate-limit fallback.
//
// A provider (env_providers row) can carry a group_name. Providers sharing a
// non-empty group_name are interchangeable fallbacks for each other (e.g. two
// 智谱 subscription keys under one "智谱" group). When the provider a workspace
// is bound to hits a 429 quota error, the parsed reset time is stored in its
// cooldown_until column; until that time passes, ActiveProvider resolves a
// healthy member of the same group instead, so the workspace keeps working
// with another provider instead of failing every turn.
//
// Both the PTY agent and the agentproxy chat path build their env through
// Resolve, so the fallback takes effect on every spawn for free. The
// rate-limit text parser lives here (not agentproxy) because the PTY service
// layer must also be able to reuse it without an import cycle.
package sceneenv

import (
	"context"
	"database/sql"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ownerScopeParams derives the account/provider owner-scope parameters for a
// workspace: org-owned workspaces see system defaults (OwnerID=0) plus that
// org; personal workspaces see system defaults plus the user. Shared by
// Resolve and ActiveProvider so both scope fallback candidates to the same
// owner set the workspace is allowed to see.
func ownerScopeParams(ctx context.Context, q Querier, wsID int64) (store.ListEnvAccountsForOwnersParams, store.ListEnvProvidersForOwnersParams) {
	var accountParams store.ListEnvAccountsForOwnersParams
	var providerParams store.ListEnvProvidersForOwnersParams
	ws, werr := q.GetWorkspace(ctx, wsID)
	if werr == nil && ws.OwnerType == "org" {
		accountParams.OwnerID = 0
		accountParams.OrgIds = []int64{ws.OwnerID}
		providerParams.OwnerID = 0
		providerParams.OrgIds = []int64{ws.OwnerID}
	} else if werr == nil {
		accountParams.OwnerID = ws.OwnerID
		providerParams.OwnerID = ws.OwnerID
	}
	return accountParams, providerParams
}

// ProviderInCooldown reports whether p is currently rate-limited: its
// cooldown_until is set and still in the future. A past or NULL value means
// healthy (cooldowns expire by comparison, no explicit clearing needed).
func ProviderInCooldown(p store.EnvProvider, now time.Time) bool {
	return p.CooldownUntil.Valid && p.CooldownUntil.Time.After(now)
}

// ActiveProvider returns the provider that should back this workspace for the
// agent's CLI: the directly-bound provider (workspaces.env_provider_id), or —
// when that provider is in a rate-limit cooldown and belongs to a group — the
// first healthy member of the same group that can serve the workspace's
// protocol. Returns ok=false when the workspace has no bound provider or it
// cannot be loaded.
//
// When the bound provider is in cooldown but has no group (or no healthy
// group member exists), the bound provider is returned anyway — there is
// nothing interchangeable to fall back to, so the workspace keeps using it and
// recovers as soon as the cooldown passes.
func ActiveProvider(ctx context.Context, q Querier, wsID int64) (store.EnvProvider, bool) {
	pid, err := q.GetWorkspaceEnvProviderID(ctx, wsID)
	if err != nil || pid <= 0 {
		return store.EnvProvider{}, false
	}
	bound, err := q.GetEnvProvider(ctx, pid)
	if err != nil {
		return store.EnvProvider{}, false
	}
	now := time.Now()
	if !ProviderInCooldown(bound, now) || bound.GroupName == "" {
		return bound, true
	}

	// Bound provider is rate-limited and grouped: look for a healthy
	// interchangeable member. Any error degrades to the bound provider.
	_, providerParams := ownerScopeParams(ctx, q, wsID)
	providers, err := q.ListEnvProvidersForOwners(ctx, providerParams)
	if err != nil {
		slog.Warn("sceneenv: list providers for fallback failed", "workspace_id", wsID, "error", err)
		return bound, true
	}
	cliType, _ := q.GetWorkspaceCliType(ctx, wsID)
	protocol := ProtocolForCLI(cliType)

	// Deterministic pick: lowest id among healthy same-group members that can
	// serve the agent's protocol (has a base_url for it).
	var fallback *store.EnvProvider
	for i := range providers {
		p := providers[i]
		if p.ID == bound.ID || p.GroupName != bound.GroupName {
			continue
		}
		if ProviderInCooldown(p, now) {
			continue
		}
		if decodeBaseURLs(p.BaseUrls)[protocol] == "" {
			continue // cannot serve this agent type — not interchangeable here
		}
		if fallback == nil || p.ID < fallback.ID {
			fp := p
			fallback = &fp
		}
	}
	if fallback == nil {
		return bound, true
	}
	return *fallback, true
}

// MarkProviderCooldown persists a provider's rate-limit reset time
// (cooldown_until), so ActiveProvider skips it and falls back to a healthy
// group member until the quota resets.
//
// The instant is normalized to UTC before storing: modernc.org/sqlite
// serializes a time.Time as its zone name + offset, and a parsed reset time
// carries a zone with an EMPTY name (e.g. time.FixedZone("", 8*3600) from a
// "2026-08-23 11:21:17 +0800 CST" message), which round-trips as
// "… +0800 +0800" — a string modernc cannot parse back into time.Time, so the
// next read fails the sqlc scan. UTC always round-trips.
func MarkProviderCooldown(ctx context.Context, q Querier, providerID int64, until time.Time) error {
	return q.SetProviderCooldown(ctx, store.SetProviderCooldownParams{
		ID:            providerID,
		CooldownUntil: sql.NullTime{Time: until.UTC(), Valid: true},
	})
}

// ClearProviderCooldown removes a provider's cooldown (user-initiated reset —
// e.g. the user re-keyed the account or the platform reset earlier than the
// parsed time).
func ClearProviderCooldown(ctx context.Context, q Querier, providerID int64) error {
	return q.ClearProviderCooldown(ctx, providerID)
}

// --- Rate-limit reset-time parsing ---
//
// Subscription platforms reject requests with a 429 that names the quota and
// the reset time in free text, e.g. (from real 智谱 / 阿里百炼 responses):
//
//	"API Error: Request rejected (429) · You have exceeded the 5-hour usage
//	 quota. It will reset at 2026-08-23 11:21:17 +0800 CST."
//	"API Error: Request rejected (429) · [1308][已达到 5 小时的使用上限。您的限额将
//	 在 2026-08-24 00:27:12 重置。]"
//	"API Error: Request rejected (429) · Your token-plan 1-week quota has been
//	 exhausted. The quota will reset at 08-29 03:09:00 UTC."
//
// ParseRateLimitReset recognizes these and returns the reset instant so the
// caller can mark the provider's cooldown. The Claude CLI surfaces this text
// either as a structured `system` error event or as raw stderr, so the scanner
// runs against the raw line.

var (
	// reRateLimitError gates whether a line looks like a quota/rate-limit
	// rejection at all. The 429 status code is the strongest signal; some
	// gateways phrase it differently, so a quota/limit phrase co-occurring
	// with a reset/exhausted phrase is also accepted.
	reRateLimitError = regexp.MustCompile(`429|((quota|限额|使用上限|rate[\s-]?limit|限流)\s*.{0,80}?(reset|重置|恢复|exhausted|用尽|已用完))`)

	// reFullDateTime matches "2026-08-23 11:21:17 +0800" or "2026-08-23 11:21:17".
	reFullDateTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?: [+-]\d{4})?`)

	// reMonthDayDateTime matches "08-29 03:09:00" with an optional trailing
	// timezone name ("UTC" in the 阿里百炼 sample).
	reMonthDayDateTime = regexp.MustCompile(`\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\s+(UTC|GMT|CST))?`)

	// reResetWord locates the reset keyword to anchor the datetime search.
	reResetWord = regexp.MustCompile(`reset|重置|恢复|将于|将在`)
)

const (
	resetSearchBack  = 48 // look this far before a reset keyword
	resetSearchAhead = 64 // and this far after it
)

// ParseRateLimitReset extracts the quota reset instant from a rate-limit error
// line. Returns ok=false when the line is not a rate-limit rejection or no
// reset time can be parsed.
func ParseRateLimitReset(line string) (time.Time, bool) {
	if !reRateLimitError.MatchString(line) {
		return time.Time{}, false
	}
	return extractResetTime(line)
}

// extractResetTime finds the reset datetime in line by anchoring on reset
// keywords ("reset" / "重置" / "恢复" / "将于" / "将在") and scanning a window
// around them for the first datetime token. The reset keyword may precede the
// token ("reset at 2026-08-23 11:21:17 +0800 CST") or follow it
// ("限额将在 2026-08-24 00:27:12 重置"), so both directions are searched.
func extractResetTime(line string) (time.Time, bool) {
	kw := reResetWord.FindAllStringIndex(line, -1)
	if len(kw) == 0 {
		return time.Time{}, false
	}
	for _, k := range kw {
		start := k[0] - resetSearchBack
		if start < 0 {
			start = 0
		}
		end := k[1] + resetSearchAhead
		if end > len(line) {
			end = len(line)
		}
		window := line[start:end]
		if m := reFullDateTime.FindString(window); m != "" {
			if t, ok := parseFullDateTime(m); ok {
				return t, true
			}
		}
		if m := reMonthDayDateTime.FindString(window); m != "" {
			if t, ok := parseMonthDayDateTime(m); ok {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

func parseFullDateTime(s string) (time.Time, bool) {
	// With explicit offset, e.g. "2026-08-23 11:21:17 +0800".
	if t, err := time.Parse("2006-01-02 15:04:05 -0700", s); err == nil {
		return t, true
	}
	// No offset: interpret in local time (the platform's clock is unknown;
	// local is the least-wrong default for a single-user personal edition).
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func parseMonthDayDateTime(s string) (time.Time, bool) {
	// Optional trailing tz name ("08-29 03:09:00 UTC"); year is absent, so use
	// the current year.
	loc := time.Local
	datePart := s
	if m := reTZName.FindString(s); m != "" {
		datePart = strings.TrimSpace(strings.TrimSuffix(s, m))
		if m == "UTC" || m == "GMT" {
			loc = time.UTC
		}
	}
	t, err := time.ParseInLocation("01-02 15:04:05", datePart, loc)
	if err != nil {
		return time.Time{}, false
	}
	now := time.Now()
	return time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc), true
}

// reTZName captures the trailing timezone token of a month-day datetime.
var reTZName = regexp.MustCompile(`(UTC|GMT|CST)\s*$`)
