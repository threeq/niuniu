package service

// Unit tests for the deterministic core of the continuous health sweep
// (Epic #726 / #744): the failure policy, probe-due check, probe-operation
// selection, request construction, and proxy-result classification. The
// DB-backed end-to-end path lives in integration_health_test.go.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func newPolicySvc(threshold int64) *IntegrationHealthService {
	return &IntegrationHealthService{
		cfg: IntegrationHealthConfig{FailureThreshold: threshold}.normalized(),
	}
}

func TestApplyPolicy_DisablesAtThreshold(t *testing.T) {
	s := newPolicySvc(3)
	var autoAt sql.NullTime

	// Failures 1 and 2: unhealthy, still enabled, no auto-disable.
	for i := int64(1); i <= 2; i++ {
		health, streak, enabled, autoAtOut, disabled, recovered := s.applyPolicy(1, i-1, autoAt, ProbeStatusFailed)
		if health != HealthUnhealthy || streak != i || enabled != 1 || disabled || recovered {
			t.Fatalf("failure %d: got health=%s streak=%d enabled=%d disabled=%v", i, health, streak, enabled, disabled)
		}
		if autoAtOut.Valid {
			t.Fatalf("failure %d: auto_disabled_at should be unset before threshold", i)
		}
	}

	// Failure 3 == threshold: auto-disable and stamp auto_disabled_at.
	health, streak, enabled, autoAtOut, disabled, _ := s.applyPolicy(1, 2, sql.NullTime{}, ProbeStatusFailed)
	if !disabled || enabled != 0 || streak != 3 || health != HealthDisabled {
		t.Fatalf("threshold failure: health=%s streak=%d enabled=%d disabled=%v", health, streak, enabled, disabled)
	}
	if !autoAtOut.Valid {
		t.Fatal("threshold failure: auto_disabled_at should be set")
	}
}

func TestApplyPolicy_OKResetsStreak(t *testing.T) {
	s := newPolicySvc(3)
	health, streak, enabled, _, disabled, recovered := s.applyPolicy(1, 2, sql.NullTime{}, ProbeStatusOK)
	if health != HealthHealthy || streak != 0 || enabled != 1 || disabled || recovered {
		t.Fatalf("ok: health=%s streak=%d enabled=%d disabled=%v recovered=%v", health, streak, enabled, disabled, recovered)
	}
}

func TestApplyPolicy_SkippedLeavesStreak(t *testing.T) {
	s := newPolicySvc(3)
	health, streak, enabled, _, disabled, _ := s.applyPolicy(1, 2, sql.NullTime{}, ProbeStatusSkipped)
	if health != HealthUnknown || streak != 2 || enabled != 1 || disabled {
		t.Fatalf("skipped: health=%s streak=%d enabled=%d disabled=%v", health, streak, enabled, disabled)
	}
}

func TestApplyPolicy_AutoRecoversOnlyAutoDisabled(t *testing.T) {
	s := newPolicySvc(3)
	// Auto-disabled (auto_disabled_at set) + a clean probe -> re-enabled.
	health, streak, enabled, autoAt, _, recovered := s.applyPolicy(0, 3, sql.NullTime{Time: time.Now(), Valid: true}, ProbeStatusOK)
	if !recovered || enabled != 1 || health != HealthHealthy || streak != 0 || autoAt.Valid {
		t.Fatalf("auto-recover: health=%s streak=%d enabled=%d recovered=%v autoAt.Valid=%v", health, streak, enabled, recovered, autoAt.Valid)
	}

	// User-disabled (no auto_disabled_at) + a clean probe -> stays off.
	health, _, enabled, _, _, recovered = s.applyPolicy(0, 0, sql.NullTime{}, ProbeStatusOK)
	if recovered || enabled != 0 || health != HealthDisabled {
		t.Fatalf("user-disabled must stay off: health=%s enabled=%d recovered=%v", health, enabled, recovered)
	}
}

func TestProbeDue(t *testing.T) {
	// Never probed -> due.
	if !probeDue(sql.NullTime{}, 0, time.Hour) {
		t.Fatal("never-probed row should be due")
	}
	// Probed 30m ago with a 1h interval -> not due.
	if probeDue(sql.NullTime{Time: time.Now().Add(-30 * time.Minute), Valid: true}, 3600, time.Hour) {
		t.Fatal("row inside its interval should not be due")
	}
	// Probed 2h ago with a 1h interval -> due.
	if !probeDue(sql.NullTime{Time: time.Now().Add(-2 * time.Hour), Valid: true}, 3600, time.Hour) {
		t.Fatal("row past its interval should be due")
	}
	// Per-row interval overrides the fallback.
	if probeDue(sql.NullTime{Time: time.Now().Add(-2 * time.Hour), Valid: true}, 86400, time.Hour) {
		t.Fatal("row with a 24h interval should not be due at 2h")
	}
}

