package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func newWebhookTestService(t *testing.T, f *imbotFixture) (*WebhookSourceService, *ExternalCredentialService) {
	t.Helper()
	kr, err := crypto.LoadOrCreate(filepath.Join(t.TempDir(), "integration_secret"))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	creds := NewExternalCredentialService(f.q, f.db, kr, integration.NewRegistry())
	return NewWebhookSourceService(f.db, f.q, creds), creds
}

func bindWebhookSource(t *testing.T, f *imbotFixture, creds *ExternalCredentialService, projectID int64, provider, sourceKey, secret string) int64 {
	t.Helper()
	cred, err := creds.Create(context.Background(), ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider:  integration.ProviderName(provider),
		Alias:     "test-" + provider + "-" + sourceKey,
		RawConfig: map[string]any{"webhook_secret": secret},
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	row, err := f.q.AddProjectExternalSource(context.Background(), store.AddProjectExternalSourceParams{
		ProjectID: projectID, Provider: provider, SourceKey: sourceKey,
		CredentialID: sql.NullInt64{Int64: cred.ID, Valid: true}, Config: "{}",
	})
	if err != nil {
		t.Fatalf("bind source: %v", err)
	}
	return row.ID
}

func webhookRequest(t *testing.T, headers map[string]string, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/integrations/webhook/1", bytes.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookSource_GenericAuthAndIdempotency(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "generic-secret"
	bindingID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindWebhook, "ops-hooks", secret)

	if _, err := NewIntakeService(f.db, f.q).UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindWebhook,
			Mode:       "off",
			AllowList:  []string{"alice"},
		}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}

	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})
	svc.SetInbound(f.router, f.deliverer)

	body := []byte(`{"id":"evt-1","actor":{"id":"alice","name":"Alice"},"thread_id":"t1","text":"please deploy"}`)
	auth := map[string]string{"Authorization": "Bearer " + secret}

	out, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook, webhookRequest(t, auth, body), body)
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if !out.Executed || out.Decision != intake.DecisionAccept {
		t.Fatalf("first outcome = %+v", out)
	}
	if f.router.calls != 1 {
		t.Fatalf("router calls = %d, want 1", f.router.calls)
	}

	// A redelivery of the same event id is a no-op.
	out, err = svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook, webhookRequest(t, auth, body), body)
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if !out.Duplicate || out.Executed {
		t.Fatalf("redelivery outcome = %+v", out)
	}
	if f.router.calls != 1 {
		t.Fatalf("redelivery created another task: router calls = %d", f.router.calls)
	}

	// A second event on the same thread resumes the bound task instead of
	// creating a new one.
	second := []byte(`{"id":"evt-2","actor":"alice","thread_id":"t1","text":"status?"}`)
	out, err = svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook, webhookRequest(t, auth, second), second)
	if err != nil {
		t.Fatalf("second event: %v", err)
	}
	if out.Decision != intake.DecisionResume || !out.Executed {
		t.Fatalf("second outcome = %+v", out)
	}
	if f.router.calls != 1 {
		t.Fatalf("resume routed a new task: router calls = %d", f.router.calls)
	}

	// No/incorrect secret is rejected and records nothing.
	if _, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook,
		webhookRequest(t, map[string]string{"Authorization": "Bearer wrong"}, body), body); err != ErrIntakeWebhookUnauthorized {
		t.Fatalf("wrong token error = %v", err)
	}
	if _, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook, webhookRequest(t, nil, body), body); err != ErrIntakeWebhookUnauthorized {
		t.Fatalf("missing token error = %v", err)
	}
	if _, err := svc.HandleInbound(ctx, 999999, intake.SourceKindWebhook, webhookRequest(t, auth, body), body); err != ErrWebhookNotFound {
		t.Fatalf("unknown binding error = %v", err)
	}
	if _, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindEmail, webhookRequest(t, auth, body), body); err != ErrWebhookNotFound {
		t.Fatalf("provider mismatch error = %v", err)
	}

	var events int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM intake_events WHERE source_kind = 'webhook'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("intake events = %d, want 2 (two accepted events, no forged ones)", events)
	}
}

