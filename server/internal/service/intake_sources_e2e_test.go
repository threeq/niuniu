package service

// End-to-end acceptance suite for the CI / Email / Webhook intake Sources
// (design 2026-11-06-intake-ci-email-webhook-design.md §13).
//
// The suite drives the product path at the service boundary: the real
// IntakeSourceService, WebhookSourceService, EmailSourceService and Intake
// pipeline over a real SQLite store. Only the external transport (IMAP) and the
// agent side (task router / message deliverer) are replaced by fixtures, so the
// binding lifecycle, normalization, policy, dedupe, routing and provenance are
// all the production code paths.
//
// The HTTP status contract these services are mapped onto (401/403/404/413/429)
// is asserted through the real Gin handler in
// internal/api/integration_webhook_test.go. This file asserts the service-layer
// contracts those statuses derive from (body cap, rate-limited error, forbidden
// peer, unauthorized signature).

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// intakeSourcesE2EFetcher stands in for the IMAP server. The poller's cursor
// still flows through the real EmailSourceService.
type intakeSourcesE2EFetcher struct {
	mu     sync.Mutex
	result IMAPFetch
	err    error
	calls  int
}

func (f *intakeSourcesE2EFetcher) Fetch(_ context.Context, _ IMAPConfig, _ string, _ IMAPCursor) (IMAPFetch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.result, f.err
}

func (f *intakeSourcesE2EFetcher) set(result IMAPFetch) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result = result
}

func (f *intakeSourcesE2EFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type intakeSourcesFixture struct {
	*imbotFixture
	creds     *ExternalCredentialService
	intakeSrc *IntakeSourceService
	extSrc    *ExternalSourceService
	hook      *WebhookSourceService
	mail      *EmailSourceService
	fetcher   *intakeSourcesE2EFetcher
}

func newIntakeSourcesFixture(t *testing.T) *intakeSourcesFixture {
	t.Helper()
	f := newIMBotFixture(t)

	kr, err := crypto.LoadOrCreate(filepath.Join(t.TempDir(), "integration_secret"))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	reg := integration.NewRegistry()
	creds := NewExternalCredentialService(f.q, f.db, kr, reg)
	authz := NewAuthz(f.q, f.db)

	fetcher := &intakeSourcesE2EFetcher{}
	mail := NewEmailSourceService(f.db, f.q, creds)
	mail.SetFetcher(fetcher)
	mail.SetInbound(f.router, f.deliverer)

	intakeSrc := NewIntakeSourceService(f.db, f.q, creds, authz, nil)
	intakeSrc.SetEmailPoller(mail)

	hook := NewWebhookSourceService(f.db, f.q, creds)
	hook.SetInbound(f.router, f.deliverer)

	return &intakeSourcesFixture{
		imbotFixture: f,
		creds:        creds,
		intakeSrc:    intakeSrc,
		extSrc:       NewExternalSourceService(f.q, f.db, creds, reg, authz, nil),
		hook:         hook,
		mail:         mail,
		fetcher:      fetcher,
	}
}

// createSource drives the real binding-create path (including the generated
// credential and, for CI, the seeded failure rule).
func (fx *intakeSourcesFixture) createSource(t *testing.T, in IntakeSourceInput) (*IntakeSourceDTO, string) {
	t.Helper()
	in.ProjectID = fx.projectID
	in.CallerUserID = 1
	dto, secret, err := fx.intakeSrc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create %s source %q: %v", in.SourceKind, in.Name, err)
	}
	return dto, secret
}

func (fx *intakeSourcesFixture) settings(t *testing.T, sourceKind, mode string, allowList []string) {
	t.Helper()
	if _, err := NewIntakeService(fx.db, fx.q).UpdateSettings(context.Background(), fx.projectID,
		UpdateIntakeSettingsRequest{Settings: []IntakeSettingInput{{
			SourceKind: sourceKind, Mode: mode, AllowList: allowList,
		}}}); err != nil {
		t.Fatalf("%s intake settings: %v", sourceKind, err)
	}
}

// deliver posts one inbound delivery through the real service. remoteAddr is
// left alone when empty (httptest's default is a public TEST-NET address).
func (fx *intakeSourcesFixture) deliver(t *testing.T, bindingID int64, kind, remoteAddr string, headers map[string]string, body []byte) (InboundOutcome, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/integrations/"+kind+"/"+itoa(bindingID), strings.NewReader(string(body)))
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return fx.hook.HandleInbound(context.Background(), bindingID, kind, req, body)
}

