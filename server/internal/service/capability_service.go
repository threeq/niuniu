package service

// capability_service.go — persistence + refresh for capability descriptions.
//
// The stored document is a CACHE of a pure function: the doc is always rebuilt
// from the confirmed contract / source metadata, and persisted only so the
// discovery surface (MCP tools, the SPA "view what the agent sees" screen) can
// read it without a rebuild. Staleness is therefore not a correctness risk —
// Ensure* always returns the freshly built doc and simply rewrites the cache
// when its digest no longer matches.
//
// Who may write. Only this service writes a capability doc, and it is only ever
// called from server-internal code paths (the publish/create pipeline and the
// read surfaces). No MCP tool and no agent-reachable endpoint accepts capability
// text as input, so "an agent cannot modify the description" is structural, not
// a policy check — see TestCapabilityNoAgentWritePath.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// CapabilityService builds, caches, and refreshes capability descriptions.
type CapabilityService struct {
	q         *store.Queries
	db        *store.DB
	providers *ExternalProviderService
	sources   *DataSourceService
	extSrc    *ExternalSourceService
}

// NewCapabilityService wires the capability service to the provider, data-source
// and project-external-source services it resolves targets through.
func NewCapabilityService(q *store.Queries, rawDB *sql.DB, providers *ExternalProviderService, sources *DataSourceService, extSrc *ExternalSourceService) *CapabilityService {
	return &CapabilityService{q: q, db: store.Wrap(rawDB), providers: providers, sources: sources, extSrc: extSrc}
}

// EnsureProvider returns the provider's capability doc, regenerating and
// persisting it when the stored copy is missing, was built by an older schema
// version, or no longer matches the contract digest.
func (s *CapabilityService) EnsureProvider(ctx context.Context, p *ProviderDef) (*CapabilityDoc, error) {
	fresh := BuildProviderCapabilityDoc(p)
	if fresh == nil || p == nil {
		return nil, nil
	}
	if stored, ok := s.load(ctx, CapabilityTargetProvider, p.ID); ok && !capabilityNeedsRefresh(stored, fresh) {
		return fresh, nil
	}
	if err := s.save(ctx, CapabilityTargetProvider, p.ID, fresh); err != nil {
		// The cache write failing does not make the doc unavailable — the caller
		// still gets the freshly built, correct document.
		return fresh, err
	}
	return fresh, nil
}

// EnsureDataSource is EnsureProvider for the data line.
func (s *CapabilityService) EnsureDataSource(ctx context.Context, dto *DataSourceDTO) (*CapabilityDoc, error) {
	fresh := BuildDataSourceCapabilityDoc(dto)
	if fresh == nil || dto == nil {
		return nil, nil
	}
	if stored, ok := s.load(ctx, CapabilityTargetDataSource, dto.ID); ok && !capabilityNeedsRefresh(stored, fresh) {
		return fresh, nil
	}
	if err := s.save(ctx, CapabilityTargetDataSource, dto.ID, fresh); err != nil {
		return fresh, err
	}
	return fresh, nil
}

// capabilityNeedsRefresh reports whether the cached doc must be rewritten: a
// schema-version bump (the renderer changed) or a source-digest change (the
// contract / metadata changed) both invalidate it.
func capabilityNeedsRefresh(stored, fresh *CapabilityDoc) bool {
	if stored == nil || fresh == nil {
		return true
	}
	return stored.SchemaVersion != fresh.SchemaVersion || stored.SourceDigest != fresh.SourceDigest
}

// EnsureProviderByName resolves a provider by name and ensures its doc.
func (s *CapabilityService) EnsureProviderByName(ctx context.Context, name string) (*CapabilityDoc, error) {
	if s.providers == nil {
		return nil, errors.New("provider service unavailable")
	}
	p, err := s.providers.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	return s.EnsureProvider(ctx, p)
}

// EnsureDataSourceByID resolves an owner-accessible source by id and ensures its
// doc.
func (s *CapabilityService) EnsureDataSourceByID(ctx context.Context, id, userID int64, orgIDs []int64) (*CapabilityDoc, error) {
	if s.sources == nil {
		return nil, errors.New("data source service unavailable")
	}
	dto, err := s.sources.Get(ctx, id, userID, orgIDs)
	if err != nil {
		return nil, err
	}
	return s.EnsureDataSource(ctx, dto)
}

