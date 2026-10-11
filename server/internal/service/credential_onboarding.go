// Package service — CredentialOnboardingService generalizes the IM-Bot one-time
// credential link (see imbot_onboarding.go) to every credential-bearing
// integration: external provider credentials and data sources.
//
// Flow: an agent calls the MCP tool `request_credential_link`, which mints a
// 32-byte URL-safe token. Only its sha256 hex is persisted (15-minute expiry,
// single use); the raw token is returned exactly once inside the link. The user
// opens the public page, fills the declared fields, and submits. The server
// writes the values through the SAME encrypted stores as the settings UI
// (integration keyring AES-GCM), so no plaintext secret ever reaches the agent
// or the conversation.
//
// The token row carries only NON-SECRET descriptors: which kind of target it
// writes to, a human title, the field list the form must render, and the
// non-secret target identity (provider/alias, or data-source name/kind/scope).
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Credential onboarding kinds. A token is bound to exactly one kind, which
// decides how the submitted values are persisted.
const (
	CredentialKindProviderCredential = "provider_credential"
	CredentialKindDataSource         = "data_source"
)

// credentialOnboardingTTL is how long an issued link stays redeemable.
const credentialOnboardingTTL = 15 * time.Minute

// maxCredentialFields caps the field list so a malformed agent request cannot
// mint an unbounded form.
const maxCredentialFields = 32

// CredentialOnboardingTTL exposes the link lifetime so handlers/tests report it
// without hardcoding a second copy of the duration.
func CredentialOnboardingTTL() time.Duration { return credentialOnboardingTTL }

// ErrCredentialTokenInvalid is returned when a token is unknown, expired, or
// already used. The same opaque error covers all three so a caller cannot
// enumerate live tokens.
var ErrCredentialTokenInvalid = errors.New("credential onboarding: token invalid or expired")

// ErrCredentialLinkInvalid is returned when the link's declared kind/target/
// field list is malformed (handlers map it to HTTP 400).
var ErrCredentialLinkInvalid = errors.New("credential onboarding: invalid link definition")

// ErrCredentialSubmissionInvalid is returned when the submitted values fail
// validation for the token's kind/target (missing required field, undeclared
// field, bad type).
var ErrCredentialSubmissionInvalid = errors.New("credential onboarding: invalid submission")

// CredentialFieldSpec is one field the public submission form renders. Only
// Label/Secret/Required/Placeholder are exposed to the browser — never a value.
type CredentialFieldSpec struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Secret      bool   `json:"secret"`
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder,omitempty"`
}

// CredentialOnboardingInfo is the token-authenticated (public) descriptor the
// submission page renders from. It deliberately contains nothing secret.
type CredentialOnboardingInfo struct {
	Kind   string                `json:"kind"`
	Title  string                `json:"title"`
	Fields []CredentialFieldSpec `json:"fields"`
}

