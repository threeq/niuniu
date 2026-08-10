package main

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerPreCommitTool wires the `harness_pre_commit_check` MCP tool. The
// agent should call this before running `git commit`; if blocked=true the
// agent should refuse to commit and surface the failures to the user.
//
// Available whenever a workspace context is set — no harness-run-id required,
// since pre-commit gates run against ad-hoc commit attempts, not phase runs.
func registerPreCommitTool(s *server.MCPServer, api *apiClient, wsid string) {
	s.AddTool(
		mcp.NewTool("harness_pre_commit_check",
			mcp.WithDescription(
				"Call this BEFORE running `git commit`. Runs every enabled harness spec with trigger_on='pre_commit' "+
					"against the supplied commit context and returns {blocked, results[]}. "+
					"If blocked=true: do NOT run `git commit`. Surface the failing specs to the user and either fix the underlying issues "+
					"(re-stage and call this tool again) or get explicit user confirmation to override. "+
					"Note: commit_message and staged_diff may be forwarded to remote AI judges. Do not include secrets, API keys, "+
					"or PII in either field. Staged diff body is capped at 1MB; for larger changesets summarise before sending.",
			),
			mcp.WithString("commit_message", mcp.Description("Commit message that would be used"), mcp.Required()),
			mcp.WithString("branch_name", mcp.Description("Current branch name")),
			mcp.WithString("staged_diff", mcp.Description("Optional: staged diff text for content-aware rubrics; capped at 1MB server-side")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			msg, errRes := requireString(args, "commit_message")
			if errRes != nil {
				return errRes, nil
			}
			body := map[string]any{
				"commit_message": msg,
			}
			if v, ok := args["branch_name"].(string); ok && v != "" {
				body["branch_name"] = v
			}
			if v, ok := args["staged_diff"].(string); ok && v != "" {
				body["staged_diff"] = v
			}
			data, err := api.post("/mcp/workspaces/"+wsid+"/harness/pre-commit-check", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// Validate the response is JSON-shaped before passing through;
			// we deliberately do not re-marshal because the server is the
			// source of truth for the schema.
			var probe map[string]any
			if err := json.Unmarshal(data, &probe); err != nil {
				return mcp.NewToolResultError("pre-commit response was not JSON: " + err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}
