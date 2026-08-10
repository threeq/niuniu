package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/niuniu-dev/niuniu/internal/browserhistory"
)

// defaultSinceDays scopes a read to the last week when the caller omits a
// window — a privacy-conscious default (never "all history" by accident).
const defaultSinceDays = 7

// registerBrowserHistoryTools wires the `read_browser_history` tool. It reads
// the LOCAL browser history databases directly (路子 A) on the machine running
// niuniu-mcp — no server round-trip. It is registered ONLY when the
// browser-history tool group is explicitly enabled (--enable-tool-groups), which
// today only the info-radar scene does. So it is OFF by default everywhere.
//
// Privacy: returns history only (URL/title/visit-time), never cookies or
// credentials. The radar skill is instructed to compress results into a topic
// summary and never persist or push the raw entries.
func registerBrowserHistoryTools(s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("read_browser_history",
			mcp.WithDescription(
				"Read the user's LOCAL browser history (Chrome/Edge/Brave/Firefox) directly off disk, "+
					"scoped to a recent time window. Returns {entries:[{title,url,visit_time,visit_count,browser,profile}]} "+
					"newest-first. Use this ONLY to infer what topics/directions the user has recently been looking at, "+
					"then summarize; do NOT echo raw URLs into anything you push to the user, and do NOT persist them. "+
					"Privacy: history only — never cookies, passwords, or form data.",
			),
			mcp.WithNumber("since_days", mcp.Description("Only visits within the last N days (default 7). Keep this small to respect privacy.")),
			mcp.WithString("domains", mcp.Description("Optional comma-separated hostname allowlist (e.g. \"github.com,arxiv.org\"); matches subdomains. Empty = all hosts.")),
			mcp.WithNumber("limit", mcp.Description("Max entries to return (default 200).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()

			sinceDays := defaultSinceDays
			if v, ok := args["since_days"].(float64); ok && v > 0 {
				sinceDays = int(v)
			}
			limit := 0
			if v, ok := args["limit"].(float64); ok && v > 0 {
				limit = int(v)
			}
			var domains []string
			if v, ok := args["domains"].(string); ok && strings.TrimSpace(v) != "" {
				for _, d := range strings.Split(v, ",") {
					if d = strings.TrimSpace(d); d != "" {
						domains = append(domains, d)
					}
				}
			}

			entries, err := browserhistory.Read(browserhistory.Query{
				Since:   time.Now().Add(-time.Duration(sinceDays) * 24 * time.Hour),
				Limit:   limit,
				Domains: domains,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out, err := json.Marshal(map[string]any{"entries": entries})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(out)), nil
		},
	)
}
