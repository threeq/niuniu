package integration

import (
	"errors"
	"fmt"
)

// ErrProviderNotRegistered is the sentinel returned (wrapped) by
// Registry.Get when no adapter is installed for the requested name.
// Callers use errors.Is(err, integration.ErrProviderNotRegistered) to
// short-circuit pre-flight checks under the proxy architecture, where
// real validation happens at first use rather than at credential save.
var ErrProviderNotRegistered = errors.New("provider not registered")

// Registry maps ProviderName to Provider implementation. Built once at
// server boot (see internal/server/server.go) and read concurrently
// thereafter, so Register must finish before any Get fires. Not
// goroutine-safe for concurrent Register/Get.
type Registry struct {
	providers map[ProviderName]Provider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[ProviderName]Provider{}}
}

// Register installs p under p.Name(). Last writer wins.
func (r *Registry) Register(p Provider) {
	r.providers[p.Name()] = p
}

// Get returns the provider for name, or an error wrapping
// ErrProviderNotRegistered if none is installed.
func (r *Registry) Get(name ProviderName) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("no provider registered: %s: %w", name, ErrProviderNotRegistered)
	}
	return p, nil
}
