package server

import "testing"

func TestRedactQueryToken(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no query", "/ws/notify", "/ws/notify"},
		{"no token param", "/api/workspaces?limit=10", "/api/workspaces?limit=10"},
		{"token scrubbed", "/ws/sse?token=eyJhbGciOi.secret.sig", "/ws/sse?token=REDACTED"},
		{"ws_token scrubbed", "/ws/x?ws_token=abc123", "/ws/x?ws_token=REDACTED"},
		{"token among others", "/ws/sse?workspace=1&token=secret", "/ws/sse?token=REDACTED&workspace=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactQueryToken(tc.in); got != tc.want {
				t.Fatalf("redactQueryToken(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
