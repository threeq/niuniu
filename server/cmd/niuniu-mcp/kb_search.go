package main

// Knowledge-base retrieval tools (kb_search / kb_list).
//
// These reach the first-class, owner-scoped Knowledge Base (KB) backend over
// /mcp/workspaces/<wsid>/kb/*. Unlike ingest_directory (which writes chunks into
// the durable memory store and retrieves via memory_search), kb_search queries
// the dedicated KB full-text index and returns pointers (rel_path + byte offset)
// plus snippets, tagged with the source KB.
//
// Visibility is enforced server-side: a workspace only sees KBs its owner owns
// AND that are bound to the workspace's project (kb_bindings). Unbound or
// cross-owner KBs are never returned — the tool cannot widen its own scope.

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerKBTools wires kb_search + kb_list. Workspace-scoped: owner + project
// are derived server-side from the workspace token, exactly like memory_* tools.
func registerKBTools(s *server.MCPServer, api *apiClient, wsid string) {
	s.AddTool(
		mcp.NewTool("kb_search",
			mcp.WithDescription("Search the knowledge bases (KB) this workspace can see, using keyword full-text retrieval. "+
				"Returns ranked hits, each with the source KB name, the file's relative path + byte offset (read the file directly for full context), and a snippet. "+
				"Only KBs owned by this workspace's owner AND bound to its project are visible. "+
				"Call kb_list first to see which KBs are available."),
			mcp.WithString("query", mcp.Description("Keyword query (CJK supported). Substring/term match, not semantic."), mcp.Required()),
			mcp.WithNumber("limit", mcp.Description("Max hits to return across all visible KBs (default 10)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			query, errRes := requireString(args, "query")
			if errRes != nil {
				return errRes, nil
			}
			path := "/mcp/workspaces/" + wsid + "/kb/search?q=" + url.QueryEscape(strings.TrimSpace(query))
			if v, ok := args["limit"].(float64); ok && int(v) > 0 {
				path += "&limit=" + strconv.Itoa(int(v))
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(prettyJSON(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("kb_list",
			mcp.WithDescription("List the knowledge bases (KB) this workspace can see (owned by its owner AND bound to its project). "+
				"Each entry has id, name, description and source_kind. Use it to discover what kb_search will cover."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get("/mcp/workspaces/" + wsid + "/kb/list")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(prettyJSON(data)), nil
		},
	)
}

// prettyJSON re-indents a JSON payload for readable tool output, falling back to
// the raw bytes if it is not valid JSON.
func prettyJSON(data []byte) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(data)
	}
	return string(out)
}
