package service

// Inbound webhook Sources for the team edition (Epic #755 / design §6.10):
// GitHub webhook deliveries and the email / generic webhook adapters.
//
// A delivery is authenticated by the per-binding secret (GitHub HMAC-SHA256, or
// a bearer token for email/generic), normalized into the same intake.Trigger
// the polling adapter produces, and handed to the shared Intake pipeline. The
// pipeline owns dedupe, actor authorization, the daily cap, decisions and
// provenance — a webhook never bypasses scope or audit.
//
// GitHub webhook event ids deliberately match the polling adapter's
// (issue_comment:<id>, pull_request_review:<id>, ...), so the same upstream
// event delivered by webhook and by polling lands exactly once.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/niuniu-dev/niuniu/internal/githubsource"
	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/niuniu-dev/niuniu/internal/webhooksource"
)

var (
	// ErrIntakeWebhookUnauthorized is returned for a signature/token failure. The HTTP
	// layer maps it to 401 so a forged delivery is rejected outright.
	ErrIntakeWebhookUnauthorized = errors.New("webhook source: unauthorized")
	// ErrWebhookNotFound covers an unknown binding AND a provider/route mismatch;
	// both look identical to a caller so binding ids cannot be probed.
	ErrWebhookNotFound = errors.New("webhook source: binding not found")
	// ErrWebhookBadRequest is a payload/normalization error.
	ErrWebhookBadRequest = errors.New("webhook source: bad request")
)

const webhookSecretKey = "webhook_secret"

// InboundOutcome describes what a delivery did. Ignored deliveries (an event
// type that is not enabled, a deleted comment, a non-final check run) are
// acknowledged with 200 and record nothing — the same behaviour as polling.
type InboundOutcome struct {
	Ignored    bool
	Duplicate  bool
	Decision   intake.Decision
	ReasonCode string
	Executed   bool
	EventID    int64
}

// WebhookSourceService owns the inbound webhook Sources and the intake
// pipeline they feed.
type WebhookSourceService struct {
	db     *store.DB
	q      *store.Queries
	creds  *ExternalCredentialService
	intake *intake.Service

	mu        sync.RWMutex
	dispatch  TaskRouter
	deliverer MessageDeliverer
}

// NewWebhookSourceService builds the adapters. It performs no I/O until a
// delivery arrives.
func NewWebhookSourceService(rawDB *sql.DB, q *store.Queries, creds *ExternalCredentialService) *WebhookSourceService {
	s := &WebhookSourceService{db: store.Wrap(rawDB), q: q, creds: creds}
	s.intake = intake.NewService(intake.Deps{
		Enabled:  true,
		Store:    imbotIntakeStore{q: q},
		Policies: imbotPolicy{q: q},
		Resolver: webhookResolver{db: s.db},
		Executor: webhookExecutor{svc: s},
	})
	return s
}

// SetInbound wires the shared task router and agent deliverer (server DI).
func (s *WebhookSourceService) SetInbound(dispatch TaskRouter, deliverer MessageDeliverer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispatch = dispatch
	s.deliverer = deliverer
}

func (s *WebhookSourceService) router() TaskRouter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dispatch
}

func (s *WebhookSourceService) messageDeliverer() MessageDeliverer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deliverer
}