// ResolveForWorkspace returns the capability docs of every integration source
// bound to the workspace's project — data sources (project bindings) and
// providers (project_external_sources) — in the deterministic order
// RenderCapabilityAgentBlock expects. Best-effort per source: one failing lookup
// does not drop the rest.
func (s *CapabilityService) ResolveForWorkspace(ctx context.Context, ws store.Workspace, userID int64, orgIDs []int64) ([]*CapabilityDoc, error) {
	var docs []*CapabilityDoc

	if s.sources != nil {
		dtos, err := s.sources.ListForWorkspace(ctx, ws, userID, orgIDs)
		if err == nil {
			for i := range dtos {
				if d, derr := s.EnsureDataSource(ctx, &dtos[i]); derr == nil && d != nil {
					docs = append(docs, d)
				}
			}
		}
	}

	if s.providers != nil && s.extSrc != nil {
		pid, err := s.q.GetProjectIDForWorkspace(ctx, ws.ID)
		if err == nil && pid > 0 {
			if bindings, berr := s.extSrc.List(ctx, pid); berr == nil {
				seen := map[string]bool{}
				for _, b := range bindings {
					name := string(b.Provider)
					if name == "" || seen[name] {
						continue
					}
					seen[name] = true
					p, perr := s.providers.GetByName(ctx, name)
					if perr != nil {
						continue
					}
					if d, derr := s.EnsureProvider(ctx, p); derr == nil && d != nil {
						docs = append(docs, d)
					}
				}
			}
		}
	}

	sort.SliceStable(docs, func(i, j int) bool {
		if docs[i].TargetKind != docs[j].TargetKind {
			return docs[i].TargetKind < docs[j].TargetKind
		}
		return docs[i].Name < docs[j].Name
	})
	return docs, nil
}

// WorkspaceCapabilities resolves the capability docs of the sources bound to a
// workspace's project, for the agent-spawn context injection. Spawn has no
// request user, so the caller identity is derived from the workspace row: the
// live session user when known, else the creator, else the owner for a personal
// workspace. Org-owned workspaces make the org's sources visible.
func (s *CapabilityService) WorkspaceCapabilities(ctx context.Context, workspaceID int64) ([]*CapabilityDoc, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	var userID int64
	switch {
	case ws.CurrentSessionUserID.Valid && ws.CurrentSessionUserID.Int64 > 0:
		userID = ws.CurrentSessionUserID.Int64
	case ws.CreatedBy.Valid && ws.CreatedBy.Int64 > 0:
		userID = ws.CreatedBy.Int64
	case ws.OwnerType == "user":
		userID = ws.OwnerID
	}
	var orgIDs []int64
	if ws.OwnerType == "org" {
		orgIDs = []int64{ws.OwnerID}
	}
	return s.ResolveForWorkspace(ctx, ws, userID, orgIDs)
}

// --- storage (raw SQL — no sqlc regeneration required) ----------------------

// load reads the cached doc for a subject. ok=false on any error (a missing or
// unreadable cache is simply a cache miss).
func (s *CapabilityService) load(ctx context.Context, kind string, id int64) (*CapabilityDoc, bool) {
	if s.db == nil || id == 0 {
		return nil, false
	}
	var digest, raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT source_digest, doc FROM integration_capabilities WHERE subject_kind = ? AND subject_id = ?`,
		kind, id).Scan(&digest, &raw)
	if err != nil {
		return nil, false
	}
	var doc CapabilityDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, false
	}
	return &doc, true
}

// save upserts the cached doc. Delete-then-insert keeps the statement portable
// across SQLite and Postgres (no dialect-specific ON CONFLICT spelling) and the
// UNIQUE(subject_kind, subject_id) constraint makes it idempotent.
func (s *CapabilityService) save(ctx context.Context, kind string, id int64, doc *CapabilityDoc) error {
	if s.db == nil || id == 0 || doc == nil {
		return nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal capability doc: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM integration_capabilities WHERE subject_kind = ? AND subject_id = ?`, kind, id); err != nil {
		return fmt.Errorf("clear capability doc: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO integration_capabilities (subject_kind, subject_id, source_digest, doc) VALUES (?, ?, ?, ?)`,
		kind, id, doc.SourceDigest, string(raw)); err != nil {
		return fmt.Errorf("insert capability doc: %w", err)
	}
	return nil
}

// Delete removes the cached doc for a subject (used when a target is removed).
func (s *CapabilityService) Delete(ctx context.Context, kind string, id int64) error {
	if s.db == nil || id == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM integration_capabilities WHERE subject_kind = ? AND subject_id = ?`, kind, id)
	return err
}
