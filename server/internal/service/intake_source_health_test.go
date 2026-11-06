package service

// Delivery-health observability (design §12): per-binding success/failure and
// 24h counters aggregated from intake_events/intake_decisions, the volatile
// 401/400/duplicate tracker, status rules, IMAP auth failure handling, and the
// log-hygiene guarantee (no secret, no payload body).

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func insertIntakeBinding(t *testing.T, db *sql.DB, projectID int64, kind, name, config string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO project_external_sources (project_id, provider, source_key, config) VALUES (?, ?, ?, ?)`,
		projectID, kind, name, config,
	)
	if err != nil {
		t.Fatalf("insert binding: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("binding id: %v", err)
	}
	return id
}

func insertDeliveryEvent(t *testing.T, db *sql.DB, id, projectID int64, kind, ref, eventID, decision string, receivedAt time.Time) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO intake_events (id, project_id, source_kind, source_ref, event_ext_id, event_kind, received_at)
		 VALUES (?, ?, ?, ?, ?, 'message', ?)`,
		id, projectID, kind, ref, eventID, receivedAt.UTC().Format("2006-01-02 15:04:05"),
	); err != nil {
		t.Fatalf("insert event %d: %v", id, err)
	}
	if decision == "" {
		return
	}
	if _, err := db.Exec(
		`INSERT INTO intake_decisions (id, event_id, project_id, mode_at_decision, strength, decision, executed)
		 VALUES (?, ?, ?, 'observe', 'inferred', ?, 0)`,
		id, id, projectID, decision,
	); err != nil {
		t.Fatalf("insert decision %d: %v", id, err)
	}
}

func healthBySourceID(t *testing.T, svc *IntakeSourceService, projectID, sourceID int64) IntakeSourceHealth {
	t.Helper()
	list, err := svc.HealthByProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	for _, h := range list {
		if h.SourceID == sourceID {
			return h
		}
	}
	t.Fatalf("source %d not in health list %+v", sourceID, list)
	return IntakeSourceHealth{}
}

func TestIntakeSourceHealth_NeverThenAggregatesDeliveries(t *testing.T) {
	f := newIMBotFixture(t)
	svc := NewIntakeSourceService(f.db, f.q, nil, nil, nil)
	bindingID := insertIntakeBinding(t, f.db, f.projectID, intake.SourceKindCI, "pipeline", `{"mode":"push","provider":"gitlab"}`)
	ref := eventRef("pipeline", bindingID)

	h := healthBySourceID(t, svc, f.projectID, bindingID)
	if h.Status != IntakeStatusNever {
		t.Fatalf("status before any delivery = %q, want never", h.Status)
	}

	now := time.Now()
	// Recent accepted + ignored, plus an accepted event older than 24h.
	insertDeliveryEvent(t, f.db, 1, f.projectID, intake.SourceKindCI, ref, "evt-1", "accept", now.Add(-time.Hour))
	insertDeliveryEvent(t, f.db, 2, f.projectID, intake.SourceKindCI, ref, "evt-2", "ignore", now.Add(-2*time.Hour))
	insertDeliveryEvent(t, f.db, 3, f.projectID, intake.SourceKindCI, ref, "evt-3", "accept", now.Add(-48*time.Hour))

	h = healthBySourceID(t, svc, f.projectID, bindingID)
	if h.Status != IntakeStatusOK {
		t.Fatalf("status = %q, want ok", h.Status)
	}
	if h.Success24h != 1 || h.Rejected24h != 1 {
		t.Fatalf("24h counts success=%d rejected=%d, want 1/1", h.Success24h, h.Rejected24h)
	}
	if h.LastSuccessAt == nil || h.LastSuccessAt.Before(now.Add(-3*time.Hour)) {
		t.Fatalf("last success = %v, want the recent accepted event", h.LastSuccessAt)
	}
	if h.Duplicate24h != 0 {
		t.Fatalf("duplicate 24h = %d, want 0 without tracker records", h.Duplicate24h)
	}
}

