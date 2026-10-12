package service

// Publish / verify / rollback for the integration draft pipeline (Epic #726 /
// #734).
//
// Publish applies a confirmed draft with the target DISABLED, runs the read-only
// probe the confirmation card displayed, and enables the target only when the
// probe passes. A failed probe restores the previously published state: an
// existing target gets its old payload and enabled flag back, while a
// just-created target is removed again, so a failed publish never leaves a live
// half-configured integration behind. The draft itself survives either way —
// that is what "失败保留草稿" buys: the user fixes the cause and retries.
//
// Rollback restores a previously published version. Because restoring an older
// configuration can re-grant authority the user has since revoked, a rollback
// whose scope (or write gate) differs from the live one is itself an
// authorization action: it returns the system-rendered card and demands a fresh
// confirmation before it applies.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// PublishDraftInput is a publish request. FromAgent must be set by the caller
// based on the request's origin: true when the request arrived through an
// agent-facing (MCP) path. An agent-originated publish is always refused.
type PublishDraftInput struct {
	DraftID   int64
	OwnerType string
	OwnerID   int64
	ByUser    int64
	FromAgent bool
}

// DraftPublishResult reports what a publish (or a rollback) actually did.
type DraftPublishResult struct {
	// Published is true only when the candidate was applied AND enabled.
	Published bool `json:"published"`
	// RolledBack is true when the candidate was applied but the probe failed, so
	// the previous state was restored and nothing new is live.
	RolledBack bool `json:"rolled_back"`
	// Enabled reports the target's enabled state after the operation.
	Enabled  bool   `json:"enabled"`
	TargetID int64  `json:"target_id,omitempty"`
	Version  int64  `json:"version,omitempty"`
	Detail   string `json:"detail,omitempty"`

	Verify       *DraftVerifyResult `json:"verify,omitempty"`
	NextAction   string             `json:"next_action,omitempty"`
	Alternatives []string           `json:"alternatives,omitempty"`
}

// RollbackInput is a rollback request.
type RollbackInput struct {
	VersionID int64
	OwnerType string
	OwnerID   int64
	ByUser    int64
	FromAgent bool
	// Confirmed acknowledges the re-confirmation the rollback demands when the
	// restored configuration differs in scope from the live one.
	Confirmed bool
	AckKeys   []string
}

// RollbackResult reports the outcome of a rollback. When the rollback needs a
// confirmation first, RequiresConfirmation is true and Card carries the
// system-rendered card to show.
type RollbackResult struct {
	RolledBack bool  `json:"rolled_back"`
	Enabled    bool  `json:"enabled"`
	TargetID   int64 `json:"target_id,omitempty"`
	Version    int64 `json:"version,omitempty"`

	RequiresConfirmation bool              `json:"requires_confirmation,omitempty"`
	Card                 *ConfirmCardModel `json:"card,omitempty"`

	Verify       *DraftVerifyResult `json:"verify,omitempty"`
	Detail       string             `json:"detail,omitempty"`
	NextAction   string             `json:"next_action,omitempty"`
	Alternatives []string           `json:"alternatives,omitempty"`
}

