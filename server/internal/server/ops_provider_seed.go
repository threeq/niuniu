package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// opsProviderWrites grants an Ops Bot owner write access on a pack's
// integration provider. Publishing is a write, so creating a publishing bot
// implies consenting to writes for that owner.
type opsProviderWrites struct{ s *Server }

func (w opsProviderWrites) EnableProviderWrites(ctx context.Context, ownerType string, ownerID int64, provider string) error {
	if w.s.externalProviderSvc == nil {
		return nil
	}
	prov, err := w.s.externalProviderSvc.GetByName(ctx, provider)
	if err != nil {
		return err
	}
	return w.s.externalProviderSvc.SetWritePref(ctx, ownerType, ownerID, prov.ID, true)
}

// contractAuth reads the declared auth shape so each provider is seeded with
// the strategy it actually needs (WeChat token exchange vs Facebook bearer).
func contractAuth(raw json.RawMessage) (authType, header, prefix string) {
	var shape struct {
		Auth struct {
			Strategy string `json:"strategy"`
			Header   string `json:"header"`
			Prefix   string `json:"prefix"`
		} `json:"auth"`
	}
	_ = json.Unmarshal(raw, &shape)
	authType = strings.TrimSpace(shape.Auth.Strategy)
	if authType == "" {
		authType = "bearer"
	}
	return authType, strings.TrimSpace(shape.Auth.Header), strings.TrimSpace(shape.Auth.Prefix)
}

// seedOpsProviders registers the provider contracts shipped inside builtin
// packs (currently WeChat). A fresh install therefore has a working provider
// definition with no manual MCP/gateway setup; the account credential is still
// entered through the one-time link.
func (s *Server) seedOpsProviders(ctx context.Context) {
	seeds, err := packs.BuiltinProviderSeeds()
	if err != nil {
		slog.Warn("ops provider seeds: read", "err", err)
		return
	}
	for _, seed := range seeds {
		authType, authHeader, authPrefix := contractAuth(seed.Contract)
		if existing, err := s.externalProviderSvc.GetByName(ctx, seed.Name); err == nil {
			if !existing.Enabled {
				if _, uerr := s.externalProviderSvc.Update(ctx, existing.ID, seed.Label, seed.BaseURL,
					authType, authHeader, authPrefix, "", string(seed.Contract), "", true); uerr != nil {
					slog.Warn("ops provider seeds: re-enable", "provider", seed.Name, "err", uerr)
				}
			}
			continue
		}
		created, err := s.externalProviderSvc.Create(ctx, seed.Name, seed.Label, seed.BaseURL)
		if err != nil {
			slog.Warn("ops provider seeds: create", "provider", seed.Name, "err", err)
			continue
		}
		if _, err := s.externalProviderSvc.Update(ctx, created.ID, seed.Label, seed.BaseURL,
			authType, authHeader, authPrefix, "", string(seed.Contract), "", true); err != nil {
			slog.Warn("ops provider seeds: publish", "provider", seed.Name, "err", err)
		}
	}
}
