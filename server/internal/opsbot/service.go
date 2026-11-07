// Package opsbot implements the Ops Bot domain: per-account bot identities on
// top of a shared Platform Pack, plus their configuration and lifecycle.
package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Roles an ops bot can have.
const (
	RoleDirector = "director"
	RoleAccount  = "account"
)

// Bot lifecycle states.
const (
	StatusActive   = "active"
	StatusPaused   = "paused"
	StatusDisabled = "disabled"
)

const (
	emptyJSON              = "{}"
	defaultApprovalPolicy  = `{"publish":true,"reply":true,"video":true}`
	defaultOwnerType       = "user"
	approvalPolicyBoolKeys = "publish|reply|video"
)

var (
	ErrAccountExists = errors.New("opsbot: account already exists")
	ErrPackNotFound  = errors.New("opsbot: pack not found")
	ErrNotFound      = errors.New("opsbot: bot not found")
	ErrInvalidConfig = errors.New("opsbot: invalid config")
	ErrInvalidStatus = errors.New("opsbot: invalid status")
	ErrInvalidInput  = errors.New("opsbot: invalid input")
)

// OwnerRef identifies who owns a bot. Bots are isolated per owner: the same
// pack + account_ref under two owners are different bots.
type OwnerRef struct {
	Type string
	ID   int64
}

// PackRegistry resolves Platform Pack manifests for an owner. *packs.Registry
// (filesystem-backed) satisfies it.
type PackRegistry interface {
	Load(ownerType string, ownerID int64, packID string, version int64) (packs.Manifest, error)
	LatestPublished(ownerType string, ownerID int64, packID string) (packs.Manifest, error)
}

// ApprovalCanceller cancels outstanding approvals for a bot; KillSwitch uses it
// so a disabled bot cannot be resumed through a stale approval card.
type ApprovalCanceller interface {
	CancelByBot(ctx context.Context, botID int64) error
}

// Service is the ops bot configuration and lifecycle service.
type Service struct {
	Q       *store.Queries
	Packs   PackRegistry
	Approve ApprovalCanceller
}

// NewService wires a service. packs may be nil only for director-only callers.
func NewService(q *store.Queries, packs PackRegistry) *Service {
	return &Service{Q: q, Packs: packs}
}

// CreateAccountInput describes a new account bot under a platform pack.
type CreateAccountInput struct {
	Owner              OwnerRef
	PackID             string
	PackVersion        int64 // 0 = latest published
	AccountRef         string
	DisplayName        string
	PersonaJSON        string
	CredentialRefsJSON string
	KnowledgeScopeJSON string
	GoalsJSON          string
	CadenceJSON        string
	ApprovalPolicyJSON string
	BudgetJSON         string
	DirectorBotID      int64
	CreatedBy          int64
}

// CreateDirectorInput describes the brand-level director bot. A director is
// pack-less: it aggregates across accounts instead of publishing to a channel.
type CreateDirectorInput struct {
	Owner              OwnerRef
	DisplayName        string
	PersonaJSON        string
	KnowledgeScopeJSON string
	GoalsJSON          string
	CadenceJSON        string
	ApprovalPolicyJSON string
	BudgetJSON         string
	CreatedBy          int64
}

// ConfigPatch updates only the non-nil fields of an existing bot.
type ConfigPatch struct {
	DisplayName        *string
	PersonaJSON        *string
	CredentialRefsJSON *string
	KnowledgeScopeJSON *string
	GoalsJSON          *string
	CadenceJSON        *string
	ApprovalPolicyJSON *string
	BudgetJSON         *string
	PackVersion        *int64
}