func (fx *intakeSourcesFixture) deliveries() int {
	fx.deliverer.mu.Lock()
	defer fx.deliverer.mu.Unlock()
	return len(fx.deliverer.calls)
}

// createIMAPBinding seeds an email/imap binding plus the imap credential it
// references. Binding creation via the API performs a live LOGIN probe, which a
// hermetic test must not do; the poller itself is the real implementation.
func (fx *intakeSourcesFixture) createIMAPBinding(t *testing.T) (bindingID, credentialID int64) {
	t.Helper()
	ctx := context.Background()
	cred, err := fx.creds.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider: integration.ProviderName(imapProviderName),
		Alias:    "e2e-inbox",
		RawConfig: map[string]any{
			"imap_host": "imap.example.test", "imap_port": 993, "security": "ssl",
			"username": "bot@example.test", "password": "secret",
		},
	})
	if err != nil {
		t.Fatalf("imap credential: %v", err)
	}
	row, err := fx.q.AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
		ProjectID:    fx.projectID,
		Provider:     intake.SourceKindEmail,
		SourceKey:    "inbox",
		CredentialID: sql.NullInt64{Int64: cred.ID, Valid: true},
		Config:       `{"mode":"imap","mailbox":"INBOX","ignore_auto":true}`,
	})
	if err != nil {
		t.Fatalf("imap binding: %v", err)
	}
	return row.ID, cred.ID
}

// e2eRawMail builds one RFC 5322 message. messageID is passed without angle
// brackets.
func e2eRawMail(messageID, from, subject, body string, extraHeaders map[string]string) []byte {
	var b strings.Builder
	b.WriteString("Message-ID: <" + messageID + ">\r\n")
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	for k, v := range extraHeaders {
		b.WriteString(k + ": " + v + "\r\n")
	}
	b.WriteString("\r\n" + body)
	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// 1. CI: failure creates a repair task, then a green pipeline resumes it.
// ---------------------------------------------------------------------------

func TestIntakeSourcesE2E_CIFailureFixLoop(t *testing.T) {
	fx := newIntakeSourcesFixture(t)
	ctx := context.Background()

	ci, secret := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindCI, Name: "ci-main", Mode: "push", Provider: "gitlab",
		Config: map[string]any{"events": []any{intake.EventKindCIFailed, intake.EventKindCISucceeded}},
	})
	// The binding was born with the default ci_failed rule; auto mode is the
	// user's explicit switch. A green pipeline needs its own rule to be
	// executable (an inferred event without a rule only records a decision).
	fx.settings(t, intake.SourceKindCI, "auto", nil)
	if _, err := NewIntakeService(fx.db, fx.q).CreateRule(ctx, fx.projectID, UpsertIntakeRuleInput{
		SourceKind: intake.SourceKindCI,
		Name:       "CI 通过",
		Conditions: intake.RuleConditions{EventKind: intake.EventKindCISucceeded},
	}); err != nil {
		t.Fatalf("success rule: %v", err)
	}
	fx.router.queue = []PlanTarget{{IssueID: fx.issueID, WorkspaceID: fx.wsID, ProjectID: fx.projectID}}

	failBody := []byte(`{
	  "object_kind":"pipeline",
	  "object_attributes":{"id":123,"status":"failed","ref":"main","sha":"abc"},
	  "project":{"path_with_namespace":"acme/api"},
	  "merge_request":{"iid":42},
	  "user":{"id":7,"name":"Alice"}
	}`)
	out, err := fx.deliver(t, ci.ID, intake.SourceKindCI, "",
		map[string]string{"X-Gitlab-Token": secret}, failBody)
	if err != nil {
		t.Fatalf("failure delivery: %v", err)
	}
	if out.Decision != intake.DecisionAccept || !out.Executed {
		t.Fatalf("failure outcome = %+v, want executed accept", out)
	}
	if fx.router.calls != 1 || fx.deliveries() != 1 {
		t.Fatalf("failure side effects: router=%d deliveries=%d", fx.router.calls, fx.deliveries())
	}

	// The thread is bound to the new task, which is what success resolves.
	var source, externalID string
	if err := fx.db.QueryRow(`SELECT external_source, external_id FROM issues WHERE id = ?`, fx.issueID).
		Scan(&source, &externalID); err != nil {
		t.Fatalf("provenance: %v", err)
	}
	if source != intake.SourceKindCI || !strings.HasSuffix(externalID, "#acme/api#pr:42") {
		t.Fatalf("provenance = %s / %s", source, externalID)
	}

	successBody := []byte(`{
	  "object_kind":"pipeline",
	  "object_attributes":{"id":123,"status":"success","ref":"main","sha":"abc"},
	  "project":{"path_with_namespace":"acme/api"},
	  "merge_request":{"iid":42},
	  "user":{"id":7,"name":"Alice"}
	}`)
	out, err = fx.deliver(t, ci.ID, intake.SourceKindCI, "",
		map[string]string{"X-Gitlab-Token": secret}, successBody)
	if err != nil {
		t.Fatalf("success delivery: %v", err)
	}
	if out.Decision != intake.DecisionResume || !out.Executed || out.ReasonCode != intake.ReasonResumed {
		t.Fatalf("success outcome = %+v, want executed resume", out)
	}
	if fx.router.calls != 1 {
		t.Fatalf("success created another task: router calls = %d", fx.router.calls)
	}
	if fx.deliveries() != 2 {
		t.Fatalf("deliveries = %d, want 2 (accept then resume)", fx.deliveries())
	}

	// A redelivery of the failure is deduped and changes nothing.
	dup, err := fx.deliver(t, ci.ID, intake.SourceKindCI, "",
		map[string]string{"X-Gitlab-Token": secret}, failBody)
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if !dup.Duplicate || dup.Executed {
		t.Fatalf("redelivery = %+v, want duplicate no-op", dup)
	}
	if fx.router.calls != 1 || fx.deliveries() != 2 {
		t.Fatalf("duplicate produced side effects")
	}
}