// PublishDraft applies a confirmed draft. Sequence:
//
//	authorize (human only) -> re-verify the approval is not stale -> apply
//	disabled -> probe -> enable, or restore the previous state
func (s *IntegrationDraftService) PublishDraft(ctx context.Context, in PublishDraftInput) (*DraftPublishResult, error) {
	// The permission principle, enforced at the service boundary rather than by
	// hiding a button: an agent may draft and request; it may never publish.
	if in.FromAgent {
		return nil, ErrAgentCannotAuthorize
	}
	row, err := s.getOwnedDraft(ctx, in.DraftID, in.OwnerType, in.OwnerID)
	if err != nil {
		return nil, err
	}
	if row.Status != DraftStatusConfirmed {
		return nil, fmt.Errorf("%w: draft %d is %q", ErrDraftNotConfirmed, row.ID, row.Status)
	}

	// The approval must cover exactly what is about to be applied. The decision
	// bytes are rebuilt from the draft's current structured fields and diffed
	// against the live state; any drift voids the approval.
	var docOps []OperationKey
	_ = json.Unmarshal([]byte(row.DocOpsJson), &docOps)
	card, err := s.cardFor(ctx, row, docOps)
	if err != nil {
		return nil, err
	}
	fresh, err := DecisionPayload(card)
	if err != nil {
		return nil, fmt.Errorf("integration draft %d: rebuild decision payload: %w", row.ID, err)
	}
	if string(fresh) != row.ConfirmJson {
		// Void the stale approval so the UI must collect a fresh one.
		_ = s.q.ResetIntegrationDraftConfirm(ctx, row.ID)
		return nil, ErrDraftStale
	}

	applied, err := s.applyAndVerify(ctx, row, row.Payload, in.ByUser)
	if err != nil {
		return nil, err
	}
	if !applied.verify.OK {
		// Restore the previous published state and keep the draft.
		s.restoreSnapshot(ctx, applied.snapshot)
		return &DraftPublishResult{
			Published:  false,
			RolledBack: true,
			Enabled:    false,
			TargetID:   applied.snapshot.TargetID,
			Detail:     "the post-publish verification probe failed; the previous configuration was restored and the draft kept",
			Verify:     applied.verify,
			NextAction: "fix the cause (credential / connectivity / probe operation) and publish the draft again",
			Alternatives: []string{
				"submit or update the credential through the one-time credential link",
				"correct the base URL or the verify_operation in the contract",
				"check the source is reachable from this machine",
			},
		}, nil
	}

	// Probe passed: enable the target and make the version live.
	if err := s.enable(ctx, row.Kind, applied.targetID, true); err != nil {
		return nil, err
	}
	if row.Kind == DraftKindProvider {
		var p ProviderDraftPayload
		_ = json.Unmarshal([]byte(row.Payload), &p)
		// The write gate is a per-user grant; set it to exactly what was approved.
		if err := s.providers.SetWritePref(ctx, in.ByUser, applied.targetID, p.WriteEnabled); err != nil {
			return nil, err
		}
	}
	version, err := s.recordVersion(ctx, row, applied.targetID, row.Payload, in.ByUser)
	if err != nil {
		return nil, err
	}
	if err := s.q.MarkIntegrationDraftPublished(ctx, store.MarkIntegrationDraftPublishedParams{
		TargetID: applied.targetID,
		ID:       row.ID,
	}); err != nil {
		return nil, err
	}
	return &DraftPublishResult{
		Published: true,
		Enabled:   true,
		TargetID:  applied.targetID,
		Version:   version,
		Detail:    "published and verified",
		Verify:    applied.verify,
	}, nil
}

