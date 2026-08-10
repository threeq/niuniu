// Package service: SceneLayerService — base-layer plus ordered scene-layer
// management for a workspace.
//
// The base layer holds the workspace's "pre-scene" state (the MCP servers /
// plugins it had before scenes existed) and is always present; everything
// else is a layered scene on top, position-ordered. ApplyResult is what the
// HTTP/SPA caller renders.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ApplyResult is the wire shape returned by Attach/Detach/Move/Apply. It
// bundles the projection itself plus the side-effect summary surfaced to
// the SPA banner (missing creds, install failures, restart needed).
type ApplyResult struct {
	Projection         *Projection           `json:"projection"`
	MissingCredentials []MissingCredential   `json:"missing_credentials"`
	InstallFailures    []PluginInstallResult `json:"install_failures"`
	RestartRequired    bool                  `json:"restart_required"`
	Digest             string                `json:"digest"`
	// DismissedPlugins lists plugin sources the user chose to ignore for this
	// workspace. Their pending/failed rows are filtered out of InstallFailures;
	// the SPA surfaces them in a separate "restore" affordance.
	DismissedPlugins []string `json:"dismissed_plugins"`
}

// MissingCredential surfaces unbound aliases referenced by the projection's
// required_credentials. Distinct from RequiredCredential because the wire
// payload includes whether the credential is optional (banner-mute vs
// banner-warn).
type MissingCredential struct {
	Alias    string `json:"alias"`
	Provider string `json:"provider"`
	Purpose  string `json:"purpose,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// SceneLayerService owns the layer rows in workspace_scene_layers and
// orchestrates SceneProjector.Apply after every mutation.
//
// Mutations on the same workspace are serialized through per-workspace
// mutexes (`mu` + `locks`) — the projector is non-transactional (it writes
// .mcp.json, CLAUDE.md fragments, and DB rows in sequence), so concurrent
// Attach/Detach/Move on the same workspace can race on file artifacts and
// produce inconsistent projection caches. Code-review finding #5.
type SceneLayerService struct {
	q         *store.Queries
	db        *store.DB
	projector *SceneProjector

	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

// NewSceneLayerService wires a layer service to its projector.
func NewSceneLayerService(db *sql.DB, projector *SceneProjector) *SceneLayerService {
	return &SceneLayerService{
		// store.NewQueries (driver-aware) — see CLAUDE.md "Driver-aware DB access".
		// Plain store.New(db) ships ?-placeholders to pgx on Postgres.
		q:         store.NewQueries(db),
		db:        store.Wrap(db),
		projector: projector,
		locks:     map[int64]*sync.Mutex{},
	}
}

// lockFor returns the per-workspace mutex, creating it on first use.
func (s *SceneLayerService) lockFor(wsID int64) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.locks[wsID]; ok {
		return l
	}
	l := &sync.Mutex{}
	s.locks[wsID] = l
	return l
}

// EnsureBaseLayer guarantees a base layer row exists for the workspace.
// Idempotent: subsequent calls re-fetch the existing row without touching DB
// state. Returns the row's base_definition for callers that want to mutate
// and re-save (UpdateMCPServers shim).
func (s *SceneLayerService) EnsureBaseLayer(ctx context.Context, wsID int64) (store.WorkspaceSceneLayer, error) {
	got, err := s.q.GetBaseLayer(ctx, wsID)
	if err == nil {
		return got, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.WorkspaceSceneLayer{}, fmt.Errorf("get base layer: %w", err)
	}
	if err := s.q.InsertBaseLayer(ctx, store.InsertBaseLayerParams{
		WorkspaceID:    wsID,
		BaseDefinition: emptyBaseDefinition,
	}); err != nil {
		return store.WorkspaceSceneLayer{}, fmt.Errorf("insert base layer: %w", err)
	}
	return s.q.GetBaseLayer(ctx, wsID)
}

// SaveBaseDefinition replaces the base layer's payload. Combines
// EnsureBaseLayer with a follow-up Update so callers (UpdateMCPServers shim)
// don't repeat the "exists?" gate themselves.
func (s *SceneLayerService) SaveBaseDefinition(ctx context.Context, wsID int64, body string) error {
	lock := s.lockFor(wsID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.EnsureBaseLayer(ctx, wsID); err != nil {
		return err
	}
	return s.q.UpdateBaseLayer(ctx, store.UpdateBaseLayerParams{
		WorkspaceID:    wsID,
		BaseDefinition: body,
	})
}

// emptyBaseDefinition is the JSON literal stored when a workspace gets its
// base layer auto-created (workspace.Create call path). It conforms to the
// SceneDefinition shape so Projection.MergeFrom can decode it without
// branching on emptiness.
const emptyBaseDefinition = `{"mcp":[],"plugins":[],"assets":{},"prompts":[],"required_credentials":[],"match":{}}`

// Attach pushes a new scene layer on top of the existing stack and triggers
// projection recompute + apply. position is nullable: nil → next available.
func (s *SceneLayerService) Attach(ctx context.Context, wsID, sceneID int64, position *int) (*ApplyResult, error) {
	lock := s.lockFor(wsID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.EnsureBaseLayer(ctx, wsID); err != nil {
		return nil, err
	}
	pos := int64(0)
	if position != nil {
		pos = int64(*position)
	} else {
		next, err := s.q.NextLayerPosition(ctx, wsID)
		if err != nil {
			return nil, fmt.Errorf("next position: %w", err)
		}
		pos = next
	}
	if _, err := s.q.AttachLayer(ctx, store.AttachLayerParams{
		WorkspaceID: wsID,
		SceneID:     sql.NullInt64{Int64: sceneID, Valid: true},
		Position:    pos,
	}); err != nil {
		return nil, fmt.Errorf("attach layer: %w", err)
	}
	return s.projector.Apply(ctx, wsID)
}

// Detach removes a non-base layer and re-applies.
func (s *SceneLayerService) Detach(ctx context.Context, wsID, layerID int64) (*ApplyResult, error) {
	lock := s.lockFor(wsID)
	lock.Lock()
	defer lock.Unlock()
	if err := s.q.DetachLayer(ctx, layerID); err != nil {
		return nil, fmt.Errorf("detach layer: %w", err)
	}
	return s.projector.Apply(ctx, wsID)
}

// Move re-orders a layer. Position semantics inherit from
// UpdateLayerPosition — caller is responsible for keeping the per-workspace
// positions unique (the partial-unique index will reject duplicates).
func (s *SceneLayerService) Move(ctx context.Context, wsID, layerID int64, newPos int) (*ApplyResult, error) {
	lock := s.lockFor(wsID)
	lock.Lock()
	defer lock.Unlock()
	if err := s.q.UpdateLayerPosition(ctx, store.UpdateLayerPositionParams{
		ID:       layerID,
		Position: int64(newPos),
	}); err != nil {
		return nil, fmt.Errorf("move layer: %w", err)
	}
	return s.projector.Apply(ctx, wsID)
}

// List returns the workspace's layers in position-asc order, base first
// (base has position=0 and is_base=1).
func (s *SceneLayerService) List(ctx context.Context, wsID int64) ([]store.WorkspaceSceneLayer, error) {
	return s.q.ListLayersForWorkspace(ctx, wsID)
}
