package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerLocalRunnerTools wires the engine-agnostic local-runner tool set
// (Epic #526 子B). These tunnel to /mcp/local-runner/* on the niuniu server,
// which dispatches over the reverse channel to the bound desktop runner and
// streams stdout/stderr back to the workspace log view.
//
// The whole group is registered only when the local-runner tool group is
// enabled for this session (the server hides it via --disable-tool-groups
// whenever the workspace has no online runner), so "prefer local, else server"
// falls out of the tools' mere presence — there is no server-side fallback
// inside local_exec. Workspace is resolved server-side from the session token,
// so no workspace id travels in the request.
func registerLocalRunnerTools(s *server.MCPServer, api *apiClient) {
	s.AddTool(
		mcp.NewTool("local_exec",
			mcp.WithDescription("Run a build/pack/test command on THIS workspace's local runner (the user's machine), inside the bound directory, using the local toolchain. Prefer this over server-side execution for build/pack/test. Streams stdout/stderr to the local execution log and returns the exit code. Errors if the local runner is offline — fall back to normal server-side execution."),
			mcp.WithString("command", mcp.Description("The shell command to run in the bound local directory"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			command, errRes := requireString(args, "command")
			if errRes != nil {
				return errRes, nil
			}
			body, err := api.post("/mcp/local-runner/exec", map[string]any{"command": command})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("local_read",
			mcp.WithDescription("Read a file from the local runner's bound directory (e.g. a build artifact that is not synced back to the remote worktree). Errors if the local runner is offline."),
			mcp.WithString("path", mcp.Description("Path to read, relative to the bound local directory"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			path, errRes := requireString(args, "path")
			if errRes != nil {
				return errRes, nil
			}
			body, err := api.post("/mcp/local-runner/read", map[string]any{"path": path})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("local_sync",
			mcp.WithDescription("Force a remote->local sync of the current worktree (git checkout + uncommitted diff) before building on the local runner. Normally the runner syncs automatically before exec; call this to sync on demand. Errors if the local runner is offline."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			body, err := api.post("/mcp/local-runner/sync", nil)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(body)), nil
		},
	)
}
