// Package service — DataSourceService manages owner-scoped external data
// source definitions for the data-integration subsystem (spec
// docs/superpowers/specs/2026-06-04-data-integration-and-dashboard-design.md).
//
// Storage shape: data_sources.config holds an AES-GCM ciphertext (base64
// keyVersion|nonce|ct|tag) of the JSON-encoded connection params
// (host/port/user/password/database/options). The crypto.Keyring (the same
// instance the external-credential subsystem uses) encapsulates the AES key;
// plaintext connection params never escape the service layer. scope_config is
// stored as plaintext JSON because it carries no secrets (allowed databases /
// table allow+deny lists).
//
// All read/write methods take (userID, orgIDs) so callers filter by the
// caller's accessible owner set: a row is visible when it is the caller's
// personal row (owner_type='user', owner_id=userID) or an org row the caller
// belongs to (owner_type='org', owner_id IN orgIDs). LoadConnConfig returns a
// dataconn.ConnConfig and is intentionally service-only (handlers never see it).
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/store"
)

var (
	// ErrDataSourceNotFound is returned when no row matches the id within the
	// caller's accessible owner set (used for both not-found and authz miss so
	// callers cannot probe existence of rows they cannot see).
	ErrDataSourceNotFound = errors.New("data source not found")
	// ErrDataSourceNameTaken is returned on the per-owner UNIQUE(name) clash.
	ErrDataSourceNameTaken = errors.New("data source name already exists for this owner")
	// ErrWorkspaceNoProject is returned by CreateForWorkspace when the workspace
	// has no linked project: data-source visibility is project-scoped, so a
	// source created without a project could never be queried by the agent.
	ErrWorkspaceNoProject = errors.New("workspace has no linked project; cannot create a data source for it")
)

// ErrDataSourceKindUnsupported wraps dataconn.ErrUnsupported so handlers can map
// it to 422 while still matching errors.Is(err, dataconn.ErrUnsupported).
type ErrDataSourceKindUnsupported struct{ Kind string }

func (e *ErrDataSourceKindUnsupported) Error() string {
	return fmt.Sprintf("data source kind %q is not supported", e.Kind)
}
func (e *ErrDataSourceKindUnsupported) Unwrap() error { return dataconn.ErrUnsupported }

// DataSourceService is the service-layer entry point for data-source CRUD +
// verify. It holds a wrapped *store.DB (driver-aware placeholder rewrite), the
// sqlc queries, the keyring (config encryption) and the connector registry
// (Verify dispatches Ping to the right connector).
type DataSourceService struct {
	q        *store.Queries
	db       *store.DB
	keyring  *crypto.Keyring
	registry *dataconn.Registry
}

func NewDataSourceService(q *store.Queries, rawDB *sql.DB, kr *crypto.Keyring, reg *dataconn.Registry) *DataSourceService {
	return &DataSourceService{q: q, db: store.Wrap(rawDB), keyring: kr, registry: reg}
}

// CreateDataSourceInput is what handlers pass to Create. Config is the
// plaintext connection params; it gets JSON-marshalled and AES-GCM encrypted
// before reaching the store. ScopeConfig is the (non-secret) data-scope policy.
type CreateDataSourceInput struct {
	OwnerType         string
	OwnerID           int64
	UserID            int64
	Name              string
	Kind              string
	Config            map[string]any
	ScopeConfig       map[string]any
	DefaultAccessMode string
	RequireConfirm    string
}

