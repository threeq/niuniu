package main

// Sandbox operation functions over MCP (Epic #726 / #739).
//
// Two layers of exposure:
//
//   - A generic pair — list_operation_functions / call_operation_function —
//     which is always registered, so the agent can discover what exists even
//     on a host whose fence is unavailable.
//   - One DYNAMIC tool per published function, named ext__<source>__<function>,
//     registered at session start from the server's published list. This is the
//     "发布后注册为 MCP 工具" requirement: an agent calls the function as a
//     first-class tool rather than through a generic dispatcher, and the tool's
//     description carries the capability note (#738) so the agent knows what it
//     does without reading documentation.
//
// The shim holds no authority of its own: every tool tunnels to the server,
// which enforces the fence, the rate accounting, the idempotency rule and the
// operation-level egress authorization.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// opFunctionSummary mirrors service.FunctionView on the server side, reduced to
// the fields the shim needs to register a tool.
type opFunctionSummary struct {
	ID           int64  `json:"id"`
	ProviderName string `json:"provider_name"`
	Name         string `json:"name"`
	ToolName     string `json:"tool_name"`
	Description  string `json:"description"`
	Runtime      string `json:"runtime"`
	Idempotent   bool   `json:"idempotent"`
	Egress       []struct {
		Operation string `json:"operation"`
		Method    string `json:"method"`
		Path      string `json:"path"`
	} `json:"egress"`
	Status string `json:"status"`
}

// registerOperationFunctionTools wires the generic pair plus one dynamic tool
// per published function.
func registerOperationFunctionTools(s *server.MCPServer, api *apiClient) {
	s.AddTool(
		mcp.NewTool("list_operation_functions",
			mcp.WithDescription("List the published sandbox operation functions for this project's integrations. "+
				"A function handles a call the declarative path cannot express (exchange a token then query, sign a request); "+
				"it runs in a throwaway OS-sandboxed process and reaches the outside world only through the integration proxy."),
			mcp.WithString("provider", mcp.Description("Optional: limit to one provider/source name")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path := "/mcp/operation-functions"
			if p, ok := req.GetArguments()["provider"].(string); ok && p != "" {
				path += "?provider=" + p
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("call_operation_function",
			mcp.WithDescription("Run a published sandbox operation function. Supply provider + function (see list_operation_functions), "+
				"or function_id. Pass idempotency_key when you may retry: a non-idempotent function refuses a repeated key "+
				"rather than risking a duplicate side effect."),
			mcp.WithString("provider", mcp.Description("Provider/source name the function belongs to")),
			mcp.WithString("function", mcp.Description("Function name")),
			mcp.WithNumber("function_id", mcp.Description("Function id (alternative to provider + function)")),
			mcp.WithObject("input", mcp.Description("Arguments object passed to the function as `input`")),
			mcp.WithString("source_key", mcp.Description("Optional: which bound source to use when the project binds several for this provider")),
			mcp.WithString("idempotency_key", mcp.Description("Stable key identifying this logical call across retries")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			return callOperationFunction(ctx, api, args["provider"], args["function"], args["function_id"],
				args["input"], args["source_key"], args["idempotency_key"])
		},
	)

	// Dynamic per-function tools. A failure here must not take down the MCP
	// server: the generic tools above still work, and the agent can discover
	// why through list_operation_functions.
	data, err := api.get("/mcp/operation-functions")
	if err != nil {
		fmt.Fprintf(os.Stderr, "operation-functions: registration skipped: %v\n", err)
		return
	}
	var envelope struct {
		Data []opFunctionSummary `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		fmt.Fprintf(os.Stderr, "operation-functions: undecodable list: %v\n", err)
		return
	}
	registered := map[string]bool{}
	for _, fn := range envelope.Data {
		name := strings.TrimSpace(fn.ToolName)
		if name == "" {
			name = "ext__" + sanitizeSegment(fn.ProviderName) + "__" + sanitizeSegment(fn.Name)
		}
		if registered[name] {
			continue
		}
		registered[name] = true
		registerFunctionTool(s, api, name, fn)
	}
}

// registerFunctionTool exposes one published function as its own MCP tool.
func registerFunctionTool(s *server.MCPServer, api *apiClient, toolName string, fn opFunctionSummary) {
	desc := fmt.Sprintf("Sandboxed operation function %q on %s.", fn.Name, fn.ProviderName)
	if d := strings.TrimSpace(fn.Description); d != "" {
		desc += " " + d
	}
	if ops := declaredOperations(fn); len(ops) > 0 {
		desc += " Reaches: " + strings.Join(ops, ", ") + "."
	}
	desc += " Runs in a throwaway OS-fenced process: no filesystem, no direct network, no environment;"
	desc += " every outbound call goes through the integration proxy (scope, credentials and audit included)."
	if fn.Idempotent {
		desc += " Safe to retry."
	} else {
		desc += " NOT idempotent: pass idempotency_key when retrying; a repeated key is refused rather than replayed."
	}
	if fn.Status == "observing" {
		desc += " Currently in its first-run observation window, so every request it makes is being audited."
	}

	provider := fn.ProviderName
	function := fn.Name
	s.AddTool(
		mcp.NewTool(toolName,
			mcp.WithDescription(desc),
			mcp.WithObject("input", mcp.Description("Arguments object passed to the function as `input`")),
			mcp.WithString("source_key", mcp.Description("Optional: which bound source to use when the project binds several for this provider")),
			mcp.WithString("idempotency_key", mcp.Description("Stable key identifying this logical call across retries")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			return callOperationFunction(ctx, api, provider, function, nil,
				args["input"], args["source_key"], args["idempotency_key"])
		},
	)
}

func callOperationFunction(ctx context.Context, api *apiClient, provider, function, functionID, input, sourceKey, idemKey any) (*mcp.CallToolResult, error) {
	body := map[string]any{}
	if p, ok := provider.(string); ok && p != "" {
		body["provider"] = p
	}
	if f, ok := function.(string); ok && f != "" {
		body["function"] = f
	}
	if id, ok := functionID.(float64); ok && id > 0 {
		body["function_id"] = int64(id)
	}
	if _, hasProvider := body["provider"]; !hasProvider {
		if _, hasID := body["function_id"]; !hasID {
			return mcp.NewToolResultError("provide either provider + function, or function_id"), nil
		}
	} else if _, hasFn := body["function"]; !hasFn {
		return mcp.NewToolResultError("provide either provider + function, or function_id"), nil
	}
	if in, ok := input.(map[string]any); ok {
		body["input"] = in
	}
	if s, ok := sourceKey.(string); ok && s != "" {
		body["source_key"] = s
	}
	if k, ok := idemKey.(string); ok && k != "" {
		body["idempotency_key"] = k
	}
	data, err := api.post("/mcp/operation-functions/call", body)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func declaredOperations(fn opFunctionSummary) []string {
	var ops []string
	for _, e := range fn.Egress {
		if e.Operation != "" {
			ops = append(ops, e.Operation)
			continue
		}
		if e.Method != "" || e.Path != "" {
			ops = append(ops, strings.TrimSpace(e.Method+" "+e.Path))
		}
	}
	return ops
}

// sanitizeSegment mirrors the server's tool-name sanitiser so a name the shim
// derives locally matches the one the server published.
func sanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "x"
	}
	return out
}