// ---------------------------------------------------------------------------
// 2. Email over IMAP: new mail creates a task, a reply resumes it.
// ---------------------------------------------------------------------------

func TestIntakeSourcesE2E_IMAPNewMailAndReplyResume(t *testing.T) {
	fx := newIntakeSourcesFixture(t)
	ctx := context.Background()

	bindingID, _ := fx.createIMAPBinding(t)
	fx.settings(t, intake.SourceKindEmail, "auto", []string{"alice@example.test"})
	fx.router.queue = []PlanTarget{{IssueID: fx.issueID, WorkspaceID: fx.wsID, ProjectID: fx.projectID}}

	fx.fetcher.set(IMAPFetch{
		UIDValidity: 7,
		Messages: []FetchedMessage{{UID: 1, Raw: e2eRawMail(
			"root@example.test", "Alice <alice@example.test>", "Deploy failed",
			"please look at the build", nil)}},
	})
	if err := fx.intakeSrc.PollNow(ctx, 1, fx.projectID, bindingID); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if fx.router.calls != 1 || fx.deliveries() != 1 {
		t.Fatalf("first mail: router=%d deliveries=%d", fx.router.calls, fx.deliveries())
	}

	var source, externalID string
	if err := fx.db.QueryRow(`SELECT external_source, external_id FROM issues WHERE id = ?`, fx.issueID).
		Scan(&source, &externalID); err != nil {
		t.Fatalf("provenance: %v", err)
	}
	if source != intake.SourceKindEmail || !strings.HasSuffix(externalID, "#root@example.test") {
		t.Fatalf("provenance = %s / %s", source, externalID)
	}

	// A reply carries the root in References; it must resume, not create.
	fx.fetcher.set(IMAPFetch{
		UIDValidity: 7,
		Messages: []FetchedMessage{{UID: 2, Raw: e2eRawMail(
			"reply@example.test", "Alice <alice@example.test>", "Re: Deploy failed",
			"fixed it", map[string]string{
				"In-Reply-To": "<root@example.test>",
				"References":  "<root@example.test>",
			})}},
	})
	if err := fx.intakeSrc.PollNow(ctx, 1, fx.projectID, bindingID); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if fx.router.calls != 1 {
		t.Fatalf("reply created another task: router calls = %d", fx.router.calls)
	}
	if fx.deliveries() != 2 {
		t.Fatalf("deliveries = %d, want 2 (new task + resume)", fx.deliveries())
	}

	var decision string
	if err := fx.db.QueryRow(`
		SELECT d.decision FROM intake_decisions d
		JOIN intake_events e ON e.id = d.event_id
		WHERE e.source_kind = 'email' AND e.thread_ext_id = 'root@example.test'
		ORDER BY d.id DESC LIMIT 1`).Scan(&decision); err != nil {
		t.Fatalf("reply decision: %v", err)
	}
	if decision != string(intake.DecisionResume) {
		t.Fatalf("reply decision = %q, want resume", decision)
	}
}

