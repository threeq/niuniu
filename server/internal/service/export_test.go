package service

import "context"

// ResolveCredentialForTest exposes the unexported resolveCredential to the
// external (service_test) package so the credential-resolution fork can be
// exercised against the shared niutest IsolationEnv fixtures without an import
// cycle (internal/testing imports service).
func (s *ExternalProxyService) ResolveCredentialForTest(ctx context.Context, in ProxyInput) (*ExternalCredentialDecoded, error) {
	return s.resolveCredential(ctx, in)
}
