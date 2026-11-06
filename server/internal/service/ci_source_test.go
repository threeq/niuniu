package service

// CI inbound Source: provider authentication, event filtering, normalization
// into the shared intake pipeline, and the success/recovery "close the loop"
// contract (resume a bound task, ignore an unbound one).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func bindCISource(t *testing.T, f *imbotFixture, creds *ExternalCredentialService, name, secret, config string) int64 {
	t.Helper()
	cred, err := creds.Create(context.Background(), ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider:  integration.ProviderName(intake.SourceKindCI),
		Alias:     "test-ci-" + name,
		RawConfig: map[string]any{"webhook_secret": secret},
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	row, err := f.q.AddProjectExternalSource(context.Background(), store.AddProjectExternalSourceParams{
		ProjectID: f.projectID, Provider: intake.SourceKindCI, SourceKey: name,
		CredentialID: sql.NullInt64{Int64: cred.ID, Valid: true}, Config: config,
	})
	if err != nil {
		t.Fatalf("bind ci source: %v", err)
	}
	return row.ID
}

func ciRequest(t *testing.T, headers map[string]string, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/integrations/ci/1", bytes.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func setCIAutoMode(t *testing.T, f *imbotFixture) {
	t.Helper()
	if _, err := NewIntakeService(f.db, f.q).UpdateSettings(context.Background(), f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{SourceKind: intake.SourceKindCI, Mode: "auto"}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
}

func seedCIRule(t *testing.T, f *imbotFixture, eventKind, name string) {
	t.Helper()
	conditions, err := json.Marshal(intake.RuleConditions{EventKind: eventKind})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.CreateIntakeRule(context.Background(), store.CreateIntakeRuleParams{
		ProjectID: f.projectID, SourceKind: intake.SourceKindCI, Name: name,
		Instruction: "resolve " + eventKind, ConditionsJson: string(conditions),
		Priority: 100, Enabled: 1,
	}); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
}

const ciFailurePayload = `{
  "id":"ci-1",
  "repository":"acme/api",
  "branch":"main",
  "commit":"abc123",
  "pipeline":{"id":"123","name":"build","url":"https://ci.example/123"},
  "job":{"id":"456","name":"test","url":"https://ci.example/456"},
  "status":"failed",
  "summary":"2 tests failed",
  "actor":{"id":"u1","name":"Alice"}
}`

func ciSuccessPayload(id string) string {
	return `{
	  "id":"` + id + `",
	  "repository":"acme/api",
	  "branch":"main",
	  "commit":"abc123",
	  "pipeline":{"id":"123","name":"build","url":"https://ci.example/123"},
	  "status":"succeeded",
	  "actor":{"id":"u1","name":"Alice"}
	}`
}

// A failing pipeline in auto mode with a matching rule creates the repair task.
func TestCISource_FailureCreatesTaskInAutoMode(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "ci-secret"
	bindingID := bindCISource(t, f, creds, "acme-build", secret, `{"provider":"generic"}`)
	setCIAutoMode(t, f)
	seedCIRule(t, f, intake.EventKindCIFailed, "CI 失败自动修复")

	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})
	svc.SetInbound(f.router, f.deliverer)

	body := []byte(ciFailurePayload)
	auth := map[string]string{"Authorization": "Bearer " + secret}
	out, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindCI, ciRequest(t, auth, body), body)
	if err != nil {
		t.Fatalf("delivery: %v", err)
	}
	if out.Decision != intake.DecisionAccept || !out.Executed {
		t.Fatalf("outcome = %+v", out)
	}
	if f.router.calls != 1 || len(f.deliverer.calls) != 1 {
		t.Fatalf("router=%d deliveries=%d, want 1/1", f.router.calls, len(f.deliverer.calls))
	}

	var (
		sourceKind, eventKind, decision string
	)
	if err := f.db.QueryRow(`
		SELECT e.source_kind, e.event_kind, d.decision
		FROM intake_events e JOIN intake_decisions d ON d.event_id = e.id
		WHERE e.source_kind = 'ci'`).Scan(&sourceKind, &eventKind, &decision); err != nil {
		t.Fatalf("read event/decision: %v", err)
	}
	if sourceKind != intake.SourceKindCI || eventKind != intake.EventKindCIFailed || decision != string(intake.DecisionAccept) {
		t.Fatalf("event=%s/%s decision=%s", sourceKind, eventKind, decision)
	}

	// The issue is bound to the pipeline thread, so the success event can resume it.
	var externalSource, externalID string
	if err := f.db.QueryRow(`SELECT external_source, external_id FROM issues WHERE id = ?`, f.issueID).
		Scan(&externalSource, &externalID); err != nil {
		t.Fatalf("provenance: %v", err)
	}
	if externalSource != intake.SourceKindCI {
		t.Fatalf("external_source = %q", externalSource)
	}
	wantID := "acme-build/" + strconv.FormatInt(bindingID, 10) + "#acme/api#main#abc123"
	if externalID != wantID {
		t.Fatalf("external_id = %q, want %q", externalID, wantID)
	}
}