// ---------------------------------------------------------------------------
// 3. Email inbound parsing (Mailgun sample): one decision, then dedupe.
// ---------------------------------------------------------------------------

func TestIntakeSourcesE2E_EmailInboundParse(t *testing.T) {
	fx := newIntakeSourcesFixture(t)

	email, secret := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindEmail, Name: "support", Mode: "inbound", Provider: "mailgun",
	})
	fx.settings(t, intake.SourceKindEmail, "auto", []string{"alice@example.test"})
	fx.router.queue = []PlanTarget{{IssueID: fx.issueID, WorkspaceID: fx.wsID, ProjectID: fx.projectID}}

	body := []byte(`{
	  "from": "Alice Example <alice@example.test>",
	  "to": "support@inbound.example.test",
	  "subject": "Deploy staging",
	  "body-plain": "please deploy v3",
	  "Message-Id": "<mailgun-e2e-1@example.test>",
	  "message-headers": "[[\"From\", \"Alice Example <alice@example.test>\"], [\"Auto-Submitted\", \"no\"]]",
	  "timestamp": "` + strconv.FormatInt(time.Now().Unix(), 10) + `"
	}`)
	auth := map[string]string{"Authorization": "Bearer " + secret}

	out, err := fx.deliver(t, email.ID, intake.SourceKindEmail, "", auth, body)
	if err != nil {
		t.Fatalf("mailgun delivery: %v", err)
	}
	if out.Decision != intake.DecisionAccept || !out.Executed {
		t.Fatalf("mailgun outcome = %+v, want executed accept", out)
	}
	if fx.router.calls != 1 || fx.deliveries() != 1 {
		t.Fatalf("mailgun side effects: router=%d deliveries=%d", fx.router.calls, fx.deliveries())
	}

	var eventKind, actor, thread string
	if err := fx.db.QueryRow(`
		SELECT event_kind, actor_ext_id, thread_ext_id FROM intake_events
		WHERE source_kind = 'email' ORDER BY id LIMIT 1`).
		Scan(&eventKind, &actor, &thread); err != nil {
		t.Fatalf("event: %v", err)
	}
	if eventKind != intake.EventKindMessage || actor != "alice@example.test" ||
		thread != "mailgun-e2e-1@example.test" {
		t.Fatalf("normalized event = %s / %s / %s", eventKind, actor, thread)
	}

	// An identical redelivery (same Message-Id) is a duplicate, not a new task.
	dup, err := fx.deliver(t, email.ID, intake.SourceKindEmail, "", auth, body)
	if err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}
	if !dup.Duplicate || dup.Executed || dup.ReasonCode != intake.ReasonDuplicate {
		t.Fatalf("duplicate outcome = %+v, want a duplicate no-op", dup)
	}
	if got := countRows(t, fx.db, "intake_events"); got != 1 {
		t.Fatalf("duplicate wrote a new event: %d", got)
	}
	if fx.router.calls != 1 || fx.deliveries() != 1 {
		t.Fatalf("duplicate produced side effects")
	}

	// Auto-replies are acknowledged but never become tasks.
	autoReply := []byte(`{
	  "from": "auto@example.test",
	  "to": "support@inbound.example.test",
	  "subject": "Out of office",
	  "body-plain": "away",
	  "Message-Id": "<auto-e2e@example.test>",
	  "message-headers": "[[\"Auto-Submitted\", \"auto-replied\"]]"
	}`)
	ignored, err := fx.deliver(t, email.ID, intake.SourceKindEmail, "", auth, autoReply)
	if err != nil {
		t.Fatalf("auto reply delivery: %v", err)
	}
	if !ignored.Ignored || ignored.EventID != 0 {
		t.Fatalf("auto reply outcome = %+v, want ignored with no event", ignored)
	}
	if fx.router.calls != 1 || fx.deliveries() != 1 {
		t.Fatalf("auto reply created work")
	}
}

// ---------------------------------------------------------------------------
// 4. Webhook: signed delivery, duplicate, and a forged signature.
// ---------------------------------------------------------------------------

