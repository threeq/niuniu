package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/niuniu-dev/niuniu/internal/docextract"
	"github.com/niuniu-dev/niuniu/internal/kbindex"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// KBService is the first-class knowledge-base backend: it owns KB metadata in
// the main database (knowledge_bases / kb_documents / kb_bindings) and drives
// the pluggable-source ingest + full-text index. The index itself lives behind
// kbindex.Manager (a per-owner SQLite sidecar, or Postgres tsvector/pg_trgm),
// completely separate from the main store's write path and from memories.
type KBService struct {
	q       *store.Queries
	dataDir string
	idx     *kbindex.Manager
}

// NewKBService constructs a KBService. KB metadata access goes through the sqlc
// Queries (q, already bound to the active driver); the full-text index is owned
// by idx (per-owner SQLite sidecar or shared Postgres index).
func NewKBService(q *store.Queries, dataDir string, idx *kbindex.Manager) *KBService {
	return &KBService{q: q, dataDir: dataDir, idx: idx}
}

// CreateKBParams describes a knowledge base to create.
type CreateKBParams struct {
	Name         string
	Description  string
	SourceKind   string // local | url | repo (default local)
	SourceAddr   string
	SourceConfig string // JSON; defaults to "{}"
}

// IngestOptions tunes an ingest run.
type IngestOptions struct {
	MaxChunkChars int  // 0 -> kbindex.DefaultChunkChars
	Force         bool // re-index even if the content hash is unchanged
}

// IngestResult summarizes an ingest run.
type IngestResult struct {
	FilesScanned   int      `json:"files_scanned"`
	FilesIngested  int      `json:"files_ingested"`
	FilesUnchanged int      `json:"files_unchanged"`
	ChunksWritten  int      `json:"chunks_written"`
	Errors         []string `json:"errors,omitempty"`
}

func validKBSourceKind(k string) bool {
	switch k {
	case KBSourceLocal, KBSourceURL, KBSourceRepo, KBSourceMcp:
		return true
	}
	return false
}

// CreateKB creates a knowledge base for owner. Name must be unique per owner.
func (s *KBService) CreateKB(ctx context.Context, owner OwnerRef, p CreateKBParams) (store.KnowledgeBase, error) {
	if err := owner.Validate(); err != nil {
		return store.KnowledgeBase{}, err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return store.KnowledgeBase{}, fmt.Errorf("kb name is required")
	}
	kind := p.SourceKind
	if kind == "" {
		kind = KBSourceLocal
	}
	if !validKBSourceKind(kind) {
		return store.KnowledgeBase{}, fmt.Errorf("invalid source kind %q", kind)
	}
	cfg := strings.TrimSpace(p.SourceConfig)
	if cfg == "" {
		cfg = "{}"
	}
	return s.q.CreateKnowledgeBase(ctx, store.CreateKnowledgeBaseParams{
		OwnerType:    owner.Type,
		OwnerID:      owner.ID,
		Name:         name,
		Description:  p.Description,
		SourceKind:   kind,
		SourceAddr:   p.SourceAddr,
		SourceConfig: cfg,
	})
}

// GetKB loads a KB and verifies it belongs to owner (tenant isolation).
func (s *KBService) GetKB(ctx context.Context, owner OwnerRef, kbID int64) (store.KnowledgeBase, error) {
	kb, err := s.q.GetKnowledgeBase(ctx, kbID)
	if err != nil {
		return store.KnowledgeBase{}, err
	}
	if kb.OwnerType != owner.Type || kb.OwnerID != owner.ID {
		return store.KnowledgeBase{}, fmt.Errorf("knowledge base not found")
	}
	return kb, nil
}

