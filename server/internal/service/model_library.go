package service

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ModelDTO is the merged, owner-facing view of a model library row. When an
// owner row shadows a system row of the same name, ID/Source/DisplayName/
// Enabled all come from the owner row so callers can edit it directly.
type ModelDTO struct {
	ID          int64  `json:"id"`
	OwnerType   string `json:"owner_type"`
	OwnerID     int64  `json:"owner_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
}

// ErrSystemModelImmutable is returned when an operation targets a system row
// (owner_type='user', owner_id=0). System rows can only be hidden by creating
// an owner row of the same name with enabled=false.
var ErrSystemModelImmutable = errors.New("system model cannot be modified")

// ModelLibraryService implements the owner-scoped shared model library. Library
// names are shared by every provider and CLI; nothing here touches provider
// configuration or runtime resolution.
type ModelLibraryService struct {
	q *store.Queries
}

func NewModelLibraryService(q *store.Queries) *ModelLibraryService {
	return &ModelLibraryService{q: q}
}

// List returns the system rows merged with the owner's rows. An owner row of
// the same name replaces the system row, so the owner can rename, disable or
// otherwise shadow a builtin. When includeDisabled is false, disabled rows are
// omitted (the dropdown view).
func (s *ModelLibraryService) List(ctx context.Context, owner OwnerRef, includeDisabled bool) ([]ModelDTO, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.q.ListModelsForOwner(ctx, store.ListModelsForOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if err != nil {
		return nil, err
	}

	merged := make(map[string]store.Model, len(rows))
	for _, m := range rows {
		if prev, ok := merged[m.Name]; ok && isSystemModel(prev) && !isSystemModel(m) {
			merged[m.Name] = m
			continue
		}
		if _, ok := merged[m.Name]; !ok {
			merged[m.Name] = m
		}
	}

	list := make([]ModelDTO, 0, len(merged))
	for _, m := range merged {
		if !includeDisabled && m.Enabled == 0 {
			continue
		}
		list = append(list, modelToDTO(m))
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

// Upsert writes an owner row. If the owner already has a row with this name it
// is updated; otherwise a new source='manual' row is created. Upserting a
// builtin name with enabled=false is how a system model gets hidden.
func (s *ModelLibraryService) Upsert(ctx context.Context, owner OwnerRef, name, displayName string, enabled bool) (ModelDTO, error) {
	if err := owner.Validate(); err != nil {
		return ModelDTO{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ModelDTO{}, errors.New("model name is required")
	}

	existing, err := s.q.GetModelByName(ctx, store.GetModelByNameParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
		Name:      name,
	})
	switch {
	case err == nil:
		updated, err := s.q.UpdateModel(ctx, store.UpdateModelParams{
			DisplayName: displayName,
			Enabled:     boolToInt(enabled),
			ID:          existing.ID,
		})
		if err != nil {
			return ModelDTO{}, err
		}
		return modelToDTO(updated), nil
	case !errors.Is(err, sql.ErrNoRows):
		return ModelDTO{}, err
	}

	created, err := s.q.CreateModel(ctx, store.CreateModelParams{
		OwnerType:   owner.Type,
		OwnerID:     owner.ID,
		Name:        name,
		DisplayName: displayName,
		Enabled:     boolToInt(enabled),
		Source:      "manual",
	})
	if err != nil {
		return ModelDTO{}, err
	}
	return modelToDTO(created), nil
}

// Get returns one model row by id without owner filtering. Callers must run
// the owner authorization check themselves; the API layer uses this to answer
// 403 (not 404) when a caller targets another owner's row.
func (s *ModelLibraryService) Get(ctx context.Context, id int64) (ModelDTO, error) {
	m, err := s.q.GetModel(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelDTO{}, ErrNotFound
	}
	if err != nil {
		return ModelDTO{}, err
	}
	return modelToDTO(m), nil
}

// Update patches display name and/or enabled on an owner row. System rows are
// immutable; another owner's row is reported as not found.
func (s *ModelLibraryService) Update(ctx context.Context, owner OwnerRef, id int64, displayName *string, enabled *bool) (ModelDTO, error) {
	if err := owner.Validate(); err != nil {
		return ModelDTO{}, err
	}
	m, err := s.ownedModel(ctx, owner, id)
	if err != nil {
		return ModelDTO{}, err
	}

	args := store.UpdateModelParams{ID: m.ID, DisplayName: m.DisplayName, Enabled: m.Enabled}
	if displayName != nil {
		args.DisplayName = *displayName
	}
	if enabled != nil {
		args.Enabled = boolToInt(*enabled)
	}
	updated, err := s.q.UpdateModel(ctx, args)
	if err != nil {
		return ModelDTO{}, err
	}
	return modelToDTO(updated), nil
}

// Delete removes an owner row. System rows are immutable; another owner's row
// is reported as not found.
func (s *ModelLibraryService) Delete(ctx context.Context, owner OwnerRef, id int64) error {
	if err := owner.Validate(); err != nil {
		return err
	}
	if _, err := s.ownedModel(ctx, owner, id); err != nil {
		return err
	}
	n, err := s.q.DeleteModel(ctx, store.DeleteModelParams{
		ID:        id,
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Import bulk-adds names from a provider's /models listing. Names already
// present in the merged library (system or owner) are skipped, so repeated
// refreshes never duplicate rows. Added rows get source='refreshed'.
func (s *ModelLibraryService) Import(ctx context.Context, owner OwnerRef, names []string) (added, skipped int, err error) {
	if err := owner.Validate(); err != nil {
		return 0, 0, err
	}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		present, err := s.nameInLibrary(ctx, owner, name)
		if err != nil {
			return added, skipped, err
		}
		if present {
			skipped++
			continue
		}
		if _, err := s.q.CreateModel(ctx, store.CreateModelParams{
			OwnerType:   owner.Type,
			OwnerID:     owner.ID,
			Name:        name,
			DisplayName: name,
			Enabled:     1,
			Source:      "refreshed",
		}); err != nil {
			return added, skipped, err
		}
		added++
	}
	return added, skipped, nil
}

// ownedModel loads id and asserts it is an editable owner row. System rows
// surface as ErrSystemModelImmutable, anything else owned by a different owner
// as ErrNotFound (never leak the existence of other owners' rows).
func (s *ModelLibraryService) ownedModel(ctx context.Context, owner OwnerRef, id int64) (store.Model, error) {
	m, err := s.q.GetModel(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Model{}, ErrNotFound
	}
	if err != nil {
		return store.Model{}, err
	}
	if isSystemModel(m) {
		return store.Model{}, ErrSystemModelImmutable
	}
	if m.OwnerType != owner.Type || m.OwnerID != owner.ID {
		return store.Model{}, ErrNotFound
	}
	return m, nil
}

func (s *ModelLibraryService) nameInLibrary(ctx context.Context, owner OwnerRef, name string) (bool, error) {
	for _, ref := range []OwnerRef{owner, {Type: "user", ID: 0}} {
		_, err := s.q.GetModelByName(ctx, store.GetModelByNameParams{
			OwnerType: ref.Type,
			OwnerID:   ref.ID,
			Name:      name,
		})
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	return false, nil
}

func isSystemModel(m store.Model) bool {
	return m.OwnerType == "user" && m.OwnerID == 0
}

func modelToDTO(m store.Model) ModelDTO {
	return ModelDTO{
		ID:          m.ID,
		OwnerType:   m.OwnerType,
		OwnerID:     m.OwnerID,
		Name:        m.Name,
		DisplayName: m.DisplayName,
		Enabled:     m.Enabled != 0,
		Source:      m.Source,
	}
}
