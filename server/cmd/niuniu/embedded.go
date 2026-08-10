package main

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/config"
)

// applyEmbeddedOverrides mutates cfg in place based on CLI flags. Called
// after config.Load(), before anything that reads cfg (logging, db, server).
// When flags.Embedded is false this is a no-op except for applying --addr
// if provided (to allow addr overrides without forcing embedded mode).
func applyEmbeddedOverrides(cfg *config.Config, flags Flags) {
	if flags.Embedded {
		cfg.Auth.Enabled = false
		cfg.Server.Host = "127.0.0.1"

		// CRITICAL: force logging to file. If a user's config.yaml has
		// log.output = "terminal" or "both", slog writes to stdout before
		// emitReady runs — the parent reads a log line as ready JSON and
		// the handshake fails silently with a bad-JSON error. This is
		// covered by TestApplyEmbeddedOverrides.
		cfg.Log.Output = "file"
		if cfg.Log.FileDir == "" {
			cfg.Log.FileDir = filepath.Join(cfg.DataDir, "logs")
		}
	}
	if flags.Addr != "" {
		h, p, hostSet, portSet, ok := parseAddrOverride(flags.Addr)
		if !ok {
			slog.Warn("invalid --addr, ignoring", "addr", flags.Addr)
		} else {
			if hostSet {
				cfg.Server.Host = h
			}
			if portSet {
				cfg.Server.Port = p
			}
		}
	}
}

// preferredPortPath returns the file that persists the last successfully
// bound embedded listen port (plain decimal port number). The desktop shell
// launches the server with --addr=127.0.0.1:0; without this file every launch
// gets a fresh OS-assigned port and any user-side config keyed on the URL
// (browser storage, tool configs) is lost.
func preferredPortPath(dataDir string) string {
	return filepath.Join(dataDir, "embedded.port")
}

// readPreferredPort returns the persisted port, or 0 if the file is missing
// or does not hold a valid port number.
func readPreferredPort(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

// savePreferredPort persists the port of addr ("host:port") for the next
// embedded launch. Best-effort: failure only costs port stability.
func savePreferredPort(path, addr string) {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(p), 0o600); err != nil {
		slog.Warn("failed to persist preferred embedded port", "path", path, "error", err)
	}
}

// listenEmbedded binds the embedded server's listener. When an ephemeral port
// is requested (port 0), it first tries the port persisted in portFile so
// restarts keep a stable URL; if that port is now occupied it falls back to
// an OS-assigned one.
func listenEmbedded(listen func(string) (net.Listener, error), host string, port int, portFile string) (net.Listener, error) {
	if port == 0 {
		if p := readPreferredPort(portFile); p != 0 {
			ln, err := listen(net.JoinHostPort(host, strconv.Itoa(p)))
			if err == nil {
				return ln, nil
			}
			slog.Info("preferred embedded port unavailable, falling back to ephemeral",
				"port", p, "error", err)
		}
	}
	return listen(net.JoinHostPort(host, strconv.Itoa(port)))
}

// parseAddrOverride accepts three forms:
//
//	"host:port" -> hostSet=true, portSet=true
//	":port"     -> portSet=true only
//	"host"      -> hostSet=true only
//
// Returns ok=false on syntactically invalid port (not an int in [0,65535]).
func parseAddrOverride(s string) (host string, port int, hostSet, portSet, ok bool) {
	if !strings.Contains(s, ":") {
		return s, 0, true, false, true
	}
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, false, false, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 0 || n > 65535 {
		return "", 0, false, false, false
	}
	if h == "" {
		return "", n, false, true, true
	}
	return h, n, true, true, true
}
