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
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// extractWriteDisabledMessage parses a server error string of the form
// "API <path> returned <status>: <body>" and extracts the JSON body's
// `message` (or `error`) field when present. The server uses two envelope
// shapes: a flat {message, code} (data proxy gates) and a nested
// {error:{code, message}} (external-proxy ProxyError, e.g. write_disabled /
// outcome_unknown). Both are unwrapped. Falls back to the original error text
// on any parse failure so 5xx errors / non-JSON bodies still surface usefully.
func extractWriteDisabledMessage(rawErr string) string {
	idx := strings.Index(rawErr, ": ")
	if idx < 0 {
		return rawErr
	}
	body := rawErr[idx+2:]
	var env struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
		Hint    string          `json:"hint"`
		Kind    string          `json:"error_kind"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return rawErr
	}
	if env.Message != "" {
		if env.Hint != "" && env.Hint != env.Message {
			return env.Message + " " + env.Hint
		}
		return env.Message
	}
	if len(env.Error) > 0 {
		// Flat string error.
		var s string
		if json.Unmarshal(env.Error, &s) == nil && s != "" {
			return s
		}
		// Nested {code, message} envelope.
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(env.Error, &nested) == nil && nested.Message != "" {
			return nested.Message
		}
	}
	return rawErr
}

// callExternalAPIIdempotency is appended to the call_external_api description.
// It is the discipline the agent must follow for side-effecting calls: never
// replay an ambiguous write, and read before writing.
const callExternalAPIIdempotency = " IDEMPOTENCY (writes): niuniu does NOT de-duplicate calls, so pass an idempotency key in the body/query when the upstream supports one, and READ the current state (GET) before a create/update so you don't duplicate it. If a write's outcome is uncertain — the result reports outcome_unknown (the request may have reached the upstream before the connection broke) — do NOT blindly retry: re-read the resource to see whether it took effect, then act on what you find."

// callExternalAPIDryRun describes the dry_run parameter.
const callExternalAPIDryRun = "dry_run (optional, default false): validate the call LOCALLY without contacting the upstream — checks provider visibility, the operation/whitelist authorization, that a credential resolves, and that required parameters match the provider's contract. Returns the per-check verdict + next_action. Use it before an expensive or side-effecting call."

func registerExternalProxyTools(s *server.MCPServer, api *apiClient) {
	// === call_external_api ===
	s.AddTool(
		mcp.NewTool("call_external_api",
			mcp.WithDescription("Call an external API through the niuniu proxy. The proxy handles auth injection, operation-level authorization, and audit logging. Address a call EITHER by a semantic operation name from the provider's contract (preferred — call get_provider_schema to list operations) OR by an explicit method+path pair. Use this to interact with any configured external provider (GitHub, Jira, TAPD, etc.)."+callExternalAPIDryRun+callExternalAPIIdempotency),
			mcp.WithString("provider", mcp.Description("Provider name (e.g. 'github', 'tapd')"), mcp.Required()),
			mcp.WithString("operation", mcp.Description("Semantic operation name from the provider's contract (e.g. 'cancelOrder'). Preferred over method+path: the proxy resolves the method/path and authorizes by operation. Call get_provider_schema to see the available operations and which are write/sensitive.")),
			mcp.WithString("method", mcp.Description("HTTP method: GET, POST, PATCH, PUT, DELETE. Required only when `operation` is not supplied.")),
			mcp.WithString("path", mcp.Description("API path relative to provider base URL, e.g. '/repos/owner/repo/issues'. Required only when `operation` is not supplied.")),
			mcp.WithObject("query", mcp.Description("Query parameters as JSON object, e.g. {\"state\":\"open\",\"labels\":\"bug\"}. Values named by the operation's path parameters are folded into the path automatically.")),
			mcp.WithObject("body", mcp.Description("Request body as JSON object (for POST/PATCH/PUT)")),
			mcp.WithString("source_key",
				mcp.Description("Optional. When a project binds multiple sources of the same provider (e.g. two TAPD workspaces), pick one by its source_key. Omit when there is only one."),
			),
			mcp.WithBoolean("dry_run",
				mcp.Description("Validate locally without contacting the upstream (see the tool description)."),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			provider, errRes := requireString(args, "provider")
			if errRes != nil {
				return errRes, nil
			}
			operation, _ := args["operation"].(string)
			// method/path are optional when an operation names the target; the
			// proxy resolves them from the contract. Require them otherwise.
			method, _ := args["method"].(string)
			path, _ := args["path"].(string)
			if operation == "" && (method == "" || path == "") {
				return mcp.NewToolResultError("either `operation`, or both `method` and `path`, are required"), nil
			}

			payload := map[string]any{"provider": provider}
			if operation != "" {
				payload["operation"] = operation
			}
			if method != "" {
				payload["method"] = method
			}
			if path != "" {
				payload["path"] = path
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
			if dry, ok := args["dry_run"].(bool); ok && dry {
				payload["dry_run"] = true
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
			mcp.WithDescription("Get a provider's structured integration contract: named operations (semantic name, method, path, parameters, read-only vs write, request example, response shape), the auth the proxy injects, a verify_operation probe, capability tags, and a content digest. Also returns `capability` — an operable \"how to drive this provider\" description (usage + worked call examples + limits) generated from the confirmed contract — and `capability_agent_block`, the exact block injected into a workspace's instruction file. Legacy providers return an empty-operations contract plus their raw profile text."),
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

	// === verify_provider ===
	s.AddTool(
		mcp.NewTool("verify_provider",
			mcp.WithDescription("Run a provider's read-only verification probe (auth + connectivity) and report the result. The probe is the contract's verify_operation, or — when unset — the least-harmful read-only operation. The response includes the probe manifest (method, path, parameters) so you can show the user exactly what will be called. Does not change the provider's enabled state."),
			mcp.WithString("provider", mcp.Description("Provider name"), mcp.Required()),
			mcp.WithString("source_key", mcp.Description("Optional source_key when the project binds multiple sources for this provider.")),
			mcp.WithBoolean("confirm", mcp.Description("Set true only after the user approves a non-GET probe shown on the confirmation card.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			provider, errRes := requireString(args, "provider")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{}
			if sk, _ := args["source_key"].(string); sk != "" {
				payload["source_key"] = sk
			}
			if cf, ok := args["confirm"].(bool); ok && cf {
				payload["confirm"] = true
			}
			data, err := api.post("/mcp/external-proxy/providers/"+url.PathEscape(provider)+"/verify", payload)
			if err != nil {
				return mcp.NewToolResultError(extractWriteDisabledMessage(err.Error())), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// === publish_provider ===
	s.AddTool(
		mcp.NewTool("publish_provider",
			mcp.WithDescription("Publish a provider contract: lint it, run the verification probe, and only on success persist the contract and enable the provider. On failure nothing is persisted (the provider keeps its previous state) and the response carries next_action + alternatives. A probe that is not GET/HEAD requires human confirmation: the first call returns requires_confirmation with the probe manifest — show it to the user, and if they approve, call again with confirm=true."),
			mcp.WithNumber("provider_id", mcp.Description("Provider id (from get_provider_schema / list_providers)"), mcp.Required()),
			mcp.WithString("profile", mcp.Description("Optional candidate contract JSON to validate and persist; omit to re-publish the stored profile.")),
			mcp.WithBoolean("confirm", mcp.Description("Set true only after the user approves a non-GET probe shown on the confirmation card.")),
			mcp.WithString("source_key", mcp.Description("Optional source_key when the project binds multiple sources for this provider.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			idFloat, ok := args["provider_id"].(float64)
			if !ok {
				return mcp.NewToolResultError("provider_id is required"), nil
			}
			payload := map[string]any{}
			if p, _ := args["profile"].(string); p != "" {
				payload["profile"] = p
			}
			if cf, ok := args["confirm"].(bool); ok && cf {
				payload["confirm"] = true
			}
			if sk, _ := args["source_key"].(string); sk != "" {
				payload["source_key"] = sk
			}
			data, err := api.post("/mcp/external-proxy/providers/"+url.PathEscape(strconv.FormatInt(int64(idFloat), 10))+"/publish", payload)
			if err != nil {
				return mcp.NewToolResultError(extractWriteDisabledMessage(err.Error())), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}
