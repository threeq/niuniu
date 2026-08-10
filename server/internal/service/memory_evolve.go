package service

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// memory_evolve.go implements automatic memory evolution: the mechanism that
// keeps a project's memory store in sync with the project as it changes, with no
// user intervention. Two deterministic, reversible passes run together:
//
//  1. Supersession (correction): when two alive "insight" memories share a
//     mem_type + title but carry different content, the older one has been
//     contradicted by newer work. The newest is kept; older ones are soft-deleted
//     and linked via superseded_by, recording WHAT replaced them.
//  2. Decay (staleness): a memory that has not been reaffirmed within IdleHorizon
//     and was never reinforced enough (reinforce_count < ProtectMinReinforce) is
//     archived (soft-deleted). Human-authored memories (source='manual') are
//     never auto-archived — only machine-captured ones decay.
//
// Both passes only soft-delete, so any evolution can be undone via Restore. The
// pass is invoked automatically at session start (see agentproxy), making it
// invisible to the user.

const (
	defaultIdleHorizon         = 30 * 24 * time.Hour // un-reinforced for >30d -> archivable
	defaultProtectMinReinforce = int64(2)            // reaffirmed >=2 times -> protected from decay
)

// supersedableTypes are the "insight" categories where a shared title genuinely
// denotes the same topic, so a newer entry corrects (supersedes) an older one.
// Free-form note/reference titles (e.g. "TODO") are intentionally excluded.
var supersedableTypes = map[string]bool{
	"pattern": true, "gotcha": true, "decision": true, "error_fix": true,
}

// EvolveOptions tunes an evolution pass. Zero values fall back to defaults, and
// Now is injectable so decay is deterministic in tests.
type EvolveOptions struct {
	Now                 time.Time     // reference "now"; zero -> time.Now()
	IdleHorizon         time.Duration // age past which an un-reinforced memory is archived; <=0 -> default
	ProtectMinReinforce int64         // reinforce_count at/above which a memory is never auto-archived; <=0 -> default
}

// EvolveResult summarizes one evolution pass.
type EvolveResult struct {
	Scanned          int      `json:"scanned"`
	Superseded       int      `json:"superseded"`
	Archived         int      `json:"archived"`
	SupersededTitles []string `json:"superseded_titles"`
	ArchivedTitles   []string `json:"archived_titles"`
}

// Reinforce records that a memory is still relevant: it bumps reinforce_count and
// stamps last_reinforced_at, protecting the memory from decay. Called whenever a
// later session reaffirms an existing memory (e.g. extraction re-surfaces it).
// A zero `at` defaults to time.Now().
func (s *MemoryService) Reinforce(ctx context.Context, id int64, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	return s.q.ReinforceMemory(ctx, store.ReinforceMemoryParams{
		LastReinforcedAt: sql.NullTime{Time: at, Valid: true},
		ID:               id,
	})
}

// EvolveProjectMemory runs an evolution pass with production defaults and returns
// the number of memories changed (superseded + archived). Convenience wrapper for
// the session-start auto-trigger so callers stay decoupled from EvolveOptions.
func (s *MemoryService) EvolveProjectMemory(ctx context.Context, projectID int64) (int, error) {
	res, err := s.EvolveForProject(ctx, projectID, EvolveOptions{})
	return res.Superseded + res.Archived, err
}

// EvolveForProject runs the supersession + decay passes over a project's alive
// memories. It is idempotent and reversible (soft-delete only).
func (s *MemoryService) EvolveForProject(ctx context.Context, projectID int64, opts EvolveOptions) (EvolveResult, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	horizon := opts.IdleHorizon
	if horizon <= 0 {
		horizon = defaultIdleHorizon
	}
	protect := opts.ProtectMinReinforce
	if protect <= 0 {
		protect = defaultProtectMinReinforce
	}

	rows, err := s.q.ListMemoriesForProject(ctx, sql.NullInt64{Int64: projectID, Valid: true})
	if err != nil {
		return EvolveResult{}, err
	}
	res := EvolveResult{Scanned: len(rows)}

	// Pass 1: supersession (correction). For each (mem_type, title) group of
	// insight memories, the newest entry wins; older contradicting ones are
	// retired. Keeper is chosen explicitly by (updated_at, id) rather than relying
	// on the query's ORDER BY, because CURRENT_TIMESTAMP is only second-granular,
	// so same-second writes tie and the row order is otherwise undefined.
	superseded := make(map[int64]bool)
	keep := make(map[string]store.Memory)
	for _, m := range rows {
		if !supersedableTypes[m.MemType] {
			continue
		}
		key := m.MemType + "|" + normKey(m.Title)
		kept, ok := keep[key]
		if !ok {
			keep[key] = m
			continue
		}
		// Only supersede when content actually differs — identical duplicates are
		// left to ConsolidateForOwner so the two passes stay independent.
		if normKey(m.Content) == normKey(kept.Content) {
			continue
		}
		newer, older := kept, m
		if isNewerMemory(m, kept) {
			newer, older = m, kept
		}
		if err := s.q.SupersedeMemory(ctx, store.SupersedeMemoryParams{
			SupersededBy: sql.NullInt64{Int64: newer.ID, Valid: true},
			ID:           older.ID,
		}); err != nil {
			return res, err
		}
		superseded[older.ID] = true
		delete(superseded, newer.ID) // newer is the survivor, even if a prior loop retired it
		keep[key] = newer
		res.Superseded++
		res.SupersededTitles = append(res.SupersededTitles, older.Title)
	}

	// Pass 2: decay (staleness archive).
	for _, m := range rows {
		if superseded[m.ID] {
			continue // already retired this pass
		}
		if m.Source == "manual" {
			continue // human-curated, never auto-archive
		}
		if m.ReinforceCount >= protect {
			continue // proven durably relevant
		}
		last := m.CreatedAt
		if m.LastReinforcedAt.Valid {
			last = m.LastReinforcedAt.Time
		}
		if now.Sub(last) <= horizon {
			continue // still fresh
		}
		if err := s.q.SoftDeleteMemory(ctx, m.ID); err != nil {
			return res, err
		}
		res.Archived++
		res.ArchivedTitles = append(res.ArchivedTitles, m.Title)
	}

	return res, nil
}

func normKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// isNewerMemory reports whether a is more current than b, by last-touched time
// and then by id (higher id == created later) to break same-second ties.
func isNewerMemory(a, b store.Memory) bool {
	if a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.ID > b.ID
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}
