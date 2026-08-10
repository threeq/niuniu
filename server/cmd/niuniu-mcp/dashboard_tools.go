// dashboard_tools.go — registers the data-dashboard MCP tools (pin_query,
// list_dashboards). Both tunnel to /mcp/dashboards/* on the niuniu server.
//
// pin_query saves a read-only query and pins it to the owner's dashboard
// (auto-creating the default dashboard on first pin). The server takes the
// CURRENT WORKSPACE from the session bearer token and records it as the saved
// query's origin workspace_id, so the agent never passes a workspace id (and the
// pinned chart can later link back to the workspace it was created in).
package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerDashboardTools(s *server.MCPServer, api *apiClient) {
	s.AddTool(
		mcp.NewTool("pin_query",
			mcp.WithDescription("Pin a chart to the user's data dashboard. Adds a panel; if the user has no dashboard yet, a default one is created (this makes the 'data dashboard' nav entry appear). The current workspace is recorded automatically so the pinned chart can link back here. To pin a self-contained chart (e.g. a niuniu-data block with chart.type='echarts'), omit source/operation and pass chart_spec (and optionally snapshot={\"result\":...}); to pin a re-runnable LIVE query, pass source + operation in the SAME shape run_data_query uses for that source kind — SQL/Trino: {statement}; redis: {command, args}; mongo: {collection, mongo_op, filter|pipeline|document}; elasticsearch: {index, es_method, es_path, query}; http: {http_method, http_path, http_query, http_body}. Read-only is enforced server-side and the panel re-runs this exact operation on every dashboard open. For a LIVE echarts panel whose result shape varies, author chart_spec={type:echarts, option} with an echarts `dataset` but OMIT its source — on each re-query the dashboard injects the result rows into the first dataset.source and the query columns as dimensions; reshape rows with echarts BUILT-IN transforms (filter/sort) and map dimensions via series.encode. ALWAYS derive dimensions/encode/transforms from the ACTUAL result columns — run run_data_query first to learn the exact column names, reference ONLY those, and generate any needed transform (filter/sort/aggregate) yourself from the data shape (do not ask the user); never sort/encode a column the query does not return (it throws and breaks the panel). Declarative JSON only: never inline fixed data, never use functions/callbacks (no eval). TIME WINDOW — a LIVE time-series panel MUST use a RELATIVE window so it actually slides on each re-run: never bake an absolute timestamp/date captured now (it freezes the window forever). Use a relative bound the engine evaluates each run (SQL: NOW() - INTERVAL '10' MINUTE; ES date field: \"gte\":\"now-10m\"), OR — for any source, and REQUIRED for a numeric epoch-millisecond timestamp field that rejects engine date math — the server-resolved token {{now-10m}} (also {{now}}, {{now-2h}}, {{now+5m}}; units ms/s/m/h/d), e.g. ES range {\"gte\":\"{{now-10m}}\"} resolves to a fresh epoch on every query. Pick a bounded window (last N minutes/hours), not an open-ended start, so the re-query stays small."),
			mcp.WithString("source", mcp.Description("Data source name or id (omit for a self-contained / static chart)")),
			mcp.WithString("name", mcp.Description("Display name for the saved query / panel"), mcp.Required()),
			mcp.WithObject("operation", mcp.Description("Query operation for a re-runnable LIVE query, in the source kind's shape — SQL/Trino {statement}; redis {command, args}; mongo {collection, mongo_op, filter|pipeline|document}; elasticsearch {index, es_method, es_path, query}; http {http_method, http_path, http_query, http_body, http_list_path}. Omit for a static chart. For an ES time-series, use a size:0 _search with a date_histogram aggregation — buckets are returned as one row per bucket. For an http source whose list is nested in the response, FIRST run the query and inspect the response, then set http_list_path (e.g. \"data.list\") so the live panel unpacks rows on every re-query — write this rule yourself; don't expect the user to supply it.")),
			mcp.WithObject("chart_spec", mcp.Description("Chart spec, e.g. {\"type\": \"bar\", \"x\": \"day\", \"y\": [\"count\"]} or {\"type\": \"echarts\", \"option\": {...}}")),
			mcp.WithObject("snapshot", mcp.Description("Optional inline snapshot for a static chart, e.g. {\"result\": <ResultSet>}")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			name, errRes := requireString(args, "name")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{"name": name}
			if source, ok := args["source"].(string); ok && source != "" {
				payload["source"] = source
			}
			if operation, ok := args["operation"].(map[string]any); ok && operation != nil {
				payload["operation"] = operation
			}
			if cs, ok := args["chart_spec"].(map[string]any); ok && cs != nil {
				payload["chart_spec"] = cs
			}
			if snap, ok := args["snapshot"].(map[string]any); ok && snap != nil {
				payload["snapshot"] = snap
			}
			data, err := api.post("/mcp/dashboards/pin", payload)
			if err != nil {
				return mcp.NewToolResultError(extractWriteDisabledMessage(err.Error())), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_dashboards",
			mcp.WithDescription("List the user's data dashboards (id + name). Use to discover an existing dashboard before pinning, or to confirm the dashboard nav entry exists."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get("/mcp/dashboards")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}
