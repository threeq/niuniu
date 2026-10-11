// handler_cred_proxy.go — loopback credential proxy for scene-projected MCP
// servers (issue #733).
//
// Route (registered in router.go, OUTSIDE the authenticated groups):
//
//	ANY /cred-proxy/:token/*
//
// The token IS the authorization — the same "the token is the auth" model the
// credential-onboarding links use. It is a sealed pointer naming a workspace +
// MCP server, not a copy of the credential: a workspace .mcp.json only ever
// carries this, and the proxy injects the real header on egress. A bad token is
// 401; a good token whose target no longer resolves is 403.
package server

import (
	"errors"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// credProxyForward serves /cred-proxy/:token/*rest. A bad token and an
// unavailable target are distinguished so a stale .mcp.json fails loudly (401)
// rather than looking like an upstream outage.
func (s *Server) credProxyForward(c *gin.Context) {
	if s.credProxySvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "UNAVAILABLE", "message": "credential proxy is not available"}})
		return
	}
	// The proxy exists for a same-host MCP client. Refusing non-loopback peers
	// keeps a token leaked out of a workspace from being replayed from the
	// network — the token is unforgeable, but not un-stealable.
	if !isLoopbackRemote(c.Request.RemoteAddr) {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "LOOPBACK_ONLY", "message": "the credential proxy is reachable from loopback only"}})
		return
	}
	target, err := s.credProxySvc.Resolve(c.Request.Context(), c.Param("token"))
	if err != nil {
		if errors.Is(err, service.ErrCredProxyTokenInvalid) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "INVALID_TOKEN", "message": err.Error()}})
			return
		}
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "TARGET_UNAVAILABLE", "message": err.Error()}})
		return
	}
	// rest is the sub-path after the token ("/..." or ""); the proxy appends it
	// to the resolved upstream path.
	s.credProxySvc.ServeHTTP(c.Writer, c.Request, target, c.Param("rest"))
}

// isLoopbackRemote reports whether an http.RemoteAddr names a loopback peer.
// The port is optional; a bare address is accepted.
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
