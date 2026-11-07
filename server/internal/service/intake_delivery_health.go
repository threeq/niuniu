package service

// Delivery health tracker (design §12): the volatile half of per-binding
// delivery observability.
//
// intake_events / intake_decisions already record every event that entered the
// pipeline, so the durable part of health is aggregated from those tables. But
// the signals an operator needs when a binding silently stops working
// deliberately leave no row: a forged delivery is rejected 401 and records
// nothing (design §8), a malformed payload is 400, and an idempotent redelivery
// is a duplicate. Design §9.1 forbids a new table for delivery health, so those
// outcomes live in a bounded in-memory tracker.
//
// The tracker never receives a secret or a payload body — callers pass only a
// coarse outcome — so a log line built here cannot leak either. A restart
// clears the counters; the durable half (last success, 24h success/rejected)
// is recomputed from the tables on the next read.

import (
	"log/slog"
	"sync"
	"time"
)

// DeliveryOutcome is the coarse result of one delivery attempt.
type DeliveryOutcome string

const (
	// DeliverySuccess is an event that entered the pipeline and produced work.
	DeliverySuccess DeliveryOutcome = "success"
	// DeliveryDuplicate is an idempotent redelivery: 200, but no new event.
	DeliveryDuplicate DeliveryOutcome = "duplicate"
	// DeliveryRejected is a delivery the policy ignored (observe mode, stopped
	// binding, capped day, no matching rule). The transport was healthy.
	DeliveryRejected DeliveryOutcome = "rejected"
	// DeliveryIgnored is an event type the binding does not subscribe to.
	DeliveryIgnored DeliveryOutcome = "ignored"
	// DeliveryAuthFailed is a wrong/missing secret (HTTP 401).
	DeliveryAuthFailed DeliveryOutcome = "auth_failed"
	// DeliveryBadRequest is a body the adapter could not normalize (HTTP 400).
	DeliveryBadRequest DeliveryOutcome = "bad_request"
	// DeliveryRateLimited is an exhausted per-binding budget (HTTP 429).
	DeliveryRateLimited DeliveryOutcome = "rate_limited"
	// DeliveryForbidden is a peer outside the binding's network policy (403).
	DeliveryForbidden DeliveryOutcome = "forbidden"
	// DeliveryPollOK is a successful IMAP poll that found no new mail (or only
	// already-seen mail). It proves the channel works and resets a failure streak.
	DeliveryPollOK DeliveryOutcome = "poll_ok"
	// DeliveryTransportFailed is an IMAP/pipeline failure that is not a login
	// rejection (DNS, TLS, mailbox selection, intake persistence).
	DeliveryTransportFailed DeliveryOutcome = "transport_failed"
	// DeliveryIMAPAuthFailed is a mailbox login rejection. It marks the binding
	// error immediately, without waiting for a streak (design §12).
	DeliveryIMAPAuthFailed DeliveryOutcome = "imap_auth_failed"
)

// Alert reason codes carried by DeliveryAlert and surfaced to the UI.
const (
	DeliveryAlertSecretMisconfigured = "secret_misconfigured"
	DeliveryAlertPayloadMismatch     = "payload_mismatch"
	DeliveryAlertIMAPAuth            = "imap_auth_failed"

	// deliveryAlertStreak is "连续 3 次" from design §12.
	deliveryAlertStreak = 3
)

// DeliveryAlert is emitted when a binding crosses an alert threshold. It
// carries no secret and no payload: only identity and a reason code.
type DeliveryAlert struct {
	BindingID  int64
	ProjectID  int64
	Provider   string
	ReasonCode string
	Message    string
}

// DeliveryHealthSnapshot is the tracker's view of one binding.
type DeliveryHealthSnapshot struct {
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	LastFailureReason   string
	Duplicate24h        int64
	ConsecutiveFailures int
	// Alert is the currently active alert reason (empty when healthy), so the
	// panel can explain a degraded binding even between delivery attempts.
	Alert       string
	AlertReason string
	// Recorded is true once the tracker has seen any outcome for the binding.
	Recorded bool
}

