package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// checkWSOrigin is the CheckOrigin hook shared by every gorilla WebSocket
// upgrader. gorilla defaults to accepting all origins, and browsers do NOT run
// a CORS preflight for WebSocket handshakes, so a "return true" hook lets any
// website open a cross-site WS to the server (CSWSH) — which, for the PTY
// terminal endpoints, hands the attacker a shell. This rejects cross-origin
// browser handshakes while still allowing:
//   - non-browser clients (CLI, native desktop) that send no Origin header;
//   - same-origin browser connections (Origin host:port == request Host);
//   - loopback origins (localhost / 127.0.0.1 / ::1) — dev proxy, personal
//     edition, desktop WebView pointed at the local server. A remote attacker's
//     page is never served from loopback, so this can't be abused to reach a
//     victim's local instance.
func checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client (no Origin); auth middleware still gates it
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// Same-origin: the Origin's host[:port] matches the Host we were reached on.
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	// Loopback origins are trusted (local dev / personal edition / desktop).
	if host := u.Hostname(); host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	return false
}

// LocalhostOnly rejects non-loopback requests. Used by /mcp/* routes that
// bypass auth — they MUST only be reachable from the niuniu-mcp binary on
// the same machine.
func LocalhostOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
		if err != nil {
			host = c.Request.RemoteAddr
		}
		host = strings.ToLower(host)
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "mcp endpoints are localhost-only"})
			return
		}
		c.Next()
	}
}