// HandleInbound authenticates and processes one delivery for a binding.
// expectedProvider is "" to accept either email or generic webhook, or a
// concrete kind to pin a route (the GitHub path pins "github").
func (s *WebhookSourceService) HandleInbound(
	ctx context.Context, bindingID int64, expectedProvider string, r *http.Request, body []byte,
) (InboundOutcome, error) {
	binding, err := s.loadBinding(ctx, bindingID)
	if err != nil {
		return InboundOutcome{}, err
	}
	if expectedProvider != "" && binding.provider != expectedProvider {
		return InboundOutcome{}, ErrWebhookNotFound
	}

	secret, err := s.bindingSecret(ctx, binding)
	if err != nil {
		return InboundOutcome{}, err
	}

	var trig intake.Trigger
	var ok bool
	switch binding.provider {
	case intake.SourceKindGitHub:
		if !githubsource.SignatureValid(secret, body, r.Header.Get("X-Hub-Signature-256")) {
			return InboundOutcome{}, ErrIntakeWebhookUnauthorized
		}
		eventType := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
		kind, known := githubsource.WebhookEventKind(eventType)
		if !known || !githubEventEnabled(binding.config, kind) {
			return InboundOutcome{Ignored: true}, nil
		}
		trig, ok, err = githubsource.NormalizeWebhook(binding.projectID, binding.name, eventType, body)
		if err != nil {
			return InboundOutcome{}, fmt.Errorf("%w: %v", ErrWebhookBadRequest, err)
		}
	case intake.SourceKindEmail, intake.SourceKindWebhook:
		if !genericSecretValid(secret, r, body) {
			return InboundOutcome{}, ErrIntakeWebhookUnauthorized
		}
		// intake_events is unique on (source_kind, source_ref, event_ext_id)
		// without project_id, so the SourceRef must identify the BINDING, not just
		// its display name — otherwise two projects naming a binding "support"
		// would cross-dedupe each other's events.
		ref := fmt.Sprintf("%s/%d", binding.name, binding.id)
		if binding.provider == intake.SourceKindEmail {
			trig, err = webhooksource.NormalizeEmail(binding.projectID, ref, body)
		} else {
			trig, err = webhooksource.NormalizeGeneric(binding.projectID, ref, body)
		}
		if err != nil {
			return InboundOutcome{}, fmt.Errorf("%w: %v", ErrWebhookBadRequest, err)
		}
		ok = true
	default:
		return InboundOutcome{}, ErrWebhookNotFound
	}
	if !ok {
		return InboundOutcome{Ignored: true}, nil
	}

	res, err := s.intake.Handle(ctx, trig)
	if err != nil {
		return InboundOutcome{}, err
	}
	return InboundOutcome{
		Duplicate:  res.Duplicate,
		Decision:   res.Decision,
		ReasonCode: res.ReasonCode,
		Executed:   res.Executed,
		EventID:    res.EventID,
	}, nil
}

type webhookBinding struct {
	id         int64
	projectID  int64
	provider   string
	name       string
	credential sql.NullInt64
	config     string
}

