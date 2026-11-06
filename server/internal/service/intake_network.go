package service

// Inbound network boundary for Intake bindings (design §4, §8): a personal
// edition server may be reachable over a LAN or a user-provided tunnel, so a
// binding can restrict which client addresses may deliver at all. The check is
// deliberately independent of the credential: it runs before the secret is
// looked up, and a rejected delivery records nothing.
//
// X-Forwarded-For is only honored when the direct peer is one of the binding's
// explicit trusted proxies. Gin's trusted-proxy setting does not apply here: the
// inbound routes are outside the authenticated group and this decision must be
// reproducible in service-level tests without an engine.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Config is the per-binding inbound network policy. Empty IPAllowList and
// TrustedProxies mean "no extra restriction" and "trust no proxy" respectively.
type Config struct {
	LocalOnly      bool
	IPAllowList    []string
	TrustedProxies []string
}

// ClientAllowed reports whether the delivery's effective client IP satisfies
// the binding's network policy. local_only admits loopback and private ranges;
// ip_allow_list, when non-empty, must also match.
func ClientAllowed(cfg Config, r *http.Request) bool {
	if r == nil {
		return false
	}
	ip := effectiveClientIP(cfg, r)
	if ip == nil {
		return false
	}
	if cfg.LocalOnly && !ip.IsLoopback() && !ip.IsPrivate() {
		return false
	}
	if len(cfg.IPAllowList) > 0 && !ipMatchesAny(ip, cfg.IPAllowList) {
		return false
	}
	return true
}

// effectiveClientIP resolves the peer address. X-Forwarded-For is consulted
// only when the direct peer is a configured trusted proxy, and then only the
// rightmost address that is not itself a trusted proxy is used — a client's
// forged left-hand entries are therefore ignored.
func effectiveClientIP(cfg Config, r *http.Request) net.IP {
	remote := parseAddr(r.RemoteAddr)
	if remote == nil {
		return nil
	}
	if len(cfg.TrustedProxies) == 0 || !ipMatchesAny(remote, cfg.TrustedProxies) {
		return remote
	}
	var hops []net.IP
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(header, ",") {
			if ip := parseAddr(part); ip != nil {
				hops = append(hops, ip)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if !ipMatchesAny(hops[i], cfg.TrustedProxies) {
			return hops[i]
		}
	}
	return remote
}

// parseAddr accepts "1.2.3.4", "1.2.3.4:443", "[::1]:443" and "[::1]"
// (optionally with a zone).
func parseAddr(raw string) net.IP {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	if i := strings.LastIndex(raw, "%"); i >= 0 {
		raw = raw[:i]
	}
	return net.ParseIP(raw)
}

// ipMatchesAny reports whether ip falls inside any CIDR (or equals any plain
// IP) in entries. Invalid entries never match.
func ipMatchesAny(ip net.IP, entries []string) bool {
	for _, entry := range entries {
		if prefix, ok := parsePrefix(entry); ok && prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func parsePrefix(entry string) (*net.IPNet, bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil, false
	}
	if strings.Contains(entry, "/") {
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, false
		}
		return network, true
	}
	ip := parseAddr(entry)
	if ip == nil {
		return nil, false
	}
	bits := 128
	if ip.To4() != nil {
		ip = ip.To4()
		bits = 32
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, true
}

// networkConfigFromBinding reads the network keys out of a binding config.
// Malformed config degrades to the most restrictive useful default: the
// credential checks still run, but no proxy is trusted.
func networkConfigFromBinding(rawConfig string) Config {
	var cfg struct {
		LocalOnly      bool     `json:"local_only"`
		IPAllowList    []string `json:"ip_allow_list"`
		TrustedProxies []string `json:"trusted_proxies"`
	}
	if strings.TrimSpace(rawConfig) != "" {
		_ = json.Unmarshal([]byte(rawConfig), &cfg)
	}
	return Config{
		LocalOnly:      cfg.LocalOnly,
		IPAllowList:    cfg.IPAllowList,
		TrustedProxies: cfg.TrustedProxies,
	}
}

// validateNetworkConfig rejects a binding whose allow list or proxy list names
// an address that can never match. Failing at create time is clearer than
// silently dropping deliveries later.
func validateNetworkConfig(cfg map[string]any) error {
	for _, key := range []string{"ip_allow_list", "trusted_proxies"} {
		raw, ok := cfg[key]
		if !ok || raw == nil {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			if ss, isStrings := raw.([]string); isStrings {
				for _, s := range ss {
					if _, valid := parsePrefix(s); !valid {
						return fmt.Errorf("%w: %s contains invalid address %q", ErrIntakeSourceInvalid, key, s)
					}
				}
				continue
			}
			return fmt.Errorf("%w: %s must be an array of CIDRs", ErrIntakeSourceInvalid, key)
		}
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("%w: %s must be an array of CIDRs", ErrIntakeSourceInvalid, key)
			}
			if _, valid := parsePrefix(s); !valid {
				return fmt.Errorf("%w: %s contains invalid address %q", ErrIntakeSourceInvalid, key, s)
			}
		}
	}
	return nil
}
