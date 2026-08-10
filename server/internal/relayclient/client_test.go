package relayclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/go-shared/pairingcrypto"
)

// TestClientVerifyEmailCodeAndRegisterDesktop covers the canonical first-login
// flow: VerifyEmailCode populates the access token in the client, and the
// next authenticated call (RegisterDesktop) carries that token in the
// Authorization header.
func TestClientVerifyEmailCodeAndRegisterDesktop(t *testing.T) {
	var lastAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/email-code/verify", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Email, Code string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Email != "e@x" || body.Code != "123456" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_code"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "t",
			"refresh_token": "rt",
			"email":         "e@x",
			"is_new_user":   true,
		})
	})
	mux.HandleFunc("/api/desktops", func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"desktop_id": "d-1", "desktop_token": "dt-1"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli := NewClient(&Config{RelayURL: srv.URL, Email: "e@x"})
	res, err := cli.VerifyEmailCode("e@x", "123456")
	if err != nil {
		t.Fatalf("VerifyEmailCode: %v", err)
	}
	if res.RefreshToken != "rt" || !res.IsNewUser {
		t.Fatalf("unexpected verify result: %+v", res)
	}
	testID, err := pairingcrypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := cli.RegisterDesktop(testID, "laptop", "darwin", "arm64", "1"); err != nil {
		t.Fatalf("RegisterDesktop: %v", err)
	}
	if cli.Cfg().DesktopID != "d-1" || cli.Cfg().DesktopToken != "dt-1" {
		t.Fatalf("cfg not populated: %+v", cli.Cfg())
	}
	if !strings.HasPrefix(lastAuth, "Bearer t") {
		t.Fatalf("expected Authorization header to carry access token, got %q", lastAuth)
	}
}

// TestClientVerifyEmailCodeErrors covers the four typed error shapes the
// relay emits and confirms each maps to its matching sentinel.
func TestClientVerifyEmailCodeErrors(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		statusCode int
		want       error
	}{
		{"invalid_code", `{"error":"invalid_code"}`, http.StatusBadRequest, ErrInvalidLoginCode},
		{"expired_code", `{"error":"expired_code"}`, http.StatusBadRequest, ErrExpiredLoginCode},
		{"too_many_attempts", `{"error":"too_many_attempts"}`, http.StatusBadRequest, ErrTooManyAttempts},
		{"invalid_email", `{"error":"invalid_email"}`, http.StatusBadRequest, ErrInvalidEmail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			cli := NewClient(&Config{RelayURL: srv.URL})
			_, err := cli.VerifyEmailCode("e@x", "123456")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// TestClientRefreshAccessToken validates the rotate-on-refresh contract:
// the client returns the new pair AND updates its in-memory access token
// so subsequent authenticated calls use the fresh credential.
func TestClientRefreshAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ RefreshToken string `json:"refresh_token"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.RefreshToken != "old-rt" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-at",
			"refresh_token": "new-rt",
			"expires_in":    900,
		})
	}))
	defer srv.Close()
	cli := NewClient(&Config{RelayURL: srv.URL})
	res, err := cli.RefreshAccessToken("old-rt")
	if err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	if res.AccessToken != "new-at" || res.RefreshToken != "new-rt" {
		t.Fatalf("unexpected refresh result: %+v", res)
	}
	if cli.AccessToken() != "new-at" {
		t.Fatalf("client.access not updated: %q", cli.AccessToken())
	}
}

// TestClientRefreshInvalid maps a 401 from the relay to ErrInvalidRefresh
// so callers can detect "log the user out" terminal failures.
func TestClientRefreshInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_refresh"})
	}))
	defer srv.Close()
	cli := NewClient(&Config{RelayURL: srv.URL})
	_, err := cli.RefreshAccessToken("rt")
	if !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("expected ErrInvalidRefresh, got %v", err)
	}
}