// ListKBs returns all knowledge bases owned by owner.
func (s *KBService) ListKBs(ctx context.Context, owner OwnerRef) ([]store.KnowledgeBase, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	return s.q.ListKnowledgeBasesForOwner(ctx, store.ListKnowledgeBasesForOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
}

// DeleteKB removes a KB, its documents (FK cascade) and its index entries.
func (s *KBService) DeleteKB(ctx context.Context, owner OwnerRef, kbID int64) error {
	kb, err := s.GetKB(ctx, owner, kbID)
	if err != nil {
		return err
	}
	// Clear the index first; on failure the KB row stays and a retry/rebuild can
	// recover (index is reconstructable, so no cross-store transaction needed).
	idx, err := s.ownerIndex(owner)
	if err == nil {
		if derr := idx.DeleteKB(ctx, kb.ID); derr != nil {
			// The index is reconstructable so we still delete the KB row, but log:
			// on Postgres the chunks live in the shared kb_chunks table, so a
			// silently-failed clear leaves orphaned rows that nothing will reclaim.
			slog.Warn("kb: clearing index on delete failed; chunks may be orphaned",
				"kb_id", kb.ID, "err", derr)
		}
	}
	return s.q.DeleteKnowledgeBase(ctx, kb.ID)
}

// Search runs a keyword full-text search within one KB and returns ranked hits
// (pointer back to rel_path + byte offset, plus a snippet).
func (s *KBService) Search(ctx context.Context, owner OwnerRef, kbID int64, query string, limit int) ([]kbindex.SearchHit, error) {
	kb, err := s.GetKB(ctx, owner, kbID)
	if err != nil {
		return nil, err
	}
	idx, err := s.ownerIndex(owner)
	if err != nil {
		return nil, err
	}
	return idx.Search(ctx, kb.ID, query, limit)
}

// KBMaxDocumentBytes caps how much of one document the browse endpoint returns.
// A corpus routinely holds multi-MB files; the reader only needs a readable head,
// and an uncapped read would pin that much memory per request.
const KBMaxDocumentBytes = 512 << 10

// ReadDocumentText returns the text of one ingested document, capped at
// maxBytes. It is the read side of the browse UI (preview a document, open a
// search hit in context); the on-disk corpus stays the source of truth, so
// nothing here reads the index sidecar.
//
// Security: this is the only path that turns a DB-recorded document back into a
// filesystem read, so it re-derives the KB's source root and refuses anything
// outside it. That matters because rel_path/uri are data — a stale row (corpus
// moved), or one written before a source change, must not become an arbitrary
// file read. relPath is preferred and joined onto the root; the stored uri is
// only a fallback for a single-file `local` source, whose root is the parent
// directory.
//
// PDF/Office documents hold no readable bytes, so they are run through the same
// extractor the ingest path uses — what you browse is what was indexed.
//
// Returns (text, truncated, extracted, error). truncated is true when the text
// was longer than maxBytes and stops at a rune boundary before the cap;
// extracted is true when the text was recovered from a binary document.
func (s *KBService) ReadDocumentText(ctx context.Context, owner OwnerRef, kbID int64, relPath, uri string, maxBytes int) (string, bool, bool, error) {
	if maxBytes <= 0 {
		maxBytes = KBMaxDocumentBytes
	}
	// Re-resolve the root from the KB row rather than trusting uri. GetKB also
	// re-verifies ownership, so this stays safe even if a caller skipped that.
	kb, err := s.GetKB(ctx, owner, kbID)
	if err != nil {
		return "", false, false, err
	}
	root, err := s.resolveSourceRoot(owner, kb)
	if err != nil {
		return "", false, false, err
	}

	abs := filepath.Join(root, filepath.FromSlash(relPath))
	if !pathWithin(abs, root) {
		// relPath contained traversal (".."). Never fall back to uri here: a
		// hostile rel_path must fail closed.
		return "", false, false, fmt.Errorf("document path escapes the knowledge base root")
	}
	if _, statErr := os.Stat(abs); statErr != nil {
		// Single-file `local` source: resolveSourceRoot returns the containing
		// directory, so rel_path may not join cleanly. Accept the recorded uri
		// only when it still lives under the same root.
		if uri != "" && pathWithin(uri, root) {
			abs = uri
		} else {
			return "", false, false, fmt.Errorf("document is no longer readable: %w", statErr)
		}
	}

	// Binary document: extract rather than read verbatim.
	if docextract.IsSupported(abs) {
		text, xerr := docextract.PlainText(abs)
		if xerr != nil {
			return "", false, false, xerr
		}
		truncated := len(text) > maxBytes
		if truncated {
			text = trimToValidUTF8(text[:maxBytes])
		}
		return text, truncated, true, nil
	}

	f, err := os.Open(abs)
	if err != nil {
		return "", false, false, fmt.Errorf("document is no longer readable: %w", err)
	}
	defer f.Close()
	// Read one byte past the cap purely to detect truncation.
	buf, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return "", false, false, err
	}
	truncated := len(buf) > maxBytes
	if truncated {
		return trimToValidUTF8(string(buf[:maxBytes])), true, false, nil
	}
	return string(buf), false, false, nil
}

