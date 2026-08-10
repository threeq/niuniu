package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// MemoryService manages owner-scoped, versioned, traceable "white-box" memory.
// Every create/update writes a snapshot row to memory_versions so the current
// state can be rolled back to any prior version. Deletes are soft by default.
type MemoryService struct {
	q         *store.Queries
	db        *store.DB
	claudeCmd string
	extractMu sync.Map // workspace_id -> *sync.Mutex (serializes session extraction)

	// claudeAccount resolves a workspace's bound Claude account so session
	// extraction spawns the `claude` CLI with the same CLAUDE_CONFIG_DIR the
	// agent session uses. Optional (nil = no injection); wired via
	// SetClaudeAccountService.
	claudeAccount claudeConfigResolver

	// extractState tracks async session-extraction progress per workspace,
	// in memory, so the UI can render a spinner that survives page reloads.
	// Guarded by extractStateMu.
	extractState   map[int64]*ExtractStatus
	extractStateMu sync.Mutex
}

// claudeConfigResolver is the minimal slice of *ClaudeAccountService that
// extraction needs to authenticate the `claude` subprocess like the agent does.
type claudeConfigResolver interface {
	ResolveForWorkspace(ctx context.Context, workspaceID, userID int64) (*ResolvedAccount, error)
}

func NewMemoryService(q *store.Queries, db *sql.DB, claudeCmd string) *MemoryService {
	return &MemoryService{q: q, db: store.Wrap(db), claudeCmd: claudeCmd}
}

// SetClaudeAccountService wires the Claude account resolver used to inject
// CLAUDE_CONFIG_DIR into the session-extraction subprocess. Without it, a bare
// `claude -p` inherits only the server's raw env and fails with "exit status 1"
// on hosts where credentials live in a niuniu-managed per-account config dir
// rather than the default ~/.claude.
func (s *MemoryService) SetClaudeAccountService(r claudeConfigResolver) {
	s.claudeAccount = r
}

// ErrMemoryNotFound is returned when a memory id does not exist.
var ErrMemoryNotFound = errors.New("memory not found")

var validMemTypes = map[string]bool{
	"pattern": true, "gotcha": true, "decision": true,
	"error_fix": true, "note": true, "reference": true,
}

var validMemSources = map[string]bool{
	"manual": true, "mcp": true, "extract": true, "generate": true, "consolidate": true,
}

// CreateMemoryInput is the payload for Create.
type CreateMemoryInput struct {
	Owner       OwnerRef
	ProjectID   *int64
	WorkspaceID *int64
	MemType     string
	Title       string
	Content     string
	Source      string
	SourcePath  string
}

// UpdateMemoryInput is the payload for Update. Source defaults to "manual".
type UpdateMemoryInput struct {
	MemType    string
	Title      string
	Content    string
	Source     string
	SourcePath string
}

func nullInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func normalizeMemType(t string) string {
	t = strings.TrimSpace(t)
	if t == "" || !validMemTypes[t] {
		return "note"
	}
	return t
}

func normalizeMemSource(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !validMemSources[s] {
		return "manual"
	}
	return s
}

// Create inserts a memory at version 1 and its first version snapshot.
func (s *MemoryService) Create(ctx context.Context, in CreateMemoryInput) (store.Memory, error) {
	if strings.TrimSpace(in.Title) == "" {
		return store.Memory{}, errors.New("title is required")
	}
	memType := normalizeMemType(in.MemType)
	source := normalizeMemSource(in.Source)
	if in.Owner.Type == "" {
		in.Owner.Type = "user"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.Memory{}, err
	}
	defer tx.Rollback()
	qtx := tx.Queries()

	m, err := qtx.CreateMemory(ctx, store.CreateMemoryParams{
		OwnerType:   in.Owner.Type,
		OwnerID:     in.Owner.ID,
		ProjectID:   nullInt64(in.ProjectID),
		WorkspaceID: nullInt64(in.WorkspaceID),
		MemType:     memType,
		Title:       strings.TrimSpace(in.Title),
		Content:     in.Content,
		Source:      source,
		SourcePath:  in.SourcePath,
	})
	if err != nil {
		return store.Memory{}, err
	}
	if _, err := qtx.InsertMemoryVersion(ctx, store.InsertMemoryVersionParams{
		MemoryID:   m.ID,
		Version:    m.Version,
		MemType:    m.MemType,
		Title:      m.Title,
		Content:    m.Content,
		Source:     m.Source,
		SourcePath: m.SourcePath,
	}); err != nil {
		return store.Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.Memory{}, err
	}
	return m, nil
}