// Rollback restores a previously published version. When the restored scope (or
// write gate) differs from what is live, it returns the system-rendered card and
// requires an explicit confirmation before applying.
func (s *IntegrationDraftService) Rollback(ctx context.Context, in RollbackInput) (*RollbackResult, error) {
	if in.FromAgent {
		return nil, ErrAgentCannotAuthorize
	}
	version, err := s.q.GetIntegrationVersion(ctx, in.VersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDraftNotFound
	}
	if err != nil {
		return nil, err
	}
	if version.OwnerType != in.OwnerType || version.OwnerID != in.OwnerID {
		return nil, ErrDraftNotFound
	}

	prev, err := s.publishedState(ctx, in.OwnerType, in.OwnerID, version.Kind, version.TargetName)
	if err != nil {
		return nil, err
	}
	card, err := buildCardFromPayload(version.Kind, version.TargetName, version.Payload,
		nil, "", UntrustedOrigin{Kind: UntrustedKindNote}, nil, prev)
	if err != nil {
		return nil, err
	}
	if rollbackNeedsConfirmation(card) {
		if !in.Confirmed {
			return &RollbackResult{RequiresConfirmation: true, Card: &card}, nil
		}
		acked := make(map[string]bool, len(in.AckKeys))
		for _, k := range in.AckKeys {
			acked[k] = true
		}
		var missing []string
		for _, key := range card.AckKeys() {
			if !acked[key] {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("%w: %s", ErrDraftAckIncomplete, strings.Join(missing, ", "))
		}
	}

	// Build the row-shaped context the shared applier needs.
	row := store.IntegrationDraft{
		OwnerType:  version.OwnerType,
		OwnerID:    version.OwnerID,
		Kind:       version.Kind,
		TargetName: version.TargetName,
		TargetID:   version.TargetID,
	}
	applied, err := s.applyAndVerify(ctx, row, version.Payload, in.ByUser)
	if err != nil {
		return nil, err
	}
	if !applied.verify.OK {
		s.restoreSnapshot(ctx, applied.snapshot)
		return &RollbackResult{
			RolledBack: false,
			Enabled:    false,
			TargetID:   applied.snapshot.TargetID,
			Detail:     "the restored configuration failed its verification probe; the live configuration was left as it was",
			Verify:     applied.verify,
			NextAction: "the old version may depend on a credential that has since expired — check the credential and retry",
		}, nil
	}
	if err := s.enable(ctx, version.Kind, applied.targetID, true); err != nil {
		return nil, err
	}
	if err := s.q.DeactivateIntegrationVersions(ctx, store.DeactivateIntegrationVersionsParams{
		OwnerType: version.OwnerType, OwnerID: version.OwnerID,
		Kind: version.Kind, TargetName: version.TargetName,
	}); err != nil {
		return nil, err
	}
	if err := s.q.ActivateIntegrationVersion(ctx, version.ID); err != nil {
		return nil, err
	}
	return &RollbackResult{
		RolledBack: true,
		Enabled:    true,
		TargetID:   applied.targetID,
		Version:    version.Version,
		Detail:     fmt.Sprintf("restored version %d", version.Version),
		Verify:     applied.verify,
	}, nil
}

// ListVersions returns the published versions of a target, newest first.
func (s *IntegrationDraftService) ListVersions(ctx context.Context, ownerType string, ownerID int64, kind, targetName string, limit int64) ([]store.IntegrationVersion, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.q.ListIntegrationVersions(ctx, store.ListIntegrationVersionsParams{
		OwnerType:  ownerType,
		OwnerID:    ownerID,
		Kind:       kind,
		TargetName: targetName,
		Limit:      limit,
	})
}

// --- apply + verify -------------------------------------------------------

// applyOutcome is the result of applying a payload and probing it.
type applyOutcome struct {
	targetID int64
	verify   *DraftVerifyResult
	snapshot *liveSnapshot
}

// applyAndVerify snapshots the live target, applies the candidate with the
// target DISABLED, then runs the verification probe.
func (s *IntegrationDraftService) applyAndVerify(ctx context.Context, row store.IntegrationDraft, payloadJSON string, byUser int64) (*applyOutcome, error) {
	snap, err := s.snapshot(ctx, row, byUser)
	if err != nil {
		return nil, err
	}
	targetID, err := s.applyPayload(ctx, row, payloadJSON)
	if err != nil {
		return nil, err
	}
	// Disable while unverified: the candidate is live-shaped but not live.
	if err := s.enable(ctx, row.Kind, targetID, false); err != nil {
		return nil, err
	}
	verify, err := s.verify(ctx, row, targetID, byUser)
	if err != nil {
		return nil, err
	}
	snap.TargetID = targetID
	if targetID != 0 {
		snap.Applied = true
	}
	return &applyOutcome{targetID: targetID, verify: verify, snapshot: snap}, nil
}

// verify runs the post-publish read-only probe. The probe shown on the approved
// card IS the probe that runs, so "what the user saw" equals "what egresses".
func (s *IntegrationDraftService) verify(ctx context.Context, row store.IntegrationDraft, targetID, byUser int64) (*DraftVerifyResult, error) {
	switch row.Kind {
	case DraftKindProvider:
		return s.verifier.VerifyProvider(ctx, row.TargetName, byUser)
	case DraftKindDataSource:
		return s.verifier.VerifyDataSource(ctx, targetID, dsOwnershipUserID(row.OwnerType, row.OwnerID), orgIDsForOwner(row.OwnerType, row.OwnerID))
	}
	return nil, fmt.Errorf("%w: unknown kind %q", ErrDraftInvalid, row.Kind)
}

// enable flips a target's enabled flag (the "默认 disabled，验证通过再启用" step).
func (s *IntegrationDraftService) enable(ctx context.Context, kind string, targetID int64, enabled bool) error {
	if targetID == 0 || s.health == nil {
		return nil
	}
	subject := ProbeSubjectProvider
	if kind == DraftKindDataSource {
		subject = ProbeSubjectDataSource
	}
	return s.health.SetEnabled(ctx, subject, targetID, enabled)
}

// --- live snapshot + restore ----------------------------------------------

// liveSnapshot is enough state to put a target back exactly as it was before a
// publish attempt.
type liveSnapshot struct {
	Kind       string
	TargetName string
	Existed    bool
	Applied    bool
	TargetID   int64
	Enabled    bool
	// Payload is the previous candidate payload, or "" when the target did not
	// exist (restoring then means removing it again).
	Payload string
}

func (s *IntegrationDraftService) snapshot(ctx context.Context, row store.IntegrationDraft, byUser int64) (*liveSnapshot, error) {
	snap := &liveSnapshot{Kind: row.Kind, TargetName: row.TargetName}
	switch row.Kind {
	case DraftKindProvider:
		prov, err := s.providers.GetByName(ctx, row.TargetName)
		if err != nil {
			return snap, nil // never published
		}
		p := ProviderDraftPayload{
			Label:      prov.Label,
			BaseURL:    prov.APIBaseURL,
			Contract:   prov.Contract(),
			Whitelist:  prov.Whitelist,
			OpenAPIURL: prov.OpenAPIURL,
		}
		if prefs, err := s.providers.ListWritePrefs(ctx, byUser); err == nil {
			p.WriteEnabled = prefs[prov.ID]
		}
		// The live functions belong to the snapshot too. A publish writes the
		// registry, so restoring only the provider row would leave a failed
		// publish's functions live — the "函数随源" rule has to hold on the
		// failure path as well as the happy one.
		if s.functions != nil {
			p.Functions = s.functions.FunctionsForProviderAsDraft(ctx, prov.ID)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		snap.Existed = true
		snap.TargetID = prov.ID
		snap.Enabled = prov.Enabled
		snap.Payload = string(raw)

	case DraftKindDataSource:
		scopeUserID := dsOwnershipUserID(row.OwnerType, row.OwnerID)
		orgIDs := orgIDsForOwner(row.OwnerType, row.OwnerID)
		id, err := s.sources.ResolveSourceID(ctx, row.TargetName, scopeUserID, orgIDs)
		if err != nil || id <= 0 {
			return snap, nil // never published
		}
		live, err := s.sources.getOwned(ctx, id, scopeUserID, orgIDs)
		if err != nil {
			return snap, nil
		}
		// Read the FULL config (decrypted) so the restore is faithful, secrets
		// included. It never leaves the process.
		cfg, err := s.sources.decryptConfig(live.Config)
		if err != nil {
			return nil, fmt.Errorf("snapshot data source %d: %w", id, err)
		}
		p := DataSourceDraftPayload{
			Name:              live.Name,
			Kind:              live.Kind,
			Config:            cfg,
			ScopeConfig:       parseJSONObject(live.ScopeConfig),
			DefaultAccessMode: live.DefaultAccessMode,
			RequireConfirm:    normalizeRequireConfirm(live.RequireConfirm),
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		snap.Existed = true
		snap.TargetID = id
		snap.Enabled = live.Enabled != 0
		snap.Payload = string(raw)
	}
	return snap, nil
}

// restoreSnapshot puts a target back the way it was before a failed publish: an
// existing target gets its old payload and enabled flag back; a target this
// publish created is removed again so nothing half-configured stays live. The
// caller keeps the draft, so the user can retry.
func (s *IntegrationDraftService) restoreSnapshot(ctx context.Context, snap *liveSnapshot) {
	if snap == nil {
		return
	}
	if !snap.Existed {
		if snap.Applied && snap.TargetID != 0 {
			s.deleteTarget(ctx, snap.Kind, snap.TargetID)
		}
		return
	}
	row := store.IntegrationDraft{Kind: snap.Kind, TargetName: snap.TargetName, TargetID: snap.TargetID}
	if _, err := s.applyPayload(ctx, row, snap.Payload); err != nil {
		logRestoreFailure(snap, err)
		return
	}
	if err := s.enable(ctx, snap.Kind, snap.TargetID, snap.Enabled); err != nil {
		logRestoreFailure(snap, err)
	}
}

// deleteTarget removes a target this publish created and whose probe failed.
func (s *IntegrationDraftService) deleteTarget(ctx context.Context, kind string, targetID int64) {
	switch kind {
	case DraftKindProvider:
		// Remove the functions first: the provider row is about to disappear,
		// and a function whose source is gone must not stay callable.
		if s.functions != nil {
			if err := s.functions.DeleteForProvider(ctx, targetID); err != nil {
				logRestoreFailure(&liveSnapshot{Kind: kind, TargetID: targetID}, err)
			}
		}
		if err := s.providers.Delete(ctx, targetID); err != nil {
			logRestoreFailure(&liveSnapshot{Kind: kind, TargetID: targetID}, err)
		}
	case DraftKindDataSource:
		// The row is owner-scoped; resolve the owner from the row itself.
		row, err := s.q.GetDataSource(ctx, targetID)
		if err != nil {
			logRestoreFailure(&liveSnapshot{Kind: kind, TargetID: targetID}, err)
			return
		}
		if err := s.sources.Delete(ctx, targetID, dsOwnershipUserID(row.OwnerType, row.OwnerID), orgIDsForOwner(row.OwnerType, row.OwnerID)); err != nil {
			logRestoreFailure(&liveSnapshot{Kind: kind, TargetID: targetID}, err)
		}
	}
}

// --- apply ----------------------------------------------------------------

// applyPayload writes a candidate payload onto the live target, creating it when
// absent. The caller disables it before probing.
func (s *IntegrationDraftService) applyPayload(ctx context.Context, row store.IntegrationDraft, payloadJSON string) (int64, error) {
	switch row.Kind {
	case DraftKindProvider:
		var p ProviderDraftPayload
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			return 0, fmt.Errorf("apply provider draft: %w", err)
		}
		return s.applyProviderFrom(ctx, row.TargetName, p, row.OwnerType, row.OwnerID, row.CreatedBy, row.ID)
	case DraftKindDataSource:
		var d DataSourceDraftPayload
		if err := json.Unmarshal([]byte(payloadJSON), &d); err != nil {
			return 0, fmt.Errorf("apply data-source draft: %w", err)
		}
		return s.applyDataSource(ctx, row, d)
	}
	return 0, fmt.Errorf("%w: unknown kind %q", ErrDraftInvalid, row.Kind)
}

func (s *IntegrationDraftService) applyProvider(ctx context.Context, name string, p ProviderDraftPayload) (int64, error) {
	return s.applyProviderFrom(ctx, name, p, "", 0, 0, 0)
}

// applyProviderFrom is applyProvider with the ownership + draft identity needed
// to write the function registry, which is owner-scoped like every other
// integration table.
func (s *IntegrationDraftService) applyProviderFrom(ctx context.Context, name string, p ProviderDraftPayload,
	ownerType string, ownerID, byUser, draftID int64) (int64, error) {

	if p.Contract == nil {
		return 0, fmt.Errorf("%w: provider contract is required", ErrDraftInvalid)
	}
	if strings.TrimSpace(name) == "" {
		return 0, fmt.Errorf("%w: provider name is required", ErrDraftInvalid)
	}
	contractJSON, err := MarshalContract(p.Contract)
	if err != nil {
		return 0, err
	}
	authType, authHeader, authPrefix := "", "", ""
	if a := p.Contract.Auth; a != nil {
		authType = strings.TrimSpace(a.Strategy)
		authPrefix = strings.TrimSpace(a.Prefix)
		if strings.TrimSpace(a.Param) != "" {
			authHeader = strings.TrimSpace(a.Param)
		} else {
			authHeader = strings.TrimSpace(a.Header)
		}
	}
	baseURL := strings.TrimSpace(p.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(p.Contract.BaseURL)
	}
	label := strings.TrimSpace(p.Label)

	// syncFunctions writes the draft's operation functions once the provider row
	// exists. It runs on both the create and the update path so a republish and
	// a first publish behave identically.
	syncFunctions := func(providerID int64) error {
		if s.functions == nil {
			if len(p.Functions) > 0 {
				return fmt.Errorf("%w: the sandbox function runtime is not available in this build", ErrDraftInvalid)
			}
			return nil
		}
		return s.functions.SyncProviderFunctions(ctx, ownerType, ownerID, providerID, name, byUser, draftID, p.Functions)
	}

	existing, err := s.providers.GetByName(ctx, name)
	if err == nil {
		if existing.CreatedBy == CreatedBySystem {
			return 0, fmt.Errorf("%w: %q is a built-in provider; a draft may not republish it", ErrDraftInvalid, name)
		}
		// A label is display metadata with no authority; a republish that does
		// not carry one must not silently rename the provider.
		if label == "" {
			label = existing.Label
		}
		if _, err := s.providers.Update(ctx, existing.ID, label, baseURL, authType, authHeader,
			authPrefix, p.Whitelist, contractJSON, p.OpenAPIURL, false); err != nil {
			return 0, err
		}
		if err := syncFunctions(existing.ID); err != nil {
			return 0, err
		}
		return existing.ID, nil
	}
	if label == "" {
		label = name
	}
	created, err := s.providers.Create(ctx, name, label, baseURL)
	if err != nil {
		return 0, err
	}
	if _, err := s.providers.Update(ctx, created.ID, label, baseURL, authType, authHeader,
		authPrefix, p.Whitelist, contractJSON, p.OpenAPIURL, false); err != nil {
		return 0, err
	}
	if err := syncFunctions(created.ID); err != nil {
		return 0, err
	}
	return created.ID, nil
}

func (s *IntegrationDraftService) applyDataSource(ctx context.Context, row store.IntegrationDraft, d DataSourceDraftPayload) (int64, error) {
	scopeUserID := dsOwnershipUserID(row.OwnerType, row.OwnerID)
	orgIDs := orgIDsForOwner(row.OwnerType, row.OwnerID)

	id, err := s.sources.ResolveSourceID(ctx, d.Name, scopeUserID, orgIDs)
	if err == nil && id > 0 {
		if err := s.sources.Update(ctx, id, scopeUserID, orgIDs, UpdateDataSourceInput{
			Name:              d.Name,
			Config:            d.Config,
			ScopeConfig:       d.ScopeConfig,
			DefaultAccessMode: d.DefaultAccessMode,
			RequireConfirm:    d.RequireConfirm,
		}); err != nil {
			return 0, err
		}
		return id, nil
	}
	created, err := s.sources.Create(ctx, CreateDataSourceInput{
		OwnerType:         row.OwnerType,
		OwnerID:           row.OwnerID,
		UserID:            row.CreatedBy,
		Name:              d.Name,
		Kind:              d.Kind,
		Config:            d.Config,
		ScopeConfig:       d.ScopeConfig,
		DefaultAccessMode: d.DefaultAccessMode,
		RequireConfirm:    d.RequireConfirm,
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

// --- versions -------------------------------------------------------------

// recordVersion appends the newly published payload to the target's version
// chain and makes it the active one.
func (s *IntegrationDraftService) recordVersion(ctx context.Context, row store.IntegrationDraft, targetID int64, payloadJSON string, byUser int64) (int64, error) {
	latest, err := s.q.LatestIntegrationVersion(ctx, store.LatestIntegrationVersionParams{
		OwnerType: row.OwnerType, OwnerID: row.OwnerID,
		Kind: row.Kind, TargetName: row.TargetName,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	next := latest + 1
	if err := s.q.DeactivateIntegrationVersions(ctx, store.DeactivateIntegrationVersionsParams{
		OwnerType: row.OwnerType, OwnerID: row.OwnerID,
		Kind: row.Kind, TargetName: row.TargetName,
	}); err != nil {
		return 0, err
	}
	v, err := s.q.CreateIntegrationVersion(ctx, store.CreateIntegrationVersionParams{
		OwnerType:     row.OwnerType,
		OwnerID:       row.OwnerID,
		Kind:          row.Kind,
		TargetName:    row.TargetName,
		TargetID:      targetID,
		Version:       next,
		Payload:       payloadJSON,
		ScopeJson:     scopeSnapshot(row.Kind, payloadJSON),
		Digest:        payloadDigest(payloadJSON),
		Active:        1,
		PublishedBy:   byUser,
		SourceDraftID: row.ID,
	})
	if err != nil {
		return 0, err
	}
	return v.Version, nil
}

// scopeSnapshot denormalizes the authority a version grants: the scope_config
// for a data source, the operation set for a provider. It is what a rollback
// diffs to decide "does restoring this re-grant authority?".
//
// For a provider it also carries the FUNCTION SCOPE (#739): which operations
// each published function may reach. A rollback that restores an older
// provider version restores its functions too, so the authority that changes
// has to be part of the same snapshot or the re-confirmation rule would miss
// it.
func scopeSnapshot(kind, payloadJSON string) string {
	switch kind {
	case DraftKindProvider:
		var p ProviderDraftPayload
		if json.Unmarshal([]byte(payloadJSON), &p) != nil || p.Contract == nil {
			return "{}"
		}
		ops := make([]OperationKey, 0, len(p.Contract.Operations))
		for _, op := range p.Contract.Operations {
			ops = append(ops, OperationKey{Method: op.Method, Path: op.Path})
		}
		b, _ := json.Marshal(map[string]any{
			"base_url":      p.BaseURL,
			"operations":    ops,
			"write_enabled": p.WriteEnabled,
			"function_scope": FunctionScopeForDraft(p.Functions),
		})
		return string(b)
	case DraftKindDataSource:
		var d DataSourceDraftPayload
		if json.Unmarshal([]byte(payloadJSON), &d) != nil {
			return "{}"
		}
		b, _ := json.Marshal(map[string]any{
			"scope_config":        d.ScopeConfig,
			"default_access_mode": d.DefaultAccessMode,
		})
		return string(b)
	}
	return "{}"
}

func payloadDigest(payloadJSON string) string {
	sum := sha256.Sum256([]byte(payloadJSON))
	return digestPrefix + hex.EncodeToString(sum[:])
}

// rollbackNeedsConfirmation reports whether restoring a version is an
// authorization action — i.e. whether it changes scope/operations or the write
// gate relative to what is live. A rollback that changes scope is exactly the
// "re-granting a revoked permission" case, so it must be re-confirmed even when
// the direction is narrowing (the user is still replacing a live policy).
func rollbackNeedsConfirmation(card ConfirmCardModel) bool {
	if len(card.Risks) > 0 {
		return true
	}
	for _, sc := range card.Scopes {
		if sc.Change != ChangeUnchanged {
			return true
		}
	}
	for _, d := range card.Diff {
		if d.Field == "operation" || strings.HasPrefix(d.Field, "scope.") {
			return true
		}
	}
	return false
}

// logRestoreFailure records a failed restore. A restore that itself fails is
// the one case the pipeline cannot resolve on its own, so it must be visible in
// the log rather than silently swallowed.
func logRestoreFailure(snap *liveSnapshot, err error) {
	slog.Error("integration draft: restoring the previous published state failed",
		"kind", snap.Kind, "target_id", snap.TargetID, "error", err)
}

// --- default verifier -----------------------------------------------------

// liveVerifier is the production DraftVerifier.
type liveVerifier struct {
	proxy     *ExternalProxyService
	sources   *DataSourceService
	providers *ExternalProviderService
}

// VerifyProvider runs the provider's read-only verification probe through the
// normal proxy path (credential resolution, auth injection, SSRF guard, audit)
// with Probe=true, so a not-yet-enabled provider is permitted and the write
// whitelist is skipped.
//
// No extra confirmation gate is applied here: the probe is resolved from the
// contract's verify_operation, which the draft lint requires and the approved
// confirmation card displayed in its decision area. Card approval IS the
// confirmation for this exact request.
func (v *liveVerifier) VerifyProvider(ctx context.Context, providerName string, userID int64) (*DraftVerifyResult, error) {
	if v.proxy == nil || v.providers == nil {
		return &DraftVerifyResult{OK: false, Detail: "provider verification is unavailable in this build"}, nil
	}
	prov, err := v.providers.GetByName(ctx, providerName)
	if err != nil {
		return &DraftVerifyResult{OK: false, Detail: fmt.Sprintf("provider %q not found", providerName)}, nil
	}
	op, source := SelectVerifyOperation(prov.Contract())
	if op == nil {
		return &DraftVerifyResult{OK: false,
			Detail: "the contract exposes no read-only operation to verify with"}, nil
	}
	manifest := BuildProbeManifest(op, source)
	res := v.proxy.runProbe(ctx, prov, op, manifest, userID, 0, "")
	return &DraftVerifyResult{OK: res.OK, Status: res.Status, Probe: manifest, Detail: res.Detail}, nil
}

// VerifyDataSource pings the source through its connector, using the same
// owner-scoped gate the settings verify button uses.
func (v *liveVerifier) VerifyDataSource(ctx context.Context, sourceID, userID int64, orgIDs []int64) (*DraftVerifyResult, error) {
	if v.sources == nil {
		return &DraftVerifyResult{OK: false, Detail: "data-source verification is unavailable in this build"}, nil
	}
	row, err := v.sources.getOwned(ctx, sourceID, userID, orgIDs)
	if err != nil {
		return &DraftVerifyResult{OK: false, Detail: err.Error()}, nil
	}
	if err := v.sources.VerifyRow(ctx, row); err != nil {
		return &DraftVerifyResult{OK: false, Detail: err.Error()}, nil
	}
	return &DraftVerifyResult{OK: true}, nil
}
