package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrClaudeDataDirNotFound is returned when no candidate Claude data
// directory contains a `projects/` subdirectory.
var ErrClaudeDataDirNotFound = errors.New("claude data directory not found")

// ErrClaudeProjectsNotFound is returned when the resolved account directory
// exists but has no `projects/` subdirectory — typically "DB row exists but
// claude CLI hasn't been logged into yet".
var ErrClaudeProjectsNotFound = errors.New("claude projects directory not found for account")

// claudeDataDirs returns existing data directories that contain a `projects/`
// subdir. Mirrors ccusage's discovery logic:
//   - $CLAUDE_CONFIG_DIR (comma-separated multi-path support)
//   - $HOME/.config/claude (XDG default)
//   - $HOME/.claude (legacy default)
//
// Returns empty slice if none exist (caller maps to ErrClaudeDataDirNotFound).
func claudeDataDirs() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(dir string) {
		clean := filepath.Clean(dir)
		if _, dup := seen[clean]; dup {
			return
		}
		projects := filepath.Join(clean, "projects")
		if st, err := os.Stat(projects); err == nil && st.IsDir() {
			seen[clean] = struct{}{}
			out = append(out, clean)
		}
	}
	if env := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); env != "" {
		for _, p := range strings.Split(env, ",") {
			if p = strings.TrimSpace(p); p != "" {
				add(p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".config", "claude"))
		add(filepath.Join(home, ".claude"))
	}
	return out
}

// ErrClaudeConfigDirInvalid is returned when an account's config_dir looks
// like a path-traversal attempt or is not absolute. Defense-in-depth against
// a tampered DB row leaking arbitrary filesystem paths into responses.
var ErrClaudeConfigDirInvalid = errors.New("claude account config_dir invalid (relative or contains '..')")

// accountWalkDirs returns directories to walk for one specific Claude account.
// configDir == "" → __default__ account, defers to claudeDataDirs() (which
// honors $CLAUDE_CONFIG_DIR, ~/.config/claude, ~/.claude).
//
// For named accounts:
//   - returns ErrClaudeConfigDirInvalid for non-absolute or `..`-traversing paths
//   - returns ErrClaudeProjectsNotFound when <configDir>/projects/ does not exist (ENOENT)
//   - returns the underlying os.Stat error for other failures (EACCES, ELOOP, …)
//   - returns ([configDir], nil) on success
//
// Returning a typed error for ENOENT (instead of nil) lets the handler
// distinguish "account never logged in" (friendly empty state) from
// "permission denied / I/O error" (jsonl_walk_failed branch).
func accountWalkDirs(configDir string) ([]string, error) {
	if configDir == "" {
		return claudeDataDirs(), nil
	}
	clean := filepath.Clean(configDir)
	if !filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+"..") {
		return nil, ErrClaudeConfigDirInvalid
	}
	st, err := os.Stat(filepath.Join(clean, "projects"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrClaudeProjectsNotFound
		}
		return nil, fmt.Errorf("stat %s/projects: %w", clean, err)
	}
	if !st.IsDir() {
		return nil, ErrClaudeProjectsNotFound
	}
	return []string{clean}, nil
}