// In observe mode a failure is recorded as a pending accept (待校准), never
// executed.
func TestCISource_FailureEntersCalibrationInObserveMode(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "ci-secret"
	bindingID := bindCISource(t, f, creds, "acme-build", secret, `{"provider":"generic"}`)
	if _, err := NewIntakeService(f.db, f.q).UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{SourceKind: intake.SourceKindCI, Mode: "observe"}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	svc.SetInbound(f.router, f.deliverer)

	body := []byte(ciFailurePayload)
	auth := map[string]string{"Authorization": "Bearer " + secret}
	out, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindCI, ciRequest(t, auth, body), body)
	if err != nil {
		t.Fatalf("delivery: %v", err)
	}
	if out.Decision != intake.DecisionAccept || out.Executed {
		t.Fatalf("outcome = %+v, want pending accept", out)
	}
	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("observe mode must not route or deliver")
	}
}

// A success event whose thread is not bound records an ignore and creates no
// task, even when a matching rule would otherwise allow auto execution.
func TestCISource_SuccessUnboundIsIgnored(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "ci-secret"
	bindingID := bindCISource(t, f, creds, "acme-build", secret,
		`{"provider":"generic","events":["ci_failed","ci_succeeded"]}`)
	setCIAutoMode(t, f)
	seedCIRule(t, f, intake.EventKindCISucceeded, "CI 通过")
	svc.SetInbound(f.router, f.deliverer)

	body := []byte(ciSuccessPayload("ci-success-unbound"))
	auth := map[string]string{"Authorization": "Bearer " + secret}
	out, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindCI, ciRequest(t, auth, body), body)
	if err != nil {
		t.Fatalf("delivery: %v", err)
	}
	if out.Decision != intake.DecisionIgnore || out.ReasonCode != intake.ReasonUnbound || out.Executed {
		t.Fatalf("outcome = %+v, want ignore/unbound", out)
	}
	if f.router.calls != 0 || len(f.deliverer.calls) != 0 {
		t.Fatalf("unbound success created work")
	}

	var decision string
	if err := f.db.QueryRow(`SELECT decision FROM intake_decisions WHERE event_id = ?`, out.EventID).
		Scan(&decision); err != nil {
		t.Fatalf("decision row: %v", err)
	}
	if decision != string(intake.DecisionIgnore) {
		t.Fatalf("decision = %q", decision)
	}
}

