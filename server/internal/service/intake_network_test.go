package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func inboundRequest(remote string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/integrations/webhook/1", nil)
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestIntakeNetwork_ClientAllowed(t *testing.T) {
	localOnly := Config{LocalOnly: true}
	open := Config{}

	cases := []struct {
		name   string
		cfg    Config
		remote string
		xff    string
		want   bool
	}{
		{"loopback v4", localOnly, "127.0.0.1:1234", "", true},
		{"loopback v6", localOnly, "[::1]:1234", "", true},
		{"private 10/8", localOnly, "10.1.2.3:5000", "", true},
		{"private 172.16/12", localOnly, "172.16.5.5:80", "", true},
		{"private 192.168/16", localOnly, "192.168.1.9:443", "", true},
		{"public v4", localOnly, "203.0.113.5:1234", "", false},
		{"public v6", localOnly, "2001:db8::1", "", false},
		{"public when not local_only", open, "203.0.113.5:1234", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := inboundRequest(tc.remote, nil)
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := ClientAllowed(tc.cfg, r); got != tc.want {
				t.Fatalf("ClientAllowed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIntakeNetwork_ForgedXFFIgnoredWithoutTrustedProxy(t *testing.T) {
	// A public peer claiming to be loopback must not gain access: no trusted
	// proxy is configured, so X-Forwarded-For is never consulted.
	r := inboundRequest("203.0.113.5:1234", map[string]string{
		"X-Forwarded-For": "127.0.0.1",
		"X-Real-IP":       "127.0.0.1",
	})
	if ClientAllowed(Config{LocalOnly: true}, r) {
		t.Fatal("forged X-Forwarded-For bypassed local_only")
	}
}

func TestIntakeNetwork_TrustedProxyHonorsRightmostUntrustedHop(t *testing.T) {
	cfg := Config{
		LocalOnly:      true,
		TrustedProxies: []string{"127.0.0.1/32", "10.0.0.0/8"},
	}

	// The direct peer is our proxy; the appended real client is private.
	r := inboundRequest("127.0.0.1:8080", map[string]string{
		"X-Forwarded-For": "192.168.1.7",
	})
	if !ClientAllowed(cfg, r) {
		t.Fatal("trusted proxy delivery from private client was rejected")
	}

	// A client-supplied forged entry on the left is ignored; the rightmost
	// untrusted hop (the address the proxy appended) is public.
	r = inboundRequest("127.0.0.1:8080", map[string]string{
		"X-Forwarded-For": "127.0.0.1, 203.0.113.10",
	})
	if ClientAllowed(cfg, r) {
		t.Fatal("forged left-hand X-Forwarded-For entry bypassed local_only")
	}

	// Without a trusted-proxy match, even a private-looking XFF is ignored.
	r = inboundRequest("203.0.113.5:1234", map[string]string{
		"X-Forwarded-For": "192.168.1.7",
	})
	if ClientAllowed(cfg, r) {
		t.Fatal("untrusted peer's X-Forwarded-For was honored")
	}
}

func TestIntakeNetwork_IPAllowList(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		remote string
		want   bool
	}{
		{"cidr hit", Config{IPAllowList: []string{"10.0.0.0/8"}}, "10.2.3.4:1234", true},
		{"cidr miss", Config{IPAllowList: []string{"10.0.0.0/8"}}, "11.2.3.4:1234", false},
		{"plain ip hit", Config{IPAllowList: []string{"127.0.0.1"}}, "127.0.0.1:1234", true},
		{"plain ip miss", Config{IPAllowList: []string{"127.0.0.1"}}, "127.0.0.2:1234", false},
		{"empty list unrestricted", Config{}, "203.0.113.9:1234", true},
		{"local_only and allow list both must pass", Config{LocalOnly: true, IPAllowList: []string{"203.0.113.0/24"}}, "203.0.113.9:1234", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := inboundRequest(tc.remote, nil)
			if got := ClientAllowed(tc.cfg, r); got != tc.want {
				t.Fatalf("ClientAllowed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIntakeNetwork_RejectedDeliveryRecordsNothing(t *testing.T) {
	f := newIMBotFixture(t)
	svc, creds := newWebhookTestService(t, f)

	const secret = "network-secret"
	bindingID := bindWebhookSourceWithConfig(t, f, creds, f.projectID, intake.SourceKindWebhook,
		"net-hooks", secret, `{"local_only":true}`)

	// A valid secret and a forged loopback XFF must still be rejected.
	r := inboundRequest("203.0.113.7:5555", map[string]string{
		"Authorization":   "Bearer " + secret,
		"X-Forwarded-For": "127.0.0.1",
	})
	body := []byte(`{"id":"net-evt-1","actor":"alice","text":"deploy"}`)
	_, err := svc.HandleInbound(context.Background(), bindingID, intake.SourceKindWebhook, r, body)
	if !errors.Is(err, ErrIntakeWebhookForbidden) {
		t.Fatalf("HandleInbound error = %v, want ErrIntakeWebhookForbidden", err)
	}
	if n := countIntakeEvents(t, f.db, f.projectID); n != 0 {
		t.Fatalf("rejected delivery recorded %d events, want 0", n)
	}
}

func TestIntakeNetwork_ConfigValidation(t *testing.T) {
	if err := validateNetworkConfig(map[string]any{
		"ip_allow_list":   []any{"10.0.0.0/8"},
		"trusted_proxies": []any{"127.0.0.1/32"},
		"local_only":      true,
	}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := validateNetworkConfig(map[string]any{
		"ip_allow_list": []any{"not-a-cidr"},
	}); !errors.Is(err, ErrIntakeSourceInvalid) {
		t.Fatalf("invalid CIDR error = %v, want ErrIntakeSourceInvalid", err)
	}
	if err := validateNetworkConfig(map[string]any{
		"trusted_proxies": "127.0.0.1/32",
	}); !errors.Is(err, ErrIntakeSourceInvalid) {
		t.Fatalf("non-array error = %v, want ErrIntakeSourceInvalid", err)
	}
}

func bindWebhookSourceWithConfig(
	t *testing.T, f *imbotFixture, creds *ExternalCredentialService,
	projectID int64, provider, sourceKey, secret, cfg string,
) int64 {
	t.Helper()
	cred, err := creds.Create(context.Background(), ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider:  integration.ProviderName(provider),
		Alias:     "test-net-" + provider + "-" + sourceKey,
		RawConfig: map[string]any{"webhook_secret": secret},
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	row, err := f.q.AddProjectExternalSource(context.Background(), store.AddProjectExternalSourceParams{
		ProjectID: projectID, Provider: provider, SourceKey: sourceKey,
		CredentialID: sql.NullInt64{Int64: cred.ID, Valid: true}, Config: cfg,
	})
	if err != nil {
		t.Fatalf("bind source: %v", err)
	}
	return row.ID
}

func countIntakeEvents(t *testing.T, db *sql.DB, projectID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM intake_events WHERE project_id = ?`, projectID).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}