// CreateAccountBot registers a bot for one platform account. Multiple accounts
// may share a pack; (owner, pack, account_ref) is unique.
func (s *Service) CreateAccountBot(ctx context.Context, in CreateAccountInput) (store.OpsBot, error) {
	owner := in.Owner.normalized()
	if err := owner.validate(); err != nil {
		return store.OpsBot{}, err
	}
	packID := strings.TrimSpace(in.PackID)
	accountRef := strings.TrimSpace(in.AccountRef)
	if packID == "" {
		return store.OpsBot{}, fmt.Errorf("%w: pack_id is required", ErrInvalidInput)
	}
	if accountRef == "" {
		return store.OpsBot{}, fmt.Errorf("%w: account_ref is required", ErrInvalidInput)
	}
	if in.PackVersion < 0 {
		return store.OpsBot{}, fmt.Errorf("%w: pack_version must be >= 0", ErrInvalidInput)
	}

	version, err := s.resolvePack(ctx, owner, packID, in.PackVersion)
	if err != nil {
		return store.OpsBot{}, err
	}
	cfg, err := normalizeConfig(configInput{
		persona: in.PersonaJSON, credentialRefs: in.CredentialRefsJSON,
		knowledgeScope: in.KnowledgeScopeJSON, goals: in.GoalsJSON,
		cadence: in.CadenceJSON, approvalPolicy: in.ApprovalPolicyJSON,
		budget: in.BudgetJSON,
	})
	if err != nil {
		return store.OpsBot{}, err
	}

	bot, err := s.Q.CreateOpsBot(ctx, store.CreateOpsBotParams{
		OwnerType:          owner.Type,
		OwnerID:            owner.ID,
		Role:               RoleAccount,
		PackID:             packID,
		PackVersion:        version,
		AccountRef:         accountRef,
		DisplayName:        strings.TrimSpace(in.DisplayName),
		PersonaJson:        cfg.persona,
		CredentialRefsJson: cfg.credentialRefs,
		KnowledgeScopeJson: cfg.knowledgeScope,
		GoalsJson:          cfg.goals,
		CadenceJson:        cfg.cadence,
		ApprovalPolicyJson: cfg.approvalPolicy,
		BudgetJson:         cfg.budget,
		DirectorBotID:      nullInt64(in.DirectorBotID),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return store.OpsBot{}, fmt.Errorf("%w: pack %s account %s", ErrAccountExists, packID, accountRef)
		}
		return store.OpsBot{}, err
	}
	return bot, nil
}

// CreateDirector registers the owner's brand-level director bot.
func (s *Service) CreateDirector(ctx context.Context, in CreateDirectorInput) (store.OpsBot, error) {
	owner := in.Owner.normalized()
	if err := owner.validate(); err != nil {
		return store.OpsBot{}, err
	}
	cfg, err := normalizeConfig(configInput{
		persona: in.PersonaJSON, knowledgeScope: in.KnowledgeScopeJSON,
		goals: in.GoalsJSON, cadence: in.CadenceJSON,
		approvalPolicy: in.ApprovalPolicyJSON, budget: in.BudgetJSON,
	})
	if err != nil {
		return store.OpsBot{}, err
	}

	bot, err := s.Q.CreateOpsBot(ctx, store.CreateOpsBotParams{
		OwnerType:          owner.Type,
		OwnerID:            owner.ID,
		Role:               RoleDirector,
		PackID:             "",
		PackVersion:        0,
		AccountRef:         "",
		DisplayName:        strings.TrimSpace(in.DisplayName),
		PersonaJson:        cfg.persona,
		CredentialRefsJson: cfg.credentialRefs,
		KnowledgeScopeJson: cfg.knowledgeScope,
		GoalsJson:          cfg.goals,
		CadenceJson:        cfg.cadence,
		ApprovalPolicyJson: cfg.approvalPolicy,
		BudgetJson:         cfg.budget,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return store.OpsBot{}, fmt.Errorf("%w: director for owner %s/%d", ErrAccountExists, in.Owner.Type, in.Owner.ID)
		}
		return store.OpsBot{}, err
	}
	return bot, nil
}

// Get returns one bot by id.
func (s *Service) Get(ctx context.Context, botID int64) (store.OpsBot, error) {
	bot, err := s.Q.GetOpsBot(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.OpsBot{}, fmt.Errorf("%w: %d", ErrNotFound, botID)
	}
	if err != nil {
		return store.OpsBot{}, err
	}
	return bot, nil
}

// ListByOwner returns every bot owned by ref, directors first.
func (s *Service) ListByOwner(ctx context.Context, owner OwnerRef) ([]store.OpsBot, error) {
	owner = owner.normalized()
	if err := owner.validate(); err != nil {
		return nil, err
	}
	return s.Q.ListOpsBotsByOwner(ctx, store.ListOpsBotsByOwnerParams{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
	})
}