// A success event following a failure resumes the bound task and delivers the
// "CI is green" signal instead of creating another task.
func TestCISource_SuccessBoundResumes(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "ci-secret"
	bindingID := bindCISource(t, f, creds, "acme-build", secret,
		`{"provider":"generic","events":["ci_failed","ci_succeeded"]}`)
	setCIAutoMode(t, f)
	seedCIRule(t, f, intake.EventKindCIFailed, "CI 失败自动修复")
	seedCIRule(t, f, intake.EventKindCISucceeded, "CI 通过")

	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})
	svc.SetInbound(f.router, f.deliverer)
	auth := map[string]string{"Authorization": "Bearer " + secret}

	failBody := []byte(ciFailurePayload)
	if _, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindCI, ciRequest(t, auth, failBody), failBody); err != nil {
		t.Fatalf("failure delivery: %v", err)
	}

	successBody := []byte(ciSuccessPayload("ci-success-bound"))
	out, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindCI, ciRequest(t, auth, successBody), successBody)
	if err != nil {
		t.Fatalf("success delivery: %v", err)
	}
	if out.Decision != intake.DecisionResume || !out.Executed {
		t.Fatalf("success outcome = %+v, want resume", out)
	}
	if f.router.calls != 1 {
		t.Fatalf("resume created another task: router calls = %d", f.router.calls)
	}
	if len(f.deliverer.calls) != 2 {
		t.Fatalf("deliveries = %d, want 2 (accept then resume)", len(f.deliverer.calls))
	}
	if got := f.deliverer.calls[1].workspaceID; got != f.wsID {
		t.Fatalf("resume delivered to workspace %d, want %d", got, f.wsID)
	}
}

// A redelivery of the same event id is deduped: no second task, no second
// delivery.
func TestCISource_DuplicateDelivery(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "ci-secret"
	bindingID := bindCISource(t, f, creds, "acme-build", secret, `{"provider":"generic"}`)
	setCIAutoMode(t, f)
	seedCIRule(t, f, intake.EventKindCIFailed, "CI 失败自动修复")
	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})
	svc.SetInbound(f.router, f.deliverer)
	auth := map[string]string{"Authorization": "Bearer " + secret}
	body := []byte(ciFailurePayload)

	first, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindCI, ciRequest(t, auth, body), body)
	if err != nil || first.Duplicate {
		t.Fatalf("first delivery = %+v, %v", first, err)
	}
	second, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindCI, ciRequest(t, auth, body), body)
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if !second.Duplicate || second.Executed {
		t.Fatalf("redelivery = %+v, want duplicate no-op", second)
	}
	if f.router.calls != 1 {
		t.Fatalf("duplicate created work: router calls = %d", f.router.calls)
	}
}

// Wrong or missing credentials are rejected before any event is recorded.
func TestCISource_WrongTokenUnauthorized(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "gitlab-token"
	bindingID := bindCISource(t, f, creds, "gitlab-pipeline", secret, `{"provider":"gitlab"}`)
	svc.SetInbound(f.router, f.deliverer)
	body := []byte(`{"object_kind":"pipeline","object_attributes":{"id":1,"status":"failed","ref":"main","sha":"abc"},"project":{"path_with_namespace":"acme/api"},"user":{"id":7,"name":"Alice"}}`)

	for _, headers := range []map[string]string{
		{"X-Gitlab-Token": "wrong"},
		{},
		{"Authorization": "Bearer " + secret},
	} {
		if _, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindCI, ciRequest(t, headers, body), body); err != ErrIntakeWebhookUnauthorized {
			t.Fatalf("headers %v error = %v, want unauthorized", headers, err)
		}
	}
	var events int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM intake_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("rejected deliveries recorded %d events", events)
	}
}

// An event outside the binding's subscription is acknowledged as ignored.
func TestCISource_DisabledEventIgnored(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "ci-secret"
	// Default subscription: failures only.
	bindingID := bindCISource(t, f, creds, "acme-build", secret, `{"provider":"generic"}`)
	setCIAutoMode(t, f)
	svc.SetInbound(f.router, f.deliverer)

	body := []byte(ciSuccessPayload("ci-success-disabled"))
	auth := map[string]string{"Authorization": "Bearer " + secret}
	out, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindCI, ciRequest(t, auth, body), body)
	if err != nil {
		t.Fatalf("delivery: %v", err)
	}
	if !out.Ignored {
		t.Fatalf("outcome = %+v, want ignored", out)
	}
	if f.router.calls != 0 {
		t.Fatalf("disabled event created work")
	}
}

