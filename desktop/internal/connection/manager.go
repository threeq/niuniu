package connection

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type HealthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	UptimeSeconds int    `json:"uptime_seconds"`
}

type Manager struct {
	client *http.Client
}

func NewManager() *Manager {
	return &Manager{client: &http.Client{Timeout: 5 * time.Second}}
}

func (m *Manager) CheckHealth(baseURL string) (*HealthResponse, error) {
	resp, err := m.client.Get(baseURL + "/api/health")
	if err != nil {
		return nil, fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health check returned %d", resp.StatusCode)
	}
	var health HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, fmt.Errorf("decode health response: %w", err)
	}
	return &health, nil
}

// BuildURL returns the canonical base URL (scheme://host[:port], no trailing
// slash) used for the health check, webview, and SSE endpoints. See
// NormalizeBaseURL for the accepted host forms.
func BuildURL(host string, port int) string {
	return NormalizeBaseURL(host, port)
}

// NormalizeBaseURL canonicalizes a user-entered address into a base URL.
//
// The host may be:
//   - a bare host / domain / IP            ("192.168.1.5", "niuniu.example.com")
//   - a host:port                          ("192.168.1.5:3000")
//   - a full URL, optionally with a path   ("https://niuniu.example.com", "http://x:8080/")
//
// port (when > 0) supplies the port only if the host does not already carry one,
// so a domain can be connected to without specifying a port. The scheme is taken
// from the host when present, otherwise defaults to http — except it defaults to
// https when the effective port is 443. A port equal to the scheme default
// (443 for https, 80 for http) is omitted from the result.
func NormalizeBaseURL(host string, port int) string {
	h := strings.TrimSpace(host)
	scheme := ""
	if i := strings.Index(h, "://"); i >= 0 {
		scheme = strings.ToLower(h[:i])
		h = h[i+3:]
	}
	// Drop any path/query/fragment — we only address the origin.
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}

	// Split a trailing :<digits> port off the host (ignores IPv6 bracket forms).
	hostPart, portPart := h, ""
	if idx := strings.LastIndex(h, ":"); idx >= 0 && !strings.Contains(h, "]") {
		if cand := h[idx+1:]; cand != "" && isAllDigits(cand) {
			hostPart, portPart = h[:idx], cand
		}
	}

	effPort := portPart
	if effPort == "" && port > 0 {
		effPort = strconv.Itoa(port)
	}

	if scheme == "" {
		if effPort == "443" {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}

	// Omit the port when it is the scheme's default.
	if (scheme == "https" && effPort == "443") || (scheme == "http" && effPort == "80") {
		effPort = ""
	}

	if effPort != "" {
		return scheme + "://" + hostPart + ":" + effPort
	}
	return scheme + "://" + hostPart
}

// KeyFor returns a stable identifier for a connection (host[:port], scheme
// stripped) used as the connWindows map key, window name, and notification id.
// Two addresses that normalize to the same origin share a key.
func KeyFor(host string, port int) string {
	u := NormalizeBaseURL(host, port)
	if i := strings.Index(u, "://"); i >= 0 {
		return u[i+3:]
	}
	return u
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
