package relayclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// TestRegisterDesktop_EmailVerificationRequired verifies that the relay's
// §8.2 enforcement (unverified account + ≥1 desktop → 403 email_verification_required)
// surfaces as the typed ErrEmailVerificationRequired sentinel. The UI branches
// on this sentinel to show a friendly "resend verification email" panel, so
// regressing it would silently re-introduce the raw "register: status 403" UX.
func TestRegisterDesktop_EmailVerificationRequired(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktops", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "email_verification_required",
			"code":  "email_verification_required",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli := NewClient(&Config{RelayURL: srv.URL, Email: "e@x"})
	cli.SetAccessToken("t")
	id, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	err = cli.RegisterDesktop(id, "laptop", "darwin", "arm64", "1")
	if !errors.Is(err, ErrEmailVerificationRequired) {
		t.Fatalf("expected ErrEmailVerificationRequired, got %v", err)
	}
}

// TestRegisterDesktop_ForbiddenOther ensures an unrelated 403 body does NOT
// collapse to ErrEmailVerificationRequired — i.e. the tag check is exact, not
// substring-matched.
func TestRegisterDesktop_ForbiddenOther(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktops", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":  "policy_violation",
			"detail": "something mentioning email_verification_required in prose",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli := NewClient(&Config{RelayURL: srv.URL, Email: "e@x"})
	cli.SetAccessToken("t")
	id, _ := pairingcrypto.GenerateIdentity()
	err := cli.RegisterDesktop(id, "laptop", "darwin", "arm64", "1")
	if err == nil {
		t.Fatalf("expected error on 403")
	}
	if errors.Is(err, ErrEmailVerificationRequired) {
		t.Fatalf("unrelated 403 must not collapse to ErrEmailVerificationRequired: %v", err)
	}
}

// TestCreatePairingSession_QuotaExceeded verifies the 402 quota_exceeded
// branch returns the typed ErrQuotaExceeded sentinel so the pair dialog can
// render a friendly "revoke or upgrade" panel.
func TestCreatePairingSession_QuotaExceeded(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/pairing/sessions", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":  "quota_exceeded",
			"detail": "mobile limit 5 reached",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli := NewClient(&Config{RelayURL: srv.URL, Email: "e@x"})
	cli.SetAccessToken("t")
	_, err := cli.CreatePairingSession("d-1")
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}
}

// TestListMobileDevices_UsesNewEndpoint guards against regressing back to
// /api/my/paired-desktops (which is DPoP-protected and yields 401 bad_token
// for account JWTs) and verifies the bare-array response decodes correctly.
func TestListMobileDevices_UsesNewEndpoint(t *testing.T) {
	var hitOldPath bool
	var hitNewPath bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/my/paired-desktops", func(w http.ResponseWriter, _ *http.Request) {
		hitOldPath = true
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/api/mobile-devices", func(w http.ResponseWriter, r *http.Request) {
		hitNewPath = true
		if r.Header.Get("Authorization") != "Bearer t" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"id": "m-1", "name": "Pixel 9", "platform": "android", "created_at": "2026-01-01T00:00:00Z"},
			{"id": "m-2", "name": "iPhone", "platform": "ios", "created_at": "2026-02-01T00:00:00Z"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli := NewClient(&Config{RelayURL: srv.URL, Email: "e@x"})
	cli.SetAccessToken("t")
	out, err := cli.ListMobileDevices()
	if err != nil {
		t.Fatalf("ListMobileDevices: %v", err)
	}
	if hitOldPath {
		t.Fatalf("regression: ListMobileDevices hit the deprecated /api/my/paired-desktops path")
	}
	if !hitNewPath {
		t.Fatalf("ListMobileDevices did not hit /api/mobile-devices")
	}
	if len(out) != 2 ||
		out[0].ID != "m-1" ||
		out[0].Name != "Pixel 9" ||
		out[0].Platform != "android" ||
		out[0].CreatedAt != "2026-01-01T00:00:00Z" ||
		out[1].Platform != "ios" ||
		out[1].CreatedAt != "2026-02-01T00:00:00Z" {
		t.Fatalf("decoded array mismatch: %+v", out)
	}
}

// TestClient_AccessTokenReuse proves that SetAccessToken lets a caller skip
// the rate-limited /api/accounts/refresh endpoint when it already holds a
// valid token. This is the primitive the service layer's userState cache
// relies on to keep a BillingPanel open from fanning out into 3-4 parallel
// refreshes (which combined with the relay's per-account refresh rate-limit
// produced the historical "status 429" regression).
func TestClient_AccessTokenReuse(t *testing.T) {
	var refreshHits int32
	var myUsageAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/refresh", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&refreshHits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "cached-token-123",
			"refresh_token": "rt-2",
		})
	})
	mux.HandleFunc("/api/billing/my-usage", func(w http.ResponseWriter, r *http.Request) {
		myUsageAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"plan_id": "free"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// First client refreshes and captures its access token.
	first := NewClient(&Config{RelayURL: srv.URL, Email: "e@x"})
	if _, err := first.RefreshAccessToken("rt-1"); err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	tok := first.AccessToken()
	if tok != "cached-token-123" {
		t.Fatalf("expected AccessToken to surface the refreshed token, got %q", tok)
	}

	// A second client receives the token directly — no refresh must be issued.
	second := NewClient(&Config{RelayURL: srv.URL, Email: "e@x"})
	second.SetAccessToken(tok)
	if _, err := second.MyUsage(); err != nil {
		t.Fatalf("MyUsage with injected token: %v", err)
	}
	if myUsageAuth != "Bearer cached-token-123" {
		t.Fatalf("MyUsage did not carry the injected bearer, got %q", myUsageAuth)
	}

	if got := atomic.LoadInt32(&refreshHits); got != 1 {
		t.Fatalf("expected exactly 1 /api/accounts/refresh hit, got %d — token reuse regressed", got)
	}
}

// TestClient_SetAccessTokenEmpty verifies clearing the token disarms
// authenticated calls — the "not logged in" guard on MyUsage must fire.
func TestClient_SetAccessTokenEmpty(t *testing.T) {
	cli := NewClient(&Config{RelayURL: "http://unused"})
	cli.SetAccessToken("tok")
	cli.SetAccessToken("")
	if _, err := cli.MyUsage(); err == nil {
		t.Fatalf("expected error after clearing access token")
	}
}
