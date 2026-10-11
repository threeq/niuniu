// Tests for the pluggable outbound auth strategies (issue #737).
//
// The static tests are a REGRESSION guard: the strategy must compose exactly
// the same header/query the pre-#737 buildAuthHeader/buildAuthQuery path did.
// The dynamic strategies (OAuth2, HMAC, SigV4, mTLS) are covered by
// end-to-end tests that run the real strategy against a test server which
// independently verifies the credential — a token endpoint that enforces
// expiry + refresh, a signature verifier that recomputes the HMAC/SigV4, and a
// TLS server that requires a client certificate. SigV4 additionally pins the
// official AWS SigV4 test-suite known-answer vector.
package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- static strategy: zero-regression guard --------------------------------

// TestStaticStrategy_MatchesLegacyInjection locks the static strategy to the
// historical buildAuthHeader/buildAuthQuery output across every credential
// shape the SPA (and legacy rows) can produce. If this ever diverges, the
// "existing static credential path has zero regression" guarantee is broken.
func TestStaticStrategy_MatchesLegacyInjection(t *testing.T) {
	cases := []struct {
		name string
		prov *ProviderDef
		cfg  map[string]any
	}{
		{"bearer_explicit", &ProviderDef{AuthType: "bearer", AuthHeader: "Authorization", AuthPrefix: "Bearer"},
			map[string]any{"auth_mode": "bearer", "token": "ghp_abc"}},
		{"basic_explicit", &ProviderDef{AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"},
			map[string]any{"auth_mode": "basic", "user": "alice", "password": "s3cret"}},
		{"custom_header", &ProviderDef{AuthType: "custom_header", AuthHeader: "X-Api-Key"},
			map[string]any{"auth_mode": "custom_header", "token": "k-123"}},
		{"query_param", &ProviderDef{AuthType: "query_param", AuthHeader: "api_key"},
			map[string]any{"auth_mode": "query_param", "api_key": "serp-1"}},
		{"legacy_lone_token_under_basic", &ProviderDef{Name: "tapd", AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"},
			map[string]any{"token": "1eadbaa879f225cc73f32762b3501100ea55c747"}},
		{"no_credential_material", &ProviderDef{AuthType: "bearer", AuthHeader: "Authorization", AuthPrefix: "Bearer"},
			map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := url.Parse("https://api.example.com/v1/things")
			req := &AuthRequest{
				Method: "GET", URL: u, Header: http.Header{},
				Provider: tc.prov, Config: tc.cfg, CredKey: "k",
			}
			outcome, err := staticAuthStrategy{}.Apply(context.Background(), req)
			if err != nil {
				t.Fatalf("static Apply: %v", err)
			}

			// Legacy behaviour, computed independently.
			wantHeader := buildAuthHeader(tc.prov, tc.cfg)
			wantParam, wantParamVal := buildAuthQuery(tc.prov, tc.cfg)

			if wantParam != "" {
				if got := req.URL.Query().Get(wantParam); got != wantParamVal {
					t.Fatalf("query param %s: got %q want %q", wantParam, got, wantParamVal)
				}
				if outcome.QueryName != wantParam {
					t.Fatalf("outcome.QueryName: got %q want %q", outcome.QueryName, wantParam)
				}
				return
			}
			if got := req.Header.Get(tc.prov.AuthHeader); got != wantHeader {
				t.Fatalf("header %s: got %q want %q", tc.prov.AuthHeader, got, wantHeader)
			}
			if wantHeader == "" && outcome.Payload != "" {
				t.Fatalf("expected no payload for empty credential, got %q", outcome.Payload)
			}
		})
	}
}

func TestNormalizeStrategyName(t *testing.T) {
	cases := map[string]string{
		"":               strategyStatic,
		"bearer":         strategyStatic,
		"oauth":          strategyStatic, // legacy bearer alias
		"Basic":          strategyStatic,
		"query_param":    strategyStatic,
		"oauth2":         strategyOAuth2,
		"oauth2_refresh": strategyOAuth2,
		"OAuth2-Refresh": strategyOAuth2,
		"hmac_sha256":    strategyHMAC,
		"sigv4":          strategySigV4,
		"AWS_SIGV4":      strategySigV4,
		"mtls":           strategyMTLS,
	}
	for in, want := range cases {
		if got := normalizeStrategyName(in); got != want {
			t.Errorf("normalizeStrategyName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveAuthStrategyName_Priority(t *testing.T) {
	prov := &ProviderDef{Name: "p", AuthType: "bearer"}

	// An explicit credential auth_strategy wins over the contract (so a
	// credential configured for OAuth2 is not overridden by a static contract).
	contract := &ProviderContract{Auth: &ContractAuth{Strategy: "sigv4"}}
	cfg := map[string]any{"auth_strategy": "oauth2_refresh", "auth_mode": "bearer"}
	if got := resolveAuthStrategyName(contract, prov, cfg); got != "oauth2_refresh" {
		t.Fatalf("credential auth_strategy should win over the contract, got %q", got)
	}
	// The contract wins when the credential names no strategy.
	if got := resolveAuthStrategyName(contract, prov, map[string]any{"auth_mode": "bearer"}); got != "sigv4" {
		t.Fatalf("contract strategy should win when the credential is silent, got %q", got)
	}
	// Legacy auth_mode when neither the credential nor the contract declares one.
	if got := resolveAuthStrategyName(&ProviderContract{}, prov, map[string]any{"auth_mode": "basic"}); got != "basic" {
		t.Fatalf("legacy auth_mode expected, got %q", got)
	}
	// Provider auth_type as the final fallback.
	if got := resolveAuthStrategyName(&ProviderContract{}, prov, map[string]any{}); got != "bearer" {
		t.Fatalf("provider auth_type fallback expected, got %q", got)
	}
}

// --- HMAC end-to-end -------------------------------------------------------

// TestHMAC_E2E_VerifierAccepts runs the HMAC strategy against a server that
// recomputes the canonical string from the raw request using an INDEPENDENT
// implementation (written from the documented scheme, not by calling the
// production helper) and rejects anything that does not match. A 200 proves
// the signed request is accepted by the target, i.e. the scheme round-trips.
func TestHMAC_E2E_VerifierAccepts(t *testing.T) {
	const secret = "super-secret-key"
	var sawAuth atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readAll(t, r)
		sig := r.Header.Get("X-Signature")
		ts := r.Header.Get("X-Timestamp")
		if sig == "" || ts == "" {
			http.Error(w, "missing signature headers", http.StatusUnauthorized)
			return
		}
		bodySum := sha256.Sum256(body)
		canonical := strings.Join([]string{
			r.Method,
			r.URL.EscapedPath(),
			r.URL.RawQuery,
			ts,
			hex.EncodeToString(bodySum[:]),
		}, "\n")
		want := hmacHex(t, secret, canonical)
		if sig != want {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		sawAuth.Store(true)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL + "/orders/42?verbose=1")
	body := []byte(`{"qty":3}`)
	req := &AuthRequest{
		Method: "POST", URL: u, Header: http.Header{},
		Body:     body,
		Provider: &ProviderDef{AuthHeader: "Authorization"},
		Config: map[string]any{
			"auth_strategy": "hmac",
			"hmac_secret":   secret,
			"hmac_key_id":   "key-1",
		},
	}
	outcome, err := hmacAuthStrategy{}.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("hmac Apply: %v", err)
	}
	if outcome.Strategy != "hmac" || outcome.HeaderName != "X-Signature" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if req.Header.Get("X-Key-Id") != "key-1" {
		t.Fatalf("key id header missing")
	}

	httpReq, err := http.NewRequest("POST", u.String(), strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	httpReq.Header = req.Header.Clone()
	resp, err := srv.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("send signed request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !sawAuth.Load() {
		t.Fatalf("target rejected the signed request: status=%d sawAuth=%v", resp.StatusCode, sawAuth.Load())
	}
}

// TestHMAC_BodyMode covers the webhook-style body-only signature.
func TestHMAC_BodyMode(t *testing.T) {
	u, _ := url.Parse("https://example.com/hook")
	body := []byte(`{"event":"push"}`)
	req := &AuthRequest{
		Method: "POST", URL: u, Header: http.Header{},
		Body:     body,
		Provider: &ProviderDef{AuthHeader: "Authorization"},
		Config: map[string]any{
			"hmac_secret":           "s3cr3t",
			"hmac_sign":             "body",
			"hmac_signature_prefix": "sha256=",
			"hmac_timestamp":        "1700000000",
		},
	}
	if _, err := (hmacAuthStrategy{}).Apply(context.Background(), req); err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := "sha256=" + hmacHex(t, "s3cr3t", string(body))
	if got := req.Header.Get("X-Signature"); got != want {
		t.Fatalf("body signature: got %q want %q", got, want)
	}
}

func TestHMAC_MissingSecret(t *testing.T) {
	u, _ := url.Parse("https://example.com/")
	_, err := (hmacAuthStrategy{}).Apply(context.Background(), &AuthRequest{
		Method: "GET", URL: u, Header: http.Header{},
		Provider: &ProviderDef{}, Config: map[string]any{"auth_strategy": "hmac"},
	})
	if err == nil || !isAuthStrategyErr(err) {
		t.Fatalf("expected AuthStrategyError, got %v", err)
	}
}

// --- AWS SigV4 -------------------------------------------------------------

// TestSigV4_AWSKnownAnswerVector pins the official AWS SigV4 test-suite
// "get-vanilla" vector. Reproducing AWS's own published signature byte-for-byte
// is the strongest available proof that a real AWS service's verifier accepts
// our requests.
//
// Vector: AKIDEXAMPLE / wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY,
// region us-east-1, service "service", 20150830T123600Z.
func TestSigV4_AWSKnownAnswerVector(t *testing.T) {
	u, _ := url.Parse("https://example.amazonaws.com/")
	req := &AuthRequest{
		Method: "GET", URL: u, Header: http.Header{},
		Provider: &ProviderDef{AuthHeader: "Authorization"},
		Config: map[string]any{
			"auth_strategy": "sigv4",
			"access_key":    "AKIDEXAMPLE",
			"secret_key":    "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
			"region":        "us-east-1",
			"service":       "service",
			"sigv4_time":    "20150830T123600Z",
		},
	}
	outcome, err := sigV4AuthStrategy{}.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("sigv4 apply: %v", err)
	}
	const wantSig = "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if outcome.Payload != wantSig {
		t.Fatalf("signature mismatch:\n got  %s\n want %s", outcome.Payload, wantSig)
	}
	wantAuthz := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, Signature=" + wantSig
	if got := req.Header.Get("Authorization"); got != wantAuthz {
		t.Fatalf("Authorization mismatch:\n got  %s\n want %s", got, wantAuthz)
	}
	if got := req.Header.Get("X-Amz-Date"); got != "20150830T123600Z" {
		t.Fatalf("X-Amz-Date: got %q", got)
	}
}

// TestSigV4_E2E_VerifierAccepts sends a SigV4-signed request to a server that
// independently recomputes the signature from the raw request.
func TestSigV4_E2E_VerifierAccepts(t *testing.T) {
	const (
		accessKey = "AKIDEXAMPLE"
		secretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
		region    = "us-east-1"
		service   = "execute-api"
	)
	// Pin the signing time so the server can recompute deterministically.
	signedAt := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	amzDate := signedAt.Format("20060102T150405Z")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readAll(t, r)
		authz := r.Header.Get("Authorization")
		var gotScope, gotSigned, gotSig string
		if _, err := fmt.Sscanf(authz, "AWS4-HMAC-SHA256 Credential=%s", &gotScope); err != nil {
			http.Error(w, "malformed authorization", http.StatusUnauthorized)
			return
		}
		parts := strings.Split(authz, ", ")
		for _, p := range parts {
			if v, ok := strings.CutPrefix(p, "SignedHeaders="); ok {
				gotSigned = v
			}
			if v, ok := strings.CutPrefix(p, "Signature="); ok {
				gotSig = v
			}
		}
		want := sigV4Verify(t, r, body, secretKey, region, service, amzDate, gotSigned)
		if gotSig != want {
			http.Error(w, "signature mismatch", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL + "/prod/orders?status=open")
	body := []byte(`{"note":"hi"}`)
	req := &AuthRequest{
		Method: "POST", URL: u, Header: http.Header{},
		Body:     body,
		Provider: &ProviderDef{AuthHeader: "Authorization"},
		Config: map[string]any{
			"auth_strategy": "sigv4",
			"access_key":    accessKey,
			"secret_key":    secretKey,
			"region":        region,
			"service":       service,
			"sigv4_time":    amzDate,
		},
	}
	if _, err := (sigV4AuthStrategy{}).Apply(context.Background(), req); err != nil {
		t.Fatalf("sigv4 apply: %v", err)
	}
	httpReq, err := http.NewRequest("POST", u.String(), strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	httpReq.Header = req.Header.Clone()
	resp, err := srv.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("send signed request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("target rejected SigV4 request: status=%d", resp.StatusCode)
	}
}

// sigV4Verify independently recomputes the SigV4 signature for a received
// request, from the spec (not from the production signer).
func sigV4Verify(t *testing.T, r *http.Request, body []byte, secret, region, service, amzDate, signedHeaders string) string {
	t.Helper()
	payloadHash := sha256Hex(body)
	names := strings.Split(signedHeaders, ";")
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		var v string
		switch n {
		case "host":
			v = r.Host
		case "x-amz-date":
			v = r.Header.Get("X-Amz-Date")
		default:
			v = r.Header.Get(n)
		}
		ch.WriteString(n + ":" + strings.TrimSpace(v) + "\n")
	}
	canonicalRequest := strings.Join([]string{
		r.Method,
		awsURIEncode(r.URL.Path, false),
		awsCanonicalQuery(r.URL),
		ch.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	dateStamp := amzDate[:8]
	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	key := sigV4SigningKey(secret, dateStamp, region, service)
	return hex.EncodeToString(hmacSHA256(key, stringToSign))
}

// --- OAuth2 end-to-end -----------------------------------------------------

// TestOAuth2_RefreshToken_E2E exercises the full OAuth2 flow against a token
// endpoint that enforces the refresh_token grant and an API that only accepts
// the currently-valid access token. It also proves auto-refresh: with a
// 1-second TTL (shorter than the refresh skew) the second call must mint a new
// token, and the token must be accepted.
func TestOAuth2_RefreshToken_E2E(t *testing.T) {
	var issues int32
	var currentToken atomic.Value
	currentToken.Store("")

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "rt-1" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		user, pass, ok := r.BasicAuth()
		wantUser, _ := url.QueryUnescape("client-1")
		wantPass, _ := url.QueryUnescape("shh")
		if !ok || user != wantUser || pass != wantPass {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		n := atomic.AddInt32(&issues, 1)
		tok := fmt.Sprintf("at-%d", n)
		currentToken.Store(tok)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": tok, "token_type": "Bearer", "expires_in": 1,
		})
	}))
	defer tokenSrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + currentToken.Load().(string)
		if r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer apiSrv.Close()

	strategy := &oauth2AuthStrategy{cache: newOAuthTokenCache()}
	cfg := map[string]any{
		"auth_strategy": "oauth2_refresh",
		"token_url":     tokenSrv.URL,
		"client_id":     "client-1",
		"client_secret": "shh",
		"refresh_token": "rt-1",
	}
	call := func() int {
		u, _ := url.Parse(apiSrv.URL + "/data")
		req := &AuthRequest{
			Method: "GET", URL: u, Header: http.Header{},
			Provider: &ProviderDef{AuthHeader: "Authorization"},
			Config:   cfg, Client: apiSrv.Client(), CredKey: "cred-1",
		}
		if _, err := strategy.Apply(context.Background(), req); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if got := req.Header.Get("Authorization"); got == "" {
			t.Fatal("no Authorization header injected")
		}
		httpReq, _ := http.NewRequest("GET", u.String(), nil)
		httpReq.Header = req.Header.Clone()
		resp, err := apiSrv.Client().Do(httpReq)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := call(); code != http.StatusOK {
		t.Fatalf("first call status=%d, want 200", code)
	}
	// TTL (1s) is below the refresh skew, so the cached token is stale on the
	// next call and a fresh one must be minted — the target accepts it.
	if code := call(); code != http.StatusOK {
		t.Fatalf("second call status=%d, want 200 (auto-refresh)", code)
	}
	if got := atomic.LoadInt32(&issues); got < 2 {
		t.Fatalf("expected the token to be refreshed at least twice, got %d issues", got)
	}
}

// TestOAuth2_ClientCredentials_E2E covers the client_credentials grant with
// `token_auth: body`.
func TestOAuth2_ClientCredentials_E2E(t *testing.T) {
	var issues int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		if r.Form.Get("client_id") != "svc" || r.Form.Get("client_secret") != "topsecret" {
			http.Error(w, "bad client", http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&issues, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "cc-token", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	strategy := &oauth2AuthStrategy{cache: newOAuthTokenCache()}
	cfg := map[string]any{
		"token_url":     tokenSrv.URL,
		"client_id":     "svc",
		"client_secret": "topsecret",
		"grant_type":    "client_credentials",
		"token_auth":    "body",
	}
	u, _ := url.Parse("https://api.example.com/x")
	req := &AuthRequest{
		Method: "GET", URL: u, Header: http.Header{},
		Provider: &ProviderDef{AuthHeader: "Authorization", AuthPrefix: "Bearer"},
		Config:   cfg, Client: tokenSrv.Client(), CredKey: "cc",
	}
	if _, err := strategy.Apply(context.Background(), req); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer cc-token" {
		t.Fatalf("Authorization: got %q", got)
	}
	// A second apply must reuse the cached token (long TTL).
	if _, err := strategy.Apply(context.Background(), req); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := atomic.LoadInt32(&issues); got != 1 {
		t.Fatalf("expected the token to be cached (1 issue), got %d", got)
	}
}

// TestOAuth2_StaticAccessTokenPassthrough covers the pasted-token shape (no
// token endpoint): the configured access_token is injected as-is.
func TestOAuth2_StaticAccessTokenPassthrough(t *testing.T) {
	u, _ := url.Parse("https://gsc.example.com/")
	req := &AuthRequest{
		Method: "GET", URL: u, Header: http.Header{},
		Provider: &ProviderDef{AuthHeader: "Authorization", AuthPrefix: "Bearer"},
		Config:   map[string]any{"auth_strategy": "oauth2_refresh", "access_token": "ya29.pasted"},
	}
	if _, err := (&oauth2AuthStrategy{cache: newOAuthTokenCache()}).Apply(context.Background(), req); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer ya29.pasted" {
		t.Fatalf("Authorization: got %q", got)
	}
}

func TestOAuth2_MissingConfig(t *testing.T) {
	u, _ := url.Parse("https://api.example.com/")
	req := &AuthRequest{
		Method: "GET", URL: u, Header: http.Header{},
		Provider: &ProviderDef{AuthHeader: "Authorization"},
		Config:   map[string]any{"token_url": "https://auth.example.com/token", "client_id": "c"},
	}
	_, err := (&oauth2AuthStrategy{cache: newOAuthTokenCache()}).Apply(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error when the grant cannot be built")
	}
	if !isAuthStrategyErr(err) {
		t.Fatalf("expected AuthStrategyError, got %T %v", err, err)
	}
}

// --- mTLS end-to-end -------------------------------------------------------

// TestMTLS_E2E_MutualTLSHandshake stands up a TLS server that requires a client
// certificate, then proves (a) the plain client is rejected and (b) the mTLS
// client built from the credential completes the handshake and is accepted.
func TestMTLS_E2E_MutualTLSHandshake(t *testing.T) {
	caPEM, caCert, caKey := newTestCA(t)
	serverCertPEM, serverKeyPEM := newSignedCert(t, caCert, caKey, "server", true)
	clientCertPEM, clientKeyPEM := newSignedCert(t, caCert, caKey, "client", false)

	serverCert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		t.Fatalf("server keypair: %v", err)
	}
	clientCAs := x509.NewCertPool()
	clientCAs.AppendCertsFromPEM(caPEM)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "no client certificate", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
	}
	srv.StartTLS()
	defer srv.Close()

	cfg := map[string]any{
		"auth_strategy":   "mtls",
		"client_cert_pem": string(clientCertPEM),
		"client_key_pem":  string(clientKeyPEM),
	}

	strategy := mtlsAuthStrategy{}
	// (a) No client certificate -> the handshake must fail.
	if resp, err := srv.Client().Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("expected the plain client to be rejected by the mTLS server")
	}

	// (b) The credential's client certificate is accepted.
	mtlsClient, err := strategy.Client(srv.Client(), cfg)
	if err != nil {
		t.Fatalf("build mTLS client: %v", err)
	}
	u, _ := url.Parse(srv.URL + "/secure")
	req := &AuthRequest{
		Method: "GET", URL: u, Header: http.Header{},
		Provider: &ProviderDef{AuthHeader: "Authorization"}, Config: cfg,
	}
	outcome, err := strategy.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !outcome.MTLS {
		t.Fatal("mTLS outcome must flag MTLS")
	}
	resp, err := mtlsClient.Get(u.String())
	if err != nil {
		t.Fatalf("mTLS request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mTLS request status=%d, want 200", resp.StatusCode)
	}
}