// Every provider preset accepts its own credential carrier and rejects another
// provider's.
func TestCISource_ProviderAuthCarriers(t *testing.T) {
	body := []byte(`{"status":"failed"}`)
	now := time.Now()

	t.Run("gitlab token", func(t *testing.T) {
		setup := func(secret string) (*WebhookSourceService, int64) {
			f := newIMBotFixture(t)
			svc, creds := newWebhookTestService(t, f)
			return svc, bindCISource(t, f, creds, "gl", secret, `{"provider":"gitlab"}`)
		}
		svc, id := setup("gl-secret")
		if _, err := svc.HandleInbound(context.Background(), id, intake.SourceKindCI,
			ciRequest(t, map[string]string{"X-Gitlab-Token": "gl-secret"}, body), body); err != nil {
			t.Fatalf("valid gitlab token: %v", err)
		}
	})
	t.Run("buildkite token", func(t *testing.T) {
		f := newIMBotFixture(t)
		svc, creds := newWebhookTestService(t, f)
		id := bindCISource(t, f, creds, "bk", "bk-secret", `{"provider":"buildkite"}`)
		if _, err := svc.HandleInbound(context.Background(), id, intake.SourceKindCI,
			ciRequest(t, map[string]string{"X-Buildkite-Token": "bk-secret"}, body), body); err != nil {
			t.Fatalf("buildkite token: %v", err)
		}
	})
	t.Run("jenkins bearer", func(t *testing.T) {
		f := newIMBotFixture(t)
		svc, creds := newWebhookTestService(t, f)
		id := bindCISource(t, f, creds, "jk", "jk-secret", `{"provider":"jenkins"}`)
		if _, err := svc.HandleInbound(context.Background(), id, intake.SourceKindCI,
			ciRequest(t, map[string]string{"Authorization": "Bearer jk-secret"}, body), body); err != nil {
			t.Fatalf("jenkins bearer: %v", err)
		}
	})
	t.Run("circleci hmac", func(t *testing.T) {
		f := newIMBotFixture(t)
		svc, creds := newWebhookTestService(t, f)
		id := bindCISource(t, f, creds, "cc", "cc-secret", `{"provider":"circleci"}`)
		mac := hmac.New(sha256.New, []byte("cc-secret"))
		_, _ = mac.Write(body)
		sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if _, err := svc.HandleInbound(context.Background(), id, intake.SourceKindCI,
			ciRequest(t, map[string]string{"Circleci-Signature": sig}, body), body); err != nil {
			t.Fatalf("circleci signature: %v", err)
		}
	})
	t.Run("generic timestamped signature", func(t *testing.T) {
		f := newIMBotFixture(t)
		svc, creds := newWebhookTestService(t, f)
		id := bindCISource(t, f, creds, "gen", "gen-secret", `{"provider":"generic"}`)
		ts := strconv.FormatInt(now.Unix(), 10)
		mac := hmac.New(sha256.New, []byte("gen-secret"))
		_, _ = mac.Write([]byte(ts + "."))
		_, _ = mac.Write(body)
		sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if _, err := svc.HandleInbound(context.Background(), id, intake.SourceKindCI,
			ciRequest(t, map[string]string{"X-Niuniu-Timestamp": ts, "X-Niuniu-Signature-256": sig}, body), body); err != nil {
			t.Fatalf("generic timestamped signature: %v", err)
		}
		// A signature without a timestamp is rejected: no replay window.
		if _, err := svc.HandleInbound(context.Background(), id, intake.SourceKindCI,
			ciRequest(t, map[string]string{"X-Niuniu-Signature-256": sig}, body), body); err != ErrIntakeWebhookUnauthorized {
			t.Fatalf("timestamp-less signature error = %v", err)
		}
	})
}