// ListVisibleKBs returns the knowledge bases visible to a workspace: owned by
// owner AND bound (kb_bindings) to projectID. Unbound KBs are intentionally
// invisible; a nil projectID (no-repo / project-less workspace) sees none.
func (s *KBService) ListVisibleKBs(ctx context.Context, owner OwnerRef, projectID *int64) ([]store.KnowledgeBase, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	if projectID == nil || *projectID <= 0 {
		return nil, nil
	}
	return s.q.ListKnowledgeBasesForProject(ctx, store.ListKnowledgeBasesForProjectParams{
		TargetID:  *projectID,
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
}

// KBDatasetDir is a bound knowledge base's read-only content directory exposed
// to the workspace agent for direct Read/Grep/Glob (KB base4, the "C" ability,
// complementing A=kb_search). Root is an absolute on-disk directory the agent
// is granted read access to; it must never be written (callers enforce
// read-only at the agent boundary).
type KBDatasetDir struct {
	KBID        int64
	Name        string
	Description string
	Root        string
}

// ResolveDatasetDirs returns the read-only dataset directories of the KBs
// visible to a workspace (owned by owner AND bound to projectID). It reuses the
// same visibility gate as kb_search, so unbound KBs and other owners' KBs are
// never exposed. Only KBs whose source root currently resolves on disk are
// returned: a url-kind KB not yet downloaded, or a local KB whose directory is
// missing, is skipped rather than failing the whole resolution (best-effort, so
// a single broken KB never blocks an agent spawn). A nil/zero projectID
// (project-less workspace) yields nothing.
func (s *KBService) ResolveDatasetDirs(ctx context.Context, owner OwnerRef, projectID int64) ([]KBDatasetDir, error) {
	if projectID <= 0 {
		return nil, nil
	}
	kbs, err := s.ListVisibleKBs(ctx, owner, &projectID)
	if err != nil {
		return nil, err
	}
	var out []KBDatasetDir
	for _, kb := range kbs {
		root, err := s.resolveSourceRoot(owner, kb)
		if err != nil {
			continue // not-yet-downloaded url source / missing local dir: skip
		}
		out = append(out, KBDatasetDir{
			KBID:        kb.ID,
			Name:        kb.Name,
			Description: kb.Description,
			Root:        root,
		})
	}
	return out, nil
}

// WorkspaceDatasetDirs resolves the read-only dataset dirs a workspace agent may
// Read/Grep/Glob. This is the entry point the agent-spawn path calls (via the
// KBDatasetResolver shim) so the exposed set always reflects the workspace's
// current KB mounts.
//
// KB is a first-class workspace citizen: the PRIMARY source of truth is the
// workspace's explicit mounts (workspace_kbs), whose materialized dirs live
// inside the workspace tree at <workspace>/datasets/<name>/ (visible in the file
// tree). Only when a workspace has NO explicit mounts do we fall back to the
// legacy project-implicit inheritance (project-bound KBs, resolved at their
// source root) so existing workspaces keep working until they mount explicitly.
func (s *KBService) WorkspaceDatasetDirs(ctx context.Context, workspaceID int64) ([]KBDatasetDir, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	owner := OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	// Decide the fallback on whether ANY explicit mount row exists — NOT on how
	// many are enabled. ListWorkspaceKBs filters disabled KBs, so a workspace
	// whose mounted KBs are all disabled would otherwise spuriously fall back to
	// project-bound KBs it never chose. Explicit mounts (even all-disabled) mean
	// the workspace opted out of project inheritance; only a workspace with zero
	// workspace_kbs rows falls back.
	raw, err := s.q.ListWorkspaceKBsForWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]KBDatasetDir, 0, len(raw))
	for _, r := range raw {
		// Re-load the KB to honor tenant isolation + the disabled filter (the raw
		// workspace_kbs row carries no owner/status). A disabled or cross-owner KB
		// is skipped, not a fallback trigger.
		kb, err := s.GetKB(ctx, owner, r.KbID)
		if err != nil {
			continue
		}
		if kb.Status == "disabled" {
			continue
		}
		if r.DatasetPath == "" {
			continue
		}
		if _, err := os.Stat(r.DatasetPath); err != nil {
			continue // dataset dir not materialized yet: skip
		}
		out = append(out, KBDatasetDir{
			KBID:        kb.ID,
			Name:        kb.Name,
			Description: kb.Description,
			Root:        r.DatasetPath,
		})
	}
	if len(raw) > 0 {
		return out, nil
	}
	// No explicit mounts: fall back to project-bound KBs (backward compat). A
	// project-less workspace (GetProjectIDForWorkspace returns 0 or errors) sees
	// no bound KB — mirror ListVisibleKBs' nil-project behavior.
	projectID, _ := s.q.GetProjectIDForWorkspace(ctx, workspaceID)
	return s.ResolveDatasetDirs(ctx, owner, projectID)
}

