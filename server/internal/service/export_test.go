package service

import (
	"context"
	"net/http"
)

// ResolveCredentialForTest exposes the unexported resolveCredential to the
// external (service_test) package so the credential-resolution fork can be
// exercised against the shared niutest IsolationEnv fixtures without an import
// cycle (internal/testing imports service).
func (s *ExternalProxyService) ResolveCredentialForTest(ctx context.Context, in ProxyInput) (*ExternalCredentialDecoded, error) {
	return s.resolveCredential(ctx, in)
}

// SetClientForTest swaps the proxy's HTTP client. The production client is
// SSRF-guarded and therefore refuses loopback — which is exactly where an
// httptest upstream listens. Tests that drive Call() end-to-end inject a plain
// client here rather than weakening the guard itself.
func (s *ExternalProxyService) SetClientForTest(c *http.Client) {
	s.client = c
}