// DataSourceDTO is the in-process / API representation of a data source row.
// Config is REDACTED in list/get results (a non-sensitive subset: kind/host/
// port/database) so the API surface never leaks credentials. ScopeConfig is
// the parsed plaintext policy. Decrypted connection params are only reachable
// via LoadConnConfig (service-only).
type DataSourceDTO struct {
	ID                int64          `json:"id"`
	OwnerType         string         `json:"owner_type"`
	OwnerID           int64          `json:"owner_id"`
	UserID            int64          `json:"user_id"`
	Name              string         `json:"name"`
	Kind              string         `json:"kind"`
	Config            map[string]any `json:"config"` // redacted (no password)
	ScopeConfig       map[string]any `json:"scope_config"`
	DefaultAccessMode string         `json:"default_access_mode"`
	RequireConfirm    string         `json:"require_confirm"`
	LastVerifiedAt    sql.NullTime   `json:"last_verified_at"`
	// Bindings is populated on the settings list/get path (ListForOwner); it is
	// left nil on the proxy path where only id/name/kind matter.
	Bindings []DataSourceBinding `json:"bindings"`
}

func (s *DataSourceService) isSupportedKind(kind string) bool {
	_, err := s.registry.Get(dataconn.SourceKind(kind))
	return err == nil
}

func normalizeAccessMode(m string) string {
	if m == "readwrite" {
		return "readwrite"
	}
	return "read"
}

func normalizeRequireConfirm(c string) string {
	switch c {
	case "always", "never", "writes_only":
		return c
	default:
		return "writes_only"
	}
}

// Create validates the kind (M1: mysql/postgres only), encrypts the config,
// and inserts the row. Returns *ErrDataSourceKindUnsupported (errors.Is ->
// dataconn.ErrUnsupported) for unsupported kinds and ErrDataSourceNameTaken on
// a per-owner name clash.
func (s *DataSourceService) Create(ctx context.Context, in CreateDataSourceInput) (int64, error) {
	if err := validateOwnerType(in.OwnerType); err != nil {
		return 0, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return 0, fmt.Errorf("data source name is required")
	}
	if !s.isSupportedKind(in.Kind) {
		return 0, &ErrDataSourceKindUnsupported{Kind: in.Kind}
	}
	cfgCipher, err := s.encryptConfig(in.Config)
	if err != nil {
		return 0, err
	}
	scopeJSON, err := marshalJSONObject(in.ScopeConfig)
	if err != nil {
		return 0, fmt.Errorf("marshal scope_config: %w", err)
	}
	id, err := s.q.CreateDataSource(ctx, store.CreateDataSourceParams{
		OwnerType:         in.OwnerType,
		OwnerID:           in.OwnerID,
		UserID:            in.UserID,
		Name:              strings.TrimSpace(in.Name),
		Kind:              in.Kind,
		Config:            cfgCipher,
		ScopeConfig:       scopeJSON,
		DefaultAccessMode: normalizeAccessMode(in.DefaultAccessMode),
		RequireConfirm:    normalizeRequireConfirm(in.RequireConfirm),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrDataSourceNameTaken
		}
		return 0, err
	}
	return id, nil
}

// CreateForWorkspace creates a data source owned by the workspace's owner and
// binds it to the workspace's PROJECT so the agent can immediately query it via
// run_data_query (visibility is project-scoped). The owner fields are taken from
// the workspace row — an agent cannot create a source for another owner — and
// `userID` is recorded as the creator. Returns the redacted DTO of the created
// source. ErrWorkspaceNoProject if the workspace has no project linkage;
// otherwise the same errors as Create (ErrDataSourceKindUnsupported,
// ErrDataSourceNameTaken).
func (s *DataSourceService) CreateForWorkspace(ctx context.Context, ws store.Workspace, userID int64, in CreateDataSourceInput) (*DataSourceDTO, error) {
	pid, err := s.q.GetProjectIDForWorkspace(ctx, ws.ID)
	if err != nil || pid <= 0 {
		return nil, ErrWorkspaceNoProject
	}
	in.OwnerType = ws.OwnerType
	in.OwnerID = ws.OwnerID
	in.UserID = userID
	id, err := s.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := s.q.CreateDataSourceBinding(ctx, store.CreateDataSourceBindingParams{
		SourceID:   id,
		TargetType: "project",
		TargetID:   pid,
	}); err != nil {
		return nil, fmt.Errorf("bind data source to project: %w", err)
	}
	var orgIDs []int64
	if ws.OwnerType == "org" {
		orgIDs = []int64{ws.OwnerID}
	}
	return s.Get(ctx, id, userID, orgIDs)
}

