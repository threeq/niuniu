package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// opsProxyCaller adapts the shared integration proxy to the channel package's
// OperationCaller. Every channel operation runs through the same egress path
// (credential resolution, auth injection, scope/write authorization, egress
// policy, audit) — no channel-specific HTTP code exists in the Ops Bot.
type opsProxyCaller struct {
	// server, not the proxy directly: initOpsBot runs before the integration
	// services are built, so the proxy is resolved lazily per call.
	s *Server
}

var _ channel.OperationCaller = (*opsProxyCaller)(nil)

func (c *opsProxyCaller) CallOperation(ctx context.Context, call channel.OperationCall) (channel.OperationResult, error) {
	proxy := c.s.externalProxySvc
	if proxy == nil {
		return channel.OperationResult{}, fmt.Errorf("opsbot: integration proxy is not configured")
	}
	out, err := proxy.Call(ctx, service.ProxyInput{
		UserID:          call.UserID,
		Provider:        call.Provider,
		Operation:       call.Operation,
		Params:          call.Params,
		Body:            call.Body,
		Query:           call.Query,
		Files:           call.Files,
		FileRoot:        c.s.cfg.DataDir,
		OwnerType:       call.OwnerType,
		OwnerID:         call.OwnerID,
		CredentialAlias: call.Alias,
	})
	if err != nil {
		return channel.OperationResult{}, err
	}
	body := map[string]any{}
	if len(out.Body) > 0 {
		if err := json.Unmarshal(out.Body, &body); err != nil {
			return channel.OperationResult{}, fmt.Errorf("opsbot: decode %s response: %w", call.Operation, err)
		}
	}
	return channel.OperationResult{Status: out.Status, Body: body}, nil
}