func TestIntakeSourcesE2E_WebhookSignatureDuplicateAndForgery(t *testing.T) {
	fx := newIntakeSourcesFixture(t)

	wh, secret := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindWebhook, Name: "ops", Mode: "push",
	})
	fx.settings(t, intake.SourceKindWebhook, "auto", []string{"alice"})
	fx.router.queue = []PlanTarget{
		{IssueID: fx.issueID, WorkspaceID: fx.wsID, ProjectID: fx.projectID},
		{IssueID: fx.issueID, WorkspaceID: fx.wsID, ProjectID: fx.projectID},
	}
	bearer := map[string]string{"Authorization": "Bearer " + secret}

	body := []byte(`{"id":"evt-1","event":"alert","thread_id":"monitor/api","actor":{"id":"alice","name":"Alice"},"text":"CPU 95% for 10m"}`)
	out, err := fx.deliver(t, wh.ID, intake.SourceKindWebhook, "", bearer, body)
	if err != nil {
		t.Fatalf("bearer delivery: %v", err)
	}
	if out.Decision != intake.DecisionAccept || !out.Executed {
		t.Fatalf("bearer outcome = %+v, want executed accept", out)
	}

	dup, err := fx.deliver(t, wh.ID, intake.SourceKindWebhook, "", bearer, body)
	if err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}
	if !dup.Duplicate || dup.Executed {
		t.Fatalf("duplicate outcome = %+v", dup)
	}
	if fx.router.calls != 1 || fx.deliveries() != 1 {
		t.Fatalf("duplicate produced side effects: router=%d deliveries=%d", fx.router.calls, fx.deliveries())
	}

	// The timestamped HMAC scheme is accepted with a fresh timestamp.
	signedBody := []byte(`{"id":"evt-2","event":"alert","thread_id":"monitor/db","actor":{"id":"alice","name":"Alice"},"text":"disk full"}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	out, err = fx.deliver(t, wh.ID, intake.SourceKindWebhook, "", map[string]string{
		"X-Webhook-Timestamp":     ts,
		"X-Webhook-Signature-256": signTimestamped(secret, ts, signedBody),
	}, signedBody)
	if err != nil {
		t.Fatalf("signed delivery: %v", err)
	}
	if out.Decision != intake.DecisionAccept || !out.Executed {
		t.Fatalf("signed outcome = %+v, want executed accept", out)
	}
	if fx.router.calls != 2 || fx.deliveries() != 2 {
		t.Fatalf("signed delivery side effects: router=%d deliveries=%d", fx.router.calls, fx.deliveries())
	}

	// A forged signature is rejected and records nothing.
	before := countRows(t, fx.db, "intake_events")
	forged := []byte(`{"id":"evt-3","thread_id":"monitor/x","actor":{"id":"alice"},"text":"rm -rf"}`)
	_, err = fx.deliver(t, wh.ID, intake.SourceKindWebhook, "", map[string]string{
		"X-Webhook-Timestamp":     ts,
		"X-Webhook-Signature-256": signTimestamped("not-the-secret", ts, forged),
	}, forged)
	if !errors.Is(err, ErrIntakeWebhookUnauthorized) {
		t.Fatalf("forged delivery error = %v, want unauthorized", err)
	}
	if got := countRows(t, fx.db, "intake_events"); got != before {
		t.Fatalf("forged delivery recorded an event: %d -> %d", before, got)
	}
}

// ---------------------------------------------------------------------------
// 5. Test delivery is a dry run: it evaluates but records nothing.
// ---------------------------------------------------------------------------

func TestIntakeSourcesE2E_TestDeliveryHasNoSideEffects(t *testing.T) {
	fx := newIntakeSourcesFixture(t)
	ctx := context.Background()

	ci, _ := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindCI, Name: "ci-preview", Mode: "push", Provider: "gitlab",
	})
	eventsBefore := countRows(t, fx.db, "intake_events")
	decisionsBefore := countRows(t, fx.db, "intake_decisions")
	issuesBefore := countRows(t, fx.db, "issues")

	res, err := fx.intakeSrc.TestDelivery(ctx, 1, fx.projectID, ci.ID, []byte(`{
	  "object_kind":"pipeline",
	  "object_attributes":{"id":9,"status":"failed","ref":"main","sha":"abc"},
	  "project":{"path_with_namespace":"acme/api"},
	  "user":{"id":7,"name":"Alice"}
	}`))
	if err != nil {
		t.Fatalf("test delivery: %v", err)
	}
	if res.SourceKind != intake.SourceKindCI || res.EventKind != intake.EventKindCIFailed {
		t.Fatalf("normalized = %s / %s", res.SourceKind, res.EventKind)
	}
	if !res.Allowed || res.Strength != string(intake.StrengthInferred) {
		t.Fatalf("policy verdict = %+v", res)
	}

	// Free text is still evaluable, with a warning that it was not a payload.
	text, err := fx.intakeSrc.TestDelivery(ctx, 1, fx.projectID, ci.ID, []byte("please fix the build"))
	if err != nil {
		t.Fatalf("text test delivery: %v", err)
	}
	if len(text.Warnings) == 0 {
		t.Fatalf("text sample produced no warning: %+v", text)
	}

	if got := countRows(t, fx.db, "intake_events"); got != eventsBefore {
		t.Fatalf("test delivery wrote events: %d -> %d", eventsBefore, got)
	}
	if got := countRows(t, fx.db, "intake_decisions"); got != decisionsBefore {
		t.Fatalf("test delivery wrote decisions: %d -> %d", decisionsBefore, got)
	}
	if got := countRows(t, fx.db, "issues"); got != issuesBefore {
		t.Fatalf("test delivery created issues: %d -> %d", issuesBefore, got)
	}
	if fx.router.calls != 0 || fx.deliveries() != 0 {
		t.Fatalf("test delivery executed work")
	}
}

// ---------------------------------------------------------------------------
// 6. Rotation retires the previous secret immediately.
// ---------------------------------------------------------------------------

func TestIntakeSourcesE2E_RotateSecretRetiresOldSecret(t *testing.T) {
	fx := newIntakeSourcesFixture(t)
	ctx := context.Background()

	wh, oldSecret := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindWebhook, Name: "rotating", Mode: "push",
	})
	makeBody := func(id string) []byte {
		return []byte(`{"id":"` + id + `","text":"hello"}`)
	}

	if _, err := fx.deliver(t, wh.ID, intake.SourceKindWebhook, "",
		map[string]string{"Authorization": "Bearer " + oldSecret}, makeBody("rotate-1")); err != nil {
		t.Fatalf("delivery with original secret: %v", err)
	}

	newSecret, err := fx.intakeSrc.RotateSecret(ctx, 1, fx.projectID, wh.ID)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if newSecret == "" || newSecret == oldSecret {
		t.Fatalf("rotated secret = %q (old %q)", newSecret, oldSecret)
	}

	before := countRows(t, fx.db, "intake_events")
	if _, err := fx.deliver(t, wh.ID, intake.SourceKindWebhook, "",
		map[string]string{"Authorization": "Bearer " + oldSecret}, makeBody("rotate-2")); !errors.Is(err, ErrIntakeWebhookUnauthorized) {
		t.Fatalf("old secret error = %v, want unauthorized", err)
	}
	if got := countRows(t, fx.db, "intake_events"); got != before {
		t.Fatalf("retired secret recorded an event: %d -> %d", before, got)
	}

	if _, err := fx.deliver(t, wh.ID, intake.SourceKindWebhook, "",
		map[string]string{"Authorization": "Bearer " + newSecret}, makeBody("rotate-3")); err != nil {
		t.Fatalf("new secret rejected: %v", err)
	}

	// An IMAP binding has no service-generated secret to rotate.
	imapID, _ := fx.createIMAPBinding(t)
	if _, err := fx.intakeSrc.RotateSecret(ctx, 1, fx.projectID, imapID); !errors.Is(err, ErrIntakeSourceNoSecret) {
		t.Fatalf("imap rotate error = %v, want no-secret", err)
	}
}

// ---------------------------------------------------------------------------
// 7. Deleting a binding cascades its generated credential and poll cursor.
// ---------------------------------------------------------------------------

func TestIntakeSourcesE2E_DeleteCascadesCredentialAndCursor(t *testing.T) {
	fx := newIntakeSourcesFixture(t)
	ctx := context.Background()

	wh, _ := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindWebhook, Name: "disposable", Mode: "push",
	})
	var whCredentialID int64
	if err := fx.db.QueryRow(`SELECT credential_id FROM project_external_sources WHERE id = ?`, wh.ID).
		Scan(&whCredentialID); err != nil {
		t.Fatalf("binding credential: %v", err)
	}

	imapID, imapCredentialID := fx.createIMAPBinding(t)
	if _, err := fx.q.UpsertEmailPollState(ctx, store.UpsertEmailPollStateParams{
		ProjectID: fx.projectID, BindingID: imapID, Mailbox: "INBOX",
	}); err != nil {
		t.Fatalf("poll state: %v", err)
	}

	// Deleting the push binding removes the generated secret, not just the row.
	if err := fx.intakeSrc.Delete(ctx, 1, fx.projectID, wh.ID); err != nil {
		t.Fatalf("delete webhook binding: %v", err)
	}
	if _, err := fx.q.GetProjectExternalSource(ctx, wh.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("binding still present: %v", err)
	}
	if _, err := fx.creds.GetMetaByID(ctx, whCredentialID); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("generated credential survived deletion: %v", err)
	}

	// Deleting the IMAP binding removes its cursor but keeps the user-owned
	// imap credential, which may be referenced elsewhere.
	if err := fx.intakeSrc.Delete(ctx, 1, fx.projectID, imapID); err != nil {
		t.Fatalf("delete imap binding: %v", err)
	}
	var pollStates int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM email_source_poll_state WHERE binding_id = ?`, imapID).
		Scan(&pollStates); err != nil {
		t.Fatalf("poll state count: %v", err)
	}
	if pollStates != 0 {
		t.Fatalf("poll state rows = %d, want 0", pollStates)
	}
	if _, err := fx.creds.GetMetaByID(ctx, imapCredentialID); err != nil {
		t.Fatalf("user-owned imap credential was deleted: %v", err)
	}

	list, err := fx.intakeSrc.List(ctx, 1, fx.projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("bindings after delete = %+v", list)
	}
}

