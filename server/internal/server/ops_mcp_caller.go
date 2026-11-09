package server

import (
	"context"
	"fmt"

	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// opsMCPCaller adapts the audited MCP gateway to the channel OperationCaller
// surface, so an MCP-bound Platform Pack runs through the same publish/metrics/
// engagement pipeline — and the same audit table — as an HTTP provider.
type opsMCPCaller struct{ s *Server }

var _ channel.OperationCaller = (*opsMCPCaller)(nil)

func (c *opsMCPCaller) CallOperation(ctx context.Context, call channel.OperationCall) (channel.OperationResult, error) {
	gateway := c.s.mcpGateway
	if gateway == nil {
		return channel.OperationResult{}, fmt.Errorf("opsbot: mcp gateway is not configured")
	}
	args := map[string]any{}
	for key, value := range call.Params {
		args[key] = value
	}
	for key, value := range call.Body {
		args[key] = value
	}
	result, err := gateway.CallTool(ctx, service.MCPCall{
		UserID:          call.UserID,
		OwnerType:       call.OwnerType,
		OwnerID:         call.OwnerID,
		CredentialAlias: call.Alias,
		Provider:        call.Provider,
		Tool:            call.Operation,
		Arguments:       args,
	})
	if err != nil {
		return channel.OperationResult{}, err
	}
	return channel.OperationResult{Status: 200, Body: result}, nil
}