// ListForOwner returns redacted DTOs for every data source in the caller's
// accessible owner set (personal + the supplied orgIDs). Each DTO carries its
// visibility bindings so the settings UI can prefill the binding editor.
func (s *DataSourceService) ListForOwner(ctx context.Context, userID int64, orgIDs []int64) ([]DataSourceDTO, error) {
	rows, err := s.q.ListDataSourcesForOwners(ctx, store.ListDataSourcesForOwnersParams{
		OwnerID: userID,
		OrgIds:  orgIDs,
	})
	if err != nil {
		return nil, err
	}
	out := make([]DataSourceDTO, 0, len(rows))
	for _, r := range rows {
		dto := s.rowToRedactedDTO(r)
		dto.Bindings = s.loadBindings(ctx, r.ID)
		out = append(out, dto)
	}
	return out, nil
}

// DataSourceBinding is a single visibility binding: the source is visible to
// the agent of any workspace that matches (TargetType, TargetID) — directly
// (workspace), via its project, or via an attached scene.
type DataSourceBinding struct {
	TargetType string `json:"target_type"` // workspace | project | scene
	TargetID   int64  `json:"target_id"`
}

// Project is the ONLY valid binding target. Data-source visibility mirrors
// external sources exactly: a source is exposed to an agent solely through the
// project its workspace belongs to. Workspace-direct and scene bindings are
// intentionally excluded (scenes are high-risk; per-workspace binding was
// dropped in favour of the project-scoped model).
func validateBindingTargetType(t string) bool {
	return t == "project"
}

// loadBindings returns the source's bindings (best-effort; empty on error —
// bindings are visibility metadata, not load-bearing for the redacted DTO).
func (s *DataSourceService) loadBindings(ctx context.Context, sourceID int64) []DataSourceBinding {
	rows, err := s.q.ListBindingsForSource(ctx, sourceID)
	if err != nil {
		return nil
	}
	out := make([]DataSourceBinding, 0, len(rows))
	for _, r := range rows {
		out = append(out, DataSourceBinding{TargetType: r.TargetType, TargetID: r.TargetID})
	}
	return out
}

// ListBindings returns the visibility bindings of a source the caller can
// access. ErrDataSourceNotFound on authz miss.
func (s *DataSourceService) ListBindings(ctx context.Context, id, userID int64, orgIDs []int64) ([]DataSourceBinding, error) {
	if _, err := s.getOwned(ctx, id, userID, orgIDs); err != nil {
		return nil, err
	}
	return s.loadBindings(ctx, id), nil
}

