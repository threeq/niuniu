// Package service: SceneService — owner-scoped CRUD + Fork for niuniu scenes.
//
// Scenes flow through three sources: 'builtin' (seeded from YAML, immutable
// per-instance — UpsertBuiltinScene only updates rows that still have
// source='builtin'), 'user' (created via SceneService.Create or Fork), and
// 'org' (same code path as 'user' but owner.Type=="org"). The same struct
// covers all three; differentiation is by .Source / .OwnerType + ID.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ErrBuiltinImmutable is returned by Update/Delete when the target row carries
// source='builtin'. Forking is the only legal way to derive a mutable copy.
var ErrBuiltinImmutable = errors.New("builtin scene is immutable; fork it to customize")

// SceneService is the CRUD + Fork facade. All methods route through *store.DB
// per CLAUDE.md "Driver-aware DB access".
type SceneService struct {
	q  *store.Queries
	db *store.DB
}

// NewSceneService follows the niuniu service constructor pattern: public
// signature takes *sql.DB for back-compat with existing test fixtures, the
// body wraps via store.Wrap(db). store.Wrap(nil) returns nil so test code
// passing a bare nil keeps working (covers ValidateSceneDefinition-only
// callers).
func NewSceneService(db *sql.DB) *SceneService {
	// MUST use store.NewQueries (driver-aware) — NOT raw store.New(db), which
	// on Postgres ships ?-placeholders straight to pgx and fails with
	// SQLSTATE 42601 "syntax error at or near ...". See CLAUDE.md
	// "Driver-aware DB access".
	return &SceneService{q: store.NewQueries(db), db: store.Wrap(db)}
}

// Get loads a scene by id. Wraps q.GetScene for symmetry with other services.
func (s *SceneService) Get(ctx context.Context, id int64) (store.Scene, error) {
	return s.q.GetScene(ctx, id)
}

// Create persists a new user/org-owned scene. The caller is responsible for
// authz (the handler layer calls Authz.EnsureOwnerWritable before us).
// definition is validated; tags are stored as a JSON array (canonicalised
// upstream by the handler — we accept []string and re-marshal here for safety).
func (s *SceneService) Create(ctx context.Context, owner OwnerRef, slug, displayName, description string, tags []string, def *SceneDefinition) (store.Scene, error) {
	if err := owner.Validate(); err != nil {
		return store.Scene{}, err
	}
	if slug == "" {
		return store.Scene{}, errors.New("slug required")
	}
	if displayName == "" {
		return store.Scene{}, errors.New("display_name required")
	}
	if def == nil {
		def = &SceneDefinition{}
	}
	if err := ValidateSceneDefinition(def); err != nil {
		return store.Scene{}, fmt.Errorf("validate scene definition: %w", err)
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		return store.Scene{}, fmt.Errorf("marshal definition: %w", err)
	}
	tagsJSON, err := marshalTags(tags)
	if err != nil {
		return store.Scene{}, err
	}
	return s.q.CreateScene(ctx, store.CreateSceneParams{
		OwnerType:   owner.Type,
		OwnerID:     owner.ID,
		Slug:        slug,
		DisplayName: displayName,
		Description: description,
		Tags:        tagsJSON,
		Source:      "user",
		SourceSlug:  "",
		Definition:  string(defJSON),
		ContentHash: HashDefinition(def),
		Enabled:     1,
	})
}

// Update mutates the scene's payload. Builtin scenes refuse the update with
// ErrBuiltinImmutable so accidentally clobbering a seed row from the admin UI
// is impossible — to customize a builtin, the caller forks first.
func (s *SceneService) Update(ctx context.Context, id int64, displayName, description string, tags []string, def *SceneDefinition) error {
	return s.UpdateWithOwner(ctx, id, nil, displayName, description, tags, def)
}

// UpdateWithOwner mutates a scene's payload and, when newOwner is non-nil,
// moves the scene into another writable owner scope. The caller is responsible
// for authz against both the current scene and the target owner.
func (s *SceneService) UpdateWithOwner(ctx context.Context, id int64, newOwner *OwnerRef, displayName, description string, tags []string, def *SceneDefinition) error {
	scene, err := s.q.GetScene(ctx, id)
	if err != nil {
		return err
	}
	if scene.Source == "builtin" {
		return ErrBuiltinImmutable
	}
	ownerType := scene.OwnerType
	ownerID := scene.OwnerID
	if newOwner != nil {
		if err := newOwner.Validate(); err != nil {
			return err
		}
		ownerType = newOwner.Type
		ownerID = newOwner.ID
	}
	if def == nil {
		def = &SceneDefinition{}
	}
	if err := ValidateSceneDefinition(def); err != nil {
		return fmt.Errorf("validate scene definition: %w", err)
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshal definition: %w", err)
	}
	tagsJSON, err := marshalTags(tags)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE scenes
		 SET owner_type = ?,
		     owner_id = ?,
		     display_name = ?,
		     description = ?,
		     tags = ?,
		     definition = ?,
		     content_hash = ?,
		     enabled = ?,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		ownerType, ownerID, displayName, description, tagsJSON, string(defJSON), HashDefinition(def), scene.Enabled, id)
	return err
}