// rawJSONLEntry is the weak schema we pull out of each line. Most fields are
// optional so a minor schema drift in CLI doesn't break parsing.
type rawJSONLEntry struct {
	Type              string   `json:"type"`
	Timestamp         string   `json:"timestamp"`
	SessionID         string   `json:"sessionId"`
	RequestID         string   `json:"requestId"`
	CostUSD           *float64 `json:"costUSD"`
	IsAPIErrorMessage bool     `json:"isApiErrorMessage"`
	Message           *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// usageEntry is the parsed, normalized form of a JSONL row that is relevant
// for windowing.
type usageEntry struct {
	Timestamp           time.Time
	Model               string
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	CostUSDExplicit     *float64 // when CLI pre-calculated it; nil otherwise
}

func (e *usageEntry) totalTokens() int64 {
	return e.InputTokens + e.OutputTokens + e.CacheCreationTokens + e.CacheReadTokens
}

func (e *usageEntry) costUSD() float64 {
	if e.CostUSDExplicit != nil {
		return *e.CostUSDExplicit
	}
	return computeCostUSD(e.Model, e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheCreationTokens)
}

// dedupKey returns the (messageId, requestId) hash. Empty string means "don't
// dedup this row" (always keep). Mirrors ccusage's createUniqueHash.
func (e rawJSONLEntry) dedupKey() string {
	if e.Message == nil || e.Message.ID == "" || e.RequestID == "" {
		return ""
	}
	return e.Message.ID + ":" + e.RequestID
}

// scanJSONL streams a single .jsonl file, applying dedup against the shared
// `seen` map. Returns parsed entries in file order. Errors on individual lines
// (malformed JSON, bad timestamp, non-utf8) are silently skipped per ccusage's
// behavior — the file may have transient corruption from CLI crashes.
func scanJSONL(path string, seen map[string]struct{}) ([]usageEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Some jsonl rows are large (full content blocks). Bump buffer to 1 MB.
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	var out []usageEntry
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw rawJSONLEntry
		if err := json.Unmarshal(line, &raw); err != nil {
			continue // skip malformed line
		}
		// Only assistant messages carry usage data we care about.
		if raw.Type != "assistant" {
			continue
		}
		if raw.IsAPIErrorMessage {
			continue
		}
		if raw.Message == nil || raw.Message.Usage == nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, raw.Timestamp)
		if err != nil {
			continue
		}
		key := raw.dedupKey()
		if key != "" {
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, usageEntry{
			Timestamp:           ts,
			Model:               raw.Message.Model,
			InputTokens:         raw.Message.Usage.InputTokens,
			OutputTokens:        raw.Message.Usage.OutputTokens,
			CacheCreationTokens: raw.Message.Usage.CacheCreationInputTokens,
			CacheReadTokens:     raw.Message.Usage.CacheReadInputTokens,
			CostUSDExplicit:     raw.CostUSD,
		})
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}

// walkAndParse iterates every *.jsonl under `dirs/projects/**`, dedupes across
// the whole walk, and returns all entries sorted by timestamp ascending.
// `cutoff` is the earliest timestamp to keep (entries older are dropped) — used
// to prune walking effort to the relevant time window.
func walkAndParse(ctx context.Context, dirs []string, cutoff time.Time) ([]usageEntry, error) {
	seen := map[string]struct{}{}
	var all []usageEntry

	for _, dataDir := range dirs {
		projects := filepath.Join(dataDir, "projects")
		err := filepath.WalkDir(projects, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				// Ignore individual file errors (permission, transient).
				return nil //nolint:nilerr
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl") {
				return nil
			}
			// Optimization: skip files whose mtime is older than cutoff - 1 day
			// (we keep 1-day grace for files that contain entries straddling
			// the cutoff). For very-old archived sessions this saves a lot of IO.
			if !cutoff.IsZero() {
				if info, err := d.Info(); err == nil {
					if info.ModTime().Before(cutoff.Add(-24 * time.Hour)) {
						return nil
					}
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, _ := scanJSONL(path, seen)
			for _, e := range entries {
				if !cutoff.IsZero() && e.Timestamp.Before(cutoff) {
					continue
				}
				all = append(all, e)
			}
			return nil
		})
		if err != nil && !errors.Is(err, ctx.Err()) {
			return all, fmt.Errorf("walk %s: %w", projects, err)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})
	return all, nil
}

// ----- 5-hour billing block algorithm (ported from ccusage _session-blocks.ts) -----

