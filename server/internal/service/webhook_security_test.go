package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

func signTimestamped(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func configureWebhookIntake(t *testing.T, f *imbotFixture, svc *WebhookSourceService, sourceKind string) {
	t.Helper()
	if _, err := NewIntakeService(f.db, f.q).UpdateSettings(context.Background(), f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: sourceKind,
			Mode:       "off",
			AllowList:  []string{"alice"},
		}},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	svc.SetInbound(f.router, f.deliverer)
}

func TestWebhookSecurity_TimestampedSignature(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "timestamp-secret"
	bindingID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindWebhook, "ts-hooks", secret)
	configureWebhookIntake(t, f, svc, intake.SourceKindWebhook)
	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})

	body := []byte(`{"id":"evt-ts-1","actor":"alice","thread_id":"t1","text":"deploy"}`)
	now := time.Now()

	// A fresh timestamp with a matching signature is accepted.
	ts := strconv.FormatInt(now.Unix(), 10)
	fresh := map[string]string{
		"X-Webhook-Timestamp":     ts,
		"X-Webhook-Signature-256": signTimestamped(secret, ts, body),
	}
	out, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook, webhookRequest(t, fresh, body), body)
	if err != nil {
		t.Fatalf("fresh timestamp rejected: %v", err)
	}
	if !out.Executed || out.Decision != intake.DecisionAccept {
		t.Fatalf("fresh outcome = %+v", out)
	}

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{
			name: "expired",
			headers: func() map[string]string {
				stale := strconv.FormatInt(now.Add(-301*time.Second).Unix(), 10)
				return map[string]string{
					"X-Webhook-Timestamp":     stale,
					"X-Webhook-Signature-256": signTimestamped(secret, stale, body),
				}
			}(),
		},
		{
			name: "future",
			headers: func() map[string]string {
				ahead := strconv.FormatInt(now.Add(301*time.Second).Unix(), 10)
				return map[string]string{
					"X-Webhook-Timestamp":     ahead,
					"X-Webhook-Signature-256": signTimestamped(secret, ahead, body),
				}
			}(),
		},
		{
			name: "wrong secret",
			headers: map[string]string{
				"X-Webhook-Timestamp":     ts,
				"X-Webhook-Signature-256": signTimestamped("not-the-secret", ts, body),
			},
		},
		{
			name: "raw body signature with a timestamp",
			headers: map[string]string{
				"X-Webhook-Timestamp":     ts,
				"X-Webhook-Signature-256": signBody(secret, body),
			},
		},
		{
			name:    "timestamp without signature",
			headers: map[string]string{"X-Webhook-Timestamp": ts},
		},
		{
			name: "non numeric timestamp",
			headers: map[string]string{
				"X-Webhook-Timestamp":     "not-a-number",
				"X-Webhook-Signature-256": signTimestamped(secret, "not-a-number", body),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook,
				webhookRequest(t, tc.headers, body), body); err != ErrIntakeWebhookUnauthorized {
				t.Fatalf("error = %v, want unauthorized", err)
			}
		})
	}
}