// Delete removes a user/org scene. Builtins are protected like Update.
func (s *SceneService) Delete(ctx context.Context, id int64) error {
	scene, err := s.q.GetScene(ctx, id)
	if err != nil {
		return err
	}
	if scene.Source == "builtin" {
		return ErrBuiltinImmutable
	}
	return s.q.DeleteScene(ctx, id)
}

// Fork clones an accessible scene's definition + tags into a new user/org-owned
// row. source_slug records the parent slug for provenance. The fork is always
// source='user' (or 'org' implicitly via owner.Type — the source column tracks
// "is this a builtin seed" vs "user-authored", not owner.Type).
func (s *SceneService) Fork(ctx context.Context, owner OwnerRef, sourceID int64, newSlug string) (store.Scene, error) {
	if err := owner.Validate(); err != nil {
		return store.Scene{}, err
	}
	if newSlug == "" {
		return store.Scene{}, errors.New("new slug required")
	}
	src, err := s.q.GetScene(ctx, sourceID)
	if err != nil {
		return store.Scene{}, fmt.Errorf("load source scene: %w", err)
	}
	return s.q.CreateScene(ctx, store.CreateSceneParams{
		OwnerType:   owner.Type,
		OwnerID:     owner.ID,
		Slug:        newSlug,
		DisplayName: src.DisplayName + " (fork)",
		Description: src.Description,
		Tags:        src.Tags,
		Source:      "user",
		SourceSlug:  src.Slug,
		Definition:  src.Definition,
		ContentHash: src.ContentHash,
		Enabled:     1,
	})
}

// ListAccessible returns scenes visible to userID, scoped to owner (caller's
// personal or an org they belong to). Wraps the q.ListScenesAccessible query
// which already inlines the org_members subquery — no Authz.Accessible call
// is needed here (architecture review note: Authz.Accessible doesn't exist
// at this name).
func (s *SceneService) ListAccessible(ctx context.Context, owner OwnerRef, userID int64) ([]store.Scene, error) {
	return s.q.ListScenesAccessible(ctx, store.ListScenesAccessibleParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
		UserID:    userID,
	})
}

// DecodeDefinition parses the JSON-encoded definition column into a typed
// SceneDefinition. Used by SceneProjector and SceneMatcher.
func DecodeDefinition(raw string) (*SceneDefinition, error) {
	if raw == "" {
		return &SceneDefinition{}, nil
	}
	def := &SceneDefinition{}
	if err := json.Unmarshal([]byte(raw), def); err != nil {
		return nil, fmt.Errorf("decode scene definition: %w", err)
	}
	return def, nil
}

// FeaturedPluginSources returns the set of plugin sources referenced by builtin
// scenes (source='builtin'), for badging "精品" plugins in the library.
// The result is a map from plugin source (e.g. "playwright@claude-plugins-official")
// to true. Non-fatal: errors are returned so callers can decide whether to skip.
func (s *SceneService) FeaturedPluginSources(ctx context.Context) (map[string]bool, error) {
	scenes, err := s.q.ListBuiltinScenes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list builtin scenes: %w", err)
	}
	featured := make(map[string]bool)
	for _, scene := range scenes {
		def, err := DecodeDefinition(scene.Definition)
		if err != nil {
			// Skip malformed scenes rather than aborting the whole call.
			continue
		}
		for _, p := range def.Plugins {
			if p.Source != "" {
				featured[p.Source] = true
			}
		}
	}
	return featured, nil
}

// marshalTags canonicalises the tags slice for storage. nil/[] both serialize
// to "[]" so the column never holds the literal "null".
func marshalTags(tags []string) (string, error) {
	if tags == nil {
		tags = []string{}
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "", fmt.Errorf("marshal tags: %w", err)
	}
	return string(b), nil
}

// DecodeTags parses the tags column. Symmetric with marshalTags; safe on
// "" / "null" / "[]" inputs.
func DecodeTags(raw string) []string {
	if raw == "" || raw == "null" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}