// UpdateConfig applies the non-nil fields of patch. Every JSON field must be a
// JSON object; the approval policy may only carry its known keys.
func (s *Service) UpdateConfig(ctx context.Context, botID int64, patch ConfigPatch) (store.OpsBot, error) {
	cur, err := s.Get(ctx, botID)
	if err != nil {
		return store.OpsBot{}, err
	}

	next := store.UpdateOpsBotConfigParams{
		ID:                 cur.ID,
		DisplayName:        cur.DisplayName,
		PersonaJson:        cur.PersonaJson,
		CredentialRefsJson: cur.CredentialRefsJson,
		KnowledgeScopeJson: cur.KnowledgeScopeJson,
		GoalsJson:          cur.GoalsJson,
		CadenceJson:        cur.CadenceJson,
		ApprovalPolicyJson: cur.ApprovalPolicyJson,
		BudgetJson:         cur.BudgetJson,
		PackVersion:        cur.PackVersion,
	}
	if patch.DisplayName != nil {
		next.DisplayName = strings.TrimSpace(*patch.DisplayName)
	}

	for _, f := range []struct {
		name  string
		patch *string
		dst   *string
		obj   bool
	}{
		{"persona_json", patch.PersonaJSON, &next.PersonaJson, true},
		{"credential_refs_json", patch.CredentialRefsJSON, &next.CredentialRefsJson, true},
		{"knowledge_scope_json", patch.KnowledgeScopeJSON, &next.KnowledgeScopeJson, true},
		{"goals_json", patch.GoalsJSON, &next.GoalsJson, true},
		{"cadence_json", patch.CadenceJSON, &next.CadenceJson, true},
		{"approval_policy_json", patch.ApprovalPolicyJSON, &next.ApprovalPolicyJson, false},
		{"budget_json", patch.BudgetJSON, &next.BudgetJson, true},
	} {
		if f.patch == nil {
			continue
		}
		var normalized string
		if f.obj {
			normalized, err = normalizeJSONObject(f.name, *f.patch)
		} else {
			normalized, err = normalizeApprovalPolicy(*f.patch)
		}
		if err != nil {
			return store.OpsBot{}, err
		}
		*f.dst = normalized
	}

	if patch.PackVersion != nil {
		version := *patch.PackVersion
		if version < 0 {
			return store.OpsBot{}, fmt.Errorf("%w: pack_version must be >= 0", ErrInvalidInput)
		}
		if version > 0 {
			if cur.PackID == "" {
				return store.OpsBot{}, fmt.Errorf("%w: pack_version set on a pack-less bot", ErrInvalidInput)
			}
			if _, err := s.resolvePack(ctx, OwnerRef{Type: cur.OwnerType, ID: cur.OwnerID}, cur.PackID, version); err != nil {
				return store.OpsBot{}, err
			}
		}
		next.PackVersion = version
	}

	updated, err := s.Q.UpdateOpsBotConfig(ctx, next)
	if errors.Is(err, sql.ErrNoRows) {
		return store.OpsBot{}, fmt.Errorf("%w: %d", ErrNotFound, botID)
	}
	if err != nil {
		return store.OpsBot{}, err
	}
	return updated, nil
}

// SetStatus moves a bot between active, paused and disabled.
func (s *Service) SetStatus(ctx context.Context, botID int64, status string) (store.OpsBot, error) {
	switch status {
	case StatusActive, StatusPaused, StatusDisabled:
	default:
		return store.OpsBot{}, fmt.Errorf("%w: %q", ErrInvalidStatus, status)
	}
	bot, err := s.Q.UpdateOpsBotStatus(ctx, store.UpdateOpsBotStatusParams{
		Status: status,
		ID:     botID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return store.OpsBot{}, fmt.Errorf("%w: %d", ErrNotFound, botID)
	}
	if err != nil {
		return store.OpsBot{}, err
	}
	return bot, nil
}

// KillSwitch disables a bot and cancels its pending approvals so no queued
// publish/reply/video action can still fire.
func (s *Service) KillSwitch(ctx context.Context, botID int64) (store.OpsBot, error) {
	bot, err := s.SetStatus(ctx, botID, StatusDisabled)
	if err != nil {
		return store.OpsBot{}, err
	}
	if s.Approve != nil {
		if err := s.Approve.CancelByBot(ctx, botID); err != nil {
			return bot, fmt.Errorf("opsbot: kill switch cancel approvals: %w", err)
		}
	}
	return bot, nil
}

// resolvePack returns the concrete pack version to pin. version 0 follows the
// latest published version.
func (s *Service) resolvePack(ctx context.Context, owner OwnerRef, packID string, version int64) (int64, error) {
	if s.Packs == nil {
		return 0, fmt.Errorf("%w: %s (no pack registry)", ErrPackNotFound, packID)
	}
	var (
		m   packs.Manifest
		err error
	)
	if version > 0 {
		m, err = s.Packs.Load(owner.Type, owner.ID, packID, version)
	} else {
		m, err = s.Packs.LatestPublished(owner.Type, owner.ID, packID)
	}
	if err != nil {
		if errors.Is(err, ErrPackNotFound) || errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("%w: %s", ErrPackNotFound, packID)
		}
		return 0, fmt.Errorf("%w: %s: %v", ErrPackNotFound, packID, err)
	}
	if m.ID != packID || m.Version <= 0 {
		return 0, fmt.Errorf("%w: %s", ErrPackNotFound, packID)
	}
	return int64(m.Version), nil
}