// VisibleSearchHit is a search hit enriched with the source KB's id + name so an
// agent searching across all visible KBs knows which base each hit came from.
type VisibleSearchHit struct {
	KBID   int64  `json:"kb_id"`
	KBName string `json:"kb_name"`
	kbindex.SearchHit
}

// SearchVisible runs a keyword full-text search across every KB visible to a
// workspace (owner-scoped + project-bound) and returns ranked hits tagged with
// their source KB. Results from different KBs are interleaved by score so the
// strongest matches surface first regardless of which base they came from.
func (s *KBService) SearchVisible(ctx context.Context, owner OwnerRef, projectID *int64, query string, limit int) ([]VisibleSearchHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	kbs, err := s.ListVisibleKBs(ctx, owner, projectID)
	if err != nil {
		return nil, err
	}
	if len(kbs) == 0 {
		return nil, nil
	}
	idx, err := s.ownerIndex(owner)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	var hits []VisibleSearchHit
	for _, kb := range kbs {
		// Over-fetch per KB so the merged top-N is accurate after re-sorting.
		raw, serr := idx.Search(ctx, kb.ID, query, limit)
		if serr != nil {
			return nil, serr
		}
		for _, h := range raw {
			hits = append(hits, VisibleSearchHit{KBID: kb.ID, KBName: kb.Name, SearchHit: h})
		}
	}
	// Lower Score = better match: SQLite FTS5 bm25() returns negative scores with
	// the most-negative being the strongest hit, and the Postgres path stores
	// -similarity(content, query) to match that convention, so we sort ASCENDING.
	// (The short-query LIKE and pg_trgm-absent ILIKE fallbacks score every hit 0,
	// so the stable sort just preserves their per-KB order.) Sorting descending
	// here would float the weakest matches to the top and truncate away the best.
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score < hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// Ingest reads the KB's source, chunks each text file, and writes the chunks to
// the full-text index. Idempotent per file via content hash unless opts.Force.
func (s *KBService) Ingest(ctx context.Context, owner OwnerRef, kbID int64, opts IngestOptions) (IngestResult, error) {
	kb, err := s.GetKB(ctx, owner, kbID)
	if err != nil {
		return IngestResult{}, err
	}
	root, err := s.resolveSourceRoot(owner, kb)
	if err != nil {
		return IngestResult{}, err
	}
	files, err := gatherTextFiles(root)
	if err != nil {
		return IngestResult{}, err
	}
	idx, err := s.ownerIndex(owner)
	if err != nil {
		return IngestResult{}, err
	}

	var res IngestResult
	for _, f := range files {
		res.FilesScanned++
		raw, rerr := os.ReadFile(f.abs)
		if rerr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", f.rel, rerr))
			continue
		}
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:])

		if !opts.Force {
			if existing, gerr := s.q.GetKBDocumentByPath(ctx, store.GetKBDocumentByPathParams{
				KbID: kb.ID, RelPath: f.rel,
			}); gerr == nil && existing.ContentHash == hash {
				res.FilesUnchanged++
				continue
			}
		}

		// PDF/Office files carry no indexable bytes: hand them to the extractor and
		// index the recovered text. A document the extractor gives up on is recorded
		// as an error and skipped rather than indexed as binary noise. Hashing still
		// uses the on-disk bytes, so idempotency is unaffected.
		text := string(raw)
		if docextract.IsSupported(f.abs) {
			extracted, xerr := docextract.PlainText(f.abs)
			if xerr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", f.rel, xerr))
				continue
			}
			text = extracted
		}

		chunks := kbindex.ChunkText(text, opts.MaxChunkChars)

		doc, derr := s.q.UpsertKBDocument(ctx, store.UpsertKBDocumentParams{
			KbID:        kb.ID,
			RelPath:     f.rel,
			Uri:         f.abs,
			ContentHash: hash,
			ChunkCount:  int64(len(chunks)),
			ByteSize:    int64(len(raw)),
		})
		if derr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", f.rel, derr))
			continue
		}
		if ierr := idx.IndexDocument(ctx, kbindex.IndexDoc{
			KBID: kb.ID, DocumentID: doc.ID, RelPath: f.rel, Chunks: chunks,
		}); ierr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", f.rel, ierr))
			continue
		}
		res.FilesIngested++
		res.ChunksWritten += len(chunks)
	}
	return res, nil
}

