package service

// IntakeSourceService manages the three Intake-owned binding kinds —
// ci, email and webhook — stored in project_external_sources. They share the
// Intake pipeline but are deliberately isolated from the "external systems"
// settings panel: ExternalSourceService refuses to list, add or delete them so
// a binding's generated secret is never orphaned by a generic unbind.
//
// A binding's provider column is the SOURCE KIND (ci|email|webhook); the
// upstream vendor (gitlab, mailgun, ...) lives in config.provider. Only the
// email IMAP mode is an exception to "credential provider == binding provider":
// it references a pre-existing `imap` credential, which the service validates
// by performing a real LOGIN probe before the binding is written.
//
// Spec: docs/superpowers/specs/2026-11-06-intake-ci-email-webhook-design.md §3,
// §6.4, §9.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/niuniu-dev/niuniu/internal/webhooksource"
)

var (
	ErrIntakeSourceNotFound       = errors.New("intake source: binding not found")
	ErrIntakeSourceExists         = errors.New("intake source: binding already exists")
	ErrIntakeSourceInvalid        = errors.New("intake source: invalid input")
	ErrIntakeSourceNoSecret       = errors.New("intake source: binding has no rotatable secret")
	ErrIntakeSourceEmailPollerOff = errors.New("intake source: email poller is not wired")
	ErrIntakeSourceReserved       = errors.New("intake source: provider is managed by intake sources")
)

// Status values exposed to the UI (design §11.3, §12). Delivery health is
// derived from intake_events/intake_decisions plus the in-memory tracker; no
// new table is involved.
const (
	IntakeStatusNever    = "never"    // not one delivery attempt yet
	IntakeStatusOK       = "ok"       // deliveries work, no failure streak
	IntakeStatusDegraded = "degraded" // backing off, or 1-2 consecutive failures
	IntakeStatusError    = "error"    // 3+ consecutive failures, or IMAP auth failed
)

// IntakeSourceInput is the create payload. SourceKind is ci | email | webhook;
// Mode is push | imap | inbound; Provider is the upstream vendor stored in
// config.provider. CredentialID is only used by email/imap.
type IntakeSourceInput struct {
	ProjectID    int64
	SourceKind   string
	Name         string
	Mode         string
	Provider     string
	Config       map[string]any
	CredentialID int64
	CallerUserID int64
}

