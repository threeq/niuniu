// Package service — ExternalCredentialService manages per-(owner, user, provider, alias)
// credentials for external issue trackers (GitHub, TAPD, Jira).
//
// Storage shape: external_provider_credentials.config holds an AES-GCM
// ciphertext (base64 keyVersion|nonce|ct|tag) of the JSON-encoded
// RawConfig map. The crypto.Keyring encapsulates the AES key — neither
// handlers nor downstream callers ever see plaintext outside GetByID
// invocations.
//
// The service deliberately exposes a redacted Config from List so the
// /api/me/external-credentials list endpoint can render metadata without
// leaking the underlying token. GetByID returns the decrypted token because
// it is only invoked by other service-layer code that needs to forward it
// through the external-proxy (ImportService, WritebackWorker).
//
// Since 2026-05-17 credentials are addressed by id rather than (owner, user,
// provider). Each credential carries an alias so users can maintain multiple
// credentials per provider (e.g. "Personal GitHub" and "Work GitHub").
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/store"
)

var (
	ErrNoCredential               = errors.New("no provider credential configured")
	ErrAliasInvalid               = errors.New("alias must be 1-40 non-whitespace characters")
	ErrAliasDuplicated            = errors.New("alias already exists for this provider")
	ErrCredentialProviderMismatch = errors.New("credential provider does not match source provider")
	ErrCredentialNotBindable      = errors.New("credential not bindable to this project")
)

// ErrCredentialInUse is returned by DeleteByID when the credential is still
// referenced by one or more project_external_sources rows.
type ErrCredentialInUse struct {
	Sources []store.ListSourcesUsingCredentialRow
}

func (e *ErrCredentialInUse) Error() string {
	return fmt.Sprintf("credential is in use by %d source(s)", len(e.Sources))
}

// ExternalCredentialService is the service-layer entry point for
// credential CRUD. It holds a wrapped *store.DB so any future raw-SQL
// helper inherits the driver-aware placeholder rewrite, plus the keyring
// (encryption) and the integration registry (used by VerifyByID to dispatch
// to the right provider adapter).
type ExternalCredentialService struct {
	q       *store.Queries
	db      *store.DB
	keyring *crypto.Keyring
	reg     *integration.Registry
	// onChange, when set, is invoked (async) after a credential is updated or
	// deleted, so dependents (e.g. the scene projector) can refresh anything
	// that snapshotted the secret — notably office-mail's per-workspace
	// config.toml. Wired by server.New to SceneProjector.ReprojectImapWorkspaces.
	onChange func(ctx context.Context, owner OwnerRef, userID int64)
}

// SetChangeHook wires a best-effort callback fired after UpdateConfig/DeleteByID.
// Optional; nil disables it.
func (s *ExternalCredentialService) SetChangeHook(fn func(ctx context.Context, owner OwnerRef, userID int64)) {
	s.onChange = fn
}

// fireChange invokes the change hook in the background (request ctx may be
// cancelled once the response is sent).
func (s *ExternalCredentialService) fireChange(ownerType string, ownerID, userID int64) {
	if s.onChange == nil {
		return
	}
	go s.onChange(context.Background(), OwnerRef{Type: ownerType, ID: ownerID}, userID)
}

func NewExternalCredentialService(q *store.Queries, rawDB *sql.DB, kr *crypto.Keyring, reg *integration.Registry) *ExternalCredentialService {
	return &ExternalCredentialService{q: q, db: store.Wrap(rawDB), keyring: kr, reg: reg}
}

// ExternalCredentialUpsertInput is what handlers pass to Create. RawConfig
// is the plaintext provider config; it gets JSON-marshalled and encrypted
// before reaching the store.
type ExternalCredentialUpsertInput struct {
	OwnerType string
	OwnerID   int64
	UserID    int64
	Provider  integration.ProviderName
	Alias     string
	RawConfig map[string]any
}

// ExternalCredentialDecoded is the in-process representation of a
// credential row. Config is decrypted plaintext when returned from GetByID;
// it is intentionally redacted (empty map) when returned from List so
// the API surface never leaks tokens through the list endpoint.
type ExternalCredentialDecoded struct {
	ID             int64
	OwnerType      string
	OwnerID        int64
	UserID         int64
	Provider       integration.ProviderName
	Alias          string
	Config         map[string]any
	LastVerifiedAt sql.NullTime
}