// ---------------------------------------------------------------------------
// 8. local_only admits loopback and rejects a public peer (forged XFF cannot
//    widen it without a trusted proxy).
// ---------------------------------------------------------------------------

func TestIntakeSourcesE2E_LocalOnlyRejectsPublicPeer(t *testing.T) {
	fx := newIntakeSourcesFixture(t)

	wh, secret := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindWebhook, Name: "local-hook", Mode: "push",
		Config: map[string]any{"local_only": true},
	})
	bearer := map[string]string{"Authorization": "Bearer " + secret}

	out, err := fx.deliver(t, wh.ID, intake.SourceKindWebhook, "203.0.113.9:51000", bearer,
		[]byte(`{"id":"public-1","text":"hello"}`))
	if !errors.Is(err, ErrIntakeWebhookForbidden) {
		t.Fatalf("public peer outcome = %+v err = %v, want forbidden", out, err)
	}

	// A forged X-Forwarded-For does not help: no trusted proxy is configured.
	forged := map[string]string{
		"Authorization":   "Bearer " + secret,
		"X-Forwarded-For": "127.0.0.1",
	}
	if _, err := fx.deliver(t, wh.ID, intake.SourceKindWebhook, "203.0.113.9:51000", forged,
		[]byte(`{"id":"public-2","text":"hello"}`)); !errors.Is(err, ErrIntakeWebhookForbidden) {
		t.Fatalf("forged XFF error = %v, want forbidden", err)
	}
	if got := countRows(t, fx.db, "intake_events"); got != 0 {
		t.Fatalf("rejected deliveries recorded %d events", got)
	}

	// Loopback is admitted (off mode records an ignored decision only).
	if _, err := fx.deliver(t, wh.ID, intake.SourceKindWebhook, "127.0.0.1:51000", bearer,
		[]byte(`{"id":"local-1","text":"hello"}`)); err != nil {
		t.Fatalf("loopback delivery: %v", err)
	}
	if got := countRows(t, fx.db, "intake_events"); got != 1 {
		t.Fatalf("events after loopback = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// 9. Rate limit and body cap: the service contracts the HTTP edge maps to
//    429 and 413. The exact status codes are pinned by
//    internal/api/integration_webhook_test.go.
// ---------------------------------------------------------------------------

func TestIntakeSourcesE2E_RateLimitAndBodyCap(t *testing.T) {
	fx := newIntakeSourcesFixture(t)
	ctx := context.Background()

	wh, secret := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindWebhook, Name: "metered", Mode: "push",
		Config: map[string]any{"max_body_bytes": 64},
	})
	limit, err := fx.hook.BodyLimit(ctx, wh.ID, intake.SourceKindWebhook)
	if err != nil {
		t.Fatalf("body limit: %v", err)
	}
	if limit != 64 {
		t.Fatalf("body limit = %d, want 64", limit)
	}

	// Absent config means the 1 MB default; a value above the ceiling clamps.
	plain, _ := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindWebhook, Name: "plain", Mode: "push",
	})
	if got, err := fx.hook.BodyLimit(ctx, plain.ID, intake.SourceKindWebhook); err != nil || got != defaultWebhookBodyBytes {
		t.Fatalf("default body limit = %d (err %v), want %d", got, err, defaultWebhookBodyBytes)
	}
	huge, _ := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindWebhook, Name: "huge", Mode: "push",
		Config: map[string]any{"max_body_bytes": int64(100 << 20)},
	})
	if got, err := fx.hook.BodyLimit(ctx, huge.ID, intake.SourceKindWebhook); err != nil || got != maxWebhookBodyBytes {
		t.Fatalf("clamped body limit = %d (err %v), want %d", got, err, maxWebhookBodyBytes)
	}

	// Spend the per-binding budget (60/min) with valid, distinct deliveries.
	// No intake settings means mode off: each delivery is recorded as an
	// ignored decision and executes nothing.
	auth := map[string]string{"Authorization": "Bearer " + secret}
	for i := 0; i < webhookRatePerMinute; i++ {
		body := []byte(`{"id":"rl-` + strconv.Itoa(i) + `","text":"burst"}`)
		if _, err := fx.deliver(t, wh.ID, intake.SourceKindWebhook, "", auth, body); err != nil {
			t.Fatalf("burst delivery %d: %v", i+1, err)
		}
	}
	_, err = fx.deliver(t, wh.ID, intake.SourceKindWebhook, "", auth,
		[]byte(`{"id":"rl-over","text":"burst"}`))
	if !errors.Is(err, ErrWebhookRateLimited) {
		t.Fatalf("over-budget error = %v, want rate limited", err)
	}
	if got := countRows(t, fx.db, "intake_events"); got != webhookRatePerMinute {
		t.Fatalf("events = %d, want %d (rejected delivery must not record)", got, webhookRatePerMinute)
	}
}