func (s *WebhookSourceService) loadBinding(ctx context.Context, bindingID int64) (webhookBinding, error) {
	if bindingID <= 0 {
		return webhookBinding{}, ErrWebhookNotFound
	}
	var b webhookBinding
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, provider, source_key, credential_id, config
		FROM project_external_sources WHERE id = ?`, bindingID,
	).Scan(&b.id, &b.projectID, &b.provider, &b.name, &b.credential, &b.config)
	if errors.Is(err, sql.ErrNoRows) {
		return webhookBinding{}, ErrWebhookNotFound
	}
	if err != nil {
		return webhookBinding{}, err
	}
	b.provider = strings.ToLower(strings.TrimSpace(b.provider))
	b.name = strings.TrimSpace(b.name)
	return b, nil
}

// bindingSecret loads the encryption-at-rest credential and extracts its
// webhook secret. A binding with no credential or no secret fails closed: an
// unauthenticated webhook is never accepted.
func (s *WebhookSourceService) bindingSecret(ctx context.Context, b webhookBinding) (string, error) {
	if s.creds == nil || !b.credential.Valid || b.credential.Int64 <= 0 {
		return "", ErrIntakeWebhookUnauthorized
	}
	cred, err := s.creds.GetBoundByID(ctx, b.credential.Int64)
	if err != nil {
		return "", ErrIntakeWebhookUnauthorized
	}
	keys := []string{webhookSecretKey, "secret"}
	if b.provider != intake.SourceKindGitHub {
		// The generic Source has no API token of its own, so a credential created
		// with the conventional `token` field is accepted as the shared secret.
		keys = append(keys, "token")
	}
	for _, k := range keys {
		if v, ok := cred.Config[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", ErrIntakeWebhookUnauthorized
}

// genericSecretValid accepts an HMAC signature (when present) or a bearer token
// in Authorization / X-Webhook-Token. Comparison is constant-time.
func genericSecretValid(secret string, r *http.Request, body []byte) bool {
	if secret == "" {
		return false
	}
	if sig := r.Header.Get("X-Webhook-Signature-256"); strings.TrimSpace(sig) != "" {
		return githubsource.SignatureValid(secret, body, sig)
	}
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-Webhook-Token"))
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1
}

func bearerToken(header string) string {
	const prefix = "bearer "
	h := strings.TrimSpace(header)
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// githubEventEnabled reports whether the binding's configured event set
// includes kind. An empty/absent list means all known events (the same default
// the polling adapter uses).
func githubEventEnabled(rawConfig, kind string) bool {
	events := githubEventsFromConfig(rawConfig)
	if len(events) == 0 {
		return githubsource.IsKnownEvent(kind)
	}
	for _, e := range events {
		if e == kind {
			return true
		}
	}
	return false
}

// --- Resolver ---------------------------------------------------------------

// webhookResolver maps a thread to the task it belongs to via issue provenance
// (external_source + external_id). A bound thread resumes; anything else routes
// a new task through the same policy as every other Source.
type webhookResolver struct{ db *store.DB }

func (r webhookResolver) Resolve(ctx context.Context, t intake.Trigger) (intake.Resolution, error) {
	thread := t.Thread
	extID := webhookExternalID(t.SourceRef, thread.ThreadExtID)
	if extID == "" {
		return intake.Resolution{Thread: thread}, nil
	}
	var issueID int64
	err := r.db.QueryRowContext(ctx, `
		SELECT i.id
		FROM issues i
		JOIN columns c ON c.id = i.column_id
		WHERE c.project_id = ? AND i.external_source = ? AND i.external_id = ?
		ORDER BY i.id DESC LIMIT 1`,
		t.ProjectID, t.SourceKind, extID,
	).Scan(&issueID)
	if errors.Is(err, sql.ErrNoRows) {
		return intake.Resolution{Thread: thread}, nil
	}
	if err != nil {
		return intake.Resolution{}, err
	}
	workspaceID, err := activeWorkspaceForIssue(ctx, r.db, issueID)
	if err != nil {
		return intake.Resolution{}, err
	}
	thread.IssueID, thread.WorkspaceID = issueID, workspaceID
	return intake.Resolution{
		Thread:      thread,
		IssueID:     issueID,
		WorkspaceID: workspaceID,
		Bound:       workspaceID != 0,
	}, nil
}

func webhookExternalID(sourceRef, thread string) string {
	sourceRef, thread = strings.TrimSpace(sourceRef), strings.TrimSpace(thread)
	if sourceRef == "" || thread == "" {
		return ""
	}
	return sourceRef + "#" + thread
}

// --- Executor ---------------------------------------------------------------

type webhookExecutor struct{ svc *WebhookSourceService }

func (e webhookExecutor) Execute(ctx context.Context, plan intake.ExecutionPlan) (intake.ExecutionResult, error) {
	if plan.Decision == intake.DecisionResume {
		if plan.WorkspaceID == 0 {
			return intake.ExecutionResult{}, errors.New("webhook intake: resume without a workspace")
		}
		e.svc.deliver(ctx, plan.WorkspaceID, plan.Trigger)
		return intake.ExecutionResult{IssueID: plan.IssueID, WorkspaceID: plan.WorkspaceID}, nil
	}

	dispatch := e.svc.router()
	if dispatch == nil {
		return intake.ExecutionResult{}, errors.New("webhook intake: no task router wired")
	}
	owner, ok := e.svc.projectOwner(ctx, plan.Trigger.ProjectID)
	if !ok {
		return intake.ExecutionResult{}, errors.New("webhook intake: project owner unresolved")
	}
	hint := RouteHint{
		ForceNew:  plan.Trigger.ForceNew || plan.IssueID == 0,
		TitleHint: strings.TrimSpace(plan.Brief.Goal),
	}
	if plan.IssueID > 0 {
		hint.ActiveIssueID = plan.IssueID
	}
	target, err := dispatch.RouteInProject(ctx, owner, plan.Trigger.ProjectID, plan.Trigger.Text, hint)
	if err != nil {
		return intake.ExecutionResult{}, fmt.Errorf("webhook intake: route: %w", err)
	}
	if target.IssueID == 0 || target.WorkspaceID == 0 {
		return intake.ExecutionResult{}, fmt.Errorf("%w: issue=%d workspace=%d",
			errRouteIncomplete, target.IssueID, target.WorkspaceID)
	}

	e.svc.recordProvenance(ctx, target.IssueID, plan.Trigger)
	e.svc.persistBrief(ctx, target.IssueID, plan)
	e.svc.deliver(ctx, target.WorkspaceID, plan.Trigger)
	return intake.ExecutionResult{IssueID: target.IssueID, WorkspaceID: target.WorkspaceID}, nil
}

func (s *WebhookSourceService) projectOwner(ctx context.Context, projectID int64) (OwnerRef, bool) {
	p, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return OwnerRef{}, false
	}
	return OwnerRef{Type: p.OwnerType, ID: p.OwnerID}, true
}

func (s *WebhookSourceService) deliver(ctx context.Context, workspaceID int64, t intake.Trigger) {
	d := s.messageDeliverer()
	if d == nil || workspaceID == 0 {
		return
	}
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		slog.Warn("webhook intake: load workspace failed", "workspace", workspaceID, "error", err)
		return
	}
	if _, _, err := d.Deliver(ctx, workspaceID, ws.Path, githubDeliveryText(t), ""); err != nil {
		slog.Warn("webhook intake: deliver failed", "workspace", workspaceID, "error", err)
	}
}

func (s *WebhookSourceService) recordProvenance(ctx context.Context, issueID int64, t intake.Trigger) {
	if issueID == 0 {
		return
	}
	extID := webhookExternalID(t.SourceRef, t.Thread.ThreadExtID)
	if extID == "" {
		extID = t.SourceRef + "#" + t.EventID
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE issues SET external_source = ?, external_id = ?, external_url = ?
		 WHERE id = ? AND (external_source IS NULL OR external_source = '')`,
		t.SourceKind, extID, t.SourceURL, issueID,
	); err != nil {
		slog.Warn("webhook intake: write issue provenance failed", "issue", issueID, "error", err)
	}
}