// SetBindings replaces the visibility bindings of a source the caller can
// access (delete-all then re-insert, in one tx). Invalid target types are
// rejected; duplicate (type,id) pairs in the input are de-duplicated.
func (s *DataSourceService) SetBindings(ctx context.Context, id, userID int64, orgIDs []int64, bindings []DataSourceBinding) error {
	if _, err := s.getOwned(ctx, id, userID, orgIDs); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if !validateBindingTargetType(b.TargetType) {
			return fmt.Errorf("invalid binding target type %q", b.TargetType)
		}
		if b.TargetID <= 0 {
			return fmt.Errorf("invalid binding target id %d", b.TargetID)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := tx.Queries()
	if err := q.DeleteBindingsForSource(ctx, id); err != nil {
		return err
	}
	for _, b := range bindings {
		key := b.TargetType + ":" + strconv.FormatInt(b.TargetID, 10)
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := q.CreateDataSourceBinding(ctx, store.CreateDataSourceBindingParams{
			SourceID:   id,
			TargetType: b.TargetType,
			TargetID:   b.TargetID,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListForProject returns redacted DTOs for the data sources bound to the given
// project (target_type='project'), filtered to the caller's accessible owner
// set. Backs the project-settings data-source association panel.
func (s *DataSourceService) ListForProject(ctx context.Context, projectID, userID int64, orgIDs []int64) ([]DataSourceDTO, error) {
	ids, err := s.q.ListSourceIDsBoundToTarget(ctx, store.ListSourceIDsBoundToTargetParams{
		TargetType: "project",
		TargetID:   projectID,
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]DataSourceDTO, 0, len(ids))
	for _, id := range ids {
		row, err := s.q.GetDataSource(ctx, id)
		if err != nil {
			continue
		}
		if !ownerVisible(row.OwnerType, row.OwnerID, userID, orgIDs) {
			continue
		}
		out = append(out, s.rowToRedactedDTO(row))
	}
	return out, nil
}

// AddBinding adds a single visibility binding to a source the caller can
// access. Idempotent: a duplicate (type,id) is a no-op. Used by the
// project-settings panel to associate an existing source without touching its
// other bindings.
func (s *DataSourceService) AddBinding(ctx context.Context, id, userID int64, orgIDs []int64, b DataSourceBinding) error {
	if _, err := s.getOwned(ctx, id, userID, orgIDs); err != nil {
		return err
	}
	if !validateBindingTargetType(b.TargetType) || b.TargetID <= 0 {
		return fmt.Errorf("invalid binding %q/%d", b.TargetType, b.TargetID)
	}
	n, err := s.q.CountDataSourceBinding(ctx, store.CountDataSourceBindingParams{
		SourceID: id, TargetType: b.TargetType, TargetID: b.TargetID,
	})
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return s.q.CreateDataSourceBinding(ctx, store.CreateDataSourceBindingParams{
		SourceID: id, TargetType: b.TargetType, TargetID: b.TargetID,
	})
}

// RemoveBinding removes a single visibility binding from a source the caller
// can access. A missing binding is a no-op.
func (s *DataSourceService) RemoveBinding(ctx context.Context, id, userID int64, orgIDs []int64, b DataSourceBinding) error {
	if _, err := s.getOwned(ctx, id, userID, orgIDs); err != nil {
		return err
	}
	return s.q.DeleteDataSourceBinding(ctx, store.DeleteDataSourceBindingParams{
		SourceID: id, TargetType: b.TargetType, TargetID: b.TargetID,
	})
}

// ListForWorkspace returns redacted DTOs for the data sources VISIBLE to the
// given workspace's agent: those bound to the workspace's PROJECT (the only
// visibility path, exactly like external sources). A source not bound to the
// project is omitted (the "not visible to all agents" rule). Owner visibility
// is re-checked as defense in depth.
func (s *DataSourceService) ListForWorkspace(ctx context.Context, ws store.Workspace, userID int64, orgIDs []int64) ([]DataSourceDTO, error) {
	ids, err := s.visibleSourceIDs(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]DataSourceDTO, 0, len(ids))
	for _, id := range ids {
		row, err := s.q.GetDataSource(ctx, id)
		if err != nil {
			continue
		}
		if !ownerVisible(row.OwnerType, row.OwnerID, userID, orgIDs) {
			continue
		}
		out = append(out, s.rowToRedactedDTO(row))
	}
	return out, nil
}

// visibleSourceIDs resolves the source ids bound to the workspace's PROJECT,
// sorted ascending for stable output. Project is the sole visibility path
// (workspace -> issue -> column -> project, 1:1). A workspace with no project
// linkage (e.g. a directory-source workspace with no issue) sees nothing.
func (s *DataSourceService) visibleSourceIDs(ctx context.Context, ws store.Workspace) ([]int64, error) {
	pid, err := s.q.GetProjectIDForWorkspace(ctx, ws.ID)
	if err != nil || pid <= 0 {
		// No project linkage -> no visible sources (not an error).
		return nil, nil
	}
	ids, err := s.q.ListSourceIDsBoundToTarget(ctx, store.ListSourceIDsBoundToTargetParams{
		TargetType: "project",
		TargetID:   pid,
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// ResolveSourceIDForWorkspace resolves a name-or-id reference to a source id
// that is BOTH owner-accessible AND visible to the workspace (bound to it, its
// project, or an attached scene). This gates the run_data_query proxy so an
// agent cannot reach an unbound source by guessing its numeric id or name.
func (s *DataSourceService) ResolveSourceIDForWorkspace(ctx context.Context, nameOrID string, ws store.Workspace, userID int64, orgIDs []int64) (int64, error) {
	ref := strings.TrimSpace(nameOrID)
	if ref == "" {
		return 0, ErrDataSourceNotFound
	}
	visible, err := s.ListForWorkspace(ctx, ws, userID, orgIDs)
	if err != nil {
		return 0, err
	}
	if id, perr := strconv.ParseInt(ref, 10, 64); perr == nil && id > 0 {
		for _, v := range visible {
			if v.ID == id {
				return id, nil
			}
		}
		return 0, ErrDataSourceNotFound
	}
	for _, v := range visible {
		if v.Name == ref {
			return v.ID, nil
		}
	}
	return 0, ErrDataSourceNotFound
}

// ResolveSourceID resolves a data source reference that is either a numeric id
// or a source name into a concrete id within the caller's accessible owner set.
// A purely numeric ref is validated (ownership-checked) via getOwned; a name is
// matched (exact) against the caller's accessible sources. Returns
// ErrDataSourceNotFound when nothing matches.
func (s *DataSourceService) ResolveSourceID(ctx context.Context, nameOrID string, userID int64, orgIDs []int64) (int64, error) {
	ref := strings.TrimSpace(nameOrID)
	if ref == "" {
		return 0, ErrDataSourceNotFound
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil && id > 0 {
		if _, err := s.getOwned(ctx, id, userID, orgIDs); err != nil {
			return 0, err
		}
		return id, nil
	}
	rows, err := s.q.ListDataSourcesForOwners(ctx, store.ListDataSourcesForOwnersParams{
		OwnerID: userID,
		OrgIds:  orgIDs,
	})
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if r.Name == ref {
			return r.ID, nil
		}
	}
	return 0, ErrDataSourceNotFound
}

// Get returns a single redacted DTO if the row is in the caller's accessible
// owner set, else ErrDataSourceNotFound.
func (s *DataSourceService) Get(ctx context.Context, id, userID int64, orgIDs []int64) (*DataSourceDTO, error) {
	row, err := s.getOwned(ctx, id, userID, orgIDs)
	if err != nil {
		return nil, err
	}
	dto := s.rowToRedactedDTO(row)
	return &dto, nil
}

// UpdateDataSourceInput carries the patchable fields. Config is optional: when
// nil the existing encrypted config is preserved (a rename / scope change must
// not require re-entering the password).
type UpdateDataSourceInput struct {
	Name              string
	Config            map[string]any
	ScopeConfig       map[string]any
	DefaultAccessMode string
	RequireConfirm    string
}

// Update applies a full update to a row the caller can access. When in.Config
// is nil the previously stored ciphertext is kept; otherwise it is re-encrypted
// after mergeRetainedSecrets fills the secret fields the edit form cannot echo
// back (so "leave blank to keep the stored password/api key" actually holds).
func (s *DataSourceService) Update(ctx context.Context, id, userID int64, orgIDs []int64, in UpdateDataSourceInput) error {
	row, err := s.getOwned(ctx, id, userID, orgIDs)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("data source name is required")
	}
	cfgCipher := row.Config
	if in.Config != nil {
		cfgCipher, err = s.encryptConfig(s.mergeRetainedSecrets(row, in.Config))
		if err != nil {
			return err
		}
	}
	scopeJSON, err := marshalJSONObject(in.ScopeConfig)
	if err != nil {
		return fmt.Errorf("marshal scope_config: %w", err)
	}
	err = s.q.UpdateDataSource(ctx, store.UpdateDataSourceParams{
		Name:              strings.TrimSpace(in.Name),
		Config:            cfgCipher,
		ScopeConfig:       scopeJSON,
		DefaultAccessMode: normalizeAccessMode(in.DefaultAccessMode),
		RequireConfirm:    normalizeRequireConfirm(in.RequireConfirm),
		ID:                id,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDataSourceNameTaken
		}
		return err
	}
	return nil
}

// mergeRetainedSecrets reconciles an edit-form config with the stored one.
// The redacted DTO never echoes secrets, so the form signals "keep what is
// stored" by leaving them blank:
//
//   - password absent/empty -> inherit the stored password;
//   - no "options" key at all -> inherit the stored options wholesale (forms
//     for kinds without option fields never send one);
//   - "options" present -> it is the authoritative set, with two exceptions:
//     options.api_key == "" inherits the stored api_key (an EMPTY value is the
//     keep marker; an ABSENT api_key drops it — that is how the ES form
//     switches from api-key to basic auth), and "scheme" is inherited when
//     absent because no form exposes it (it is API-set; an edit must not
//     silently downgrade https).
//
// A decrypt failure falls back to the incoming config unchanged (the previous
// replace semantics) — never block an update on an unreadable old blob.
func (s *DataSourceService) mergeRetainedSecrets(row store.DataSource, next map[string]any) map[string]any {
	prev, err := s.decryptConfig(row.Config)
	if err != nil {
		return next
	}
	if stringField(next, "password") == "" {
		if pw := stringField(prev, "password"); pw != "" {
			next["password"] = pw
		}
	}
	// SSH-tunnel secrets follow the same "leave blank to keep" contract; run
	// this before the options branches below, which return early for kinds
	// without an options block.
	mergeRetainedSSHSecrets(prev, next)
	prevOpts, _ := prev["options"].(map[string]any)
	nextOpts, hasOpts := next["options"].(map[string]any)
	if _, present := next["options"]; !present {
		if len(prevOpts) > 0 {
			next["options"] = prevOpts
		}
		return next
	}
	if !hasOpts || len(prevOpts) == 0 {
		return next
	}
	if ak, ok := nextOpts["api_key"].(string); ok && ak == "" {
		if pv, ok := prevOpts["api_key"]; ok {
			nextOpts["api_key"] = pv
		} else {
			delete(nextOpts, "api_key")
		}
	}
	// headers (http auth) are redacted from the DTO, so the edit form signals
	// "keep stored headers" by sending an empty (or absent) headers map.
	if h, present := nextOpts["headers"]; present {
		if m, ok := h.(map[string]any); !ok || len(m) == 0 {
			if pv, ok := prevOpts["headers"]; ok {
				nextOpts["headers"] = pv
			} else {
				delete(nextOpts, "headers")
			}
		}
	}
	if _, ok := nextOpts["scheme"]; !ok {
		if sv, ok := prevOpts["scheme"]; ok {
			nextOpts["scheme"] = sv
		}
	}
	return next
}

// mergeRetainedSSHSecrets fills the ssh block's secret fields (password,
// private_key) from the stored config when the edit form left them blank —
// mirroring the password/api_key "leave blank to keep" contract for the tunnel.
// Only applies when both configs carry an "ssh" object.
func mergeRetainedSSHSecrets(prev, next map[string]any) {
	nextSSH, ok := next["ssh"].(map[string]any)
	if !ok {
		return
	}
	prevSSH, ok := prev["ssh"].(map[string]any)
	if !ok {
		return
	}
	for _, k := range []string{"password", "private_key"} {
		if stringField(nextSSH, k) == "" {
			if v := stringField(prevSSH, k); v != "" {
				nextSSH[k] = v
			}
		}
	}
}

// Delete removes a row the caller can access. Returns ErrDataSourceNotFound
// when the row is outside the accessible set.
func (s *DataSourceService) Delete(ctx context.Context, id, userID int64, orgIDs []int64) error {
	if _, err := s.getOwned(ctx, id, userID, orgIDs); err != nil {
		return err
	}
	return s.q.DeleteDataSource(ctx, id)
}

// Verify decrypts the config, dispatches Ping to the connector for the row's
// kind, and on success bumps last_verified_at. Returns ErrDataSourceNotFound
// (authz), *ErrDataSourceKindUnsupported (unsupported kind), or the raw Ping
// error (connection failure).
func (s *DataSourceService) Verify(ctx context.Context, id, userID int64, orgIDs []int64) error {
	row, err := s.getOwned(ctx, id, userID, orgIDs)
	if err != nil {
		return err
	}
	conn, err := s.registry.Get(dataconn.SourceKind(row.Kind))
	if err != nil {
		return &ErrDataSourceKindUnsupported{Kind: row.Kind}
	}
	cc, err := s.connConfigFromRow(row)
	if err != nil {
		return err
	}
	// When the source is configured with an SSH tunnel, route the Ping through a
	// local port forward (no-op cleanup when disabled).
	cc, cleanup, err := dataconn.OpenTunnel(ctx, cc)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := conn.Ping(ctx, cc); err != nil {
		return err
	}
	return s.q.TouchDataSourceVerified(ctx, id)
}

// LoadConnConfig returns the decrypted dataconn.ConnConfig for a row the caller
// can access. Service-only: the data proxy uses this to execute queries; it is
// never reachable from a handler. Returns ErrDataSourceNotFound on authz miss.
func (s *DataSourceService) LoadConnConfig(ctx context.Context, id, userID int64, orgIDs []int64) (dataconn.ConnConfig, error) {
	row, err := s.getOwned(ctx, id, userID, orgIDs)
	if err != nil {
		return dataconn.ConnConfig{}, err
	}
	return s.connConfigFromRow(row)
}

// getOwned loads a row by id and enforces the accessible-owner-set gate.
func (s *DataSourceService) getOwned(ctx context.Context, id, userID int64, orgIDs []int64) (store.DataSource, error) {
	row, err := s.q.GetDataSource(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.DataSource{}, ErrDataSourceNotFound
	}
	if err != nil {
		return store.DataSource{}, err
	}
	if !ownerVisible(row.OwnerType, row.OwnerID, userID, orgIDs) {
		return store.DataSource{}, ErrDataSourceNotFound
	}
	return row, nil
}

// ownerVisible reports whether a (owner_type, owner_id) row is in the caller's
// accessible set: personal row of the caller, or an org row the caller belongs
// to (orgIDs).
func ownerVisible(ownerType string, ownerID, userID int64, orgIDs []int64) bool {
	switch ownerType {
	case "user":
		return ownerID == userID
	case "org":
		for _, oid := range orgIDs {
			if oid == ownerID {
				return true
			}
		}
	}
	return false
}

func (s *DataSourceService) encryptConfig(cfg map[string]any) (string, error) {
	plaintext, err := marshalJSONObject(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	ct, err := s.keyring.Encrypt([]byte(plaintext))
	if err != nil {
		return "", fmt.Errorf("encrypt config: %w", err)
	}
	return string(ct), nil
}

// decryptConfig decrypts a row's ciphertext config into a map.
func (s *DataSourceService) decryptConfig(cipher string) (map[string]any, error) {
	pt, err := s.keyring.Decrypt([]byte(cipher))
	if err != nil {
		return nil, fmt.Errorf("decrypt config: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(pt, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return cfg, nil
}

// connConfigFromRow decrypts a row and builds a dataconn.ConnConfig.
func (s *DataSourceService) connConfigFromRow(row store.DataSource) (dataconn.ConnConfig, error) {
	cfg, err := s.decryptConfig(row.Config)
	if err != nil {
		return dataconn.ConnConfig{}, err
	}
	cc := dataconn.ConnConfig{
		Kind:     dataconn.SourceKind(row.Kind),
		Host:     stringField(cfg, "host"),
		Port:     intField(cfg, "port"),
		User:     stringField(cfg, "user"),
		Password: stringField(cfg, "password"),
		Database: stringField(cfg, "database"),
	}
	if opts, ok := cfg["options"].(map[string]any); ok {
		cc.Options = opts
	}
	cc.SSH = parseSSHConfig(cfg)
	return cc, nil
}

// parseSSHConfig extracts the optional SSH-tunnel block from a decrypted config
// map. Returns nil when no "ssh" object is present. An empty auth_method is left
// for dataconn to infer from whichever credential is set.
func parseSSHConfig(cfg map[string]any) *dataconn.SSHConfig {
	raw, ok := cfg["ssh"].(map[string]any)
	if !ok {
		return nil
	}
	enabled, _ := raw["enabled"].(bool)
	return &dataconn.SSHConfig{
		Enabled:    enabled,
		Host:       stringField(raw, "host"),
		Port:       intField(raw, "port"),
		User:       stringField(raw, "user"),
		AuthMethod: dataconn.SSHAuthMethod(stringField(raw, "auth_method")),
		Password:   stringField(raw, "password"),
		PrivateKey: stringField(raw, "private_key"),
		HostKey:    stringField(raw, "host_key"),
	}
}

// rowToRedactedDTO builds a DTO whose Config holds only the non-secret subset
// — the password and options.api_key are never returned. Non-secret options
// (trino schema, mongo URI params like authSource, the ES scheme) ARE
// returned so the edit form can echo them back instead of silently dropping
// them on the next save.
func (s *DataSourceService) rowToRedactedDTO(row store.DataSource) DataSourceDTO {
	redacted := map[string]any{}
	if cfg, err := s.decryptConfig(row.Config); err == nil {
		for _, k := range []string{"host", "port", "database", "user"} {
			if v, ok := cfg[k]; ok {
				redacted[k] = v
			}
		}
		if opts, ok := cfg["options"].(map[string]any); ok && len(opts) > 0 {
			pub := make(map[string]any, len(opts))
			for k, v := range opts {
				// api_key (elasticsearch) and headers (http — may carry an
				// Authorization bearer token) are secrets: never echo them.
				if k == "api_key" || k == "headers" {
					continue
				}
				pub[k] = v
			}
			if len(pub) > 0 {
				redacted["options"] = pub
			}
		}
		// SSH tunnel block: echo the non-secret fields so the edit form can
		// prefill them; the ssh password / private_key are secrets and never
		// returned (the form re-signals "keep stored" by leaving them blank).
		if ssh, ok := cfg["ssh"].(map[string]any); ok && len(ssh) > 0 {
			pub := make(map[string]any)
			for _, k := range []string{"enabled", "host", "port", "user", "auth_method", "host_key"} {
				if v, ok := ssh[k]; ok {
					pub[k] = v
				}
			}
			// has_private_key lets the UI show "a key is stored" without leaking it.
			if pk := stringField(ssh, "private_key"); pk != "" {
				pub["has_private_key"] = true
			}
			if len(pub) > 0 {
				redacted["ssh"] = pub
			}
		}
	}
	return DataSourceDTO{
		ID:                row.ID,
		OwnerType:         row.OwnerType,
		OwnerID:           row.OwnerID,
		UserID:            row.UserID,
		Name:              row.Name,
		Kind:              row.Kind,
		Config:            redacted,
		ScopeConfig:       parseJSONObject(row.ScopeConfig),
		DefaultAccessMode: row.DefaultAccessMode,
		RequireConfirm:    row.RequireConfirm,
		LastVerifiedAt:    row.LastVerifiedAt,
	}
}

// marshalJSONObject marshals a possibly-nil map to a JSON object string
// (nil -> "{}") so the stored column is always a valid JSON object.
func marshalJSONObject(m map[string]any) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parseJSONObject parses a stored JSON object string, returning an empty map on
// any error (the column is best-effort metadata, not load-bearing for authz at
// the DTO layer).
func parseJSONObject(s string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(s) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// intField coerces a JSON number (float64) or numeric string into an int.
func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}