func TestSelectProbeOperation(t *testing.T) {
	c := &ProviderContract{
		VerifyOperation: "search",
		Operations: []ContractOperation{
			{Name: "write_thing", Method: "POST", ReadOnly: false},
			{Name: "search", Method: "GET", ReadOnly: true},
		},
	}
	if op := selectProbeOperation(c); op == nil || op.Name != "search" {
		t.Fatalf("declared verify_operation should win, got %+v", op)
	}

	// No declared verify_operation -> first read-only op.
	c2 := &ProviderContract{Operations: []ContractOperation{
		{Name: "write_thing", Method: "POST"},
		{Name: "list", Method: "GET", ReadOnly: true},
	}}
	if op := selectProbeOperation(c2); op == nil || op.Name != "list" {
		t.Fatalf("fallback should pick the read-only op, got %+v", op)
	}

	// Nothing read-only -> nil.
	c3 := &ProviderContract{Operations: []ContractOperation{{Name: "write_thing", Method: "POST"}}}
	if op := selectProbeOperation(c3); op != nil {
		t.Fatalf("no read-only op should yield nil, got %+v", op)
	}
	if op := selectProbeOperation(nil); op != nil {
		t.Fatal("nil contract should yield nil")
	}
}

func TestProbeRequestFor(t *testing.T) {
	// GET: leftover example keys become query params; {placeholders} are filled.
	op := &ContractOperation{
		Name: "search", Method: "GET", Path: "/search",
		RequestExample: json.RawMessage(`{"engine":"google","q":"best crm"}`),
	}
	method, path, query, body, ok := probeRequestFor(op)
	if !ok || method != "GET" || path != "/search" || body != nil {
		t.Fatalf("get: ok=%v method=%s path=%s body=%v", ok, method, path, body)
	}
	if query["engine"] != "google" || query["q"] != "best crm" {
		t.Fatalf("get query not populated: %v", query)
	}

	// Path placeholder is substituted and removed from the query/body.
	op2 := &ContractOperation{
		Name: "get_site", Method: "GET", Path: "/sites/{siteUrl}",
		RequestExample: json.RawMessage(`{"siteUrl":"https://ex.com"}`),
	}
	_, path2, query2, _, ok2 := probeRequestFor(op2)
	if !ok2 || path2 != "/sites/https://ex.com" {
		t.Fatalf("placeholder not substituted: ok=%v path=%s", ok2, path2)
	}
	if _, present := query2["siteUrl"]; present {
		t.Fatal("substituted placeholder must not remain a query param")
	}

	// An unresolved placeholder is unprobeable.
	op3 := &ContractOperation{Name: "x", Method: "GET", Path: "/sites/{siteUrl}"}
	if _, _, _, _, ok3 := probeRequestFor(op3); ok3 {
		t.Fatal("unresolved path placeholder must not be probeable")
	}

	// POST: example becomes the body.
	op4 := &ContractOperation{
		Name: "query", Method: "POST", Path: "/analytics",
		RequestExample: json.RawMessage(`{"startDate":"2026-01-01"}`),
	}
	_, _, query4, body4, ok4 := probeRequestFor(op4)
	if !ok4 || query4 != nil || body4["startDate"] != "2026-01-01" {
		t.Fatalf("post body not populated: ok=%v query=%v body=%v", ok4, query4, body4)
	}
}

func TestClassifyProbeCall(t *testing.T) {
	if s, _ := classifyProbeCall(&ProxyOutput{Status: 200}, nil); s != ProbeStatusOK {
		t.Fatalf("2xx should be ok, got %s", s)
	}
	if s, _ := classifyProbeCall(&ProxyOutput{Status: http.StatusUnauthorized}, nil); s != ProbeStatusFailed {
		t.Fatalf("401 should be failed, got %s", s)
	}
	if s, _ := classifyProbeCall(&ProxyOutput{Status: 500}, nil); s != ProbeStatusFailed {
		t.Fatalf("500 should be failed, got %s", s)
	}
	// A permission-gate refusal is skipped, not a health failure.
	perr := &ProxyError{Code: "write_disabled", Message: "not in safe set"}
	if s, _ := classifyProbeCall(nil, perr); s != ProbeStatusSkipped {
		t.Fatalf("permission refusal should be skipped, got %s", s)
	}
	// A transport error is a real failure.
	if s, _ := classifyProbeCall(nil, errors.New("dial tcp: refused")); s != ProbeStatusFailed {
		t.Fatalf("transport error should be failed, got %s", s)
	}
}