func (s *WebhookSourceService) persistBrief(ctx context.Context, issueID int64, plan intake.ExecutionPlan) {
	if s.db == nil || issueID == 0 {
		return
	}
	if _, err := s.q.GetLatestTaskBrief(ctx, issueID); err == nil {
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("webhook intake: read current task brief failed", "issue", issueID, "error", err)
		return
	}
	brief := plan.Brief
	if brief.Provenance.DecisionID == 0 {
		brief.Provenance.DecisionID = plan.DecisionID
	}
	if _, err := NewTaskBriefWriter(s.db).Append(ctx, issueID, brief, 0); err != nil {
		slog.Warn("webhook intake: persist task brief failed", "issue", issueID, "error", err)
	}
}

// --- Configuration surface --------------------------------------------------

// WebhookBindingDTO is one email/generic webhook binding. The secret is never
// returned after creation; HasSecret reports whether authentication is set up.
type WebhookBindingDTO struct {
	ID          int64  `json:"id"`
	SourceKind  string `json:"source_kind"`
	Name        string `json:"name"`
	InboundPath string `json:"inbound_path"`
	HasSecret   bool   `json:"has_secret"`
}

// ListWebhookBindings returns the project's email/generic webhook bindings.
func (s *WebhookSourceService) ListWebhookBindings(ctx context.Context, projectID int64) ([]WebhookBindingDTO, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, provider, source_key, credential_id
		FROM project_external_sources
		WHERE project_id = ? AND LOWER(provider) IN (?, ?)
		ORDER BY provider, source_key`,
		projectID, intake.SourceKindEmail, intake.SourceKindWebhook)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WebhookBindingDTO, 0)
	for rows.Next() {
		var (
			id   int64
			p    string
			key  string
			cred sql.NullInt64
		)
		if err := rows.Scan(&id, &p, &key, &cred); err != nil {
			return nil, err
		}
		kind := strings.ToLower(strings.TrimSpace(p))
		out = append(out, WebhookBindingDTO{
			ID:          id,
			SourceKind:  kind,
			Name:        key,
			InboundPath: webhookInboundPath(kind, id),
			HasSecret:   cred.Valid && cred.Int64 > 0,
		})
	}
	return out, rows.Err()
}

// CreateWebhookBinding creates a binding plus the encrypted credential that
// holds its generated secret. The plaintext secret is returned exactly once.
func (s *WebhookSourceService) CreateWebhookBinding(ctx context.Context, projectID, callerUserID int64, sourceKind, name string) (WebhookBindingDTO, string, error) {
	kind := strings.ToLower(strings.TrimSpace(sourceKind))
	if kind != intake.SourceKindEmail && kind != intake.SourceKindWebhook {
		return WebhookBindingDTO{}, "", fmt.Errorf("source_kind must be email or webhook")
	}
	key := strings.TrimSpace(name)
	if !validWebhookBindingName(key) {
		return WebhookBindingDTO{}, "", fmt.Errorf("name must be 1-64 characters of letters, digits, '-', '_' or '.'")
	}
	if s.creds == nil {
		return WebhookBindingDTO{}, "", errors.New("webhook source: credential service is not wired")
	}

	secret, err := randomSecret()
	if err != nil {
		return WebhookBindingDTO{}, "", err
	}
	aliasSuffix, err := randomHex(6)
	if err != nil {
		return WebhookBindingDTO{}, "", err
	}
	cred, err := s.creds.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: "user",
		OwnerID:   callerUserID,
		UserID:    callerUserID,
		Provider:  integration.ProviderName(kind),
		Alias:     "intake-" + key + "-" + aliasSuffix,
		RawConfig: map[string]any{webhookSecretKey: secret},
	})
	if err != nil {
		return WebhookBindingDTO{}, "", err
	}

	row, err := s.q.AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
		ProjectID:    projectID,
		Provider:     kind,
		SourceKey:    key,
		CredentialID: sql.NullInt64{Int64: cred.ID, Valid: true},
		Config:       "{}",
	})
	if err != nil {
		// The binding failed, so the credential has no referent. Best-effort:
		// leaving it behind would be a secret with no owner-visible purpose.
		_ = s.creds.DeleteByID(ctx, cred.ID, "user", callerUserID, callerUserID)
		return WebhookBindingDTO{}, "", err
	}
	return WebhookBindingDTO{
		ID:          row.ID,
		SourceKind:  kind,
		Name:        key,
		InboundPath: webhookInboundPath(kind, row.ID),
		HasSecret:   true,
	}, secret, nil
}

// DeleteWebhookBinding removes a binding and its credential. A binding outside
// the project is not found (never silently ignored).
func (s *WebhookSourceService) DeleteWebhookBinding(ctx context.Context, projectID, bindingID int64) error {
	var (
		provider string
		cred     sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT provider, credential_id FROM project_external_sources
		WHERE id = ? AND project_id = ?`, bindingID, projectID,
	).Scan(&provider, &cred)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrWebhookNotFound
	}
	if err != nil {
		return err
	}
	kind := strings.ToLower(strings.TrimSpace(provider))
	if kind != intake.SourceKindEmail && kind != intake.SourceKindWebhook {
		return ErrWebhookNotFound
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM project_external_sources WHERE id = ? AND project_id = ?`,
		bindingID, projectID,
	); err != nil {
		return err
	}
	if s.creds != nil && cred.Valid && cred.Int64 > 0 {
		if meta, err := s.creds.GetMetaByID(ctx, cred.Int64); err == nil {
			if err := s.creds.DeleteByID(ctx, cred.Int64, meta.OwnerType, meta.OwnerID, meta.UserID); err != nil {
				slog.Warn("webhook intake: delete binding credential failed",
					"binding", bindingID, "credential", cred.Int64, "error", err)
			}
		}
	}
	return nil
}

func webhookInboundPath(sourceKind string, bindingID int64) string {
	if sourceKind == intake.SourceKindEmail {
		return fmt.Sprintf("/api/integrations/email/%d", bindingID)
	}
	return fmt.Sprintf("/api/integrations/webhook/%d", bindingID)
}

func validWebhookBindingName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