// Create encrypts the RawConfig and inserts a new credential row.
// Returns ErrAliasInvalid for bad aliases and ErrAliasDuplicated on
// unique-constraint violations.
func (s *ExternalCredentialService) Create(ctx context.Context, in ExternalCredentialUpsertInput) (*ExternalCredentialDecoded, error) {
	if err := validateOwnerType(in.OwnerType); err != nil {
		return nil, err
	}
	if err := validateAlias(in.Alias); err != nil {
		return nil, err
	}
	plaintext, err := json.Marshal(in.RawConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	ct, err := s.keyring.Encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt config: %w", err)
	}
	row, err := s.q.CreateExternalCredential(ctx, store.CreateExternalCredentialParams{
		OwnerType:      in.OwnerType,
		OwnerID:        in.OwnerID,
		UserID:         in.UserID,
		Provider:       string(in.Provider),
		Alias:          strings.TrimSpace(in.Alias),
		Config:         string(ct),
		LastVerifiedAt: sql.NullTime{},
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAliasDuplicated
		}
		return nil, err
	}
	return rowToDecoded(row, in.RawConfig), nil
}

// Rename changes only the alias field. last_verified_at is intentionally left
// untouched because the token has not changed.
func (s *ExternalCredentialService) Rename(ctx context.Context, id int64, ownerType string, ownerID, userID int64, newAlias string) (*ExternalCredentialDecoded, error) {
	if err := validateAlias(newAlias); err != nil {
		return nil, err
	}
	row, err := s.q.RenameExternalCredential(ctx, store.RenameExternalCredentialParams{
		Alias:     strings.TrimSpace(newAlias),
		ID:        id,
		OwnerType: ownerType,
		OwnerID:   ownerID,
		UserID:    userID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoCredential
	}
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAliasDuplicated
		}
		return nil, err
	}
	return rowToDecoded(row, nil), nil
}

