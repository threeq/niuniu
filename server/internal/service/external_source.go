// Package service — ExternalSourceService manages per-project bindings to
// external issue trackers (GitHub repos today; TAPD workspaces next).
//
// The legacy SPA-driven "Browse upstream issues" drawer was removed when
// the integration model shifted to an AI-driven proxy: the agent now
// browses upstream issues via /mcp/external-proxy/call on demand instead
// of the server caching list-issues responses for an SPA drawer. The
// remaining surface is just CRUD on the bindings themselves.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ExternalSourceService is the service-layer entry point for project ↔
// upstream-tracker bindings. Construction takes the raw *sql.DB so callers
// don't need to know about *store.DB; the wrap happens inside.
type ExternalSourceService struct {
	q     *store.Queries
	db    *store.DB
	creds *ExternalCredentialService
	reg   *integration.Registry
	authz *Authz
}

// NewExternalSourceService wires the dependencies.
func NewExternalSourceService(q *store.Queries, rawDB *sql.DB, creds *ExternalCredentialService, reg *integration.Registry, authz *Authz) *ExternalSourceService {
	return &ExternalSourceService{
		q:     q,
		db:    store.Wrap(rawDB),
		creds: creds,
		reg:   reg,
		authz: authz,
	}
}

// ExternalSourceDTO is the redacted-config shape returned by Add / List.
// Config is the parsed JSON map (e.g. {"default_state": "open"}); we
// surface it as-is because it never carries secrets — provider tokens
// live in external_provider_credentials, not here.
type ExternalSourceDTO struct {
	ID        int64                    `json:"id"`
	ProjectID int64                    `json:"project_id"`
	Provider  integration.ProviderName `json:"provider"`
	SourceKey string                   `json:"source_key"`
	Config    map[string]any           `json:"config"`
}

// Add upserts a (project, provider, source_key) binding. The sqlc query
// is ON CONFLICT DO UPDATE so re-Adding with new config rewrites in place
// — the SPA settings page treats Add and Update as one button.
//
// credentialID is mandatory: every source binding must reference a
// credential. The service cross-checks that the credential belongs to the
// caller and its provider matches the source provider.
func (s *ExternalSourceService) Add(ctx context.Context, projectID int64, provider integration.ProviderName, sourceKey string, credentialID int64, config map[string]any, callerUserID int64) (*ExternalSourceDTO, error) {
	if config == nil {
		config = map[string]any{}
	}

	// Cross-check: the credential must exist, its provider must match, and its
	// ownership must make it bindable into this project.
	projOwner, err := s.authz.CanAccessProject(ctx, callerUserID, projectID)
	if err != nil {
		return nil, err
	}
	// load credential identity (no decrypt needed) to validate ownership.
	meta, err := s.creds.GetMetaByID(ctx, credentialID)
	if err != nil {
		return nil, fmt.Errorf("credential lookup: %w", err)
	}
	if meta.Provider != provider {
		return nil, ErrCredentialProviderMismatch
	}
	switch meta.OwnerType {
	case "user":
		if meta.OwnerID != callerUserID { // personal cred must be the caller's own
			return nil, ErrCredentialNotBindable
		}
	case "org":
		// org cred only bindable into a project owned by the SAME org; caller already
		// passed CanAccessProject (=> member of that org).
		if !(projOwner.Type == "org" && projOwner.ID == meta.OwnerID) {
			return nil, ErrCredentialNotBindable
		}
	default:
		return nil, ErrCredentialNotBindable
	}

	cfgBytes, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	row, err := s.q.AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
		ProjectID:    projectID,
		Provider:     string(provider),
		SourceKey:    sourceKey,
		CredentialID: sql.NullInt64{Int64: credentialID, Valid: true},
		Config:       string(cfgBytes),
	})
	if err != nil {
		return nil, err
	}
	return rowToSourceDTO(row), nil
}

// List returns every binding for the project, sorted by (provider,
// source_key) so the settings UI renders deterministically.
func (s *ExternalSourceService) List(ctx context.Context, projectID int64) ([]ExternalSourceDTO, error) {
	rows, err := s.q.ListProjectExternalSources(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]ExternalSourceDTO, len(rows))
	for i, r := range rows {
		out[i] = *rowToSourceDTO(r)
	}
	return out, nil
}

// Delete removes a binding by source row id. The sqlc query is a plain
// :exec that doesn't surface affected rows, matching the credential
// service's no-op-on-missing semantic.
func (s *ExternalSourceService) Delete(ctx context.Context, sourceID int64) error {
	return s.q.DeleteProjectExternalSource(ctx, sourceID)
}

// rowToSourceDTO unmarshals the stored JSON config and lifts the row into
// the DTO shape. Best-effort unmarshal: a malformed config (legacy row,
// hand-edited DB, etc.) yields an empty map rather than failing the whole
// list call.
func rowToSourceDTO(r store.ProjectExternalSource) *ExternalSourceDTO {
	cfg := map[string]any{}
	if r.Config != "" {
		_ = json.Unmarshal([]byte(r.Config), &cfg)
	}
	return &ExternalSourceDTO{
		ID:        r.ID,
		ProjectID: r.ProjectID,
		Provider:  integration.ProviderName(r.Provider),
		SourceKey: r.SourceKey,
		Config:    cfg,
	}
}