// CredentialSubmitResult carries only non-secret identifiers of whatever the
// submission created or updated.
type CredentialSubmitResult struct {
	Kind         string `json:"kind"`
	CredentialID int64  `json:"credential_id,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Alias        string `json:"alias,omitempty"`
	SourceID     int64  `json:"source_id,omitempty"`
	Name         string `json:"name,omitempty"`
}

// providerCredentialTarget is the non-secret target of a provider_credential
// link: which provider + alias the values belong to, the auth mode that shapes
// the config, and (for edits) the credential id to update.
type providerCredentialTarget struct {
	Provider     string `json:"provider"`
	Alias        string `json:"alias"`
	AuthMode     string `json:"auth_mode"`
	CredentialID int64  `json:"credential_id"`
}

// dataSourceTarget is the non-secret target of a data_source link: the source
// identity, kind, and policy. SourceID > 0 means "update that row" (blank
// fields keep the stored secret); SourceID == 0 means "create and bind".
type dataSourceTarget struct {
	SourceID          int64          `json:"source_id"`
	Name              string         `json:"name"`
	Kind              string         `json:"kind"`
	ScopeConfig       map[string]any `json:"scope_config"`
	DefaultAccessMode string         `json:"default_access_mode"`
	RequireConfirm    string         `json:"require_confirm"`
}

// CredentialOnboardingService owns the generic one-time credential link.
type CredentialOnboardingService struct {
	q   *store.Queries
	ext *ExternalCredentialService
	ds  *DataSourceService
}

func NewCredentialOnboardingService(q *store.Queries, ext *ExternalCredentialService, ds *DataSourceService) *CredentialOnboardingService {
	return &CredentialOnboardingService{q: q, ext: ext, ds: ds}
}

// credentialFieldNameRe constrains field names to the shape the catalog and the
// form renderer expect; anything else is rejected at issue time rather than
// silently rendered as an unknown field.
var credentialFieldNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// credentialFieldCatalog maps a canonical field name to its render spec. Names
// are shared with the settings UI's own field vocabulary (data-sources-panel
// config keys, AddCredentialDialog auth-mode fields) so a link's fields mean
// the same thing as the equivalent hand-built source/credential.
var credentialFieldCatalog = map[string]CredentialFieldSpec{
	// Connection identity.
	"host":         {Label: "主机 / Host", Required: true, Placeholder: "db.example.com"},
	"port":         {Label: "端口 / Port", Placeholder: "3306"},
	"user":         {Label: "用户名 / User", Required: true},
	"username":     {Label: "用户名 / User", Required: true},
	"password":     {Label: "密码 / Password", Secret: true, Required: true},
	"database":     {Label: "数据库 / Database"},
	"base_path":    {Label: "Base Path", Placeholder: "/v1"},
	"list_path":    {Label: "List Path", Placeholder: "data.list"},
	"scheme":       {Label: "协议 / Scheme", Placeholder: "https"},
	"headers":      {Label: "请求头 Headers (JSON)", Secret: true, Placeholder: `{"Authorization":"Bearer ..."}`},
	"ssh_host":     {Label: "SSH 主机"},
	"ssh_port":     {Label: "SSH 端口"},
	"ssh_user":     {Label: "SSH 用户名"},
	"ssh_password": {Label: "SSH 密码", Secret: true},
	"private_key":  {Label: "SSH 私钥", Secret: true},

	// Secrets / tokens.
	"token":         {Label: "Token", Secret: true, Required: true},
	"api_key":       {Label: "API Key", Secret: true, Required: true},
	"secret":        {Label: "Secret", Secret: true, Required: true},
	"app_id":        {Label: "App ID", Required: true},
	"app_secret":    {Label: "App Secret", Secret: true, Required: true},
	"bot_token":     {Label: "Bot Token", Secret: true, Required: true},
	"client_id":     {Label: "Client ID", Required: true},
	"client_secret": {Label: "Client Secret", Secret: true, Required: true},
	"corp_id":       {Label: "Corp ID", Required: true},
	"agent_id":      {Label: "Agent ID", Required: true},
	"aes_key":       {Label: "EncodingAESKey", Secret: true, Required: true},
	"robot_code":    {Label: "Robot Code"},
	"webhook_secret": {Label: "Webhook Secret", Secret: true},

	// IMAP / SMTP (external_credential auth_mode=imap).
	"imap_host":   {Label: "IMAP 主机", Required: true},
	"imap_port":   {Label: "IMAP 端口", Placeholder: "993"},
	"smtp_host":   {Label: "SMTP 主机"},
	"smtp_port":   {Label: "SMTP 端口", Placeholder: "465"},
	"security":    {Label: "加密方式 / Security", Placeholder: "ssl | starttls | none"},
}

// credentialFieldSpecFor resolves a declared field name to its render spec.
// Unknown names default to secret (fail-safe: never render an undeclared field
// in plaintext) with a humanized label.
func credentialFieldSpecFor(name string) CredentialFieldSpec {
	if spec, ok := credentialFieldCatalog[name]; ok {
		spec.Name = name
		return spec
	}
	return CredentialFieldSpec{Name: name, Label: humanizeFieldName(name), Secret: true}
}

func humanizeFieldName(name string) string {
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// Issue mints a one-time token for the given kind/title/fields/target. The raw
// token is returned exactly once; only its sha256 hex is stored. issuedBy is the
// user the resulting credential is attributed to (the agent's caller).
func (s *CredentialOnboardingService) Issue(ctx context.Context, projectID, issuedBy int64, kind, title string, fields []string, target map[string]any) (string, error) {
	specs, err := normalizeCredentialFields(fields)
	if err != nil {
		return "", err
	}
	targetJSON, err := s.validateTarget(kind, target)
	if err != nil {
		return "", err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "凭据录入 / Credential entry"
	}
	if len([]rune(title)) > 200 {
		return "", fmt.Errorf("%w: title too long", ErrCredentialLinkInvalid)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	rawToken := base64.RawURLEncoding.EncodeToString(buf)
	hash := sha256.Sum256([]byte(rawToken))

	fieldsJSON, err := json.Marshal(specs)
	if err != nil {
		return "", err
	}

	if _, err := s.q.CreateCredentialOnboardingToken(ctx, store.CreateCredentialOnboardingTokenParams{
		TokenHash:  hex.EncodeToString(hash[:]),
		ProjectID:  projectID,
		IssuedBy:   issuedBy,
		Kind:       kind,
		Title:      title,
		FieldsJson: string(fieldsJSON),
		TargetJson: targetJSON,
		ExpiresAt:  time.Now().UTC().Add(credentialOnboardingTTL),
	}); err != nil {
		return "", err
	}
	return rawToken, nil
}

// normalizeCredentialFields validates + de-duplicates the declared field names
// and returns their render specs (which are what gets persisted).
func normalizeCredentialFields(fields []string) ([]CredentialFieldSpec, error) {
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: at least one field is required", ErrCredentialLinkInvalid)
	}
	if len(fields) > maxCredentialFields {
		return nil, fmt.Errorf("%w: too many fields (max %d)", ErrCredentialLinkInvalid, maxCredentialFields)
	}
	seen := make(map[string]bool, len(fields))
	out := make([]CredentialFieldSpec, 0, len(fields))
	for _, raw := range fields {
		name := strings.TrimSpace(raw)
		if !credentialFieldNameRe.MatchString(name) {
			return nil, fmt.Errorf("%w: bad field name %q", ErrCredentialLinkInvalid, raw)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, credentialFieldSpecFor(name))
	}
	return out, nil
}

// validateTarget checks the kind-specific target identity and returns the JSON
// blob to persist. Secrets are never accepted here — a target that carries a
// secret-shaped key is rejected outright.
func (s *CredentialOnboardingService) validateTarget(kind string, target map[string]any) (string, error) {
	switch kind {
	case CredentialKindProviderCredential:
		var t providerCredentialTarget
		if err := decodeTarget(target, &t); err != nil {
			return "", err
		}
		if strings.TrimSpace(t.Provider) == "" {
			return "", fmt.Errorf("%w: provider is required", ErrCredentialLinkInvalid)
		}
		if strings.TrimSpace(t.Alias) == "" {
			return "", fmt.Errorf("%w: alias is required", ErrCredentialLinkInvalid)
		}
		if err := validateAlias(t.Alias); err != nil {
			return "", fmt.Errorf("%w: %s", ErrCredentialLinkInvalid, err.Error())
		}
		if t.CredentialID < 0 {
			return "", fmt.Errorf("%w: bad credential_id", ErrCredentialLinkInvalid)
		}
		return mustJSON(t), nil
	case CredentialKindDataSource:
		var t dataSourceTarget
		if err := decodeTarget(target, &t); err != nil {
			return "", err
		}
		if t.SourceID < 0 {
			return "", fmt.Errorf("%w: bad source_id", ErrCredentialLinkInvalid)
		}
		// A create (no source_id) must declare the name + kind; an update
		// inherits both from the stored row.
		if t.SourceID == 0 {
			if strings.TrimSpace(t.Name) == "" {
				return "", fmt.Errorf("%w: name is required when creating a data source", ErrCredentialLinkInvalid)
			}
			if strings.TrimSpace(t.Kind) == "" {
				return "", fmt.Errorf("%w: kind is required when creating a data source", ErrCredentialLinkInvalid)
			}
			if !s.ds.isSupportedKind(t.Kind) {
				return "", fmt.Errorf("%w: unsupported data source kind %q", ErrCredentialLinkInvalid, t.Kind)
			}
		}
		return mustJSON(t), nil
	default:
		return "", fmt.Errorf("%w: kind must be %s|%s", ErrCredentialLinkInvalid, CredentialKindProviderCredential, CredentialKindDataSource)
	}
}

// decodeTarget re-marshals the generic target map into the kind's typed struct,
// rejecting unknown keys so a stray secret cannot ride along in the target.
func decodeTarget(target map[string]any, out any) error {
	if target == nil {
		target = map[string]any{}
	}
	b, err := json.Marshal(target)
	if err != nil {
		return fmt.Errorf("%w: bad target", ErrCredentialLinkInvalid)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: bad target: %s", ErrCredentialLinkInvalid, err.Error())
	}
	return nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Info loads a token's non-secret descriptor without consuming it. Returns
// ErrCredentialTokenInvalid for unknown/expired/used tokens.
func (s *CredentialOnboardingService) Info(ctx context.Context, rawToken string) (*CredentialOnboardingInfo, error) {
	row, err := s.liveToken(ctx, rawToken)
	if err != nil {
		return nil, err
	}
	var fields []CredentialFieldSpec
	if err := json.Unmarshal([]byte(row.FieldsJson), &fields); err != nil {
		return nil, fmt.Errorf("credential onboarding: corrupt field list: %w", err)
	}
	// An update link treats every field as optional: leaving one blank keeps the
	// stored value. Say so in the render spec so the form doesn't demand input.
	if tokenTargetsExistingRow(row) {
		for i := range fields {
			fields[i].Required = false
		}
	}
	return &CredentialOnboardingInfo{Kind: row.Kind, Title: row.Title, Fields: fields}, nil
}

// tokenTargetsExistingRow reports whether the token updates an existing
// credential/source (as opposed to creating one). Update links relax the
// required-field contract to "leave blank to keep".
func tokenTargetsExistingRow(row store.CredentialOnboardingToken) bool {
	switch row.Kind {
	case CredentialKindProviderCredential:
		var t providerCredentialTarget
		if err := json.Unmarshal([]byte(row.TargetJson), &t); err != nil {
			return false
		}
		return t.CredentialID > 0
	case CredentialKindDataSource:
		var t dataSourceTarget
		if err := json.Unmarshal([]byte(row.TargetJson), &t); err != nil {
			return false
		}
		return t.SourceID > 0
	}
	return false
}

// liveToken resolves a raw token to its row, rejecting unknown/expired/used
// tokens with the same opaque error.
func (s *CredentialOnboardingService) liveToken(ctx context.Context, rawToken string) (store.CredentialOnboardingToken, error) {
	if strings.TrimSpace(rawToken) == "" {
		return store.CredentialOnboardingToken{}, ErrCredentialTokenInvalid
	}
	hash := sha256.Sum256([]byte(rawToken))
	row, err := s.q.GetCredentialOnboardingTokenByHash(ctx, hex.EncodeToString(hash[:]))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.CredentialOnboardingToken{}, ErrCredentialTokenInvalid
		}
		return store.CredentialOnboardingToken{}, err
	}
	if row.UsedAt.Valid || time.Now().UTC().After(row.ExpiresAt) {
		return store.CredentialOnboardingToken{}, ErrCredentialTokenInvalid
	}
	return row, nil
}

// Submit redeems a token: it validates the values against the declared field
// list, writes them through the encrypted store for the token's kind, and marks
// the token used. No submitted value is ever returned or logged.
func (s *CredentialOnboardingService) Submit(ctx context.Context, rawToken string, values map[string]any) (*CredentialSubmitResult, error) {
	row, err := s.liveToken(ctx, rawToken)
	if err != nil {
		return nil, err
	}

	var fields []CredentialFieldSpec
	if err := json.Unmarshal([]byte(row.FieldsJson), &fields); err != nil {
		return nil, fmt.Errorf("credential onboarding: corrupt field list: %w", err)
	}
	clean, err := validateSubmittedValues(fields, values, !tokenTargetsExistingRow(row))
	if err != nil {
		return nil, err
	}

	ownerType, ownerID, err := s.projectOwner(ctx, row.ProjectID)
	if err != nil {
		return nil, err
	}

	// Claim the token BEFORE writing, with a conditional UPDATE that only the
	// first redeemer can win. Validating first (above) keeps a bad submission
	// from burning the link, while claiming first means two concurrent
	// submissions cannot both write a credential. A write that fails after the
	// claim does consume the link — the safe direction for a one-time token.
	claimed, err := s.q.MarkCredentialOnboardingTokenUsed(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if claimed == 0 {
		return nil, ErrCredentialTokenInvalid
	}

	var result *CredentialSubmitResult
	switch row.Kind {
	case CredentialKindProviderCredential:
		result, err = s.submitProviderCredential(ctx, row, ownerType, ownerID, clean)
	case CredentialKindDataSource:
		result, err = s.submitDataSource(ctx, row, ownerType, ownerID, clean)
	default:
		return nil, fmt.Errorf("%w: unknown kind %q", ErrCredentialLinkInvalid, row.Kind)
	}
	if err != nil {
		slog.Warn("credential onboarding: write failed after token claim", "tokenID", row.ID, "kind", row.Kind, "error", err)
		return nil, err
	}
	return result, nil
}

// validateSubmittedValues enforces the link's declared contract: only declared
// fields are accepted, required fields must be non-empty (unless this is an
// update link, where blank means "keep the stored value"), and all values are
// coerced to trimmed strings.
func validateSubmittedValues(fields []CredentialFieldSpec, values map[string]any, enforceRequired bool) (map[string]string, error) {
	declared := make(map[string]CredentialFieldSpec, len(fields))
	for _, f := range fields {
		declared[f.Name] = f
	}
	for k := range values {
		if _, ok := declared[k]; !ok {
			return nil, fmt.Errorf("%w: undeclared field %q", ErrCredentialSubmissionInvalid, k)
		}
	}
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		v := strings.TrimSpace(stringifyValue(values[f.Name]))
		if v == "" && f.Required && enforceRequired {
			return nil, fmt.Errorf("%w: %s is required", ErrCredentialSubmissionInvalid, f.Name)
		}
		if v != "" {
			out[f.Name] = v
		}
	}
	return out, nil
}

// stringifyValue coerces a JSON scalar to its string form; containers are
// rejected (a field value is always a scalar).
func stringifyValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	case bool:
		return fmt.Sprintf("%t", t)
	default:
		return ""
	}
}

// projectOwner resolves the owner a credential written for this project belongs
// to. The credential is owner-level (like IM bots) — not bound to the project.
func (s *CredentialOnboardingService) projectOwner(ctx context.Context, projectID int64) (string, int64, error) {
	p, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return "", 0, fmt.Errorf("credential onboarding: project lookup: %w", err)
	}
	return p.OwnerType, p.OwnerID, nil
}

// submitProviderCredential creates or updates an external provider credential
// from the submitted values.
func (s *CredentialOnboardingService) submitProviderCredential(ctx context.Context, row store.CredentialOnboardingToken, ownerType string, ownerID int64, values map[string]string) (*CredentialSubmitResult, error) {
	var target providerCredentialTarget
	if err := json.Unmarshal([]byte(row.TargetJson), &target); err != nil {
		return nil, fmt.Errorf("credential onboarding: corrupt target: %w", err)
	}

	// Build the config shape the provider adapters expect: an auth_mode tag plus
	// the field values (AddCredentialDialog writes the same shape).
	cfg := make(map[string]any, len(values)+1)
	for k, v := range values {
		cfg[k] = v
	}
	if target.AuthMode != "" {
		cfg["auth_mode"] = target.AuthMode
	}
	if p, ok := cfg["port"]; ok {
		cfg["port"] = toIntOrKeep(p)
	}

	if target.CredentialID > 0 {
		existing, err := s.ext.GetByID(ctx, target.CredentialID, ownerType, ownerID, row.IssuedBy)
		if err != nil {
			return nil, fmt.Errorf("%w: credential %d not found", ErrCredentialLinkInvalid, target.CredentialID)
		}
		if target.Provider != "" && string(existing.Provider) != target.Provider {
			return nil, fmt.Errorf("%w: credential %d belongs to provider %q", ErrCredentialLinkInvalid, target.CredentialID, existing.Provider)
		}
		// Edit semantics: a blank field keeps the stored value.
		merged := make(map[string]any, len(existing.Config))
		for k, v := range existing.Config {
			merged[k] = v
		}
		for k, v := range cfg {
			if str, ok := v.(string); ok && str == "" {
				continue
			}
			merged[k] = v
		}
		if _, err := s.ext.UpdateConfig(ctx, target.CredentialID, ownerType, ownerID, row.IssuedBy, merged); err != nil {
			return nil, err
		}
		return &CredentialSubmitResult{Kind: CredentialKindProviderCredential, CredentialID: target.CredentialID, Provider: target.Provider, Alias: target.Alias}, nil
	}

	decoded, err := s.ext.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: ownerType,
		OwnerID:   ownerID,
		UserID:    row.IssuedBy,
		Provider:  integration.ProviderName(target.Provider),
		Alias:     target.Alias,
		RawConfig: cfg,
	})
	if err != nil {
		return nil, err
	}
	return &CredentialSubmitResult{Kind: CredentialKindProviderCredential, CredentialID: decoded.ID, Provider: string(decoded.Provider), Alias: decoded.Alias}, nil
}

// submitDataSource creates (and project-binds) or updates a data source from
// the submitted values.
func (s *CredentialOnboardingService) submitDataSource(ctx context.Context, row store.CredentialOnboardingToken, ownerType string, ownerID int64, values map[string]string) (*CredentialSubmitResult, error) {
	var target dataSourceTarget
	if err := json.Unmarshal([]byte(row.TargetJson), &target); err != nil {
		return nil, fmt.Errorf("credential onboarding: corrupt target: %w", err)
	}
	cfg := make(map[string]any, len(values))
	for k, v := range values {
		cfg[k] = v
	}
	coerceDataSourceNumericFields(target.Kind, cfg)

	orgIDs := orgIDsForOwner(ownerType, ownerID)
	// DataSourceService authorizes on owner identity (not user_id), so the
	// ownership-scoped user id differs from the recorded issuer for org owners.
	scopeUserID := dsOwnershipUserID(ownerType, ownerID)

	if target.SourceID > 0 {
		existing, err := s.ds.Get(ctx, target.SourceID, scopeUserID, orgIDs)
		if err != nil {
			return nil, fmt.Errorf("%w: data source %d not found", ErrCredentialLinkInvalid, target.SourceID)
		}
		if target.Kind != "" && existing.Kind != target.Kind {
			return nil, fmt.Errorf("%w: data source %d is kind %q", ErrCredentialLinkInvalid, target.SourceID, existing.Kind)
		}
		// Edit semantics: anything the link did not declare keeps its stored
		// value (mirrors the settings form's "leave blank to keep").
		name := target.Name
		if strings.TrimSpace(name) == "" {
			name = existing.Name
		}
		scope := target.ScopeConfig
		if scope == nil {
			scope = existing.ScopeConfig
		}
		if err := s.ds.Update(ctx, target.SourceID, scopeUserID, orgIDs, UpdateDataSourceInput{
			Name:              name,
			Config:            cfg,
			ScopeConfig:       scope,
			DefaultAccessMode: target.DefaultAccessMode,
			RequireConfirm:    target.RequireConfirm,
		}); err != nil {
			return nil, err
		}
		return &CredentialSubmitResult{Kind: CredentialKindDataSource, SourceID: target.SourceID, Name: name}, nil
	}

	id, err := s.ds.Create(ctx, CreateDataSourceInput{
		OwnerType:         ownerType,
		OwnerID:           ownerID,
		UserID:            row.IssuedBy,
		Name:              target.Name,
		Kind:              target.Kind,
		Config:            cfg,
		ScopeConfig:       target.ScopeConfig,
		DefaultAccessMode: target.DefaultAccessMode,
		RequireConfirm:    target.RequireConfirm,
	})
	if err != nil {
		return nil, err
	}
	// Bind to the project the link was issued for, so the agent can immediately
	// query the source via run_data_query.
	if err := s.ds.AddBinding(ctx, id, scopeUserID, orgIDs, DataSourceBinding{TargetType: "project", TargetID: row.ProjectID}); err != nil {
		return nil, err
	}
	return &CredentialSubmitResult{Kind: CredentialKindDataSource, SourceID: id, Name: target.Name}, nil
}

// dsOwnershipUserID returns the user id that satisfies DataSourceService's
// owner-visibility check for this owner: a personal owner matches on user id,
// an org row is matched through orgIDs instead.
func dsOwnershipUserID(ownerType string, ownerID int64) int64 {
	if ownerType == "user" {
		return ownerID
	}
	return 0
}

// HasCredential reports whether the target already holds a stored credential —
// the only thing an agent may learn about a secret. Never returns the value.
func (s *CredentialOnboardingService) HasCredential(ctx context.Context, projectID int64, kind string, target map[string]any) (bool, error) {
	ownerType, ownerID, err := s.projectOwner(ctx, projectID)
	if err != nil {
		return false, err
	}
	switch kind {
	case CredentialKindProviderCredential:
		var t providerCredentialTarget
		if err := decodeTarget(target, &t); err != nil {
			return false, err
		}
		if t.CredentialID > 0 {
			meta, err := s.ext.GetMetaByID(ctx, t.CredentialID)
			if err != nil {
				return false, nil
			}
			return meta.OwnerType == ownerType && meta.OwnerID == ownerID, nil
		}
		if strings.TrimSpace(t.Provider) == "" || strings.TrimSpace(t.Alias) == "" {
			return false, nil
		}
		// Alias lookup spans users (the issuer's row is what matters); use the
		// owner-scoped listing and match on alias.
		creds, err := s.ext.ListForOwner(ctx, ownerType, ownerID, integration.ProviderName(t.Provider))
		if err != nil {
			return false, err
		}
		for _, c := range creds {
			if strings.EqualFold(c.Alias, strings.TrimSpace(t.Alias)) {
				return true, nil
			}
		}
		return false, nil
	case CredentialKindDataSource:
		var t dataSourceTarget
		if err := decodeTarget(target, &t); err != nil {
			return false, err
		}
		if t.SourceID <= 0 {
			// A create link's response goes to the browser, so the agent learns
			// only the name it declared — resolve the id from it.
			if strings.TrimSpace(t.Name) == "" {
				return false, nil
			}
			id, err := s.ds.ResolveSourceID(ctx, t.Name, dsOwnershipUserID(ownerType, ownerID), orgIDsForOwner(ownerType, ownerID))
			if err != nil {
				return false, nil
			}
			t.SourceID = id
		}
		return s.ds.HasCredential(ctx, t.SourceID, ownerType, ownerID)
	default:
		return false, fmt.Errorf("%w: kind must be %s|%s", ErrCredentialLinkInvalid, CredentialKindProviderCredential, CredentialKindDataSource)
	}
}

// orgIDsForOwner returns the owner-scope filter for owner-scoped lookups: an org
// owner makes the org's rows visible; a personal owner returns nil.
func orgIDsForOwner(ownerType string, ownerID int64) []int64 {
	if ownerType == "org" {
		return []int64{ownerID}
	}
	return nil
}

// coerceDataSourceNumericFields turns string form values into the JSON numbers
// the connector config readers expect (intField only reads JSON numbers).
func coerceDataSourceNumericFields(kind string, cfg map[string]any) {
	for _, k := range []string{"port", "imap_port", "smtp_port"} {
		if v, ok := cfg[k]; ok {
			cfg[k] = toIntOrKeep(v)
		}
	}
	if kind == "redis" {
		if v, ok := cfg["database"]; ok {
			cfg["database"] = toIntOrKeep(v)
		}
	}
}

// toIntOrKeep converts a numeric string to an int, leaving anything else as-is.
func toIntOrKeep(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return v
	}
	return n
}