// Update bumps the version, persists the new content, and snapshots it.
func (s *MemoryService) Update(ctx context.Context, id int64, in UpdateMemoryInput) (store.Memory, error) {
	if strings.TrimSpace(in.Title) == "" {
		return store.Memory{}, errors.New("title is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.Memory{}, err
	}
	defer tx.Rollback()
	qtx := tx.Queries()

	cur, err := qtx.GetMemory(ctx, id)
	if err == sql.ErrNoRows {
		return store.Memory{}, ErrMemoryNotFound
	}
	if err != nil {
		return store.Memory{}, err
	}

	next := cur.Version + 1
	source := normalizeMemSource(in.Source)
	updated, err := qtx.UpdateMemoryContent(ctx, store.UpdateMemoryContentParams{
		ID:         id,
		MemType:    normalizeMemType(in.MemType),
		Title:      strings.TrimSpace(in.Title),
		Content:    in.Content,
		Source:     source,
		SourcePath: in.SourcePath,
		Version:    next,
	})
	if err != nil {
		return store.Memory{}, err
	}
	if _, err := qtx.InsertMemoryVersion(ctx, store.InsertMemoryVersionParams{
		MemoryID:   updated.ID,
		Version:    next,
		MemType:    updated.MemType,
		Title:      updated.Title,
		Content:    updated.Content,
		Source:     updated.Source,
		SourcePath: updated.SourcePath,
	}); err != nil {
		return store.Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.Memory{}, err
	}
	return updated, nil
}

// Rollback restores the content of targetVersion as a NEW forward version,
// preserving the full history (no destructive rewind).
func (s *MemoryService) Rollback(ctx context.Context, id, targetVersion int64) (store.Memory, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.Memory{}, err
	}
	defer tx.Rollback()
	qtx := tx.Queries()

	cur, err := qtx.GetMemory(ctx, id)
	if err == sql.ErrNoRows {
		return store.Memory{}, ErrMemoryNotFound
	}
	if err != nil {
		return store.Memory{}, err
	}
	// Rolling back to the current version is a no-op: skip writing a duplicate
	// snapshot and bumping the version for no content change.
	if targetVersion == cur.Version {
		return cur, nil
	}
	target, err := qtx.GetMemoryVersion(ctx, store.GetMemoryVersionParams{
		MemoryID: id,
		Version:  targetVersion,
	})
	if err == sql.ErrNoRows {
		return store.Memory{}, fmt.Errorf("version %d not found", targetVersion)
	}
	if err != nil {
		return store.Memory{}, err
	}

	next := cur.Version + 1
	updated, err := qtx.UpdateMemoryContent(ctx, store.UpdateMemoryContentParams{
		ID:         id,
		MemType:    target.MemType,
		Title:      target.Title,
		Content:    target.Content,
		Source:     target.Source,
		SourcePath: target.SourcePath,
		Version:    next,
	})
	if err != nil {
		return store.Memory{}, err
	}
	if _, err := qtx.InsertMemoryVersion(ctx, store.InsertMemoryVersionParams{
		MemoryID:   updated.ID,
		Version:    next,
		MemType:    updated.MemType,
		Title:      updated.Title,
		Content:    updated.Content,
		Source:     updated.Source,
		SourcePath: updated.SourcePath,
	}); err != nil {
		return store.Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.Memory{}, err
	}
	return updated, nil
}

func (s *MemoryService) Get(ctx context.Context, id int64) (store.Memory, error) {
	m, err := s.q.GetMemory(ctx, id)
	if err == sql.ErrNoRows {
		return store.Memory{}, ErrMemoryNotFound
	}
	return m, err
}