// RebuildIndex drops the KB's index entries and re-indexes every source file.
// Used to recover from a corrupt/missing sidecar or after a tokenizer change;
// the source files remain the source of truth.
func (s *KBService) RebuildIndex(ctx context.Context, owner OwnerRef, kbID int64) error {
	kb, err := s.GetKB(ctx, owner, kbID)
	if err != nil {
		return err
	}
	idx, err := s.ownerIndex(owner)
	if err != nil {
		return err
	}
	if err := idx.DeleteKB(ctx, kb.ID); err != nil {
		return err
	}
	_, err = s.Ingest(ctx, owner, kb.ID, IngestOptions{Force: true})
	return err
}

// ownerIndex returns the KBIndex for owner (per-owner SQLite sidecar, or the
// shared Postgres index).
func (s *KBService) ownerIndex(owner OwnerRef) (kbindex.KBIndex, error) {
	return s.idx.Get(owner.KBIndexPath(s.dataDir))
}


// trimToValidUTF8 drops trailing bytes until s is valid UTF-8. Cutting a corpus
// at a fixed byte offset lands mid-rune for any multi-byte character — routine
// for the Chinese corpora these KBs are built from — and an invalid tail would
// be replaced with U+FFFD once the response is marshalled to JSON. Trimming at
// most three bytes is cheaper than shipping mojibake.
func trimToValidUTF8(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