type deliveryBucket struct {
	success   int64
	duplicate int64
	rejected  int64
	failure   int64
}

type deliveryHealthState struct {
	lastSuccess       time.Time
	lastFailure       time.Time
	lastFailureReason string
	consecutive       int
	consecutiveAuth   int
	consecutiveBad    int
	alert             string
	alertReason       string
	// deliveries counts real delivery outcomes. A successful empty poll is NOT a
	// delivery: an email binding that has never received mail stays `never`.
	deliveries int64
	buckets    map[int64]*deliveryBucket
}

// DeliveryHealthTracker is shared by the inbound webhook service, the IMAP
// poller and the read paths (intake sources / mission control).
type DeliveryHealthTracker struct {
	now func() time.Time

	mu       sync.Mutex
	bindings map[int64]*deliveryHealthState
	alert    func(DeliveryAlert)
}

func NewDeliveryHealthTracker() *DeliveryHealthTracker {
	return &DeliveryHealthTracker{
		now:      time.Now,
		bindings: map[int64]*deliveryHealthState{},
	}
}

// SetClock pins the tracker's clock (tests).
func (t *DeliveryHealthTracker) SetClock(now func() time.Time) {
	if t == nil || now == nil {
		return
	}
	t.now = now
}

// SetAlertHandler registers the notification sink. It is called at most once per
// streak, outside the tracker lock.
func (t *DeliveryHealthTracker) SetAlertHandler(fn func(DeliveryAlert)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.alert = fn
}

func (t *DeliveryHealthTracker) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// Record accounts for one delivery attempt. bindingID is the
// project_external_sources id; provider is the source kind (ci/email/webhook).
func (t *DeliveryHealthTracker) Record(bindingID, projectID int64, provider string, outcome DeliveryOutcome) {
	if t == nil || bindingID <= 0 {
		return
	}
	now := t.clock()
	var (
		alert     DeliveryAlert
		hasAlert  bool
		logReason string
	)

	t.mu.Lock()
	st := t.bindings[bindingID]
	if st == nil {
		st = &deliveryHealthState{buckets: map[int64]*deliveryBucket{}}
		t.bindings[bindingID] = st
	}
	t.prune(st, now)
	bucket := st.buckets[now.Unix()/3600]
	if bucket == nil {
		bucket = &deliveryBucket{}
		st.buckets[now.Unix()/3600] = bucket
	}

	if outcome != DeliveryPollOK {
		st.deliveries++
	}
	switch outcome {
	case DeliverySuccess:
		st.lastSuccess = now
		st.resetStreak()
		bucket.success++
	case DeliveryDuplicate:
		st.resetStreak()
		bucket.duplicate++
	case DeliveryRejected, DeliveryIgnored, DeliveryPollOK:
		// The transport and credentials worked even though no task was created.
		st.resetStreak()
		if outcome == DeliveryRejected {
			bucket.rejected++
		}
	case DeliveryAuthFailed:
		bucket.failure++
		st.consecutive++
		st.consecutiveBad = 0
		st.consecutiveAuth++
		st.lastFailure, st.lastFailureReason = now, string(outcome)
		if st.consecutiveAuth == deliveryAlertStreak {
			st.alert, st.alertReason = DeliveryAlertSecretMisconfigured, string(outcome)
			alert = DeliveryAlert{
				BindingID: bindingID, ProjectID: projectID, Provider: provider,
				ReasonCode: DeliveryAlertSecretMisconfigured,
				Message:    "three consecutive 401 responses — the secret may be misconfigured",
			}
			hasAlert, logReason = true, DeliveryAlertSecretMisconfigured
		}
	case DeliveryBadRequest:
		bucket.failure++
		st.consecutive++
		st.consecutiveAuth = 0
		st.consecutiveBad++
		st.lastFailure, st.lastFailureReason = now, string(outcome)
		if st.consecutiveBad == deliveryAlertStreak {
			st.alert, st.alertReason = DeliveryAlertPayloadMismatch, string(outcome)
			alert = DeliveryAlert{
				BindingID: bindingID, ProjectID: projectID, Provider: provider,
				ReasonCode: DeliveryAlertPayloadMismatch,
				Message:    "three consecutive 400 responses — the payload format does not match",
			}
			hasAlert, logReason = true, DeliveryAlertPayloadMismatch
		}
	case DeliveryIMAPAuthFailed:
		bucket.failure++
		st.consecutive++
		st.consecutiveAuth, st.consecutiveBad = 0, 0
		st.lastFailure, st.lastFailureReason = now, string(outcome)
		st.alert, st.alertReason = DeliveryAlertIMAPAuth, string(outcome)
		// One alert per failure streak: the backoff keeps retrying, and a toast
		// for every retry would be noise.
		if st.consecutive == 1 {
			alert = DeliveryAlert{
				BindingID: bindingID, ProjectID: projectID, Provider: provider,
				ReasonCode: DeliveryAlertIMAPAuth,
				Message:    "the mailbox rejected the login — the IMAP credential may be invalid",
			}
			hasAlert, logReason = true, DeliveryAlertIMAPAuth
		}
	default: // rate limited, forbidden, transport failure
		bucket.failure++
		st.consecutive++
		st.consecutiveAuth, st.consecutiveBad = 0, 0
		st.lastFailure, st.lastFailureReason = now, string(outcome)
	}
	handler := t.alert
	t.mu.Unlock()

	if hasAlert {
		// Only the binding identity and the reason code are logged: a delivery
		// body or a secret must never reach a log line (design §12).
		slog.Warn("intake source: delivery alert",
			"binding", bindingID, "provider", provider, "reason_code", logReason)
		if handler != nil {
			handler(alert)
		}
	}
}