// ---------------------------------------------------------------------------
// 10. Intake bindings never leak into the generic "external systems" panel.
// ---------------------------------------------------------------------------

func TestIntakeSourcesE2E_IntakeBindingsStayOutOfExternalPanel(t *testing.T) {
	fx := newIntakeSourcesFixture(t)
	ctx := context.Background()

	ci, _ := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindCI, Name: "panel-ci", Mode: "push", Provider: "gitlab",
	})
	email, _ := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindEmail, Name: "panel-mail", Mode: "inbound", Provider: "mailgun",
	})
	wh, _ := fx.createSource(t, IntakeSourceInput{
		SourceKind: intake.SourceKindWebhook, Name: "panel-hook", Mode: "push",
	})

	// The Intake panel sees all three.
	list, err := fx.intakeSrc.List(ctx, 1, fx.projectID)
	if err != nil {
		t.Fatalf("intake list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("intake list = %+v, want 3 bindings", list)
	}

	// The external-systems panel sees none of them.
	ext, err := fx.extSrc.List(ctx, fx.projectID)
	if err != nil {
		t.Fatalf("external list: %v", err)
	}
	if len(ext) != 0 {
		t.Fatalf("external panel leaked intake bindings: %+v", ext)
	}

	// And it cannot create or delete one, which would bypass secret cleanup.
	if _, err := fx.extSrc.Add(ctx, fx.projectID, integration.ProviderName(intake.SourceKindWebhook),
		"smuggled", 0, nil, 1); !errors.Is(err, ErrIntakeSourceReserved) {
		t.Fatalf("external add error = %v, want reserved", err)
	}
	if err := fx.extSrc.Delete(ctx, wh.ID); !errors.Is(err, ErrIntakeSourceReserved) {
		t.Fatalf("external delete error = %v, want reserved", err)
	}
	if _, err := fx.q.GetProjectExternalSource(ctx, wh.ID); err != nil {
		t.Fatalf("reserved binding was deleted: %v", err)
	}

	// Deleting through the Intake service still works and leaves the others.
	if err := fx.intakeSrc.Delete(ctx, 1, fx.projectID, ci.ID); err != nil {
		t.Fatalf("intake delete: %v", err)
	}
	if _, err := fx.q.GetProjectExternalSource(ctx, email.ID); err != nil {
		t.Fatalf("deleting one binding disturbed another: %v", err)
	}
}
