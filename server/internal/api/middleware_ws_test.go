package api

import (
	"net/http"
	"testing"
)

func TestCheckWSOrigin(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"no origin (cli/native)", "self.example.com", "", true},
		{"same origin", "self.example.com", "https://self.example.com", true},
		{"same origin with port", "localhost:3000", "http://localhost:3000", true},
		{"loopback origin", "self.example.com", "http://127.0.0.1:5173", true},
		{"localhost origin", "self.example.com", "http://localhost:5173", true},
		{"cross-site attacker", "self.example.com", "https://evil.com", false},
		{"cross-site attacker subdomain trick", "self.example.com", "https://self.example.com.evil.com", false},
		{"malformed origin", "self.example.com", "://not a url", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{Host: tc.host, Header: http.Header{}}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := checkWSOrigin(r); got != tc.want {
				t.Fatalf("checkWSOrigin(host=%q, origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}