// IntakeSourceDTO is one binding as returned to the settings UI. The secret is
// never included; HasSecret only says one is configured.
type IntakeSourceDTO struct {
	ID          int64      `json:"id"`
	ProjectID   int64      `json:"project_id"`
	SourceKind  string     `json:"source_kind"`
	Name        string     `json:"name"`
	Mode        string     `json:"mode"`
	Provider    string     `json:"provider"`
	InboundPath string     `json:"inbound_path"`
	HasSecret   bool       `json:"has_secret"`
	Status      string     `json:"status"`
	LastEventAt *time.Time `json:"last_event_at,omitempty"`

	// Delivery health (design §12), derived from intake_events/decisions plus
	// the in-memory tracker.
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
	Events24h           int64      `json:"events_24h"`
	Success24h          int64      `json:"success_24h"`
	Duplicate24h        int64      `json:"duplicate_24h"`
	Rejected24h         int64      `json:"rejected_24h"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	Alert               string     `json:"alert,omitempty"`
	AlertReason         string     `json:"alert_reason,omitempty"`
	// LastError/BackoffUntil expose the IMAP cursor's failure state so the
	// settings dialog can explain a degraded or errored binding (design §11.3).
	LastError    string         `json:"last_error,omitempty"`
	BackoffUntil *time.Time     `json:"backoff_until,omitempty"`
	Config       map[string]any `json:"config"`
}

// IntakeSourceStatus is the per-binding health view (design §12): recent
// success/failure, 24h success/dedup/rejected, and the current backoff.
type IntakeSourceStatus struct {
	Status              string     `json:"status"`
	LastEventAt         *time.Time `json:"last_event_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
	Events24h           int64      `json:"events_24h"`
	Success24h          int64      `json:"success_24h"`
	Duplicate24h        int64      `json:"duplicate_24h"`
	Rejected24h         int64      `json:"rejected_24h"`
	LastError           string     `json:"last_error,omitempty"`
	BackoffUntil        *time.Time `json:"backoff_until,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	// Alert is the active alert reason code, e.g. secret_misconfigured.
	Alert       string `json:"alert,omitempty"`
	AlertReason string `json:"alert_reason,omitempty"`
}

// IntakeSourceHealth is one binding's delivery health in the mission control
// panel. Every field is aggregated from intake_events/intake_decisions (durable)
// or the delivery tracker (volatile 401/400/duplicate signals); there is no
// health table.
type IntakeSourceHealth struct {
	SourceID            int64      `json:"source_id"`
	ProjectID           int64      `json:"project_id"`
	Name                string     `json:"name"`
	SourceKind          string     `json:"source_kind"`
	Provider            string     `json:"provider"`
	Mode                string     `json:"mode"`
	Status              string     `json:"status"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	BackoffUntil        *time.Time `json:"backoff_until,omitempty"`
	Success24h          int64      `json:"success_24h"`
	Duplicate24h        int64      `json:"duplicate_24h"`
	Rejected24h         int64      `json:"rejected_24h"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	Alert               string     `json:"alert,omitempty"`
	AlertReason         string     `json:"alert_reason,omitempty"`
}

// IntakeSourceTestResult is the dry-run outcome of a synthetic event: the
// normalized trigger plus the policy verdict. Nothing is recorded or executed.
type IntakeSourceTestResult struct {
	SourceKind  string   `json:"source_kind"`
	EventKind   string   `json:"event_kind"`
	ActorExtID  string   `json:"actor_ext_id"`
	ThreadExtID string   `json:"thread_ext_id"`
	Text        string   `json:"text"`
	Mode        string   `json:"mode"`
	Paused      bool     `json:"paused"`
	Strength    string   `json:"strength"`
	ReasonCode  string   `json:"reason_code"`
	Allowed     bool     `json:"allowed"`
	MatchedRule []int64  `json:"matched_rule_ids,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// EmailSourcePoller is implemented by the IMAP poller (Task 4) and wired by
// server DI. PollNow stays unavailable until then instead of pretending to poll.
type EmailSourcePoller interface {
	PollBinding(ctx context.Context, bindingID int64) error
}

// IntakeSourceService owns the ci/email/webhook bindings.
type IntakeSourceService struct {
	db     *store.DB
	q      *store.Queries
	creds  *ExternalCredentialService
	authz  *Authz
	policy *CredentialUsagePolicyService

	poller   EmailSourcePoller
	delivery *DeliveryHealthTracker
}

func NewIntakeSourceService(rawDB *sql.DB, q *store.Queries, creds *ExternalCredentialService, authz *Authz, policy *CredentialUsagePolicyService) *IntakeSourceService {
	return &IntakeSourceService{
		db:     store.Wrap(rawDB),
		q:      q,
		creds:  creds,
		authz:  authz,
		policy: policy,
	}
}

// SetEmailPoller wires the IMAP poller used by PollNow.
func (s *IntakeSourceService) SetEmailPoller(p EmailSourcePoller) { s.poller = p }

// SetDeliveryHealth wires the shared in-memory delivery tracker used to explain
// 401/400/duplicate outcomes that leave no intake_events row.
func (s *IntakeSourceService) SetDeliveryHealth(t *DeliveryHealthTracker) { s.delivery = t }

func isIntakeSourceKind(kind string) bool {
	switch kind {
	case intake.SourceKindCI, intake.SourceKindEmail, intake.SourceKindWebhook:
		return true
	}
	return false
}

// List returns the project's ci/email/webhook bindings. Status is derived from
// the binding's IMAP backoff (email) and, for every kind, its recent events.
func (s *IntakeSourceService) List(ctx context.Context, callerUserID, projectID int64) ([]IntakeSourceDTO, error) {
	if err := s.authorizeProject(ctx, callerUserID, projectID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListProjectExternalSources(ctx, projectID)
	if err != nil {
		return nil, err
	}
	stats, err := s.eventStats(ctx, projectID)
	if err != nil {
		return nil, err
	}
	pollStates, err := s.q.ListEmailPollStatesByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	pollByBinding := make(map[int64]store.EmailSourcePollState, len(pollStates))
	for _, p := range pollStates {
		pollByBinding[p.BindingID] = p
	}

	now := time.Now()
	out := make([]IntakeSourceDTO, 0, len(rows))
	for _, r := range rows {
		kind := strings.ToLower(strings.TrimSpace(r.Provider))
		if !isIntakeSourceKind(kind) {
			continue
		}
		dto := s.dto(r)
		stat, hasStat := stats[kind+"|"+eventRef(r.SourceKey, r.ID)]
		if hasStat {
			dto.LastEventAt = stat.lastAt
			dto.LastSuccessAt = stat.lastSuccessAt
			dto.Events24h = stat.events24h
			dto.Success24h = stat.success24h
			dto.Rejected24h = stat.rejected24h
		}
		var snap DeliveryHealthSnapshot
		if s.delivery != nil {
			snap = s.delivery.Snapshot(r.ID)
			dto.Duplicate24h = snap.Duplicate24h
			dto.ConsecutiveFailures = snap.ConsecutiveFailures
			dto.Alert = snap.Alert
			dto.AlertReason = snap.AlertReason
			if dto.LastFailureAt == nil {
				dto.LastFailureAt = snap.LastFailureAt
			}
		}
		var ps *store.EmailSourcePollState
		if v, ok := pollByBinding[r.ID]; ok {
			ps = &v
			dto.LastError = v.LastError
			if v.BackoffUntil.Valid {
				t := v.BackoffUntil.Time
				dto.BackoffUntil = &t
			}
		}
		dto.Status = resolveIntakeStatus(hasStat || snap.Recorded, snap, ps, now)
		out = append(out, dto)
	}
	return out, nil
}

// Create validates the kind/mode/provider, then writes the binding. Push kinds
// (ci/webhook/email-inbound) get a freshly generated secret stored in an
// encrypted credential; email IMAP references the caller's existing imap
// credential after a real LOGIN probe. The plaintext secret is returned exactly
// once (empty for IMAP).
func (s *IntakeSourceService) Create(ctx context.Context, in IntakeSourceInput) (*IntakeSourceDTO, string, error) {
	kind := strings.ToLower(strings.TrimSpace(in.SourceKind))
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	name := strings.TrimSpace(in.Name)
	if !isIntakeSourceKind(kind) {
		return nil, "", fmt.Errorf("%w: source_kind must be ci, email or webhook", ErrIntakeSourceInvalid)
	}
	if !validWebhookBindingName(name) {
		return nil, "", fmt.Errorf("%w: name must be 1-64 characters of letters, digits, '-', '_' or '.'", ErrIntakeSourceInvalid)
	}
	if mode == "" {
		mode = "push"
	}
	switch kind {
	case intake.SourceKindEmail:
		if mode != "imap" && mode != "inbound" {
			return nil, "", fmt.Errorf("%w: email mode must be imap or inbound", ErrIntakeSourceInvalid)
		}
	default:
		if mode != "push" {
			return nil, "", fmt.Errorf("%w: %s mode must be push", ErrIntakeSourceInvalid, kind)
		}
	}
	if s.authz == nil || s.creds == nil {
		return nil, "", errors.New("intake source: service is not fully wired")
	}
	projOwner, err := s.authz.CanAccessProject(ctx, in.CallerUserID, in.ProjectID)
	if err != nil {
		return nil, "", err
	}
	if _, err := s.q.GetProjectExternalSourceByKey(ctx, store.GetProjectExternalSourceByKeyParams{
		ProjectID: in.ProjectID, Provider: kind, SourceKey: name,
	}); err == nil {
		return nil, "", ErrIntakeSourceExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}

	cfg := cloneConfig(in.Config)
	cfg["mode"] = mode
	if in.Provider != "" {
		cfg["provider"] = strings.TrimSpace(in.Provider)
	}
	if err := validateNetworkConfig(cfg); err != nil {
		return nil, "", err
	}
	if kind == intake.SourceKindCI {
		// The provider chooses the auth preset and the default subscription.
		// Binding it at create time keeps credentials from pointing at a vendor
		// the inbound path cannot normalize.
		providerName := strings.TrimSpace(in.Provider)
		if providerName == "" {
			if v, ok := cfg["provider"].(string); ok {
				providerName = v
			}
		}
		preset, ok := webhooksource.CIProviderPresetFor(providerName)
		if !ok {
			return nil, "", fmt.Errorf("%w: unknown ci provider %q", ErrIntakeSourceInvalid, providerName)
		}
		cfg["provider"] = preset.Provider
		if _, exists := cfg["events"]; !exists {
			cfg["events"] = preset.DefaultEvents
		}
	}

	if kind == intake.SourceKindEmail && mode == "inbound" {
		// The vendor preset only chooses the preferred body field, but binding it
		// at create time keeps a binding from naming a provider the inbound path
		// does not recognize.
		providerName := strings.TrimSpace(in.Provider)
		if providerName == "" {
			if v, ok := cfg["provider"].(string); ok {
				providerName = v
			}
		}
		preset, ok := webhooksource.EmailProviderPresetFor(providerName)
		if !ok {
			return nil, "", fmt.Errorf("%w: unknown email provider %q", ErrIntakeSourceInvalid, providerName)
		}
		cfg["provider"] = preset.Provider
		if _, exists := cfg["events"]; !exists {
			cfg["events"] = []string{intake.EventKindMessage}
		}
	}

	if kind == intake.SourceKindEmail && mode == "imap" {
		return s.createIMAPSource(ctx, in, projOwner, cfg)
	}
	return s.createPushSource(ctx, in, kind, mode, cfg)
}

func (s *IntakeSourceService) createPushSource(ctx context.Context, in IntakeSourceInput, kind, mode string, cfg map[string]any) (*IntakeSourceDTO, string, error) {
	secret, err := randomSecret()
	if err != nil {
		return nil, "", err
	}
	suffix, err := randomHex(6)
	if err != nil {
		return nil, "", err
	}
	cred, err := s.creds.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: "user",
		OwnerID:   in.CallerUserID,
		UserID:    in.CallerUserID,
		Provider:  integration.ProviderName(kind),
		Alias:     intakeCredentialAlias(strings.TrimSpace(in.Name), suffix),
		RawConfig: map[string]any{webhookSecretKey: secret},
	})
	if err != nil {
		return nil, "", err
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		_ = s.creds.DeleteByID(ctx, cred.ID, "user", in.CallerUserID, in.CallerUserID)
		return nil, "", fmt.Errorf("marshal config: %w", err)
	}

	// The binding and, for CI, its seeded rule + policy version land together so
	// a half-created binding never evaluates against an empty policy.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		_ = s.creds.DeleteByID(ctx, cred.ID, "user", in.CallerUserID, in.CallerUserID)
		return nil, "", err
	}
	defer func() { _ = tx.Rollback() }()

	// Roll back before cleaning up the credential: SQLite holds the write lock
	// until the transaction ends, so a delete on another connection would fail.
	fail := func(cause error) (*IntakeSourceDTO, string, error) {
		_ = tx.Rollback()
		_ = s.creds.DeleteByID(ctx, cred.ID, "user", in.CallerUserID, in.CallerUserID)
		return nil, "", cause
	}

	qtx := tx.Queries()
	row, err := qtx.AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
		ProjectID:    in.ProjectID,
		Provider:     kind,
		SourceKey:    strings.TrimSpace(in.Name),
		CredentialID: sql.NullInt64{Int64: cred.ID, Valid: true},
		Config:       string(cfgJSON),
	})
	if err != nil {
		return fail(err)
	}
	if kind == intake.SourceKindCI {
		seeded, err := seedCIFailureRule(ctx, qtx, in.ProjectID)
		if err != nil {
			return fail(err)
		}
		if seeded {
			if err := writeIntakePolicyVersion(ctx, qtx, in.ProjectID, intake.SourceKindCI); err != nil {
				return fail(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	dto := s.dto(row)
	return &dto, secret, nil
}

const (
	ciDefaultRuleName        = "CI 失败自动修复"
	ciDefaultRuleInstruction = "定位 CI 失败原因，提交修复，确认同一流水线通过"
)

// seedCIFailureRule adds the out-of-the-box repair rule once per project, so a
// new CI binding is not born with an empty rule set. Seeding does not enable
// execution: the source mode stays observe until the user switches it.
func seedCIFailureRule(ctx context.Context, qtx *store.Queries, projectID int64) (bool, error) {
	rules, err := qtx.ListIntakeRulesByProject(ctx, projectID)
	if err != nil {
		return false, err
	}
	for _, r := range rules {
		if r.SourceKind == intake.SourceKindCI && r.Name == ciDefaultRuleName {
			return false, nil
		}
	}
	conditions, err := json.Marshal(intake.RuleConditions{EventKind: intake.EventKindCIFailed})
	if err != nil {
		return false, err
	}
	if _, err := qtx.CreateIntakeRule(ctx, store.CreateIntakeRuleParams{
		ProjectID:      projectID,
		SourceKind:     intake.SourceKindCI,
		Name:           ciDefaultRuleName,
		Instruction:    ciDefaultRuleInstruction,
		ConditionsJson: string(conditions),
		Priority:       100,
		Enabled:        1,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *IntakeSourceService) createIMAPSource(ctx context.Context, in IntakeSourceInput, projOwner OwnerRef, cfg map[string]any) (*IntakeSourceDTO, string, error) {
	if in.CredentialID <= 0 {
		return nil, "", fmt.Errorf("%w: email imap requires credential_id", ErrIntakeSourceInvalid)
	}
	meta, err := s.creds.GetMetaByID(ctx, in.CredentialID)
	if err != nil {
		return nil, "", fmt.Errorf("credential lookup: %w", err)
	}
	if meta.Provider != integration.ProviderName(imapProviderName) {
		return nil, "", ErrCredentialProviderMismatch
	}
	if err := credentialBindableBy(ctx, meta, projOwner, in.ProjectID, in.CallerUserID, s.policy); err != nil {
		return nil, "", err
	}
	// Bind-time login probe (design §6.4): a credential that cannot log in must
	// never produce a binding, otherwise the poller would fail every minute.
	if err := s.creds.VerifyImapByID(ctx, meta.ID, meta.OwnerType, meta.OwnerID, meta.UserID); err != nil {
		return nil, "", err
	}
	mailbox := "INBOX"
	if v, ok := cfg["mailbox"].(string); ok && strings.TrimSpace(v) != "" {
		mailbox = strings.TrimSpace(v)
	}
	cfg["mailbox"] = mailbox
	if _, ok := cfg["poll_interval_sec"]; !ok {
		cfg["poll_interval_sec"] = 60
	}
	if _, ok := cfg["ignore_auto"]; !ok {
		cfg["ignore_auto"] = true
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("marshal config: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := tx.Queries()
	row, err := qtx.AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
		ProjectID:    in.ProjectID,
		Provider:     intake.SourceKindEmail,
		SourceKey:    strings.TrimSpace(in.Name),
		CredentialID: sql.NullInt64{Int64: meta.ID, Valid: true},
		Config:       string(cfgJSON),
	})
	if err != nil {
		return nil, "", err
	}
	if _, err := qtx.UpsertEmailPollState(ctx, store.UpsertEmailPollStateParams{
		ProjectID: in.ProjectID,
		BindingID: row.ID,
		Mailbox:   mailbox,
	}); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	dto := s.dto(row)
	return &dto, "", nil
}

// Delete removes the binding, its poll cursor, and the credential it OWNS.
// A referenced imap credential belongs to the user and is left untouched — only
// generated ci/email/webhook secrets are cleaned up.
func (s *IntakeSourceService) Delete(ctx context.Context, callerUserID, projectID, sourceID int64) error {
	if err := s.authorizeProject(ctx, callerUserID, projectID); err != nil {
		return err
	}
	row, err := s.loadBinding(ctx, projectID, sourceID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM email_source_poll_state WHERE binding_id = ?`, sourceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_external_sources WHERE id = ? AND project_id = ?`, sourceID, projectID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.delivery != nil {
		s.delivery.Purge(sourceID)
	}
	if s.creds != nil && row.CredentialID.Valid && row.CredentialID.Int64 > 0 {
		if meta, err := s.creds.GetMetaByID(ctx, row.CredentialID.Int64); err == nil {
			if meta.Provider != integration.ProviderName(imapProviderName) {
				_ = s.creds.DeleteByID(ctx, meta.ID, meta.OwnerType, meta.OwnerID, meta.UserID)
			}
		}
	}
	return nil
}

// RotateSecret replaces a generated secret; the previous one stops working
// immediately because the credential config is overwritten wholesale. IMAP
// bindings have no service-generated secret to rotate.
func (s *IntakeSourceService) RotateSecret(ctx context.Context, callerUserID, projectID, sourceID int64) (string, error) {
	if err := s.authorizeProject(ctx, callerUserID, projectID); err != nil {
		return "", err
	}
	row, err := s.loadBinding(ctx, projectID, sourceID)
	if err != nil {
		return "", err
	}
	if !row.CredentialID.Valid || row.CredentialID.Int64 <= 0 {
		return "", ErrIntakeSourceNoSecret
	}
	meta, err := s.creds.GetMetaByID(ctx, row.CredentialID.Int64)
	if err != nil {
		return "", err
	}
	if meta.Provider == integration.ProviderName(imapProviderName) {
		return "", ErrIntakeSourceNoSecret
	}
	secret, err := randomSecret()
	if err != nil {
		return "", err
	}
	if _, err := s.creds.UpdateConfig(ctx, meta.ID, meta.OwnerType, meta.OwnerID, meta.UserID,
		map[string]any{webhookSecretKey: secret}); err != nil {
		return "", err
	}
	return secret, nil
}

// TestDelivery normalizes a synthetic event and evaluates the project policy
// WITHOUT recording it, reserving a daily slot, or executing anything.
func (s *IntakeSourceService) TestDelivery(ctx context.Context, callerUserID, projectID, sourceID int64, sample []byte) (*IntakeSourceTestResult, error) {
	if err := s.authorizeProject(ctx, callerUserID, projectID); err != nil {
		return nil, err
	}
	row, err := s.loadBinding(ctx, projectID, sourceID)
	if err != nil {
		return nil, err
	}
	kind := strings.ToLower(strings.TrimSpace(row.Provider))
	ref := eventRef(row.SourceKey, row.ID)
	if len(sample) == 0 {
		sample, _ = json.Marshal(map[string]any{
			"text":  "Test delivery from " + row.SourceKey,
			"actor": "intake-test",
			"event": "test",
		})
	}

	var warnings []string
	trig, err := normalizeTestTrigger(kind, configString(row.Config, "provider"), projectID, ref, sample)
	if err != nil {
		// A non-JSON sample is treated as raw text so the policy can still be
		// exercised from the UI's free-text field.
		trig = intake.Trigger{
			ProjectID:  projectID,
			SourceKind: kind,
			SourceRef:  ref,
			EventID:    "test",
			EventKind:  intake.EventKindMessage,
			Text:       strings.TrimSpace(string(sample)),
			ReceiveAll: true,
		}
		warnings = append(warnings, fmt.Sprintf("sample is not a valid %s payload; evaluated as plain text", kind))
	}
	trig.ProjectID = projectID
	trig.SourceRef = ref
	trig.ReceivedAt = time.Now()

	pol, err := (imbotPolicy{q: s.q}).LoadPolicy(ctx, projectID, kind)
	if err != nil {
		return nil, err
	}
	authz := pol.Evaluate(trig)
	return &IntakeSourceTestResult{
		SourceKind:  trig.SourceKind,
		EventKind:   trig.EventKind,
		ActorExtID:  trig.Actor.ExtID,
		ThreadExtID: trig.Thread.ThreadExtID,
		Text:        trig.Text,
		Mode:        string(pol.Mode),
		Paused:      pol.Paused,
		Strength:    string(authz.Strength),
		ReasonCode:  authz.ReasonCode,
		Allowed:     authz.Strength != intake.StrengthDenied,
		MatchedRule: authz.MatchedRuleIDs,
		Warnings:    warnings,
	}, nil
}

// PollNow triggers one immediate IMAP poll. Only email/imap bindings qualify.
func (s *IntakeSourceService) PollNow(ctx context.Context, callerUserID, projectID, sourceID int64) error {
	if err := s.authorizeProject(ctx, callerUserID, projectID); err != nil {
		return err
	}
	row, err := s.loadBinding(ctx, projectID, sourceID)
	if err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(row.Provider)) != intake.SourceKindEmail ||
		configString(row.Config, "mode") != "imap" {
		return fmt.Errorf("%w: poll is only available for email bindings in imap mode", ErrIntakeSourceInvalid)
	}
	if s.poller == nil {
		return ErrIntakeSourceEmailPollerOff
	}
	return s.poller.PollBinding(ctx, sourceID)
}

// Health returns one binding's delivery health (design §12).
func (s *IntakeSourceService) Health(ctx context.Context, callerUserID, projectID, sourceID int64) (IntakeSourceStatus, error) {
	if err := s.authorizeProject(ctx, callerUserID, projectID); err != nil {
		return IntakeSourceStatus{}, err
	}
	row, err := s.loadBinding(ctx, projectID, sourceID)
	if err != nil {
		return IntakeSourceStatus{}, err
	}
	stats, err := s.eventStats(ctx, projectID)
	if err != nil {
		return IntakeSourceStatus{}, err
	}
	ps, err := s.pollStateFor(ctx, row)
	if err != nil {
		return IntakeSourceStatus{}, err
	}
	stat, hasStat := stats[strings.ToLower(strings.TrimSpace(row.Provider))+"|"+eventRef(row.SourceKey, row.ID)]
	snap := withPersistedIMAPAlert(s.snapshot(row.ID), ps)
	now := time.Now()
	st := IntakeSourceStatus{
		Status:              resolveIntakeStatus(hasStat || snap.Recorded, snap, ps, now),
		LastEventAt:         stat.lastAt,
		LastSuccessAt:       stat.lastSuccessAt,
		Events24h:           stat.events24h,
		Success24h:          stat.success24h,
		Rejected24h:         stat.rejected24h,
		Duplicate24h:        snap.Duplicate24h,
		ConsecutiveFailures: snap.ConsecutiveFailures,
		Alert:               snap.Alert,
		AlertReason:         snap.AlertReason,
	}
	st.LastFailureAt = laterTime(snap.LastFailureAt, pollFailureAt(ps))
	if ps != nil {
		st.LastError = ps.LastError
		if ps.BackoffUntil.Valid {
			t := ps.BackoffUntil.Time
			st.BackoffUntil = &t
		}
	}
	return st, nil
}

// HealthByProject returns delivery health for every intake binding in a
// project. It does not re-check caller access: mission control has already
// scoped the project to the caller's accessible set.
func (s *IntakeSourceService) HealthByProject(ctx context.Context, projectID int64) ([]IntakeSourceHealth, error) {
	rows, err := s.q.ListProjectExternalSources(ctx, projectID)
	if err != nil {
		return nil, err
	}
	stats, err := s.eventStats(ctx, projectID)
	if err != nil {
		return nil, err
	}
	pollStates, err := s.q.ListEmailPollStatesByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	pollByBinding := make(map[int64]store.EmailSourcePollState, len(pollStates))
	for _, p := range pollStates {
		pollByBinding[p.BindingID] = p
	}

	now := time.Now()
	out := make([]IntakeSourceHealth, 0, len(rows))
	for _, r := range rows {
		kind := strings.ToLower(strings.TrimSpace(r.Provider))
		if !isIntakeSourceKind(kind) {
			continue
		}
		stat, hasStat := stats[kind+"|"+eventRef(r.SourceKey, r.ID)]
		var ps *store.EmailSourcePollState
		if v, ok := pollByBinding[r.ID]; ok {
			ps = &v
		}
		snap := withPersistedIMAPAlert(s.snapshot(r.ID), ps)
		h := IntakeSourceHealth{
			SourceID:            r.ID,
			ProjectID:           r.ProjectID,
			Name:                r.SourceKey,
			SourceKind:          kind,
			Provider:            configString(r.Config, "provider"),
			Mode:                configString(r.Config, "mode"),
			Status:              resolveIntakeStatus(hasStat || snap.Recorded, snap, ps, now),
			LastSuccessAt:       stat.lastSuccessAt,
			LastFailureAt:       laterTime(snap.LastFailureAt, pollFailureAt(ps)),
			Success24h:          stat.success24h,
			Duplicate24h:        snap.Duplicate24h,
			Rejected24h:         stat.rejected24h,
			ConsecutiveFailures: snap.ConsecutiveFailures,
			Alert:               snap.Alert,
			AlertReason:         snap.AlertReason,
		}
		if ps != nil {
			h.LastError = ps.LastError
			if ps.BackoffUntil.Valid {
				t := ps.BackoffUntil.Time
				h.BackoffUntil = &t
			}
		}
		out = append(out, h)
	}
	return out, nil
}

func (s *IntakeSourceService) snapshot(bindingID int64) DeliveryHealthSnapshot {
	if s.delivery == nil {
		return DeliveryHealthSnapshot{}
	}
	return s.delivery.Snapshot(bindingID)
}

func (s *IntakeSourceService) pollStateFor(ctx context.Context, row store.ProjectExternalSource) (*store.EmailSourcePollState, error) {
	if strings.ToLower(strings.TrimSpace(row.Provider)) != intake.SourceKindEmail {
		return nil, nil
	}
	ps, err := s.q.GetEmailPollState(ctx, store.GetEmailPollStateParams{
		BindingID: row.ID,
		Mailbox:   configString(row.Config, "mailbox"),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ps, nil
}

// withPersistedIMAPAlert re-derives an IMAP auth alert from the persisted
// cursor error when the in-memory tracker no longer has it (e.g. after a
// restart). The error string is produced by ErrIMAPAuth and contains no secret.
func withPersistedIMAPAlert(snap DeliveryHealthSnapshot, ps *store.EmailSourcePollState) DeliveryHealthSnapshot {
	if snap.Alert == "" && ps != nil && strings.Contains(ps.LastError, "imap login rejected") {
		snap.Alert = DeliveryAlertIMAPAuth
		snap.AlertReason = string(DeliveryIMAPAuthFailed)
	}
	return snap
}

// pollFailureAt is the persisted cursor's last update; it is only a failure
// time when the cursor actually holds an error.
func pollFailureAt(ps *store.EmailSourcePollState) *time.Time {
	if ps == nil || strings.TrimSpace(ps.LastError) == "" || ps.UpdatedAt.IsZero() {
		return nil
	}
	t := ps.UpdatedAt
	return &t
}

func laterTime(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	}
	return a
}

// resolveIntakeStatus applies the design §12 status rules. hasActivity is true
// when the binding has ever recorded an event or a tracker outcome; a binding
// that has seen neither is `never` rather than a healthy-looking `ok`.
func resolveIntakeStatus(hasActivity bool, snap DeliveryHealthSnapshot, ps *store.EmailSourcePollState, now time.Time) string {
	if snap.Alert == DeliveryAlertIMAPAuth {
		return IntakeStatusError
	}
	if ps != nil {
		if ps.BackoffUntil.Valid && ps.BackoffUntil.Time.After(now) {
			return IntakeStatusDegraded
		}
		if strings.TrimSpace(ps.LastError) != "" {
			return IntakeStatusError
		}
	}
	if snap.ConsecutiveFailures >= deliveryAlertStreak {
		return IntakeStatusError
	}
	if snap.ConsecutiveFailures > 0 {
		return IntakeStatusDegraded
	}
	if hasActivity {
		return IntakeStatusOK
	}
	return IntakeStatusNever
}

// --- internals --------------------------------------------------------------

// authorizeProject is the service-layer access gate shared by every read and
// mutation: the binding id alone must never be enough to inspect, rotate, test
// or delete another project's source.
func (s *IntakeSourceService) authorizeProject(ctx context.Context, callerUserID, projectID int64) error {
	if s.authz == nil {
		return errors.New("intake source: service is not fully wired")
	}
	_, err := s.authz.CanAccessProject(ctx, callerUserID, projectID)
	return err
}

type eventStat struct {
	lastAt        *time.Time
	lastSuccessAt *time.Time
	events24h     int64
	success24h    int64
	rejected24h   int64
}

// eventStats aggregates the project's intake events per (source_kind,
// source_ref), joined to their decision so success and rejection can be
// separated. source_ref is "<name>/<binding id>" for every intake binding, so
// two projects can reuse a binding name without mixing their counters.
//
// A success is a decision that accepted or resumed an event; a rejection is a
// policy ignore. Duplicates never reach this table, so they are counted by the
// in-memory tracker instead.
func (s *IntakeSourceService) eventStats(ctx context.Context, projectID int64) (map[string]eventStat, error) {
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.source_kind, e.source_ref, MAX(e.received_at),
		       SUM(CASE WHEN e.received_at >= ? THEN 1 ELSE 0 END),
		       MAX(CASE WHEN d.decision IN ('accept','resume') THEN e.received_at END),
		       COUNT(DISTINCT CASE WHEN e.received_at >= ? AND d.decision IN ('accept','resume') THEN e.id END),
		       COUNT(DISTINCT CASE WHEN e.received_at >= ? AND d.decision = 'ignore' THEN e.id END)
		FROM intake_events e
		LEFT JOIN intake_decisions d ON d.event_id = e.id
		WHERE e.project_id = ?
		GROUP BY e.source_kind, e.source_ref`, cutoff, cutoff, cutoff, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]eventStat)
	for rows.Next() {
		var kind, ref string
		var last, lastSuccess any
		var n24, ok24, rejected24 sql.NullInt64
		if err := rows.Scan(&kind, &ref, &last, &n24, &lastSuccess, &ok24, &rejected24); err != nil {
			return nil, err
		}
		out[kind+"|"+ref] = eventStat{
			lastAt:        asTimePtr(last),
			lastSuccessAt: asTimePtr(lastSuccess),
			events24h:     n24.Int64,
			success24h:    ok24.Int64,
			rejected24h:   rejected24.Int64,
		}
	}
	return out, rows.Err()
}

func (s *IntakeSourceService) loadBinding(ctx context.Context, projectID, sourceID int64) (store.ProjectExternalSource, error) {
	row, err := s.q.GetProjectExternalSource(ctx, sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ProjectExternalSource{}, ErrIntakeSourceNotFound
	}
	if err != nil {
		return store.ProjectExternalSource{}, err
	}
	if row.ProjectID != projectID || !isIntakeSourceKind(strings.ToLower(strings.TrimSpace(row.Provider))) {
		return store.ProjectExternalSource{}, ErrIntakeSourceNotFound
	}
	return row, nil
}

func (s *IntakeSourceService) dto(r store.ProjectExternalSource) IntakeSourceDTO {
	cfg := map[string]any{}
	if r.Config != "" {
		_ = json.Unmarshal([]byte(r.Config), &cfg)
	}
	kind := strings.ToLower(strings.TrimSpace(r.Provider))
	return IntakeSourceDTO{
		ID:          r.ID,
		ProjectID:   r.ProjectID,
		SourceKind:  kind,
		Name:        r.SourceKey,
		Mode:        configString(r.Config, "mode"),
		Provider:    configString(r.Config, "provider"),
		InboundPath: intakeInboundPath(kind, r.ID),
		HasSecret:   r.CredentialID.Valid && r.CredentialID.Int64 > 0,
		Status:      IntakeStatusOK,
		Config:      cfg,
	}
}

func intakeInboundPath(kind string, bindingID int64) string {
	return fmt.Sprintf("/api/integrations/%s/%d", kind, bindingID)
}

func eventRef(name string, bindingID int64) string {
	return fmt.Sprintf("%s/%d", name, bindingID)
}

func configString(rawConfig, key string) string {
	if rawConfig == "" {
		return ""
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(rawConfig), &cfg); err != nil {
		return ""
	}
	if v, ok := cfg[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func cloneConfig(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+3)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// intakeCredentialAlias keeps the alias within the credential service's 40-char
// limit: "intake-" + name (truncated) + "-" + 6-char suffix.
func intakeCredentialAlias(name, suffix string) string {
	const prefix = "intake-"
	room := 40 - len(prefix) - 1 - len(suffix)
	if room < 0 {
		room = 0
	}
	if len(name) > room {
		name = name[:room]
	}
	return prefix + name + "-" + suffix
}

// credentialBindableBy applies the binding ownership rules used by external
// sources: a personal credential must belong to the caller; an org credential
// must belong to the project's org and, when a policy layer is wired, be
// explicitly granted to this project. This is the ONLY place where binding and
// credential providers differ (email binding -> imap credential).
func credentialBindableBy(ctx context.Context, meta *CredentialMeta, projOwner OwnerRef, projectID, callerUserID int64, policy *CredentialUsagePolicyService) error {
	switch meta.OwnerType {
	case "user":
		if meta.OwnerID != callerUserID || meta.UserID != callerUserID {
			return ErrCredentialNotBindable
		}
	case "org":
		if !(projOwner.Type == "org" && projOwner.ID == meta.OwnerID) {
			return ErrCredentialNotBindable
		}
		if policy != nil {
			allowed, err := policy.IsProjectAuthorized(ctx, meta.ID, projectID)
			if err != nil {
				return fmt.Errorf("usage policy check: %w", err)
			}
			if !allowed {
				return ErrCredentialNotGranted
			}
		}
	default:
		return ErrCredentialNotBindable
	}
	return nil
}

// normalizeTestTrigger runs the same adapter the real inbound path uses. A CI
// sample that normalizes to none of the CI kinds falls back to the generic shape
// so the UI's free-text test field still exercises the policy.
func normalizeTestTrigger(kind, provider string, projectID int64, ref string, raw []byte) (intake.Trigger, error) {
	var (
		trig intake.Trigger
		err  error
	)
	switch kind {
	case intake.SourceKindEmail:
		var res webhooksource.EmailNormalizeResult
		res, err = webhooksource.NormalizeEmailMessage(projectID, ref, raw,
			webhooksource.EmailNormalizeOptions{Provider: provider})
		trig = res.Trigger
	case intake.SourceKindWebhook:
		trig, err = webhooksource.NormalizeGeneric(projectID, ref, raw)
	case intake.SourceKindCI:
		var ok bool
		trig, ok, err = webhooksource.NormalizeCI(projectID, ref, provider, raw)
		if err == nil && !ok {
			trig, err = webhooksource.NormalizeGeneric(projectID, ref, raw)
			trig.SourceKind = intake.SourceKindCI
		}
	default:
		return intake.Trigger{}, fmt.Errorf("%w: unknown source kind %q", ErrIntakeSourceInvalid, kind)
	}
	return trig, err
}