func (o OwnerRef) normalized() OwnerRef {
	if o.Type == "" {
		o.Type = defaultOwnerType
	}
	return o
}

func (o OwnerRef) validate() error {
	if o.Type != "user" && o.Type != "org" {
		return fmt.Errorf("%w: owner type %q", ErrInvalidInput, o.Type)
	}
	if o.ID <= 0 {
		return fmt.Errorf("%w: owner id must be > 0", ErrInvalidInput)
	}
	return nil
}

type configInput struct {
	persona, credentialRefs, knowledgeScope, goals, cadence, approvalPolicy, budget string
}

type normalizedConfig struct {
	persona, credentialRefs, knowledgeScope, goals, cadence, approvalPolicy, budget string
}

func normalizeConfig(in configInput) (normalizedConfig, error) {
	var out normalizedConfig
	var err error
	for _, f := range []struct {
		name string
		in   string
		out  *string
	}{
		{"persona_json", in.persona, &out.persona},
		{"credential_refs_json", in.credentialRefs, &out.credentialRefs},
		{"knowledge_scope_json", in.knowledgeScope, &out.knowledgeScope},
		{"goals_json", in.goals, &out.goals},
		{"cadence_json", in.cadence, &out.cadence},
		{"budget_json", in.budget, &out.budget},
	} {
		*f.out, err = normalizeJSONObject(f.name, f.in)
		if err != nil {
			return normalizedConfig{}, err
		}
	}
	out.approvalPolicy, err = normalizeApprovalPolicy(in.approvalPolicy)
	if err != nil {
		return normalizedConfig{}, err
	}
	return out, nil
}

// normalizeJSONObject accepts an empty value as {} and otherwise requires a
// JSON object (not an array, scalar or null).
func normalizeJSONObject(field, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return emptyJSON, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", fmt.Errorf("%w: %s must be a JSON object: %v", ErrInvalidConfig, field, err)
	}
	if obj == nil {
		return "", fmt.Errorf("%w: %s must be a JSON object", ErrInvalidConfig, field)
	}
	return raw, nil
}

func normalizeApprovalPolicy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultApprovalPolicy, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", fmt.Errorf("%w: approval_policy_json must be a JSON object: %v", ErrInvalidConfig, err)
	}
	if obj == nil {
		return "", fmt.Errorf("%w: approval_policy_json must be a JSON object", ErrInvalidConfig)
	}
	for key, val := range obj {
		switch key {
		case "publish", "reply", "video":
			var enabled bool
			if err := json.Unmarshal(val, &enabled); err != nil {
				return "", fmt.Errorf("%w: approval_policy_json.%s must be a boolean", ErrInvalidConfig, key)
			}
		case "operator_id", "reply_hourly_limit":
			var n float64
			if err := json.Unmarshal(val, &n); err != nil {
				return "", fmt.Errorf("%w: approval_policy_json.%s must be a number", ErrInvalidConfig, key)
			}
		default:
			return "", fmt.Errorf("%w: approval_policy_json key %q not allowed (allowed: %s)", ErrInvalidConfig, key, approvalPolicyBoolKeys)
		}
	}
	return raw, nil
}

func nullInt64(v int64) sql.NullInt64 {
	if v <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value violates unique constraint")
}