func (st *deliveryHealthState) resetStreak() {
	st.consecutive = 0
	st.consecutiveAuth = 0
	st.consecutiveBad = 0
	st.alert = ""
	st.alertReason = ""
}

// prune drops hourly buckets older than the 24h window plus one hour of slack.
func (t *DeliveryHealthTracker) prune(st *deliveryHealthState, now time.Time) {
	cutoff := now.Add(-25 * time.Hour).Unix() / 3600
	for hour := range st.buckets {
		if hour < cutoff {
			delete(st.buckets, hour)
		}
	}
}

// Snapshot returns the tracker's current view of a binding.
func (t *DeliveryHealthTracker) Snapshot(bindingID int64) DeliveryHealthSnapshot {
	if t == nil {
		return DeliveryHealthSnapshot{}
	}
	now := t.clock()
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.bindings[bindingID]
	if st == nil {
		return DeliveryHealthSnapshot{}
	}
	t.prune(st, now)
	cutoff := now.Add(-24 * time.Hour).Unix()
	var out DeliveryHealthSnapshot
	out.Recorded = st.deliveries > 0
	for hour, b := range st.buckets {
		if hour*3600 < cutoff {
			continue
		}
		out.Duplicate24h += b.duplicate
	}
	if !st.lastSuccess.IsZero() {
		v := st.lastSuccess
		out.LastSuccessAt = &v
	}
	if !st.lastFailure.IsZero() {
		v := st.lastFailure
		out.LastFailureAt = &v
	}
	out.LastFailureReason = st.lastFailureReason
	out.ConsecutiveFailures = st.consecutive
	out.Alert = st.alert
	out.AlertReason = st.alertReason
	return out
}

// Purge forgets a binding when it is deleted, so a recreated id cannot inherit
// an old streak.
func (t *DeliveryHealthTracker) Purge(bindingID int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.bindings, bindingID)
}
