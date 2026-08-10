// data_proxy.go — registers the data integration MCP tools:
//
//   - list_data_sources  — discover configured data sources and their kinds
//   - run_data_query     — run a SQL statement (SQL/Trino kinds) or a
//     structured operation (redis/mongo/elasticsearch kinds) against a
//     named source; exactly one of statement/operation per call (#361)
//
// Both tools authenticate via the session bearer token (MCPTokenAuth) and
// tunnel to /mcp/data-proxy/* on the niuniu server. Gate rejections
// (403 {message, code}) are surfaced to the agent via extractWriteDisabledMessage.
package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerDataProxyTools(s *server.MCPServer, api *apiClient) {
	s.AddTool(
		mcp.NewTool("list_data_sources",
			mcp.WithDescription("List all data sources configured for this workspace. Returns id, name, kind, default_access_mode (read/readwrite), and scope_config. Kinds: SQL databases (mysql/postgres/clickhouse/mssql/mariadb/tidb/oceanbase/starrocks/doris/cockroachdb/greenplum/redshift/opengauss/polardbpg/yugabyte), federated SQL (trino), NoSQL (redis/mongo/elasticsearch), and generic HTTP/JSON REST APIs (http). scope_config (databases/tables_allow/tables_deny) is the subset of data you are authorized to touch; the lists are interpreted per kind — table names for SQL, key prefixes for redis, collection names for mongo, index names/patterns for elasticsearch, request path prefixes for http (http also has an optional `methods` allow-list of HTTP verbs). Call this before run_data_query: the source kind decides which input to pass there (statement for SQL/Trino, operation for redis/mongo/elasticsearch). Vector search needs no dedicated kind: pgvector runs through an ordinary postgres source with plain SQL (e.g. ORDER BY embedding <=> '[...]' LIMIT k), elasticsearch supports kNN via a _search body with `knn`, and redis via FT.SEARCH."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get("/mcp/data-proxy/sources")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("run_data_query",
			mcp.WithDescription("Run a query or operation against a configured niuniu data source. Call list_data_sources first to learn each source's kind, then pass EXACTLY ONE of `statement` or `operation` depending on that kind:\n\n- SQL kinds (mysql/postgres/clickhouse/mssql/mariadb/tidb/oceanbase/starrocks/doris/cockroachdb/greenplum/redshift/opengauss/polardbpg/yugabyte) and trino: pass `statement` with a single SQL statement. Dialect follows the source kind; Trino tables are qualified catalog.schema.table. Do NOT pass `operation`.\n- redis: pass `operation` = {\"command\": \"GET\", \"args\": [\"user:42\"]} — one command per call, args as strings.\n- mongo: pass `operation` = {\"collection\": \"users\", \"mongo_op\": \"find\", \"filter\": {...}}. Reads: find/countDocuments/estimatedDocumentCount/distinct (use `filter`) and aggregate (use `pipeline`: [...] instead). Writes: insertOne/insertMany (`document`), updateOne/updateMany/replaceOne/findOneAndUpdate/findOneAndReplace (`filter` + `document`), deleteOne/deleteMany/findOneAndDelete (`filter`). Admin/DDL ops (drop, createIndex, runCommand, ...) are denied.\n- elasticsearch: pass `operation` = {\"index\": \"logs-2026.06.10\", \"es_method\": \"GET\", \"es_path\": \"_search\", \"query\": {...}}. `index` is ONE concrete index (no wildcards/commas). Whitelisted endpoints — reads: _search, _count, _mget, _doc/{id}, _source/{id}, _mapping; writes: _doc, _doc/{id}, _create/{id}, _update/{id}, _bulk, _update_by_query, _delete_by_query.\n- http: pass `operation` = {\"http_method\": \"GET\", \"http_path\": \"/v1/pets\", \"http_query\": {\"status\": \"available\"}, \"http_body\": {...}}. Calls the source's configured base URL + http_path. http_method GET/HEAD/POST are reads (POST is treated as a read because JSON APIs use it for queries — search / GraphQL / JSON-RPC), PUT/PATCH/DELETE are writes; the path is authorized against the source's scope (path prefixes). A JSON array response becomes rows, a JSON object becomes a single row; if the list is nested in the response (e.g. {code,data:{list:[...]}}), add http_list_path:\"data.list\" to unpack it into rows.\n\nReturns normalized columns+rows. The niuniu server enforces read/write separation, data-scope authorization, and may require user confirmation for writes.\n\nRELATIVE TIME: the server resolves {{now}} tokens in your query to a concrete epoch-millisecond value before running it — {{now}}, {{now-10m}}, {{now-2h}}, {{now+5m}} (units ms/s/m/h/d). Use these for a sliding window that you intend to PIN as a live panel (so each re-run gets a fresh bound), especially against a numeric epoch-millisecond timestamp field that rejects engine date math (e.g. ES range {\"gte\":\"{{now-10m}}\"}); a token alone in a field becomes a JSON number, a token inside text (a SQL statement) becomes its digits.\n\nRENDERING CHARTS IN THE WORKSPACE: emit a fenced code block with language 'niuniu-data' (or its alias 'chart') whose body is JSON. Two ways:\n1) Query-derived (simple): {title, source, statement, result, chart} where result is this tool's return value and chart is {type:'line'|'bar'|'area'|'pie'|'scatter'|'table', x:'<col>', y:['<col>',...]}. ALWAYS include `source` when the data came from a data source, so the chart can be pinned to a data dashboard as a LIVE (re-runnable) panel: SQL/Trino -> `source` + `statement`; NoSQL (redis/mongo/elasticsearch/http) -> `source` + `operation` (the very operation object you passed to this tool). Omit `source` only for a purely illustrative chart not backed by any data source.\n2) Direct ECharts (full control): {title, chart:{type:'echarts', option:{...full ECharts option...}}}. `option` is a plain-JSON ECharts config handed straight to echarts.setOption (declarative only, no functions). A `result` is NOT required for this mode, so you can render any chart you can express as an ECharts option (even without querying a data source). When the data DID come from a data source, ALSO include top-level `source` (plus `statement` for SQL or `operation` for NoSQL) so this chart can likewise be pinned as a live panel. Design-system colors are applied by default and can be overridden inside option. Shortcut: a fenced block with language 'echarts' whose body is the bare option JSON renders the same way, no envelope needed."),
			mcp.WithString("source", mcp.Description("Data source name or id (use list_data_sources to discover available names)"), mcp.Required()),
			mcp.WithString("statement", mcp.Description("SQL/Trino sources only: a single SQL statement (reads: SELECT/SHOW/EXPLAIN/WITH...SELECT). Mutually exclusive with operation.")),
			mcp.WithObject("operation", mcp.Description("NoSQL/HTTP sources only: JSON object with the field group matching the source kind — redis {command, args}; mongo {collection, mongo_op, filter|pipeline, document}; elasticsearch {index, es_method, es_path, query}; http {http_method, http_path, http_query, http_body, http_list_path}. For http: http_method is GET/HEAD/POST (read) or PUT/PATCH/DELETE (write) — POST counts as a read since JSON APIs use it for queries; http_path is a path relative to the source's base URL (e.g. \"/v1/pets\"); http_query is a key->value object of query-string params; http_body is the JSON request body; http_list_path is the OPTIONAL unpack rule — a dotted path to the array inside the response to expand into rows (e.g. \"data.list\"). Omit http_list_path to get the response as-is (top-level array -> rows, object -> one row); set it when the list is nested. It overrides the source's default list_path. Mutually exclusive with statement.")),
			mcp.WithNumber("row_limit", mcp.Description("Max rows (default 1000)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			source, errRes := requireString(args, "source")
			if errRes != nil {
				return errRes, nil
			}
			statement, _ := args["statement"].(string)
			operation, _ := args["operation"].(map[string]any)
			if (statement == "") == (operation == nil) {
				return mcp.NewToolResultError("provide exactly one of statement (SQL/Trino sources) or operation (redis/mongo/elasticsearch sources)"), nil
			}
			payload := map[string]any{"source": source}
			if statement != "" {
				payload["statement"] = statement
			}
			if operation != nil {
				payload["operation"] = operation
			}
			if rl, ok := args["row_limit"].(float64); ok {
				payload["row_limit"] = int(rl)
			}
			data, err := api.post("/mcp/data-proxy/query", payload)
			if err != nil {
				return mcp.NewToolResultError(extractWriteDisabledMessage(err.Error())), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("create_data_source",
			mcp.WithDescription("Create a new niuniu data source so it can be queried with run_data_query. The source is owned by this workspace's owner and automatically bound to this workspace's project, so it becomes immediately visible here (the workspace must belong to a project). Use this to let the user set up a data connection through conversation instead of the settings UI.\n\nKINDS & `config` fields:\n- SQL (mysql/postgres/clickhouse/mssql/mariadb/tidb/oceanbase/starrocks/doris/cockroachdb/greenplum/redshift/opengauss/polardbpg/yugabyte) and trino: {host, port, user, password, database}. trino also accepts options.schema.\n- redis: {host, port, password, database} (database = logical db number as a string).\n- mongo: {host, port, user, password, database}; options may carry URI params (e.g. authSource).\n- elasticsearch: {host, port} with options.scheme (http|https) and EITHER options.api_key OR {user, password} for basic auth.\n- http: {host} (bare hostname, no scheme) with options.scheme (https default | http), options.base_path (optional URL prefix like \"/v1\"), optional options.headers (object of static request headers, e.g. {\"Authorization\": \"Bearer …\"}), optional {user, password} for HTTP basic auth, and optional options.list_path — the DEFAULT unpack rule: a dotted path to the array inside the JSON response to expand into rows (e.g. \"data.list\"); blank returns the response as-is. A query can override it per request via the operation's http_list_path.\n\n`scope_config` (the authorization boundary, interpreted per kind): {databases, tables_allow, tables_deny}. For SQL these are database / table names; redis -> key prefixes; mongo -> collection names; elasticsearch -> index names/patterns; http -> request PATH PREFIXES (e.g. tables_allow:[\"/v1/pets\"]). Set tables_allow to restrict access to exactly the objects the agent should reach. http also accepts `methods`: an allow-list of HTTP verbs (e.g. [\"GET\",\"POST\"]); blank allows all whitelisted verbs (GET/HEAD/POST are reads, PUT/PATCH/DELETE writes).\n\n`default_access_mode`: \"read\" (default) blocks all writes; \"readwrite\" allows writes (still subject to confirmation). `require_confirm`: \"writes_only\" (default) | \"always\" | \"never\". Secrets (password / api_key / headers) are encrypted at rest and never returned. Set `verify: true` to test connectivity right after creating."),
			mcp.WithString("name", mcp.Description("Human-readable unique source name (used as the reference in run_data_query)"), mcp.Required()),
			mcp.WithString("kind", mcp.Description("Source kind: one of mysql/postgres/clickhouse/mssql/mariadb/tidb/oceanbase/starrocks/doris/cockroachdb/greenplum/redshift/opengauss/polardbpg/yugabyte/trino/redis/mongo/elasticsearch/http"), mcp.Required()),
			mcp.WithObject("config", mcp.Description("Connection params for the kind (see tool description): host/port/user/password/database and an optional nested options object")),
			mcp.WithObject("scope_config", mcp.Description("Authorization scope {databases, tables_allow, tables_deny}, interpreted per kind (path prefixes for http)")),
			mcp.WithString("default_access_mode", mcp.Description("read (default) | readwrite")),
			mcp.WithString("require_confirm", mcp.Description("writes_only (default) | always | never")),
			mcp.WithBoolean("verify", mcp.Description("If true, ping the source after creating and report whether it is reachable")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			name, errRes := requireString(args, "name")
			if errRes != nil {
				return errRes, nil
			}
			kind, errRes := requireString(args, "kind")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{"name": name, "kind": kind}
			if cfg, ok := args["config"].(map[string]any); ok {
				payload["config"] = cfg
			}
			if sc, ok := args["scope_config"].(map[string]any); ok {
				payload["scope_config"] = sc
			}
			if m, ok := args["default_access_mode"].(string); ok && m != "" {
				payload["default_access_mode"] = m
			}
			if rc, ok := args["require_confirm"].(string); ok && rc != "" {
				payload["require_confirm"] = rc
			}
			if v, ok := args["verify"].(bool); ok {
				payload["verify"] = v
			}
			data, err := api.post("/mcp/data-proxy/sources", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}
