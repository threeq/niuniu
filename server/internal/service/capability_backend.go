package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// CapabilityBackend binds one capability of a capability module (e.g. the
// video-gen module's tts/image/video capabilities) to a concrete backend
// adapter implementation (e.g. openai-compat, seedance, kling).
//
// Backends live in their own table and their own env namespace (NN_CAP_*)
// deliberately apart from env_providers: env_providers configure the LLM
// accounts an agent session uses, capability backends configure the
// third-party generation services the module's tools call. Ownership
// granularity differs too: a capability row is either a user-scoped global
// default (owner_type "user"; owner_id 0 = system-wide when auth is off) or a
// per-workspace override (owner_type "workspace"), and resolution prefers the
// workspace row. APIKey holds the literal credential and is therefore never
// serialized into an HTTP response (the API layer reports has_api_key).
type CapabilityBackend struct {
	ID          int64             `json:"id"`
	OwnerType   string            `json:"owner_type"`
	OwnerID     int64             `json:"owner_id"`
	Module      string            `json:"module"`
	Capability  string            `json:"capability"`
	Backend     string            `json:"backend"`
	Name        string            `json:"name"`
	BaseURL     string            `json:"base_url"`
	APIKey      string            `json:"api_key"`
	ExtraConfig map[string]string `json:"extra_config"`
	Enabled     bool              `json:"enabled"`
	Position    int               `json:"position"`
}

// CapabilityBackendService manages capability_backends rows.
type CapabilityBackendService struct {
	q  *store.Queries
	db *store.DB
}

func NewCapabilityBackendService(q *store.Queries, db *sql.DB) *CapabilityBackendService {
	return &CapabilityBackendService{q: q, db: store.Wrap(db)}
}

// List returns the module's backends, optionally narrowed to one owner
// (ownerType "" = every owner, i.e. the auth-off / admin view).
func (s *CapabilityBackendService) List(ctx context.Context, ownerType string, ownerID int64, module string) ([]CapabilityBackend, error) {
	if ownerType == "" {
		rows, err := s.q.ListCapabilityBackendsByModule(ctx, module)
		if err != nil {
			return nil, err
		}
		return capabilityBackendsFromRows(rows), nil
	}
	rows, err := s.q.ListCapabilityBackendsByOwner(ctx, store.ListCapabilityBackendsByOwnerParams{
		Module:    module,
		OwnerType: ownerType,
		OwnerID:   ownerID,
	})
	if err != nil {
		return nil, err
	}
	return capabilityBackendsFromRows(rows), nil
}

// Get returns one backend row (credential included) or the store's not-found
// error. The API layer needs it to authorize update/delete against the row's
// owner; it is never serialized directly.
func (s *CapabilityBackendService) Get(ctx context.Context, id int64) (*CapabilityBackend, error) {
	row, err := s.q.GetCapabilityBackend(ctx, id)
	if err != nil {
		return nil, err
	}
	return capabilityBackendFromRow(row), nil
}

func (s *CapabilityBackendService) Create(ctx context.Context, in CapabilityBackend) (*CapabilityBackend, error) {
	row, err := s.q.CreateCapabilityBackend(ctx, store.CreateCapabilityBackendParams{
		OwnerType:   in.OwnerType,
		OwnerID:     in.OwnerID,
		Module:      in.Module,
		Capability:  in.Capability,
		Backend:     in.Backend,
		Name:        in.Name,
		BaseUrl:     in.BaseURL,
		ApiKey:      in.APIKey,
		ExtraConfig: marshalExtraConfig(in.ExtraConfig),
		Enabled:     boolToInt(in.Enabled),
		Position:    int64(in.Position),
	})
	if err != nil {
		return nil, err
	}
	return capabilityBackendFromRow(row), nil
}

// Update rewrites a backend row. apiKeySet=false (or an empty in.APIKey) keeps
// the stored credential — the edit dialog never receives the plaintext key, so
// an untouched key field must not blank it. Returns the updated row.
func (s *CapabilityBackendService) Update(ctx context.Context, id int64, in CapabilityBackend, apiKeySet bool) (*CapabilityBackend, error) {
	cur, err := s.q.GetCapabilityBackend(ctx, id)
	if err != nil {
		return nil, err
	}
	apiKey := cur.ApiKey
	if apiKeySet {
		apiKey = in.APIKey
	}
	row, err := s.q.UpdateCapabilityBackend(ctx, store.UpdateCapabilityBackendParams{
		ID:          id,
		Module:      in.Module,
		Capability:  in.Capability,
		Backend:     in.Backend,
		Name:        in.Name,
		BaseUrl:     in.BaseURL,
		ApiKey:      apiKey,
		ExtraConfig: marshalExtraConfig(in.ExtraConfig),
		Enabled:     boolToInt(in.Enabled),
		Position:    int64(in.Position),
	})
	if err != nil {
		return nil, err
	}
	return capabilityBackendFromRow(row), nil
}