func TestIntakeSourceHealth_AuthFailureStreakAlertsThenRecovers(t *testing.T) {
	f := newIMBotFixture(t)
	tracker := NewDeliveryHealthTracker()
	var alerts []DeliveryAlert
	tracker.SetAlertHandler(func(a DeliveryAlert) { alerts = append(alerts, a) })
	svc := NewIntakeSourceService(f.db, f.q, nil, nil, nil)
	svc.SetDeliveryHealth(tracker)
	bindingID := insertIntakeBinding(t, f.db, f.projectID, intake.SourceKindWebhook, "hooks", `{"mode":"push"}`)

	tracker.Record(bindingID, f.projectID, intake.SourceKindWebhook, DeliveryAuthFailed)
	tracker.Record(bindingID, f.projectID, intake.SourceKindWebhook, DeliveryAuthFailed)
	if len(alerts) != 0 {
		t.Fatalf("two failures must not alert: %+v", alerts)
	}
	if h := healthBySourceID(t, svc, f.projectID, bindingID); h.Status != IntakeStatusDegraded {
		t.Fatalf("status after two 401s = %q, want degraded", h.Status)
	}

	tracker.Record(bindingID, f.projectID, intake.SourceKindWebhook, DeliveryAuthFailed)
	if len(alerts) != 1 || alerts[0].ReasonCode != DeliveryAlertSecretMisconfigured {
		t.Fatalf("alerts = %+v, want one secret_misconfigured", alerts)
	}
	h := healthBySourceID(t, svc, f.projectID, bindingID)
	if h.Status != IntakeStatusError || h.Alert != DeliveryAlertSecretMisconfigured {
		t.Fatalf("health = %+v, want error + secret_misconfigured", h)
	}

	// A successful delivery clears the streak and the alert.
	tracker.Record(bindingID, f.projectID, intake.SourceKindWebhook, DeliverySuccess)
	h = healthBySourceID(t, svc, f.projectID, bindingID)
	if h.Status != IntakeStatusOK || h.Alert != "" || h.ConsecutiveFailures != 0 {
		t.Fatalf("health after recovery = %+v, want ok and no alert", h)
	}

	// A different failure kind breaks the 401 streak; three 400s alert on their own.
	tracker.Record(bindingID, f.projectID, intake.SourceKindWebhook, DeliveryBadRequest)
	tracker.Record(bindingID, f.projectID, intake.SourceKindWebhook, DeliveryBadRequest)
	tracker.Record(bindingID, f.projectID, intake.SourceKindWebhook, DeliveryBadRequest)
	if len(alerts) != 2 || alerts[1].ReasonCode != DeliveryAlertPayloadMismatch {
		t.Fatalf("alerts = %+v, want a second payload_mismatch", alerts)
	}
}

func TestIntakeSourceHealth_DuplicatesAreCountedWithoutEvents(t *testing.T) {
	f := newIMBotFixture(t)
	tracker := NewDeliveryHealthTracker()
	svc := NewIntakeSourceService(f.db, f.q, nil, nil, nil)
	svc.SetDeliveryHealth(tracker)
	bindingID := insertIntakeBinding(t, f.db, f.projectID, intake.SourceKindWebhook, "hooks", `{"mode":"push"}`)

	for i := 0; i < 3; i++ {
		tracker.Record(bindingID, f.projectID, intake.SourceKindWebhook, DeliveryDuplicate)
	}
	h := healthBySourceID(t, svc, f.projectID, bindingID)
	if h.Duplicate24h != 3 {
		t.Fatalf("duplicate 24h = %d, want 3", h.Duplicate24h)
	}
	if h.Status != IntakeStatusOK {
		t.Fatalf("status = %q, want ok (duplicates are successful deliveries)", h.Status)
	}
}