func TestWebhookSecurity_TimestampWindowBoundary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"id":"evt","text":"hello"}`)
	const secret = "boundary-secret"

	for _, tc := range []struct {
		offset time.Duration
		want   bool
	}{
		{0, true},
		{300 * time.Second, true},
		{-300 * time.Second, true},
		{301 * time.Second, false},
		{-301 * time.Second, false},
	} {
		ts := strconv.FormatInt(now.Add(tc.offset).Unix(), 10)
		r := webhookRequest(t, map[string]string{
			"X-Webhook-Timestamp":     ts,
			"X-Webhook-Signature-256": signTimestamped(secret, ts, body),
		}, body)
		if got := genericSecretValidAt(secret, r, body, now); got != tc.want {
			t.Fatalf("offset %v: valid = %v, want %v", tc.offset, got, tc.want)
		}
	}
}

func TestWebhookSecurity_RawHMACAndBearerCompatibility(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "compat-secret"
	bindingID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindWebhook, "compat-hooks", secret)
	configureWebhookIntake(t, f, svc, intake.SourceKindWebhook)
	for i := 0; i < 2; i++ {
		f.router.queue = append(f.router.queue, PlanTarget{
			IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
		})
	}

	// A raw-body HMAC with no timestamp still works (backward compatibility).
	rawBody := []byte(`{"id":"evt-raw-1","actor":"alice","thread_id":"t1","text":"raw"}`)
	out, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook,
		webhookRequest(t, map[string]string{"X-Webhook-Signature-256": signBody(secret, rawBody)}, rawBody), rawBody)
	if err != nil || !out.Executed {
		t.Fatalf("raw HMAC delivery: out=%+v err=%v", out, err)
	}

	// A bearer token is unchanged.
	bearerBody := []byte(`{"id":"evt-bearer-1","actor":"alice","thread_id":"t2","text":"bearer"}`)
	out, err = svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook,
		webhookRequest(t, map[string]string{"Authorization": "Bearer " + secret}, bearerBody), bearerBody)
	if err != nil || !out.Executed {
		t.Fatalf("bearer delivery: out=%+v err=%v", out, err)
	}
}

func TestWebhookSecurity_RateLimit(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "rate-secret"
	bindingID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindWebhook, "rate-hooks", secret)

	// Exhaust this binding's burst deterministically.
	for i := 0; i < webhookRatePerMinute; i++ {
		if !svc.allowDelivery(bindingID) {
			t.Fatalf("delivery %d of the burst was refused", i+1)
		}
	}
	if svc.allowDelivery(bindingID) {
		t.Fatal("the delivery after the per-minute budget was allowed")
	}

	// Every other binding has an independent budget.
	otherID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindWebhook, "rate-hooks-2", "other-secret")
	if !svc.allowDelivery(otherID) {
		t.Fatal("a different binding shared the exhausted budget")
	}

	// A correctly authenticated delivery is rejected once the budget is gone.
	body := []byte(`{"id":"evt-rate-1","text":"hello"}`)
	_, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook,
		webhookRequest(t, map[string]string{"Authorization": "Bearer " + secret}, body), body)
	if !errors.Is(err, ErrWebhookRateLimited) {
		t.Fatalf("exhausted binding error = %v, want rate limited", err)
	}

	// Authentication still precedes the budget: a forgery is 401, not 429.
	if _, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook,
		webhookRequest(t, nil, body), body); !errors.Is(err, ErrIntakeWebhookUnauthorized) {
		t.Fatalf("unauthenticated error = %v, want unauthorized", err)
	}
}

func TestWebhookSecurity_EventFilter(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	const secret = "event-secret"
	bindingID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindWebhook, "event-hooks", secret)
	if _, err := f.db.Exec(`UPDATE project_external_sources SET config = '{"events":["alert"]}' WHERE id = ?`, bindingID); err != nil {
		t.Fatal(err)
	}
	configureWebhookIntake(t, f, svc, intake.SourceKindWebhook)
	f.router.queue = append(f.router.queue, PlanTarget{
		IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID,
	})

	auth := map[string]string{"Authorization": "Bearer " + secret}

	ignoredBody := []byte(`{"id":"evt-hb-1","event":"heartbeat","actor":"alice","thread_id":"t1","text":"ping"}`)
	out, err := svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook, webhookRequest(t, auth, ignoredBody), ignoredBody)
	if err != nil {
		t.Fatalf("ignored event: %v", err)
	}
	if !out.Ignored {
		t.Fatalf("heartbeat outcome = %+v, want ignored", out)
	}
	if f.router.calls != 0 {
		t.Fatalf("a filtered event reached routing: calls = %d", f.router.calls)
	}

	allowedBody := []byte(`{"id":"evt-al-1","event":"alert","actor":"alice","thread_id":"t2","text":"cpu high"}`)
	out, err = svc.HandleInbound(ctx, bindingID, intake.SourceKindWebhook, webhookRequest(t, auth, allowedBody), allowedBody)
	if err != nil {
		t.Fatalf("allowed event: %v", err)
	}
	if out.Ignored || !out.Executed {
		t.Fatalf("alert outcome = %+v", out)
	}

	var events int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM intake_events WHERE event_ext_id = 'evt-hb-1'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("filtered event was recorded %d times", events)
	}
}

func TestWebhookSecurity_BodyLimit(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	svc, creds := newWebhookTestService(t, f)

	bindingID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindWebhook, "body-hooks", "body-secret")

	check := func(config string, want int64) {
		t.Helper()
		if _, err := f.db.Exec(`UPDATE project_external_sources SET config = ? WHERE id = ?`, config, bindingID); err != nil {
			t.Fatal(err)
		}
		got, err := svc.BodyLimit(ctx, bindingID, intake.SourceKindWebhook)
		if err != nil {
			t.Fatalf("config %s: %v", config, err)
		}
		if got != want {
			t.Fatalf("config %s: limit = %d, want %d", config, got, want)
		}
	}

	check(`{}`, defaultWebhookBodyBytes)
	check(``, defaultWebhookBodyBytes)
	check(`not-json`, defaultWebhookBodyBytes)
	check(`{"max_body_bytes":0}`, defaultWebhookBodyBytes)
	check(`{"max_body_bytes":-5}`, defaultWebhookBodyBytes)
	check(`{"max_body_bytes":2097152}`, 2<<20)
	check(`{"max_body_bytes":999999999}`, maxWebhookBodyBytes)

	if _, err := svc.BodyLimit(ctx, 999999, intake.SourceKindWebhook); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("unknown binding error = %v", err)
	}
	if _, err := svc.BodyLimit(ctx, bindingID, intake.SourceKindEmail); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("provider mismatch error = %v", err)
	}

	githubID := bindWebhookSource(t, f, creds, f.projectID, intake.SourceKindGitHub, "acme/repo", "gh-secret")
	got, err := svc.BodyLimit(ctx, githubID, intake.SourceKindGitHub)
	if err != nil {
		t.Fatalf("github limit: %v", err)
	}
	if got != maxWebhookBodyBytes {
		t.Fatalf("github limit = %d, want %d", got, maxWebhookBodyBytes)
	}
}