func TestMTLS_MissingCertificate(t *testing.T) {
	u, _ := url.Parse("https://api.example.com/")
	_, err := (mtlsAuthStrategy{}).Apply(context.Background(), &AuthRequest{
		Method: "GET", URL: u, Header: http.Header{},
		Provider: &ProviderDef{}, Config: map[string]any{"auth_strategy": "mtls"},
	})
	if err == nil || !isAuthStrategyErr(err) {
		t.Fatalf("expected AuthStrategyError, got %v", err)
	}
}

// --- test helpers ----------------------------------------------------------

func isAuthStrategyErr(err error) bool {
	var se *AuthStrategyError
	return errors.As(err, &se)
}

func hmacHex(t *testing.T, secret, message string) string {
	t.Helper()
	got, err := hmacSign("sha256", secret, message)
	if err != nil {
		t.Fatalf("hmacSign: %v", err)
	}
	return got
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	var buf []byte
	b := make([]byte, 4096)
	for {
		n, err := r.Body.Read(b)
		buf = append(buf, b[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}

// newTestCA mints an in-memory CA for the mTLS test.
func newTestCA(t *testing.T) (caPEM []byte, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "niuniu-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, key
}

// newSignedCert mints a leaf certificate signed by the test CA. Server certs
// get a 127.0.0.1 SAN so httptest's client can verify them.
func newSignedCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, server bool) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if server {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
		tmpl.DNSNames = []string{"localhost"}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