func TestIntakeSourceHealth_IMAPAuthFailureMarksError(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc := NewIntakeSourceService(f.db, f.q, nil, nil, nil)
	bindingID := insertIntakeBinding(t, f.db, f.projectID, intake.SourceKindEmail, "inbox", `{"mode":"imap","mailbox":"INBOX"}`)
	if _, err := f.q.UpsertEmailPollState(ctx, store.UpsertEmailPollStateParams{
		ProjectID: f.projectID, BindingID: bindingID, Mailbox: "INBOX",
	}); err != nil {
		t.Fatalf("poll state: %v", err)
	}
	if _, err := f.q.SetEmailPollBackoff(ctx, store.SetEmailPollBackoffParams{
		BackoffUntil: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true},
		LastError:    (&ErrIMAPAuth{Detail: "AUTHENTICATIONFAILED"}).Error(),
		BindingID:    bindingID,
		Mailbox:      "INBOX",
	}); err != nil {
		t.Fatalf("backoff: %v", err)
	}

	h := healthBySourceID(t, svc, f.projectID, bindingID)
	if h.Status != IntakeStatusError {
		t.Fatalf("status = %q, want error for an IMAP auth failure (even while backing off)", h.Status)
	}
	if h.Alert != DeliveryAlertIMAPAuth {
		t.Fatalf("alert = %q, want imap_auth_failed (recovered from the persisted cursor)", h.Alert)
	}
	if h.BackoffUntil == nil {
		t.Fatalf("backoff deadline missing: %+v", h)
	}
	if !strings.Contains(h.LastError, "login rejected") {
		t.Fatalf("last error = %q, want the IMAP login rejection", h.LastError)
	}
}

func TestIMAPFailureOutcome(t *testing.T) {
	if got := imapFailureOutcome(&ErrIMAPAuth{Detail: "x"}); got != DeliveryIMAPAuthFailed {
		t.Fatalf("auth error -> %q, want %q", got, DeliveryIMAPAuthFailed)
	}
	if got := imapFailureOutcome(context.DeadlineExceeded); got != DeliveryTransportFailed {
		t.Fatalf("transport error -> %q, want %q", got, DeliveryTransportFailed)
	}
}

// A 401 streak must log only identity and reason: the delivery body and the
// configured secret must never appear in a log line (design §12).
func TestDeliveryHealth_AlertLogOmitsSecretAndBody(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)
	tracker := NewDeliveryHealthTracker()
	svc.SetDeliveryHealth(tracker)

	const secret = "super-secret-token"
	bindingID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindWebhook, "hooks", secret)

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	body := []byte(`{"text":"BODY_SENTINEL_12345"}`)
	for i := 0; i < 3; i++ {
		req := webhookRequest(t, map[string]string{"Authorization": "Bearer wrong"}, body)
		if _, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook, req, body); err == nil {
			t.Fatal("wrong bearer must be rejected")
		}
	}

	logged := buf.String()
	if !strings.Contains(logged, DeliveryAlertSecretMisconfigured) {
		t.Fatalf("expected an alert log line, got %q", logged)
	}
	if strings.Contains(logged, secret) {
		t.Fatalf("log leaked the configured secret: %q", logged)
	}
	if strings.Contains(logged, "BODY_SENTINEL_12345") {
		t.Fatalf("log leaked the delivery body: %q", logged)
	}
	if !strings.Contains(logged, "binding=") || !strings.Contains(logged, "provider=") {
		t.Fatalf("log must identify binding and provider: %q", logged)
	}
}

func TestMissionControl_IncludesIntakeSourceHealth(t *testing.T) {
	db := openOrgTestDB(t)
	userID, projectID, _, _ := seedMissionControl(t, db)

	bindingID := insertIntakeBinding(t, db, projectID, intake.SourceKindCI, "pipeline", `{"mode":"push","provider":"gitlab"}`)
	now := time.Now()
	insertDeliveryEvent(t, db, 1, projectID, intake.SourceKindCI, eventRef("pipeline", bindingID), "evt-1", "accept", now.Add(-time.Hour))

	tracker := NewDeliveryHealthTracker()
	tracker.Record(bindingID, projectID, intake.SourceKindCI, DeliveryDuplicate)
	sources := NewIntakeSourceService(db, store.NewQueries(db), nil, nil, nil)
	sources.SetDeliveryHealth(tracker)

	mc := newMissionControlService(db)
	mc.SetIntakeSources(sources)
	view, err := mc.View(context.Background(), userID, nil)
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if len(view.Intake.Sources) != 1 {
		t.Fatalf("sources = %+v, want one binding", view.Intake.Sources)
	}
	got := view.Intake.Sources[0]
	if got.SourceID != bindingID || got.Success24h != 1 || got.Duplicate24h != 1 || got.Status != IntakeStatusOK {
		t.Fatalf("source health = %+v", got)
	}
}