// fiveHourBillingBlock computes Anthropic's 5h billing block from sorted entries.
// Returns the block containing `now` (or the latest block if `now` falls in a gap).
// `now` must be >= the latest entry timestamp for IsActive to be meaningful.
func fiveHourBillingBlock(sorted []usageEntry, now time.Time) (block, bool) {
	if len(sorted) == 0 {
		return block{}, false
	}
	const blockDur = 5 * time.Hour

	currentStart := floorToUTCHour(sorted[0].Timestamp)
	currentEntries := []usageEntry{sorted[0]}
	closeBlock := func(start time.Time, ents []usageEntry) block {
		end := start.Add(blockDur)
		actualEnd := ents[len(ents)-1].Timestamp
		isActive := now.Sub(actualEnd) < blockDur && now.Before(end)
		return block{
			Start:         start,
			End:           end,
			ActualEnd:     actualEnd,
			IsActive:      isActive,
			Entries:       ents,
		}
	}
	var blocks []block
	for i := 1; i < len(sorted); i++ {
		e := sorted[i]
		sinceStart := e.Timestamp.Sub(currentStart)
		sinceLast := e.Timestamp.Sub(currentEntries[len(currentEntries)-1].Timestamp)
		if sinceStart > blockDur || sinceLast > blockDur {
			blocks = append(blocks, closeBlock(currentStart, currentEntries))
			currentStart = floorToUTCHour(e.Timestamp)
			currentEntries = []usageEntry{e}
		} else {
			currentEntries = append(currentEntries, e)
		}
	}
	blocks = append(blocks, closeBlock(currentStart, currentEntries))

	// Latest block is the candidate for "current".
	last := blocks[len(blocks)-1]
	return last, true
}

func floorToUTCHour(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
}

type block struct {
	Start     time.Time
	End       time.Time
	ActualEnd time.Time
	IsActive  bool
	Entries   []usageEntry
}

// ----- Window aggregation -----

// aggregateWindows produces the four windows from sorted entries.
// `now` is injectable for testability.
func aggregateWindows(sorted []usageEntry, now time.Time) []UsageWindow {
	out := []UsageWindow{}

	// 5h billing block window.
	if blk, ok := fiveHourBillingBlock(sorted, now); ok {
		w := buildWindow("five_hour_billing", blk.Entries)
		s, e := blk.Start, blk.End
		w.BlockStart = &s
		w.BlockEnd = &e
		w.IsActive = blk.IsActive
		out = append(out, w)
	} else {
		// Empty window placeholder so frontend doesn't crash on missing slot.
		// Models must be a non-nil slice — Go marshals nil slices to JSON null,
		// and the frontend reads .length on it (account-usage-card.tsx).
		out = append(out, UsageWindow{Type: "five_hour_billing", Models: []ModelUsage{}})
	}

	// 7d windows.
	cutoff := now.Add(-7 * 24 * time.Hour)
	var allInWeek []usageEntry
	for _, e := range sorted {
		if !e.Timestamp.Before(cutoff) {
			allInWeek = append(allInWeek, e)
		}
	}
	out = append(out, buildWindow("seven_day_all", allInWeek))
	out = append(out, buildWindow("seven_day_sonnet", filterByModelSubstring(allInWeek, "sonnet")))
	out = append(out, buildWindow("seven_day_opus", filterByModelSubstring(allInWeek, "opus")))

	return out
}

func filterByModelSubstring(entries []usageEntry, substr string) []usageEntry {
	if substr == "" {
		return entries
	}
	low := strings.ToLower(substr)
	var out []usageEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Model), low) {
			out = append(out, e)
		}
	}
	return out
}

func buildWindow(typ string, entries []usageEntry) UsageWindow {
	w := UsageWindow{Type: typ, Models: []ModelUsage{}}
	if len(entries) == 0 {
		return w
	}
	perModel := map[string]*ModelUsage{}
	for _, e := range entries {
		w.InputTokens += e.InputTokens
		w.OutputTokens += e.OutputTokens
		w.CacheCreationTokens += e.CacheCreationTokens
		w.CacheReadTokens += e.CacheReadTokens
		w.CostUSD += e.costUSD()

		mu, ok := perModel[e.Model]
		if !ok {
			mu = &ModelUsage{Name: e.Model}
			perModel[e.Model] = mu
		}
		mu.TotalTokens += e.totalTokens()
		mu.CostUSD += e.costUSD()
	}
	w.TotalTokens = w.InputTokens + w.OutputTokens + w.CacheCreationTokens + w.CacheReadTokens
	// Sort models by total tokens desc for stable display.
	for _, mu := range perModel {
		w.Models = append(w.Models, *mu)
	}
	sort.Slice(w.Models, func(i, j int) bool {
		return w.Models[i].TotalTokens > w.Models[j].TotalTokens
	})
	return w
}