// UpdateConfig replaces the encrypted token/config payload and resets
// last_verified_at so the SPA shows "unverified" until re-verified.
func (s *ExternalCredentialService) UpdateConfig(ctx context.Context, id int64, ownerType string, ownerID, userID int64, rawConfig map[string]any) (*ExternalCredentialDecoded, error) {
	plaintext, err := json.Marshal(rawConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	ct, err := s.keyring.Encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt config: %w", err)
	}
	row, err := s.q.UpdateExternalCredentialConfig(ctx, store.UpdateExternalCredentialConfigParams{
		Config:    string(ct),
		ID:        id,
		OwnerType: ownerType,
		OwnerID:   ownerID,
		UserID:    userID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoCredential
	}
	if err != nil {
		return nil, err
	}
	// Password/host may have changed — refresh any per-workspace snapshot.
	s.fireChange(ownerType, ownerID, userID)
	return rowToDecoded(row, rawConfig), nil
}

// GetByID loads and decrypts a credential by its numeric id. Returns
// ErrNoCredential when no row matches (id not found or ownership mismatch).
func (s *ExternalCredentialService) GetByID(ctx context.Context, id int64, ownerType string, ownerID, userID int64) (*ExternalCredentialDecoded, error) {
	row, err := s.q.GetExternalCredentialByID(ctx, store.GetExternalCredentialByIDParams{
		ID:        id,
		OwnerType: ownerType,
		OwnerID:   ownerID,
		UserID:    userID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoCredential
	}
	if err != nil {
		return nil, err
	}
	pt, err := s.keyring.Decrypt([]byte(row.Config))
	if err != nil {
		return nil, fmt.Errorf("decrypt config: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(pt, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return rowToDecoded(row, cfg), nil
}

// GetBoundByID loads a credential referenced by a project_external_sources row.
// Callers must perform project access checks before invoking this method. Source
// bindings store the credential id chosen by the user who created the binding;
// using the bound id keeps org-owned projects working with personal credentials
// while Add still verifies the selected credential belongs to the caller.
//
// NOTE: one-to-many (ignores user_id) — proxy-injection path ONLY. NEVER call
// from the scene-materialization path (would leak the binder's secret to other
// members). See spec 2026-06-27 规约3.
func (s *ExternalCredentialService) GetBoundByID(ctx context.Context, id int64) (*ExternalCredentialDecoded, error) {
	var row store.ExternalProviderCredential
	err := s.db.QueryRowContext(ctx, `
		SELECT id, owner_type, owner_id, user_id, provider, alias, config, last_verified_at, created_at, updated_at
		FROM external_provider_credentials
		WHERE id = ?`, id).Scan(
		&row.ID,
		&row.OwnerType,
		&row.OwnerID,
		&row.UserID,
		&row.Provider,
		&row.Alias,
		&row.Config,
		&row.LastVerifiedAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoCredential
	}
	if err != nil {
		return nil, err
	}
	pt, err := s.keyring.Decrypt([]byte(row.Config))
	if err != nil {
		return nil, fmt.Errorf("decrypt config: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(pt, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return rowToDecoded(row, cfg), nil
}

// GetDecryptedConfigByAlias returns the decrypted config map for the
// (owner, user, provider, alias) credential. Unlike GetByID it addresses the
// credential by its scene-facing alias rather than a numeric id, so the scene
// projector can resolve ${cred:<alias>.<field>} placeholders without knowing
// the credential row id.
//
// The user_id dimension is mandatory (spec §4.3): an org-shared mailbox has one
// (org, user_id) row per member, so the caller passes the workspace's
// created_by to decrypt "the binding made by whoever owns this workspace".
// Returns ErrNoCredential when no row matches the full (owner, user, provider,
// alias) tuple — never silently widens the scope.
func (s *ExternalCredentialService) GetDecryptedConfigByAlias(
	ctx context.Context, owner OwnerRef, userID int64,
	provider integration.ProviderName, alias string,
) (map[string]any, error) {
	var ct string
	err := s.db.QueryRowContext(ctx, `
		SELECT config FROM external_provider_credentials
		WHERE owner_type = ? AND owner_id = ? AND user_id = ? AND provider = ? AND alias = ?`,
		owner.Type, owner.ID, userID, string(provider), strings.TrimSpace(alias)).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoCredential
	}
	if err != nil {
		return nil, err
	}
	pt, err := s.keyring.Decrypt([]byte(ct))
	if err != nil {
		return nil, fmt.Errorf("decrypt config: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(pt, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return cfg, nil
}

// VerifyImapByID loads+decrypts an imap credential and performs a bind-time
// IMAP LOGIN probe (spec §11: immediate feedback for non-technical users).
// Returns *ErrIMAPAuth on credential rejection, ErrNoCredential when not found,
// ErrCredentialProviderMismatch if the credential isn't imap, or a connect/IO
// error. On success it touches last_verified_at. Does NOT block Create — the
// SPA calls this explicitly after binding (deferred-verify pattern).
func (s *ExternalCredentialService) VerifyImapByID(ctx context.Context, id int64, ownerType string, ownerID, userID int64) error {
	cred, err := s.GetByID(ctx, id, ownerType, ownerID, userID)
	if err != nil {
		return err
	}
	if cred.Provider != integration.ProviderName(imapProviderName) {
		return ErrCredentialProviderMismatch
	}
	host := stringFromConfig(cred.Config, "imap_host")
	user := stringFromConfig(cred.Config, "username")
	pass := stringFromConfig(cred.Config, "password")
	if host == "" || user == "" || pass == "" {
		return &ErrIMAPAuth{Detail: "credential missing host/username/password"}
	}
	port := 0
	if p, ok := cred.Config["imap_port"]; ok {
		port = credInt(p)
	}
	if err := VerifyImapLogin(ctx, host, port, stringFromConfig(cred.Config, "security"), user, pass); err != nil {
		return err
	}
	_ = s.q.TouchCredentialVerifiedAtByID(ctx, store.TouchCredentialVerifiedAtByIDParams{
		ID: id, OwnerType: ownerType, OwnerID: ownerID, UserID: userID,
	})
	return nil
}

func stringFromConfig(cfg map[string]any, key string) string {
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return ""
}

// List returns metadata for all credentials in the (owner_type, owner_id,
// user_id) scope. Config is intentionally redacted to an empty map.
func (s *ExternalCredentialService) List(ctx context.Context, ownerType string, ownerID, userID int64) ([]ExternalCredentialDecoded, error) {
	rows, err := s.q.ListExternalCredentialsForUser(ctx, store.ListExternalCredentialsForUserParams{
		OwnerType: ownerType,
		OwnerID:   ownerID,
		UserID:    userID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ExternalCredentialDecoded, 0, len(rows))
	for _, r := range rows {
		out = append(out, ExternalCredentialDecoded{
			ID:             r.ID,
			OwnerType:      r.OwnerType,
			OwnerID:        r.OwnerID,
			UserID:         r.UserID,
			Provider:       integration.ProviderName(r.Provider),
			Alias:          r.Alias,
			Config:         map[string]any{}, // redacted
			LastVerifiedAt: r.LastVerifiedAt,
		})
	}
	return out, nil
}

// ListByProvider returns redacted credential metadata filtered by provider.
// Used by the source-binding picker in the SPA.
func (s *ExternalCredentialService) ListByProvider(ctx context.Context, ownerType string, ownerID, userID int64, provider integration.ProviderName) ([]ExternalCredentialDecoded, error) {
	rows, err := s.q.ListExternalCredentialsForUserByProvider(ctx, store.ListExternalCredentialsForUserByProviderParams{
		OwnerType: ownerType,
		OwnerID:   ownerID,
		UserID:    userID,
		Provider:  string(provider),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ExternalCredentialDecoded, 0, len(rows))
	for _, r := range rows {
		out = append(out, ExternalCredentialDecoded{
			ID:             r.ID,
			OwnerType:      r.OwnerType,
			OwnerID:        r.OwnerID,
			UserID:         r.UserID,
			Provider:       integration.ProviderName(r.Provider),
			Alias:          r.Alias,
			Config:         map[string]any{}, // redacted
			LastVerifiedAt: r.LastVerifiedAt,
		})
	}
	return out, nil
}

// ListForOwner returns redacted metadata for ALL credentials owned by
// (ownerType, ownerID), regardless of which member created them — for org
// (team) credentials where one credential serves the whole org. Unlike List,
// user_id is intentionally NOT a filter. SECURITY (spec 2026-06-27 规约3): this
// is a one-to-many listing for the PROXY/management surface only; the scene
// materialization path must keep using the per-(owner,user_id) lookups.
//
// provider == "" means "do not filter by provider".
func (s *ExternalCredentialService) ListForOwner(ctx context.Context, ownerType string, ownerID int64, provider integration.ProviderName) ([]ExternalCredentialDecoded, error) {
	query := `
		SELECT id, owner_type, owner_id, user_id, provider, alias, last_verified_at
		FROM external_provider_credentials
		WHERE owner_type = ? AND owner_id = ?`
	args := []any{ownerType, ownerID}
	if provider != "" {
		query += ` AND provider = ?`
		args = append(args, string(provider))
	}
	query += ` ORDER BY alias`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ExternalCredentialDecoded, 0)
	for rows.Next() {
		var r ExternalCredentialDecoded
		var prov string
		if err := rows.Scan(&r.ID, &r.OwnerType, &r.OwnerID, &r.UserID, &prov, &r.Alias, &r.LastVerifiedAt); err != nil {
			return nil, err
		}
		r.Provider = integration.ProviderName(prov)
		r.Config = map[string]any{} // redacted
		out = append(out, r)
	}
	return out, rows.Err()
}

// CredentialMeta is the lightweight, non-decrypted identity of a credential row.
// Used by binding validation (external_source.Add) which only needs ownership +
// provider, not the secret config.
type CredentialMeta struct {
	ID        int64
	OwnerType string
	OwnerID   int64
	UserID    int64
	Provider  integration.ProviderName
	Alias     string
}

// GetMetaByID loads a credential's identity by id WITHOUT decrypting its config.
// Returns ErrNoCredential when no row matches. Unlike GetByID it performs no
// ownership filtering — the caller (binding validation) decides what ownership
// is acceptable based on the returned OwnerType/OwnerID.
func (s *ExternalCredentialService) GetMetaByID(ctx context.Context, id int64) (*CredentialMeta, error) {
	var m CredentialMeta
	var prov string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, owner_type, owner_id, user_id, provider, alias
		FROM external_provider_credentials
		WHERE id = ?`, id).Scan(&m.ID, &m.OwnerType, &m.OwnerID, &m.UserID, &prov, &m.Alias)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoCredential
	}
	if err != nil {
		return nil, err
	}
	m.Provider = integration.ProviderName(prov)
	return &m, nil
}

// DecryptedCredential pairs a credential's identity with its decrypted config.
type DecryptedCredential struct {
	ID     int64
	Alias  string
	Config map[string]any
}

// ListDecryptedForProject returns the decrypted configs of the project's bound
// external sources for the given provider that were bound by userID (the
// credential's user_id), in ONE query + per-row decrypt. Scoping to userID
// matters for ORG projects: office-mail materializes the credential as PLAINTEXT
// into the workspace's config.toml, so a member must only ever get the mailboxes
// THEY bound — never another member's password. For a personal project the
// binder is always the owner, so this is a no-op narrowing. Bindings without a
// credential (NULL credential_id) are excluded by the join. Corrupt rows are
// skipped (logged). Ordered by alias for stable output.
func (s *ExternalCredentialService) ListDecryptedForProject(
	ctx context.Context, projectID, userID int64, provider integration.ProviderName,
) ([]DecryptedCredential, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.alias, c.config
		FROM project_external_sources pes
		JOIN external_provider_credentials c ON c.id = pes.credential_id
		WHERE pes.project_id = ? AND pes.provider = ? AND c.user_id = ?
		ORDER BY c.alias`,
		projectID, string(provider), userID)
	if err != nil {
		return nil, err
	}
	return s.scanDecryptedCredentials(rows)
}

// scanDecryptedCredentials drains rows shaped (id, alias, config-ciphertext),
// decrypting + unmarshaling each config. Corrupt rows are logged and skipped.
func (s *ExternalCredentialService) scanDecryptedCredentials(rows *sql.Rows) ([]DecryptedCredential, error) {
	defer rows.Close()
	var out []DecryptedCredential
	for rows.Next() {
		var id int64
		var alias, ct string
		if err := rows.Scan(&id, &alias, &ct); err != nil {
			return nil, err
		}
		pt, err := s.keyring.Decrypt([]byte(ct))
		if err != nil {
			slog.Warn("external credential: decrypt failed, skipping", "alias", alias, "err", err)
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal(pt, &cfg); err != nil {
			slog.Warn("external credential: unmarshal failed, skipping", "alias", alias, "err", err)
			continue
		}
		out = append(out, DecryptedCredential{ID: id, Alias: alias, Config: cfg})
	}
	return out, rows.Err()
}

// ListUsages returns the project_external_sources rows that reference
// the credential — the same set DeleteByID checks before refusing. Used
// by the SPA to render which projects + source_keys are blocking a
// would-be delete (and to surface a per-binding unbind action).
func (s *ExternalCredentialService) ListUsages(ctx context.Context, id int64) ([]store.ListSourcesUsingCredentialRow, error) {
	return s.q.ListSourcesUsingCredential(ctx, sql.NullInt64{Int64: id, Valid: true})
}

// DeleteByID drops the credential row. Returns ErrCredentialInUse when the
// credential is referenced by one or more project_external_sources rows.
func (s *ExternalCredentialService) DeleteByID(ctx context.Context, id int64, ownerType string, ownerID, userID int64) error {
	cnt, err := s.q.CountSourcesUsingCredential(ctx, sql.NullInt64{Int64: id, Valid: true})
	if err != nil {
		return err
	}
	if cnt > 0 {
		sources, _ := s.q.ListSourcesUsingCredential(ctx, sql.NullInt64{Int64: id, Valid: true})
		return &ErrCredentialInUse{Sources: sources}
	}
	if err := s.q.DeleteExternalCredentialByID(ctx, store.DeleteExternalCredentialByIDParams{
		ID:        id,
		OwnerType: ownerType,
		OwnerID:   ownerID,
		UserID:    userID,
	}); err != nil {
		return err
	}
	// Credential gone — drop it from any per-workspace snapshot.
	s.fireChange(ownerType, ownerID, userID)
	return nil
}

func validateAlias(s string) error {
	trim := strings.TrimSpace(s)
	if trim == "" {
		return ErrAliasInvalid
	}
	if len([]rune(trim)) > 40 {
		return ErrAliasInvalid
	}
	return nil
}

func rowToDecoded(row store.ExternalProviderCredential, plain map[string]any) *ExternalCredentialDecoded {
	return &ExternalCredentialDecoded{
		ID:             row.ID,
		OwnerType:      row.OwnerType,
		OwnerID:        row.OwnerID,
		UserID:         row.UserID,
		Provider:       integration.ProviderName(row.Provider),
		Alias:          row.Alias,
		Config:         plain,
		LastVerifiedAt: row.LastVerifiedAt,
	}
}

func validateOwnerType(t string) error {
	if t != "user" && t != "org" {
		return fmt.Errorf("invalid owner_type: %q (must be 'user' or 'org')", t)
	}
	return nil
}
