// external_proxy.go — registers the 3 replacement MCP tools for the
// AI-Adaptive External API Proxy: call_external_api, list_providers,
// get_provider_schema. These replace the previous 10 L4 MCP intent
// tools (search_work_items, get_work_item_detail, etc.) with a generic,
// schema-driven proxy layer.
//
// All three tunnel to /mcp/external-proxy/* on the niuniu server. The
// MCP shim authenticates with a session token via MCPTokenAuth, just
// like the previous L4 work-item tools did.
package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// extractWriteDisabledMessage parses a server error string of the form
// "API <path> returned <status>: <body>" and extracts the JSON body's
// `message` (or `error`) field when present. Falls back to the original
// error text on any parse failure so 5xx errors / non-JSON bodies still
// surface usefully.
func extractWriteDisabledMessage(rawErr string) string {
	idx := strings.Index(rawErr, ": ")
	if idx < 0 {
		return rawErr
	}
	body := rawErr[idx+2:]
	var env struct {
		Message string `json:"message"`
		Error   string `json:"error"`
		Hint    string `json:"hint"`
		Kind    string `json:"error_kind"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return rawErr
	}
	switch {
	case env.Message != "":
		if env.Hint != "" && env.Hint != env.Message {
			return env.Message + " " + env.Hint
		}
		return env.Message
	case env.Error != "":
		return env.Error
	default:
		return rawErr
	}
}

func registerExternalProxyTools(s *server.MCPServer, api *apiClient) {
	// === call_external_api ===
	s.AddTool(
		mcp.NewTool("call_external_api",
			mcp.WithDescription("Call an external API through the niuniu proxy. The proxy handles auth injection, whitelist enforcement, and audit logging. Use this to interact with any configured external provider (GitHub, Jira, TAPD, etc.)."),
			mcp.WithString("provider", mcp.Description("Provider name (e.g. 'github', 'tapd')"), mcp.Required()),
			mcp.WithString("method", mcp.Description("HTTP method: GET, POST, PATCH, PUT, DELETE"), mcp.Required()),
			mcp.WithString("path", mcp.Description("API path relative to provider base URL, e.g. '/repos/owner/repo/issues'"), mcp.Required()),
			mcp.WithObject("query", mcp.Description("Query parameters as JSON object, e.g. {\"state\":\"open\",\"labels\":\"bug\"}")),
			mcp.WithObject("body", mcp.Description("Request body as JSON object (for POST/PATCH/PUT)")),
			mcp.WithString("source_key",
				mcp.Description("Optional. When a project binds multiple sources of the same provider (e.g. two TAPD workspaces), pick one by its source_key. Omit when there is only one."),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			provider, errRes := requireString(args, "provider")
			if errRes != nil {
				return errRes, nil
			}
			method, errRes := requireString(args, "method")
			if errRes != nil {
				return errRes, nil
			}
			path, errRes := requireString(args, "path")
			if errRes != nil {
				return errRes, nil
			}

			payload := map[string]any{
				"provider": provider,
				"method":   method,
				"path":     path,
			}
			if q, ok := args["query"].(map[string]any); ok {
				payload["query"] = q
			}
			if b, ok := args["body"].(map[string]any); ok {
				payload["body"] = b
			}
			if sourceKey, _ := args["source_key"].(string); sourceKey != "" {
				payload["source_key"] = sourceKey
			}

			data, err := api.post("/mcp/external-proxy/call", payload)
			if err != nil {
				return mcp.NewToolResultError(extractWriteDisabledMessage(err.Error())), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// === list_providers ===
	s.AddTool(
		mcp.NewTool("list_providers",
			mcp.WithDescription("List configured external API providers with their connection status and capabilities."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get("/mcp/external-proxy/providers")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// === get_provider_schema ===
	s.AddTool(
		mcp.NewTool("get_provider_schema",
			mcp.WithDescription("Get the API schema/profile for a provider, including available endpoints, parameters, and response formats. Supports OpenAPI auto-discovery and AI-generated profiles."),
			mcp.WithString("provider", mcp.Description("Provider name"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			provider, errRes := requireString(args, "provider")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.get("/mcp/external-proxy/providers/" + url.PathEscape(provider) + "/schema")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}