// ListForOwner returns alive memories for an owner, optionally filtered by type.
func (s *MemoryService) ListForOwner(ctx context.Context, owner OwnerRef, memType string) ([]store.Memory, error) {
	rows, err := s.q.ListMemoriesForOwner(ctx, store.ListMemoriesForOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if err != nil {
		return nil, err
	}
	return filterByType(rows, memType), nil
}

func (s *MemoryService) ListForProject(ctx context.Context, projectID int64, memType string) ([]store.Memory, error) {
	rows, err := s.q.ListMemoriesForProject(ctx, sql.NullInt64{Int64: projectID, Valid: true})
	if err != nil {
		return nil, err
	}
	return filterByType(rows, memType), nil
}

func (s *MemoryService) ListDeleted(ctx context.Context, owner OwnerRef) ([]store.Memory, error) {
	return s.q.ListDeletedMemoriesForOwner(ctx, store.ListDeletedMemoriesForOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
}

// Search returns alive memories whose title or content matches q (case-insensitive).
// Search returns alive memories whose title or content contains q as a literal,
// case-insensitive substring. Matching is done in Go (single source of truth):
// SQL LIKE would treat user-typed %/_ as wildcards and behaves differently
// across SQLite/Postgres, and sqlc's parser rejects an ESCAPE clause. Owner
// memory sets are small (a curated knowledge store, not high-volume rows), so
// listing + filtering in Go is correct and cheap; revisit with FTS if that
// assumption ever changes.
func (s *MemoryService) Search(ctx context.Context, owner OwnerRef, q string) ([]store.Memory, error) {
	rows, err := s.q.ListMemoriesForOwner(ctx, store.ListMemoriesForOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(q))
	if needle == "" {
		return rows, nil
	}
	out := make([]store.Memory, 0, len(rows))
	for _, m := range rows {
		if strings.Contains(strings.ToLower(m.Title), needle) || strings.Contains(strings.ToLower(m.Content), needle) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *MemoryService) ListVersions(ctx context.Context, id int64) ([]store.MemoryVersion, error) {
	return s.q.ListMemoryVersions(ctx, id)
}

func (s *MemoryService) SoftDelete(ctx context.Context, id int64) error {
	return s.q.SoftDeleteMemory(ctx, id)
}

func (s *MemoryService) Restore(ctx context.Context, id int64) error {
	return s.q.RestoreMemory(ctx, id)
}

func (s *MemoryService) HardDelete(ctx context.Context, id int64) error {
	return s.q.HardDeleteMemory(ctx, id)
}

func (s *MemoryService) Count(ctx context.Context, owner OwnerRef) (int64, error) {
	return s.q.CountMemoriesForOwner(ctx, store.CountMemoriesForOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
}

// ConsolidateResult summarizes a consolidation pass.
type ConsolidateResult struct {
	Scanned int      `json:"scanned"`
	Deleted int      `json:"deleted"`
	Kept    int      `json:"kept"`
	Merged  []string `json:"merged"` // titles that were de-duplicated
}

// ConsolidateForOwner performs an idle "dream"-style tidy: it groups alive
// memories by (mem_type, normalized title, normalized content) and, within each
// EXACT-duplicate group, keeps the most recently updated entry while
// soft-deleting the rest. Keying on content as well as title means two distinct
// notes that merely share a title (e.g. "TODO") are NOT collapsed — only true
// duplicates are removed. Reversible (soft-delete), so a pass can be undone.
func (s *MemoryService) ConsolidateForOwner(ctx context.Context, owner OwnerRef) (ConsolidateResult, error) {
	rows, err := s.q.ListMemoriesForOwner(ctx, store.ListMemoriesForOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if err != nil {
		return ConsolidateResult{}, err
	}
	seen := make(map[string]store.Memory, len(rows))
	var toDelete []int64
	var merged []string
	for _, m := range rows {
		key := strings.ToLower(strings.TrimSpace(m.MemType)) + "|" +
			strings.ToLower(strings.TrimSpace(m.Title)) + "|" +
			strings.TrimSpace(m.Content)
		prev, ok := seen[key]
		if !ok {
			seen[key] = m
			continue
		}
		// Duplicate: keep the more recently updated, soft-delete the older.
		if m.UpdatedAt.After(prev.UpdatedAt) {
			seen[key] = m
			toDelete = append(toDelete, prev.ID)
		} else {
			toDelete = append(toDelete, m.ID)
		}
		merged = append(merged, m.Title)
	}
	for _, id := range toDelete {
		if err := s.q.SoftDeleteMemory(ctx, id); err != nil {
			return ConsolidateResult{}, err
		}
	}
	return ConsolidateResult{
		Scanned: len(rows),
		Deleted: len(toDelete),
		Kept:    len(seen),
		Merged:  merged,
	}, nil
}

func filterByType(rows []store.Memory, memType string) []store.Memory {
	memType = strings.TrimSpace(memType)
	if memType == "" {
		return rows
	}
	out := make([]store.Memory, 0, len(rows))
	for _, m := range rows {
		if m.MemType == memType {
			out = append(out, m)
		}
	}
	return out
}