func (s *CapabilityBackendService) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteCapabilityBackend(ctx, id)
}

// ResolveEnv resolves the environment variables one workspace's capability
// module process should be spawned with. Per capability the winner is the
// workspace's own enabled row if it has one, otherwise the user's global
// default (enabled, lowest position). Each winner expands to
//
//	NN_CAP_<CAP>_BACKEND / NN_CAP_<CAP>_BASE_URL / NN_CAP_<CAP>_API_KEY
//
// (the API_KEY item is omitted when the row has no key) plus one
// NN_CAP_<CAP>_<KEY> per extra_config entry, with CAP in TTS|IMAGE|VIDEO and
// extra keys upper-cased.
func (s *CapabilityBackendService) ResolveEnv(ctx context.Context, userID, workspaceID int64, module string) (map[string]string, error) {
	rows, err := s.q.ListCapabilityBackendsForResolve(ctx, store.ListCapabilityBackendsForResolveParams{
		Module:    module,
		OwnerID:   workspaceID,
		OwnerID_2: userID,
	})
	if err != nil {
		return nil, err
	}
	winners := make(map[string]store.CapabilityBackend)
	for _, row := range rows {
		cur, ok := winners[row.Capability]
		if !ok || resolveOutranks(row, cur) {
			winners[row.Capability] = row
		}
	}
	env := make(map[string]string, len(winners)*4)
	for capability, row := range winners {
		prefix := "NN_CAP_" + capabilityEnvSegment(capability) + "_"
		env[prefix+"BACKEND"] = row.Backend
		env[prefix+"BASE_URL"] = row.BaseUrl
		if row.ApiKey != "" {
			env[prefix+"API_KEY"] = row.ApiKey
		}
		// extra_config is applied last so explicitly-configured params win over
		// the three canonical keys on a (pathological) name collision.
		for k, v := range unmarshalExtraConfig(row.ExtraConfig) {
			env[prefix+strings.ToUpper(k)] = v
		}
	}
	return env, nil
}

// resolveOutranks reports whether candidate should beat current for the same
// capability: the workspace row always beats the user global default, then
// lower position wins, then lower id (stable tie-break).
func resolveOutranks(candidate, current store.CapabilityBackend) bool {
	cRank, wRank := ownerRank(candidate.OwnerType), ownerRank(current.OwnerType)
	if cRank != wRank {
		return cRank < wRank
	}
	if candidate.Position != current.Position {
		return candidate.Position < current.Position
	}
	return candidate.ID < current.ID
}

// ownerRank orders owner scopes by resolution priority (smaller wins).
func ownerRank(ownerType string) int {
	if ownerType == "workspace" {
		return 0
	}
	return 1
}

// capabilityEnvSegment maps a capability key to its NN_CAP_* segment. The
// frozen contract names TTS|IMAGE|VIDEO; unknown capabilities fall back to an
// upper-cased, sanitized segment so a future capability still yields usable
// env names instead of a broken one.
func capabilityEnvSegment(capability string) string {
	switch strings.ToLower(capability) {
	case "tts":
		return "TTS"
	case "image":
		return "IMAGE"
	case "video":
		return "VIDEO"
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(capability) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func capabilityBackendFromRow(row store.CapabilityBackend) *CapabilityBackend {
	return &CapabilityBackend{
		ID:          row.ID,
		OwnerType:   row.OwnerType,
		OwnerID:     row.OwnerID,
		Module:      row.Module,
		Capability:  row.Capability,
		Backend:     row.Backend,
		Name:        row.Name,
		BaseURL:     row.BaseUrl,
		APIKey:      row.ApiKey,
		ExtraConfig: unmarshalExtraConfig(row.ExtraConfig),
		Enabled:     row.Enabled != 0,
		Position:    int(row.Position),
	}
}

func capabilityBackendsFromRows(rows []store.CapabilityBackend) []CapabilityBackend {
	out := make([]CapabilityBackend, len(rows))
	for i, row := range rows {
		out[i] = *capabilityBackendFromRow(row)
	}
	return out
}

// marshalExtraConfig renders the map as the JSON object stored in
// extra_config; nil / empty becomes "{}" so the column never holds "".
func marshalExtraConfig(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// unmarshalExtraConfig decodes extra_config; malformed / empty input yields an
// empty (non-nil) map so callers can range over it unconditionally.
func unmarshalExtraConfig(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