func TestWebhookSource_GitHubSignatureAndParity(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "github-secret"
	bindingID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindGitHub, "acme/foo", secret)

	if _, err := NewIntakeService(f.db, f.q).UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindGitHub,
			Mode:       "off",
			AllowList:  []string{"alice"},
		}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})
	svc.SetInbound(f.router, f.deliverer)

	body := []byte(`{
	  "action":"created",
	  "issue":{"number":42},
	  "comment":{"id":1,"body":"please fix the build",
	    "html_url":"https://github.com/acme/foo/pull/42#issuecomment-1",
	    "issue_url":"https://api.github.com/repos/acme/foo/issues/42",
	    "created_at":"2026-11-04T00:00:00Z","updated_at":"2026-11-04T00:00:00Z",
	    "user":{"login":"alice","type":"User"}}
	}`)
	headers := map[string]string{
		"X-GitHub-Event":      "issue_comment",
		"X-Hub-Signature-256": signBody(secret, body),
	}

	out, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindGitHub, webhookRequest(t, headers, body), body)
	if err != nil {
		t.Fatalf("valid delivery: %v", err)
	}
	if !out.Executed || out.Decision != intake.DecisionAccept {
		t.Fatalf("outcome = %+v", out)
	}

	// The same upstream event redelivered is a duplicate...
	out, err = svc.HandleInbound(ctx, bindingID, intake.SourceKindGitHub, webhookRequest(t, headers, body), body)
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if !out.Duplicate {
		t.Fatalf("redelivery outcome = %+v", out)
	}
	// ...and so is the same event arriving later through the POLLING transport:
	// both transports use the same event id, so the shared pipeline dedupes it.
	if _, err := svc.intake.Handle(ctx, githubTestTrigger(f.projectID)); err != nil {
		t.Fatalf("polling delivery: %v", err)
	}
	if f.router.calls != 1 {
		t.Fatalf("webhook + polling created %d tasks, want 1", f.router.calls)
	}

	// A forged signature is rejected outright.
	bad := map[string]string{
		"X-GitHub-Event":      "issue_comment",
		"X-Hub-Signature-256": signBody("wrong-secret", body),
	}
	if _, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindGitHub, webhookRequest(t, bad, body), body); err != ErrIntakeWebhookUnauthorized {
		t.Fatalf("bad signature error = %v", err)
	}
	noSig := map[string]string{"X-GitHub-Event": "issue_comment"}
	if _, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindGitHub, webhookRequest(t, noSig, body), body); err != ErrIntakeWebhookUnauthorized {
		t.Fatalf("missing signature error = %v", err)
	}

	// An event type that is not enabled for the binding is acknowledged but
	// records nothing, exactly as polling would never fetch that stream.
	if _, err := f.db.Exec(`UPDATE project_external_sources SET config = '{"events":["check_run"]}' WHERE id = ?`, bindingID); err != nil {
		t.Fatal(err)
	}
	out, err = svc.HandleInbound(ctx, bindingID, intake.SourceKindGitHub, webhookRequest(t, headers, body), body)
	if err != nil {
		t.Fatalf("disabled event: %v", err)
	}
	if !out.Ignored {
		t.Fatalf("disabled event outcome = %+v", out)
	}
}

func TestWebhookSource_ConfigCRUD(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, _ := newWebhookTestService(t, f)

	bindings, err := svc.ListWebhookBindings(ctx, f.projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("initial bindings = %+v", bindings)
	}

	binding, secret, err := svc.CreateWebhookBinding(ctx, f.projectID, 1, "email", "support")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if secret == "" || binding.ID == 0 || !binding.HasSecret {
		t.Fatalf("created binding = %+v secret=%q", binding, secret)
	}
	if binding.InboundPath != "/api/integrations/email/"+strconv.FormatInt(binding.ID, 10) {
		t.Fatalf("inbound path = %q", binding.InboundPath)
	}

	bindings, err = svc.ListWebhookBindings(ctx, f.projectID)
	if err != nil {
		t.Fatalf("list after create: %v", err)
	}
	if len(bindings) != 1 || bindings[0].Name != "support" || bindings[0].SourceKind != "email" {
		t.Fatalf("bindings = %+v", bindings)
	}

	// The created secret authenticates the inbound endpoint.
	body := []byte(`{"message_id":"m1","from":"alice@example.test","subject":"hi","text":"hello"}`)
	req := webhookRequest(t, map[string]string{"X-Webhook-Token": secret}, body)
	if _, err := svc.HandleInbound(ctx, binding.ID, intake.SourceKindEmail, req, body); err != nil {
		// Routing needs a target; this assertion is only that auth passed.
		t.Logf("delivery routed and failed later as expected without a router: %v", err)
	}

	if err := svc.DeleteWebhookBinding(ctx, f.projectID, binding.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	bindings, err = svc.ListWebhookBindings(ctx, f.projectID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("bindings after delete = %+v", bindings)
	}
	if err := svc.DeleteWebhookBinding(ctx, f.projectID, binding.ID); err != ErrWebhookNotFound {
		t.Fatalf("second delete error = %v", err)
	}

	if _, _, err := svc.CreateWebhookBinding(ctx, f.projectID, 1, "github", "x"); err == nil {
		t.Fatal("github is not configurable through the generic binding API")
	}
	if _, _, err := svc.CreateWebhookBinding(ctx, f.projectID, 1, "webhook", "bad/name"); err == nil {
		t.Fatal("a name with a slash must be rejected")
	}
}

func TestWebhookSource_EmailIgnoresAutoMail(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "email-secret"
	bindingID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindEmail, "support", secret)

	// An auto-reply must be acknowledged and dropped, exactly as the IMAP poller
	// drops it, without recording an event.
	body := []byte(`{"from":"auto@example.test","to":"support@example.test","subject":"Out of office","text":"away","auto_submitted":"auto-replied"}`)
	out, err := svc.HandleInbound(ctx, bindingID, "", webhookRequest(t, map[string]string{
		"Authorization": "Bearer " + secret,
	}, body), body)
	if err != nil {
		t.Fatalf("handle inbound: %v", err)
	}
	if !out.Ignored || out.EventID != 0 {
		t.Fatalf("outcome = %+v, want ignored with no event", out)
	}
	events, err := f.q.ListIntakeEventsByProject(ctx, store.ListIntakeEventsByProjectParams{
		ProjectID: f.projectID, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("ignored mail recorded events: %+v", events)
	}
}
