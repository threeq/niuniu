package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Version is set at build time via -ldflags -X main.Version=...
var Version = "dev"

// Built-in tool groups that a scene can hide via --disable-tool-groups, so the
// agent's toolset stays focused (e.g. the office assistant has no use for
// cross-agent coordination or a git/harness gate). Names are part of the scene
// contract (scene YAML disable_tool_groups) — keep them stable.
const (
	toolGroupMultiAgent = "multi-agent"  // blackboard_* + inbox_*
	toolGroupHarness    = "harness"      // harness_pre_commit_check + gate_run + gate_results
	toolGroupLocalRun   = "local-runner" // local_exec / local_read / local_sync (Epic #526 子B)
)

// Opt-in tool groups: OFF by default, registered ONLY when named in
// --enable-tool-groups. Used for privacy-sensitive tools that must never appear
// unless a scene explicitly turns them on (mirror of disable_tool_groups, but
// inverted default). Set by scene projection from scene YAML enable_tool_groups.
const (
	toolGroupBrowserHistory = "browser-history" // read_browser_history (info-radar 路子 A)
)

// parseToolGroups turns a comma-separated --disable-tool-groups value into a
// set. Blank entries are skipped; unknown names are kept (harmless — no
// registration consults them) so older/newer launchers don't error.
func parseToolGroups(csv string) map[string]bool {
	out := map[string]bool{}
	for _, g := range strings.Split(csv, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out[g] = true
		}
	}
	return out
}

func main() {
	// Subcommand dispatch — checked BEFORE flag.Parse because the
	// worktree-{create,remove} hooks read JSON from stdin and have no
	// CLI flags to parse. Falling through to the default MCP server
	// behavior would just block reading mcp-go's stdio framing.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "worktree-create":
			os.Exit(runWorktreeCreate(os.Stdin, os.Stdout, os.Stderr))
		case "worktree-remove":
			os.Exit(runWorktreeRemove(os.Stdin, os.Stdout, os.Stderr))
		case "read-hook":
			// PreToolUse hook for the built-in Read tool: reroutes large
			// images / binary documents to the niuniu MCP fast-path tools,
			// with loop-prevention so fast-path fallbacks reach built-in
			// Read exactly once. See readhook.go.
			os.Exit(runReadHook(os.Stdin, os.Stdout, os.Stderr))
		}
	}

	apiBase := flag.String("api-base", "", "niuniu API server URL (required)")
	workspaceID := flag.Int64("workspace-id", 0, "workspace ID (enables workspace tools)")
	projectID := flag.Int64("project-id", 0, "project ID (enables project tools)")
	// --inbox-dir is kept for backward compatibility with older launch scripts,
	// but is no longer used — inbox storage is managed by the server process now
	// and reached via /mcp/inbox/{send,read}.
	_ = flag.String("inbox-dir", "", "deprecated: inbox directory (ignored; retained for backward compatibility)")
	agentName := flag.String("agent-name", "agent", "agent name for attribution")
	// --disable-tool-groups hides whole groups of built-in tools (comma-separated)
	// so a scene can keep the agent's toolset focused. Recognized groups:
	//   multi-agent — blackboard_* + inbox_* (cross-agent coordination)
	//   harness     — harness_pre_commit_check + gate_run + gate_results
	// Unknown names are ignored. Set by scene projection from disable_tool_groups.
	disableToolGroups := flag.String("disable-tool-groups", "", "comma-separated built-in tool groups to hide (multi-agent,harness)")
	// --enable-tool-groups turns ON opt-in groups that are OFF by default
	// (privacy-sensitive tools). Recognized: browser-history. Set by scene
	// projection from scene YAML enable_tool_groups.
	enableToolGroups := flag.String("enable-tool-groups", "", "comma-separated opt-in tool groups to enable (browser-history)")
	flag.Parse()

	disabledGroups := parseToolGroups(*disableToolGroups)
	enabledGroups := parseToolGroups(*enableToolGroups)

	if *apiBase == "" {
		fmt.Fprintf(os.Stderr, "--api-base is required\n")
		os.Exit(1)
	}

	s := server.NewMCPServer("niuniu", Version)
	api := &apiClient{
		base:      strings.TrimRight(*apiBase, "/"),
		client:    &http.Client{Timeout: 30 * time.Second},
		authToken: os.Getenv("NIUNIU_MCP_TOKEN"),
	}

	// niuniu_permission_prompt forwards Claude CLI permission prompts to the
	// niuniu server, which blocks until the user decides via chat UI. Always
	// register — it is independent of project / workspace / harness flags.
	registerPermissionPromptTool(s, api)

	// niuniu_ask_user_question forwards multiple-choice user questions to the
	// niuniu chat UI (substitute for Claude Code's built-in AskUserQuestion
	// which requires a TTY). Always registered; only useful with a workspace
	// context but we let it be tried so 401 surfaces cleanly upstream.
	registerAskUserTool(s, api)

	// External API proxy — replacement for the 10 old L4 work-item tools.
	// Three generic tools (call_external_api, list_providers, get_provider_schema)
	// tunnel to /mcp/external-proxy/* on the niuniu server.
	registerExternalProxyTools(s, api)

	// Data integration proxy — run_data_query tunnels to /mcp/data-proxy/query,
	// which runs the three-layer gate + audit before executing a read-only query.
	registerDataProxyTools(s, api)

	// Data dashboards — pin_query / list_dashboards tunnel to /mcp/dashboards/*.
	// pin_query records the current workspace (from the session token) as the
	// pinned query's origin so the chart can link back to this workspace.
	registerDashboardTools(s, api)

	// Read-accel document fast path (#281) — read_document extracts text from
	// PDF/Office files purely in Go (no external runtime), or just metadata with
	// meta_only=true. Always registered: it operates on local file paths and
	// needs no project/workspace context or API calls. On parse failure it emits
	// a {fallback:"read",...} envelope steering the agent to the built-in Read.
	registerDocumentTools(s)

	// Read-accel image fast path + optional OCR (#283) — read_image OCRs
	// images to text when Tesseract is installed, else degrades to model
	// vision (downsampled image) with an install-guide link. Always
	// registered: it operates on local file paths, probes Tesseract itself,
	// and never errors on a missing OCR engine.
	registerImageTools(s)

	// browser-history group (info-radar 路子 A) — read_browser_history reads the
	// LOCAL browser history DBs directly. OFF by default; registered ONLY when a
	// scene opts in via enable_tool_groups (today: info-radar). Privacy-sensitive,
	// so it must never appear unless explicitly enabled.
	if enabledGroups[toolGroupBrowserHistory] {
		registerBrowserHistoryTools(s)
	}

	// create_managed_task (#391) — the conversational office assistant uses this
	// to stand up a recurring scheduled task from a natural-language request. The
	// server provisions the backing workspace + cron schedule from the MCP
	// token's owner, so it needs no project/workspace flag — always registered.
	registerManagedTaskTool(s, api)

	if *projectID > 0 {
		pid := strconv.FormatInt(*projectID, 10)
		registerProjectTools(s, api, pid)
		// Issue write tools (delete/update + checklist write) operate on a bare
		// issue_id, so they only need project context to be enabled — same gate
		// as get_issue_detail above.
		registerIssueWriteTools(s, api)
		// IM Bot onboarding tools (Epic #555 T4): five project-scoped tools for
		// the guidance agent. The pid closure is captured at registration time;
		// no project_id argument is exposed to the agent.
		registerIMBotTools(s, api, pid)
	}

	if *workspaceID > 0 {
		wsid := strconv.FormatInt(*workspaceID, 10)
		// multi-agent group: cross-agent coordination primitives. A single-agent
		// scene (e.g. the office assistant) hides them via disable_tool_groups.
		if !disabledGroups[toolGroupMultiAgent] {
			registerBlackboardTools(s, api, wsid, *agentName)
			// Inbox tools also require a workspace ID — the server keys inbox
			// storage by workspace and we send it in the request body.
			registerInboxTools(s, api, *workspaceID, *agentName)
		}
		// harness group: pre-commit gate check (agent calls before `git commit`).
		// No-repo scenes (no git, no harness) hide it via disable_tool_groups.
		if !disabledGroups[toolGroupHarness] {
			registerPreCommitTool(s, api, wsid)
		}
		// White-box memory tools (generate/extract/search/consolidate).
		// Owner is derived server-side from the workspace.
		registerMemoryTools(s, api, wsid)
		// Local knowledge-base ingestion (ingest_directory): scan a local
		// directory -> slice -> write memories via the memory extract endpoint +
		// blackboard for idempotency. Retrieval reuses memory_search.
		registerKnowledgeTools(s, api, wsid, *agentName)
		// First-class KB retrieval (kb_search / kb_list): keyword FTS over the
		// owner-scoped Knowledge Base index, restricted to KBs bound to this
		// workspace's project. Distinct from ingest_directory + memory_search.
		registerKBTools(s, api, wsid)
		// local-runner group (Epic #526 子B): local_exec/local_read/local_sync.
		// The server disables this group in the generated .mcp.json whenever the
		// workspace has no online desktop runner, so the tools appear only when a
		// runner is live and the agent otherwise falls back to server execution.
		if !disabledGroups[toolGroupLocalRun] {
			registerLocalRunnerTools(s, api)
		}
	}

	// Gate tools (gate_run / gate_results) are issue-bound / workspace-scoped —
	// they do not depend on a harness run, so register unconditionally whenever
	// a workspace is bound (unless the harness group is disabled for this scene).
	if *workspaceID > 0 && !disabledGroups[toolGroupHarness] {
		wsid := strconv.FormatInt(*workspaceID, 10)
		registerHarnessTools(s, api, wsid)
		registerCheckpointTools(s, api, wsid)
	}

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

// --- HTTP client ---

type apiClient struct {
	base      string
	client    *http.Client
	authToken string // raw MCP session token from NIUNIU_MCP_TOKEN env var
}

// permissionPromptClient is used ONLY for /mcp/permission-prompt — that route
// blocks server-side until the user decides (default 2h). The shared
// apiClient has a 30s timeout which would prematurely drop the wait.
// Timeout: 0 means no client-side ceiling; ctx propagation handles
// agent-killed cancellation.
var permissionPromptClient = &http.Client{Timeout: 0}

// askUserClient mirrors permissionPromptClient — same blocking semantics
// (the server waits until the user answers in chat).
var askUserClient = &http.Client{Timeout: 0}

// addAuth adds the Authorization header if a token is configured.
func (c *apiClient) addAuth(req *http.Request) {
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
}

func (c *apiClient) get(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.base+path, nil)
	if err != nil {
		return nil, err
	}
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API %s returned %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *apiClient) post(path string, payload any) ([]byte, error) {
	var buf bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&buf).Encode(payload); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest("POST", c.base+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API %s returned %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

// requireString extracts a required string argument. Returns error tool result if missing.
func requireString(args map[string]any, key string) (string, *mcp.CallToolResult) {
	v, ok := args[key].(string)
	if !ok || v == "" {
		return "", mcp.NewToolResultError(fmt.Sprintf("%s is required and must be a non-empty string", key))
	}
	return v, nil
}

// requireNumber extracts a required number argument.
func requireNumber(args map[string]any, key string) (float64, *mcp.CallToolResult) {
	v, ok := args[key].(float64)
	if !ok {
		return 0, mcp.NewToolResultError(fmt.Sprintf("%s is required and must be a number", key))
	}
	return v, nil
}

// patch sends a JSON-encoded PATCH and returns the response body. Mirrors
// post in error handling — non-2xx codes surface as an error carrying the
// full response body so callers (e.g. external-work-item write tools) can
// pull error_kind/message out of the 403 write_disabled envelope.
func (c *apiClient) patch(path string, payload any) ([]byte, error) {
	var buf bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&buf).Encode(payload); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest("PATCH", c.base+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API %s returned %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

// put sends a JSON-encoded PUT and returns the response body. Mirrors post —
// the issue write routes (update / move / lifecycle / labels / checklist
// update) are all PUT on the niuniu server.
func (c *apiClient) put(path string, payload any) ([]byte, error) {
	var buf bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&buf).Encode(payload); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest("PUT", c.base+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API %s returned %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *apiClient) doDelete(path string) error {
	req, err := http.NewRequest("DELETE", c.base+path, nil)
	if err != nil {
		return err
	}
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API %s returned %d: %s", path, resp.StatusCode, string(body))
	}
	return nil
}

// --- Project tools ---

func registerProjectTools(s *server.MCPServer, api *apiClient, pid string) {
	s.AddTool(
		mcp.NewTool("get_project_summary",
			mcp.WithDescription("Project overview: project info, board structure, issue stats."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			projectData, err := api.get("/mcp/projects/" + pid)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			columnsData, err := api.get("/mcp/projects/" + pid + "/columns")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// Fetch issues to compute priority stats (matching old niuniu-mcp behavior)
			issuesData, err := api.get("/mcp/projects/" + pid + "/issues")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var issues []map[string]any
			json.Unmarshal(issuesData, &issues)
			stats := map[string]any{
				"total_issues": len(issues),
				"by_priority":  map[string]int{"high": 0, "medium": 0, "low": 0},
			}
			byPri := stats["by_priority"].(map[string]int)
			for _, iss := range issues {
				if p, ok := iss["priority"].(float64); ok {
					switch int(p) {
					case 3:
						byPri["high"]++
					case 2:
						byPri["medium"]++
					case 1:
						byPri["low"]++
					}
				}
			}
			statsJSON, _ := json.Marshal(stats)
			result := fmt.Sprintf(`{"project":%s,"columns":%s,"stats":%s}`,
				string(projectData), string(columnsData), string(statsJSON))
			return mcp.NewToolResultText(result), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_issues",
			mcp.WithDescription("Query project issues; filter by column, lifecycle status, label, or keyword."),
			mcp.WithNumber("column_id", mcp.Description("Filter by column ID")),
			mcp.WithString("lifecycle_status", mcp.Description("Filter by lifecycle status")),
			mcp.WithString("label", mcp.Description("Filter by label (partial match)")),
			mcp.WithString("search", mcp.Description("Search title or description")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			params := url.Values{}
			if v, ok := req.GetArguments()["column_id"].(float64); ok && v > 0 {
				params.Set("column_id", strconv.FormatInt(int64(v), 10))
			}
			if v, ok := req.GetArguments()["lifecycle_status"].(string); ok && v != "" {
				params.Set("lifecycle_status", v)
			}
			if v, ok := req.GetArguments()["label"].(string); ok && v != "" {
				params.Set("label", v)
			}
			if v, ok := req.GetArguments()["search"].(string); ok && v != "" {
				params.Set("search", v)
			}
			path := "/mcp/projects/" + pid + "/issues"
			if len(params) > 0 {
				path += "?" + params.Encode()
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("get_issue_detail",
			mcp.WithDescription("Issue detail: full description, checklist, and comments."),
			mcp.WithNumber("issue_id", mcp.Description("Issue ID"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueIDF, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			issueID := strconv.FormatInt(int64(issueIDF), 10)
			issueData, err := api.get("/mcp/issues/" + issueID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			checklistData, err := api.get("/mcp/issues/" + issueID + "/checklists")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			commentsData, err := api.get("/mcp/issues/" + issueID + "/comments")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result := fmt.Sprintf(`{"issue":%s,"checklists":%s,"comments":%s}`,
				string(issueData), string(checklistData), string(commentsData))
			return mcp.NewToolResultText(result), nil
		},
	)

	s.AddTool(
		mcp.NewTool("batch_create_issues",
			mcp.WithDescription("Batch-create issues into the board's first column. Each task optionally supports "+
				"executable-Epic fields: parent_issue_id (attach under an Epic parent; parent and child must share a "+
				"project or the whole batch fails), issue_type ('task' default | 'epic'), exec_wave (child execution wave; same wave parallel, waves serial)."),
			mcp.WithArray("tasks",
				mcp.Description("Task list (array). Per item: title (required), description, priority (0-3), "+
					"estimate_type, estimate, checklist (string array), parent_issue_id, issue_type, exec_wave."),
				mcp.Required(),
				mcp.Items(map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":           map[string]any{"type": "string", "description": "Issue title (required)"},
						"description":     map[string]any{"type": "string"},
						"priority":        map[string]any{"type": "integer", "description": "0-3"},
						"estimate_type":   map[string]any{"type": "string"},
						"estimate":        map[string]any{"type": "number"},
						"checklist":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"parent_issue_id": map[string]any{"type": "integer", "description": "Attach under an Epic parent (same project)"},
						"issue_type":      map[string]any{"type": "string", "description": "'task' (default) | 'epic'"},
						"exec_wave":       map[string]any{"type": "integer", "description": "Child execution wave"},
					},
					"required": []any{"title"},
				}),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			tasks := req.GetArguments()["tasks"]
			if tasks == nil {
				return mcp.NewToolResultError("tasks is required"), nil
			}
			body := map[string]any{"tasks": tasks}
			data, err := api.post("/mcp/projects/"+pid+"/issues/batch", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// batch_move_issues — move N issues to a target column (appended to end,
	// input order preserved). Cross-project ids are skipped with reason
	// "cross_project"; unauthorized ids are skipped with reason "forbidden".
	// Server returns {succeeded, skipped:[{id,reason}]}.
	s.AddTool(
		mcp.NewTool("batch_move_issues",
			mcp.WithDescription("Batch-move issues to a target column (appended to its end, preserving relative order)."),
			mcp.WithArray("issue_ids", mcp.Description("Issue IDs to move"), mcp.Required()),
			mcp.WithNumber("column_id", mcp.Description("Target column ID"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			if args["issue_ids"] == nil {
				return mcp.NewToolResultError("issue_ids is required"), nil
			}
			colID, errRes := requireNumber(args, "column_id")
			if errRes != nil {
				return errRes, nil
			}
			body := map[string]any{
				"issue_ids": args["issue_ids"],
				"column_id": int64(colID),
			}
			data, err := api.post("/mcp/issues/batch/move", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// batch_update_issues — change priority and/or add/remove labels across
	// many issues in one call. Performs up to two server calls (priority +
	// labels) and returns their combined results. At least one of priority /
	// add_label_ids / remove_label_ids must be provided.
	s.AddTool(
		mcp.NewTool("batch_update_issues",
			mcp.WithDescription("Batch-update issues: change priority and/or add/remove labels."),
			mcp.WithArray("issue_ids", mcp.Description("Issue ID list"), mcp.Required()),
			mcp.WithNumber("priority", mcp.Description("Priority 0-3 (omit to leave unchanged)")),
			mcp.WithArray("add_label_ids", mcp.Description("Label IDs to add")),
			mcp.WithArray("remove_label_ids", mcp.Description("Label IDs to remove")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			if args["issue_ids"] == nil {
				return mcp.NewToolResultError("issue_ids is required"), nil
			}
			var out []string
			didSomething := false
			if p, ok := args["priority"].(float64); ok {
				body := map[string]any{"issue_ids": args["issue_ids"], "priority": int64(p)}
				data, err := api.post("/mcp/issues/batch/priority", body)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				out = append(out, "priority: "+string(data))
				didSomething = true
			}
			add := args["add_label_ids"]
			rem := args["remove_label_ids"]
			if add != nil || rem != nil {
				body := map[string]any{"issue_ids": args["issue_ids"]}
				if add != nil {
					body["add_label_ids"] = add
				}
				if rem != nil {
					body["remove_label_ids"] = rem
				}
				data, err := api.post("/mcp/issues/batch/labels", body)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				out = append(out, "labels: "+string(data))
				didSomething = true
			}
			if !didSomething {
				return mcp.NewToolResultError("nothing to update: provide priority and/or add_label_ids/remove_label_ids"), nil
			}
			return mcp.NewToolResultText(strings.Join(out, "\n")), nil
		},
	)

	// batch_delete_issues — hard-delete N issues. Unauthorized ids are
	// skipped with reason "forbidden".
	s.AddTool(
		mcp.NewTool("batch_delete_issues",
			mcp.WithDescription("Batch-delete issues (hard delete, irreversible)."),
			mcp.WithArray("issue_ids", mcp.Description("Issue IDs to delete"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			if args["issue_ids"] == nil {
				return mcp.NewToolResultError("issue_ids is required"), nil
			}
			body := map[string]any{"issue_ids": args["issue_ids"]}
			data, err := api.post("/mcp/issues/batch/delete", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// list_external_sources — returns the project's configured external
	// tracker bindings (GitHub repo / TAPD workspace / Jira project). Pair
	// with call_external_api: read the binding here to learn which provider
	// + source_key applies to this project, then issue the actual API call
	// through the proxy. Each row carries credential_id so the proxy can
	// pick the right token at call time.
	s.AddTool(
		mcp.NewTool("list_external_sources",
			mcp.WithDescription("List external tracker bindings for the current project (GitHub repo / TAPD workspace / Jira project). Use this to discover which provider + source_key to pass to call_external_api."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get("/mcp/projects/" + pid + "/external-sources")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

// --- Issue write tools ---

// mcpIssue is the subset of the IssueResponse JSON (GET /mcp/issues/:id) that
// update_issue needs to read back so it can merge partial updates over the
// current values. The server's UpdateIssue overwrites *every* scalar field
// from the request body (omitted fields become zero values), so a partial
// update has to re-send the unchanged fields verbatim.
type mcpIssue struct {
	ID            int64   `json:"id"`
	Title         string  `json:"title"`
	Description   *string `json:"description"`
	Priority      int64   `json:"priority"`
	StartDate     string  `json:"start_date"`
	DueDate       string  `json:"due_date"`
	EstimateType  string  `json:"estimate_type"`
	Estimate      float64 `json:"estimate"`
	ActualTime    float64 `json:"actual_time"`
	GoalCondition string  `json:"goal_condition"`
	// Executable Epic fields — read back so a partial exec-fields update can
	// merge over the current values (the exec-fields endpoint overwrites all 4).
	ParentIssueID *int64 `json:"parent_issue_id"`
	IssueType     string `json:"issue_type"`
	ExecWave      int64  `json:"exec_wave"`
	ExecStatus    string `json:"exec_status"`
}

type mcpChecklistItem struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	IsCompleted int64  `json:"is_completed"`
}

func (c *apiClient) getIssue(issueID string) (mcpIssue, error) {
	data, err := c.get("/mcp/issues/" + issueID)
	if err != nil {
		return mcpIssue{}, err
	}
	var iss mcpIssue
	if err := json.Unmarshal(data, &iss); err != nil {
		return mcpIssue{}, fmt.Errorf("decode issue: %w", err)
	}
	return iss, nil
}

// getChecklistItem reads back a single checklist row by id so a partial edit
// (rename or toggle) can preserve the fields the caller didn't touch — the
// server's checklist Update overwrites both title and is_completed at once.
func (c *apiClient) getChecklistItem(issueID string, itemID int64) (mcpChecklistItem, bool, error) {
	listData, err := c.get("/mcp/issues/" + issueID + "/checklists")
	if err != nil {
		return mcpChecklistItem{}, false, err
	}
	var items []mcpChecklistItem
	if err := json.Unmarshal(listData, &items); err != nil {
		return mcpChecklistItem{}, false, fmt.Errorf("decode checklist: %w", err)
	}
	for _, it := range items {
		if it.ID == itemID {
			return it, true, nil
		}
	}
	return mcpChecklistItem{}, false, nil
}

// registerIssueWriteTools registers the issue mutation tools (delete_issue,
// update_issue, add_checklist_item, update_checklist_item, delete_checklist_item).
// update_checklist_item both renames and checks/unchecks an item (its `completed`
// flag supersedes the former toggle_checklist_item tool). Each tool tunnels
// to /mcp/issues/* (or /mcp/checklists/*) on the niuniu server, which enforces
// the same CanAccessIssue ownership gate the UI uses — a cross-tenant issue_id
// comes back as 403/404.
func registerIssueWriteTools(s *server.MCPServer, api *apiClient) {
	s.AddTool(
		mcp.NewTool("delete_issue",
			mcp.WithDescription("Delete an issue. Destructive: the title is read first for the receipt. Cross-tenant/no-access issues return 403/404."),
			mcp.WithNumber("issue_id", mcp.Description("Issue ID to delete"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueIDF, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			issueID := strconv.FormatInt(int64(issueIDF), 10)
			// Read the issue first so the response can echo what was removed and
			// so an access error (403/404) surfaces before any destructive call.
			iss, err := api.getIssue(issueID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if err := api.doDelete("/mcp/issues/" + issueID); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out, _ := json.Marshal(map[string]any{
				"deleted":  true,
				"issue_id": iss.ID,
				"title":    iss.Title,
			})
			return mcp.NewToolResultText(string(out)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("update_issue",
			mcp.WithDescription("Update an issue. All fields optional — pass only what changes; omitted fields keep their value. Can update title/description/priority/goal_condition, the task planning fields (start_date/due_date/estimate_type/estimate/actual_time), replace labels, move column, and set lifecycle status in one call. No-access returns 403/404."),
			mcp.WithNumber("issue_id", mcp.Description("Issue ID to update"), mcp.Required()),
			mcp.WithString("title", mcp.Description("New title")),
			mcp.WithString("description", mcp.Description("New description")),
			mcp.WithNumber("priority", mcp.Description("Priority number: 0=low 1=medium 2=high 3=urgent (must be a number, not a string)")),
			mcp.WithString("goal_condition", mcp.Description("Completion criterion for autohost LLM review (max 4000 chars); empty string clears it")),
			mcp.WithString("start_date", mcp.Description("Task start date, format YYYY-MM-DD (e.g. 2026-07-01); empty string clears it")),
			mcp.WithString("due_date", mcp.Description("Task due date, format YYYY-MM-DD (e.g. 2026-07-31); empty string clears it")),
			mcp.WithString("estimate_type", mcp.Description("Estimate unit: 'points' (story points) | 'hours'")),
			mcp.WithNumber("estimate", mcp.Description("Estimated workload (story points or hours, per estimate_type); 0 clears it")),
			mcp.WithNumber("actual_time", mcp.Description("Actual time spent, in hours; 0 clears it")),
			mcp.WithArray("label_ids", mcp.Description("Replace all labels with this array of label IDs (numbers). Empty array clears all labels. Label IDs come from get_issue_detail / list_issues.")),
			mcp.WithNumber("column_id", mcp.Description("Move the issue to this column ID (must be in the same project)")),
			mcp.WithString("lifecycle_status", mcp.Description("Set lifecycle status, e.g. created/spec/plan/implement/test")),
			mcp.WithNumber("parent_issue_id", mcp.Description("Executable Epic: attach under a parent Epic (same project). 0 detaches the parent.")),
			mcp.WithString("issue_type", mcp.Description("Executable Epic: 'task' (default) | 'epic'")),
			mcp.WithNumber("exec_wave", mcp.Description("Executable Epic: child execution wave (same wave parallel, waves serial)")),
			mcp.WithString("exec_status", mcp.Description("Executable Epic: exec state idle/running/done/failed/paused (usually engine-managed)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueIDF, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			issueID := strconv.FormatInt(int64(issueIDF), 10)

			_, hasTitle := args["title"]
			_, hasDesc := args["description"]
			_, hasPriority := args["priority"]
			_, hasGoal := args["goal_condition"]
			_, hasStartDate := args["start_date"]
			_, hasDueDate := args["due_date"]
			_, hasEstimateType := args["estimate_type"]
			_, hasEstimate := args["estimate"]
			_, hasActualTime := args["actual_time"]
			_, hasLabels := args["label_ids"]
			_, hasColumn := args["column_id"]
			_, hasLifecycle := args["lifecycle_status"]
			_, hasParent := args["parent_issue_id"]
			_, hasIssueType := args["issue_type"]
			_, hasExecWave := args["exec_wave"]
			_, hasExecStatus := args["exec_status"]
			hasExec := hasParent || hasIssueType || hasExecWave || hasExecStatus
			// The task planning fields (dates + estimate) share the same full-overwrite
			// PUT /mcp/issues/:id route as title/description/priority/goal_condition.
			hasScalar := hasTitle || hasDesc || hasPriority || hasGoal ||
				hasStartDate || hasDueDate || hasEstimateType || hasEstimate || hasActualTime

			if !hasScalar && !hasLabels && !hasColumn && !hasLifecycle && !hasExec {
				return mcp.NewToolResultError("at least one updatable field is required (title/description/priority/goal_condition/start_date/due_date/estimate_type/estimate/actual_time/label_ids/column_id/lifecycle_status/parent_issue_id/issue_type/exec_wave/exec_status)"), nil
			}

			var applied []string

			// update_issue fans out to several PUT routes (no server-side
			// transaction). If a later step fails, earlier steps have already
			// committed — surface what landed so the agent isn't blind to the
			// partial state and can retry only the remainder.
			failPartial := func(err error) (*mcp.CallToolResult, error) {
				msg := err.Error()
				if len(applied) > 0 {
					msg = fmt.Sprintf("%s (partial update: already applied %s before this failure)", msg, strings.Join(applied, ", "))
				}
				return mcp.NewToolResultError(msg), nil
			}

			// Scalar fields go through PUT /mcp/issues/:id, which is a full
			// overwrite — fetch current values first and merge the provided ones.
			if hasScalar {
				cur, err := api.getIssue(issueID)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				body := map[string]any{
					"title":         cur.Title,
					"start_date":    cur.StartDate,
					"due_date":      cur.DueDate,
					"estimate_type": cur.EstimateType,
					"estimate":      cur.Estimate,
					"actual_time":   cur.ActualTime,
				}
				if cur.Description != nil {
					body["description"] = *cur.Description
				} else {
					body["description"] = ""
				}
				body["priority"] = cur.Priority
				// goal_condition is a pointer on the server: send current so an
				// untouched value stays put, override below if the caller set it.
				body["goal_condition"] = cur.GoalCondition

				if hasTitle {
					if v, ok := args["title"].(string); ok {
						body["title"] = v
					}
				}
				if hasDesc {
					if v, ok := args["description"].(string); ok {
						body["description"] = v
					}
				}
				if hasPriority {
					if v, ok := args["priority"].(float64); ok {
						body["priority"] = int64(v)
					}
				}
				if hasGoal {
					if v, ok := args["goal_condition"].(string); ok {
						body["goal_condition"] = v
					}
				}
				if hasStartDate {
					if v, ok := args["start_date"].(string); ok {
						body["start_date"] = v
					}
				}
				if hasDueDate {
					if v, ok := args["due_date"].(string); ok {
						body["due_date"] = v
					}
				}
				if hasEstimateType {
					if v, ok := args["estimate_type"].(string); ok {
						body["estimate_type"] = v
					}
				}
				if hasEstimate {
					if v, ok := args["estimate"].(float64); ok {
						body["estimate"] = v
					}
				}
				if hasActualTime {
					if v, ok := args["actual_time"].(float64); ok {
						body["actual_time"] = v
					}
				}
				if _, err := api.put("/mcp/issues/"+issueID, body); err != nil {
					return failPartial(err)
				}
				applied = append(applied, "fields")
			}

			if hasLabels {
				labelIDs := []int64{}
				if raw, ok := args["label_ids"].([]any); ok {
					for _, v := range raw {
						if f, ok := v.(float64); ok {
							labelIDs = append(labelIDs, int64(f))
						}
					}
				}
				if _, err := api.put("/mcp/issues/"+issueID+"/labels", map[string]any{"label_ids": labelIDs}); err != nil {
					return failPartial(err)
				}
				applied = append(applied, "labels")
			}

			if hasColumn {
				if v, ok := args["column_id"].(float64); ok {
					if _, err := api.put("/mcp/issues/"+issueID+"/move", map[string]any{
						"column_id": int64(v),
						"position":  0,
					}); err != nil {
						return failPartial(err)
					}
					applied = append(applied, "column")
				}
			}

			if hasLifecycle {
				if v, ok := args["lifecycle_status"].(string); ok && v != "" {
					if _, err := api.put("/mcp/issues/"+issueID+"/lifecycle", map[string]any{"lifecycleStatus": v}); err != nil {
						return failPartial(err)
					}
					applied = append(applied, "lifecycle")
				}
			}

			// Executable Epic fields go through PUT /mcp/issues/:id/exec-fields,
			// which overwrites all four — fetch current values and merge the
			// provided ones. parent_issue_id == 0 clears the parent.
			if hasExec {
				cur, err := api.getIssue(issueID)
				if err != nil {
					return failPartial(err)
				}
				body := map[string]any{
					"issue_type":  cur.IssueType,
					"exec_wave":   cur.ExecWave,
					"exec_status": cur.ExecStatus,
				}
				if cur.ParentIssueID != nil {
					body["parent_issue_id"] = *cur.ParentIssueID
				} else {
					body["parent_issue_id"] = nil
				}
				if hasParent {
					if v, ok := args["parent_issue_id"].(float64); ok {
						if int64(v) == 0 {
							body["parent_issue_id"] = nil
						} else {
							body["parent_issue_id"] = int64(v)
						}
					}
				}
				if hasIssueType {
					if v, ok := args["issue_type"].(string); ok {
						body["issue_type"] = v
					}
				}
				if hasExecWave {
					if v, ok := args["exec_wave"].(float64); ok {
						body["exec_wave"] = int64(v)
					}
				}
				if hasExecStatus {
					if v, ok := args["exec_status"].(string); ok {
						body["exec_status"] = v
					}
				}
				if _, err := api.put("/mcp/issues/"+issueID+"/exec-fields", body); err != nil {
					return failPartial(err)
				}
				applied = append(applied, "exec_fields")
			}

			// Return the fresh issue state so the agent sees the merged result.
			data, err := api.get("/mcp/issues/" + issueID)
			if err != nil {
				// The writes succeeded; only the read-back failed.
				return mcp.NewToolResultText(fmt.Sprintf(`{"updated":true,"applied":["%s"]}`, strings.Join(applied, `","`))), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// start_workspace — Executable Epic mode B: an Epic orchestration agent calls
	// this to dispatch a child issue's workspace on its own schedule (instead of
	// the mode-A backend wave loop). Creates a workspace bound 1:1 to the issue
	// (owner + repos resolved from the issue's project) and returns its id.
	// Cross-tenant issue_id returns 403/404 via the same CanAccessProject gate.
	s.AddTool(
		mcp.NewTool("start_workspace",
			mcp.WithDescription("Create and start a dedicated workspace (1:1) for an issue; returns workspace_id. "+
				"Used in executable-Epic mode B where an orchestration agent dispatches child workspaces. No-access returns 403/404. "+
				"Cost guardrails: at the concurrency cap it returns queued=true (queued, auto-starts when a slot frees — do not retry); "+
				"near the orchestration tree's budget it returns warn (confirm with the user via niuniu_ask_user_question before fanning out further); "+
				"over budget the call errors (stop fan-out and ask the user)."),
			mcp.WithNumber("issue_id", mcp.Description("Issue ID to start a workspace for"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueIDF, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			issueID := strconv.FormatInt(int64(issueIDF), 10)
			data, err := api.post("/mcp/issues/"+issueID+"/start-workspace", map[string]any{})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// advance_issue — AI-native board: the orchestration/worker agent self-reports
	// progress by moving its issue's card to another column (skip / back allowed).
	// When the destination column's primitive is `instruct`, the server ensures the
	// issue's workspace and sends that column's instruction to the agent. This is a
	// DISTINCT tool from update_issue (which also moves columns) so the move boundary
	// can carry the instruct / gate side effects.
	s.AddTool(
		mcp.NewTool("advance_issue",
			mcp.WithDescription("Move an issue's card to another board column (self-report progress); may skip or go back (e.g. review bounces it to implement). "+
				"If the target is an instruct column, the server ensures the issue's workspace exists and sends that column's instruction to the agent. "+
				"For the AI-native board: read the issue + column menu and decide the path yourself. No-access returns 403/404. "+
					"If the response has blocked=true (blocked_reason explains, e.g. a livelock cap from repeated bounces), the issue is "+
					"blocked-needs-human: stop routing and wait for a human."),
			mcp.WithNumber("issue_id", mcp.Description("Issue ID to move"), mcp.Required()),
			mcp.WithString("to_column", mcp.Description("Target column: column ID (numeric string) or name (case-insensitive, within the issue's project)"), mcp.Required()),
			mcp.WithString("reason", mcp.Description("Reason for the move (optional, logged)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueIDF, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			toColumn, errRes := requireString(args, "to_column")
			if errRes != nil {
				return errRes, nil
			}
			body := map[string]any{"to_column": toColumn}
			if reason, ok := args["reason"].(string); ok {
				body["reason"] = reason
			}
			issueID := strconv.FormatInt(int64(issueIDF), 10)
			data, err := api.post("/mcp/issues/"+issueID+"/advance", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// abandon_issue — AI-native board: the agent declares it cannot / should not do
	// this issue and parks it back in the backlog with a reason (spec §19,
	// abandoned-with-reason terminal state). Distinct from advance_issue so the move
	// carries the terminal-state + attention side effects.
	s.AddTool(
		mcp.NewTool("abandon_issue",
			mcp.WithDescription("Abandon an issue: call when you judge it can't or shouldn't be done by you. "+
				"The card returns to the backlog, the issue is marked abandoned with the reason, and the workspace switches to needs-human. "+
				"Do not keep routing the issue afterward. No-access returns 403/404."),
			mcp.WithNumber("issue_id", mcp.Description("Issue ID to abandon"), mcp.Required()),
			mcp.WithString("reason", mcp.Description("Reason for abandoning (required, e.g. needs product decision / lacks permission / out of scope for this issue)"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueIDF, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			reason, errRes := requireString(args, "reason")
			if errRes != nil {
				return errRes, nil
			}
			issueID := strconv.FormatInt(int64(issueIDF), 10)
			data, err := api.post("/mcp/issues/"+issueID+"/abandon", map[string]any{"reason": reason})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// request_changes — AI-native board Review 闭环 (#623): the reviewer (a review-column
	// agent, or a human via REST) marks the issue "需修改" and leaves issue-level feedback.
	// The server records the comment, bounces the card back to the implement lane, and
	// injects the TWO-layer review context — kanban issue comments (macro: why it did not
	// pass / pass criteria) + unresolved workspace diff comments (micro: line-level) — into
	// the agent's CONTINUATION (existing worktree, changes intact — not a restart). The
	// worker then reworks and self-advances back to 审查 with a per-comment change summary.
	s.AddTool(
		mcp.NewTool("request_changes",
			mcp.WithDescription("Review bounce: mark an issue 需修改 and send it back to the implement lane for rework. "+
				"Leave issue-level feedback in `comment` (why it did not pass / what the pass criteria are / 验收 gap). "+
				"The server records the comment and injects BOTH the kanban issue comments AND the unresolved line-level diff "+
				"comments into the worker agent, which continues (does NOT restart) on top of its existing changes and then "+
				"advances back to the review column with a change summary. Use this instead of a bare advance_issue when a "+
				"review does not pass. No-access returns 403/404."),
			mcp.WithNumber("issue_id", mcp.Description("Issue ID to bounce back for changes"), mcp.Required()),
			mcp.WithString("comment", mcp.Description("Issue-level review feedback: why it did not pass, the pass criteria, and the acceptance gap (recommended)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueIDF, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			body := map[string]any{}
			if comment, ok := args["comment"].(string); ok {
				body["comment"] = comment
			}
			issueID := strconv.FormatInt(int64(issueIDF), 10)
			data, err := api.post("/mcp/issues/"+issueID+"/request-changes", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("add_checklist_item",
			mcp.WithDescription("Add a checklist item to an issue. No-access returns 403/404."),
			mcp.WithNumber("issue_id", mcp.Description("Issue ID"), mcp.Required()),
			mcp.WithString("title", mcp.Description("Item text"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueIDF, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			title, errRes := requireString(args, "title")
			if errRes != nil {
				return errRes, nil
			}
			issueID := strconv.FormatInt(int64(issueIDF), 10)
			data, err := api.post("/mcp/issues/"+issueID+"/checklists", map[string]any{"title": title})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("update_checklist_item",
			mcp.WithDescription("Edit a checklist item's text and/or completed flag. Pass only what changes; the omitted field keeps its current value (the server overwrites both at once, so the current value is read back and merged). At least one of title/completed is required. No-access returns 403/404."),
			mcp.WithNumber("issue_id", mcp.Description("ID of the issue the item belongs to"), mcp.Required()),
			mcp.WithNumber("checklist_item_id", mcp.Description("Checklist item ID (from get_issue_detail)"), mcp.Required()),
			mcp.WithString("title", mcp.Description("New item text (rename)")),
			mcp.WithBoolean("completed", mcp.Description("true=completed, false=not completed")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueIDF, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			itemIDF, errRes := requireNumber(args, "checklist_item_id")
			if errRes != nil {
				return errRes, nil
			}
			newTitle, hasTitle := args["title"].(string)
			newCompleted, hasCompleted := args["completed"].(bool)
			if !hasTitle && !hasCompleted {
				return mcp.NewToolResultError("at least one of title/completed is required"), nil
			}
			issueID := strconv.FormatInt(int64(issueIDF), 10)
			itemID := int64(itemIDF)

			cur, found, err := api.getChecklistItem(issueID, itemID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if !found {
				return mcp.NewToolResultError(fmt.Sprintf("checklist item %d not found on issue %s", itemID, issueID)), nil
			}
			title := cur.Title
			if hasTitle {
				title = newTitle
			}
			isCompleted := cur.IsCompleted
			if hasCompleted {
				if newCompleted {
					isCompleted = 1
				} else {
					isCompleted = 0
				}
			}
			data, err := api.put("/mcp/checklists/"+strconv.FormatInt(itemID, 10), map[string]any{
				"title":        title,
				"is_completed": isCompleted,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("delete_checklist_item",
			mcp.WithDescription("Delete a checklist item from an issue. No-access returns 403/404."),
			mcp.WithNumber("issue_id", mcp.Description("ID of the issue the item belongs to"), mcp.Required()),
			mcp.WithNumber("checklist_item_id", mcp.Description("Checklist item ID to delete (from get_issue_detail)"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueIDF, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			itemIDF, errRes := requireNumber(args, "checklist_item_id")
			if errRes != nil {
				return errRes, nil
			}
			issueID := strconv.FormatInt(int64(issueIDF), 10)
			itemID := int64(itemIDF)

			// Read the item first so the response can echo what was removed and so
			// an access error (403/404) or missing id surfaces before the delete.
			cur, found, err := api.getChecklistItem(issueID, itemID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if !found {
				return mcp.NewToolResultError(fmt.Sprintf("checklist item %d not found on issue %s", itemID, issueID)), nil
			}
			if err := api.doDelete("/mcp/checklists/" + strconv.FormatInt(itemID, 10)); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out, _ := json.Marshal(map[string]any{
				"deleted":           true,
				"checklist_item_id": itemID,
				"issue_id":          issueID,
				"title":             cur.Title,
			})
			return mcp.NewToolResultText(string(out)), nil
		},
	)
}

// --- Blackboard tools ---

func registerBlackboardTools(s *server.MCPServer, api *apiClient, wsid, agentName string) {
	s.AddTool(
		mcp.NewTool("blackboard_read",
			mcp.WithDescription("Read an entry from the shared blackboard by key"),
			mcp.WithString("key", mcp.Description("Entry key"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			key, errRes := requireString(args, "key")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.get("/mcp/workspaces/" + wsid + "/team/blackboard?key=" + url.QueryEscape(key))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if len(data) == 0 || string(data) == "null" {
				return mcp.NewToolResultText("not found"), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("blackboard_write",
			mcp.WithDescription("Write an entry to the shared blackboard"),
			mcp.WithString("key", mcp.Description("Entry key"), mcp.Required()),
			mcp.WithString("type", mcp.Description("Entry type: plan, code, review, result, error, status"), mcp.Required()),
			mcp.WithString("content", mcp.Description("Entry content"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			key, errRes := requireString(args, "key")
			if errRes != nil {
				return errRes, nil
			}
			typ, errRes := requireString(args, "type")
			if errRes != nil {
				return errRes, nil
			}
			content, errRes := requireString(args, "content")
			if errRes != nil {
				return errRes, nil
			}
			body := map[string]string{
				"key":      key,
				"type":     typ,
				"content":  content,
				"producer": agentName,
			}
			_, err := api.post("/mcp/workspaces/"+wsid+"/team/blackboard", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText("ok"), nil
		},
	)

	s.AddTool(
		mcp.NewTool("blackboard_list",
			mcp.WithDescription("List entries on the shared blackboard"),
			mcp.WithString("type_filter", mcp.Description("Filter by entry type")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path := "/mcp/workspaces/" + wsid + "/team/blackboard"
			if v, ok := req.GetArguments()["type_filter"].(string); ok && v != "" {
				path += "?type=" + url.QueryEscape(v)
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

// --- Inbox tools (HTTP-backed) ---

// inboxMessage mirrors service.InboxMessage on the server side, decoded from
// the JSON returned by POST /mcp/inbox/read.
type inboxMessage struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
	MessageID string `json:"messageId"`
	Read      bool   `json:"read"`
}

// postWithAgentHeader posts a JSON payload and sets X-Niuniu-Agent. The shared
// apiClient.post helper has no header hook, so inbox_send (which must prove
// sender identity to the server) uses this local variant.
func (c *apiClient) postWithAgentHeader(path string, payload any, agent string) ([]byte, error) {
	var buf bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&buf).Encode(payload); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest("POST", c.base+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if agent != "" {
		req.Header.Set("X-Niuniu-Agent", agent)
	}
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API %s returned %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func registerInboxTools(s *server.MCPServer, api *apiClient, workspaceID int64, agentName string) {
	s.AddTool(
		mcp.NewTool("inbox_send",
			mcp.WithDescription("Send a message to a team member's inbox"),
			mcp.WithString("to", mcp.Description("Recipient name"), mcp.Required()),
			mcp.WithString("text", mcp.Description("Message text"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			to, errRes := requireString(args, "to")
			if errRes != nil {
				return errRes, nil
			}
			text, errRes := requireString(args, "text")
			if errRes != nil {
				return errRes, nil
			}
			body := map[string]any{
				"workspace_id": workspaceID,
				"to":           to,
				"text":         text,
			}
			if _, err := api.postWithAgentHeader("/mcp/inbox/send", body, agentName); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText("sent"), nil
		},
	)

	s.AddTool(
		mcp.NewTool("inbox_read",
			mcp.WithDescription("Read unread messages from your inbox"),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			body := map[string]any{
				"workspace_id": workspaceID,
				"agent":        agentName,
			}
			data, err := api.post("/mcp/inbox/read", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var messages []inboxMessage
			if err := json.Unmarshal(data, &messages); err != nil || len(messages) == 0 {
				return mcp.NewToolResultText("no messages"), nil
			}
			var b strings.Builder
			for i, m := range messages {
				if i > 0 {
					b.WriteString("\n")
				}
				fmt.Fprintf(&b, "[%d] from %s at %s: %s", i+1, m.From, m.Timestamp, m.Text)
			}
			return mcp.NewToolResultText(b.String()), nil
		},
	)
}

// --- White-box memory tools ---

// registerMemoryTools wires the three memory tools that close the memory loop:
// generate (distill from session — pass source_path to record an extract from a
// deliverable/diff instead), search (retrieve), and consolidate (idle dedup/tidy,
// the "Dream" pass). All key off the workspace; the server derives owner +
// project from it.
func registerMemoryTools(s *server.MCPServer, api *apiClient, wsid string) {
	memTypeDesc := "Type: pattern, gotcha, decision, error_fix, note, or reference"

	s.AddTool(
		mcp.NewTool("memory_generate",
			mcp.WithDescription("Save a durable memory distilled from this session (decisions, conventions, insights worth remembering next time). "+
				"When the memory comes from a specific produced artifact or diff, also pass source_path so it is recorded with that traceability anchor."),
			mcp.WithString("title", mcp.Description("Short title"), mcp.Required()),
			mcp.WithString("content", mcp.Description("Detailed memory content"), mcp.Required()),
			mcp.WithString("mem_type", mcp.Description(memTypeDesc)),
			mcp.WithString("source_path", mcp.Description("Optional: file path / diff anchor the memory was extracted from. When set, the memory is recorded with this source for traceability.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			title, errRes := requireString(args, "title")
			if errRes != nil {
				return errRes, nil
			}
			content, errRes := requireString(args, "content")
			if errRes != nil {
				return errRes, nil
			}
			body := map[string]any{"title": title, "content": content}
			if v, ok := args["mem_type"].(string); ok && v != "" {
				body["mem_type"] = v
			}
			// With a source anchor this is an "extract" (traceable to a produced
			// artifact/diff); without one it's a session-distilled memory. The two
			// keep distinct server endpoints; route by source_path presence.
			endpoint := "/mcp/workspaces/" + wsid + "/memory/generate"
			if sp, ok := args["source_path"].(string); ok && sp != "" {
				body["source_path"] = sp
				endpoint = "/mcp/workspaces/" + wsid + "/memory/extract"
			}
			data, err := api.post(endpoint, body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("memory_search",
			mcp.WithDescription("Search durable memories by keyword (title/content), optionally filtered by type"),
			mcp.WithString("query", mcp.Description("Search keyword; empty returns all")),
			mcp.WithString("mem_type", mcp.Description(memTypeDesc)),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			path := "/mcp/workspaces/" + wsid + "/memory"
			q := url.Values{}
			if v, ok := args["query"].(string); ok && v != "" {
				q.Set("q", v)
			}
			if v, ok := args["mem_type"].(string); ok && v != "" {
				q.Set("type", v)
			}
			if enc := q.Encode(); enc != "" {
				path += "?" + enc
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("memory_consolidate",
			mcp.WithDescription("Idle tidy pass: de-duplicate and consolidate memories (keeps newest per title, soft-deletes redundant ones). Returns a summary."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.post("/mcp/workspaces/"+wsid+"/memory/consolidate", map[string]any{})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("memory_update",
			mcp.WithDescription("Update an existing memory by id (e.g. correct an outdated insight). Reversible: the prior version is snapshotted. Omitted fields keep their current value."),
			mcp.WithNumber("id", mcp.Description("Memory id (from memory_search)"), mcp.Required()),
			mcp.WithString("title", mcp.Description("New title (optional)")),
			mcp.WithString("content", mcp.Description("New content (optional)")),
			mcp.WithString("mem_type", mcp.Description(memTypeDesc)),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			idF, ok := args["id"].(float64)
			if !ok {
				return mcp.NewToolResultError("id is required"), nil
			}
			body := map[string]any{"id": int64(idF)}
			if v, ok := args["title"].(string); ok && v != "" {
				body["title"] = v
			}
			if v, ok := args["content"].(string); ok && v != "" {
				body["content"] = v
			}
			if v, ok := args["mem_type"].(string); ok && v != "" {
				body["mem_type"] = v
			}
			data, err := api.post("/mcp/workspaces/"+wsid+"/memory/update", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("memory_delete",
			mcp.WithDescription("Soft-delete (archive) a stale/outdated memory by id. Reversible: it is archived, not erased, and can be brought back with memory_restore."),
			mcp.WithNumber("id", mcp.Description("Memory id (from memory_search)"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			idF, ok := req.GetArguments()["id"].(float64)
			if !ok {
				return mcp.NewToolResultError("id is required"), nil
			}
			data, err := api.post("/mcp/workspaces/"+wsid+"/memory/delete", map[string]any{"id": int64(idF)})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("memory_restore",
			mcp.WithDescription("Restore a previously soft-deleted (archived) memory by id."),
			mcp.WithNumber("id", mcp.Description("Memory id"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			idF, ok := req.GetArguments()["id"].(float64)
			if !ok {
				return mcp.NewToolResultError("id is required"), nil
			}
			data, err := api.post("/mcp/workspaces/"+wsid+"/memory/restore", map[string]any{"id": int64(idF)})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

// --- Harness gate tools (issue-bound / workspace-scoped) ---

func registerHarnessTools(s *server.MCPServer, api *apiClient, wsid string) {
	s.AddTool(
		mcp.NewTool("gate_run",
			mcp.WithDescription("Execute gate checks for the current workspace and return the verdict"),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.post("/mcp/workspaces/"+wsid+"/harness/gate-check", nil)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// gate_results intentionally does NOT filter by run ID — returns latest workspace checks
	s.AddTool(
		mcp.NewTool("gate_results",
			mcp.WithDescription("Get the latest gate check results"),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get("/mcp/workspaces/" + wsid + "/harness/checks")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

// --- Autohost 安全网: hidden-ref checkpoint tools (workspace-scoped) ---

// registerCheckpointTools exposes the hidden-ref checkpoint system to the agent:
// view the step-by-step timeline, inspect a step's diff, take a manual snapshot,
// and one-click revert the worktree to a checkpoint (without losing later work —
// the refs survive). All are thin forwarders to /mcp/workspaces/<wsid>/checkpoints*.
func registerCheckpointTools(s *server.MCPServer, api *apiClient, wsid string) {
	s.AddTool(
		mcp.NewTool("checkpoint_timeline",
			mcp.WithDescription("List this workspace's autohost checkpoints (refs/niuniu/<ws>/<issue>/<step>): a step-by-step snapshot timeline with kind (advance/gate_pass/autohost_final/manual), gate status, label and time. Use it to find a step to diff or revert to."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get("/mcp/workspaces/" + wsid + "/checkpoints")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("checkpoint_diff",
			mcp.WithDescription("Show the file-level diff a single checkpoint introduced (its snapshot vs the previous one). Pass a checkpoint_id from checkpoint_timeline."),
			mcp.WithNumber("checkpoint_id", mcp.Required(), mcp.Description("The checkpoint row id (from a repo entry in checkpoint_timeline).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cid, errRes := requireNumber(req.GetArguments(), "checkpoint_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.get("/mcp/workspaces/" + wsid + "/checkpoints/" + strconv.FormatInt(int64(cid), 10) + "/diff")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("checkpoint_create",
			mcp.WithDescription("Take a manual checkpoint (hidden-ref snapshot) of the current worktree state, so you can revert to it later. Optional label describes the moment."),
			mcp.WithString("label", mcp.Description("Short description of what this checkpoint captures (optional).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			label, _ := req.GetArguments()["label"].(string)
			data, err := api.post("/mcp/workspaces/"+wsid+"/checkpoints", map[string]any{"label": label})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("checkpoint_revert",
			mcp.WithDescription("Revert every repo worktree to a checkpoint step (from checkpoint_timeline). Restores files exactly to that snapshot WITHOUT deleting later checkpoints, so no work is lost — you can revert forward again. Use it to precisely roll back a bad multi-step change."),
			mcp.WithNumber("step", mcp.Required(), mcp.Description("The checkpoint step number to rewind the worktree(s) to.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			step, errRes := requireNumber(req.GetArguments(), "step")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.post("/mcp/workspaces/"+wsid+"/checkpoints/revert", map[string]any{"step": int(step)})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

// --- Permission prompt tool ---

// registerPermissionPromptTool registers the niuniu_permission_prompt MCP
// tool. Claude CLI invokes this (via --permission-prompt-tool) whenever it
// needs user approval for a tool call; we forward the request to the niuniu
// server, which surfaces a confirmation dialog in the chat UI and blocks
// until the user decides (default 2h). The shape of the response is the
// Anthropic-defined contract: {"behavior":"allow","updatedInput":{...}} or
// {"behavior":"deny","message":"..."}.
func registerPermissionPromptTool(s *server.MCPServer, api *apiClient) {
	s.AddTool(
		mcp.NewTool("niuniu_permission_prompt",
			mcp.WithDescription("Prompt the user (via niuniu chat UI) to approve or deny a tool invocation. Blocks until the user decides or until the workspace timeout (default 2h)."),
			mcp.WithString("tool_name",
				mcp.Required(),
				mcp.Description("Name of the tool whose invocation needs approval (e.g. 'Bash', 'Edit')."),
			),
			mcp.WithObject("tool_input",
				mcp.Required(),
				mcp.Description("Original tool input as JSON object — passed through to niuniu so the user sees what Claude wants to do."),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			toolName, errRes := requireString(args, "tool_name")
			if errRes != nil {
				return errRes, nil
			}
			toolInput, ok := args["tool_input"].(map[string]any)
			if !ok {
				return mcp.NewToolResultError("tool_input is required and must be an object"), nil
			}

			body, err := json.Marshal(map[string]any{
				"tool_name":  toolName,
				"tool_input": toolInput,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			httpReq, err := http.NewRequestWithContext(ctx, "POST", api.base+"/mcp/permission-prompt", bytes.NewReader(body))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			httpReq.Header.Set("Content-Type", "application/json")
			if api.authToken != "" {
				httpReq.Header.Set("Authorization", "Bearer "+api.authToken)
			}
			resp, err := permissionPromptClient.Do(httpReq)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer resp.Body.Close()
			respBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if resp.StatusCode != http.StatusOK {
				return mcp.NewToolResultError(fmt.Sprintf("server returned %d: %s", resp.StatusCode, string(respBytes))), nil
			}
			// Claude CLI parses the tool result text as JSON {behavior, ...}.
			return mcp.NewToolResultText(string(respBytes)), nil
		},
	)
}

// --- Ask-user-question tool ---

// registerAskUserTool registers niuniu_ask_user_question — the chat-routable
// substitute for Claude Code's built-in AskUserQuestion (which fails in
// niuniu's non-PTY sessions because there is no TTY for the multiple-choice
// UI). The agent posts a `questions` array; the niuniu server surfaces a
// card in chat and blocks until the user submits answers (default 2h).
//
// Response JSON: {"answered": true, "answers": [{"question": "...",
// "labels": ["..."], "notes": "..."}]} on success, or
// {"answered": false, "reason": "timeout"|"cancelled"} when the request
// hits a terminal non-answer state.
func registerAskUserTool(s *server.MCPServer, api *apiClient) {
	s.AddTool(
		mcp.NewTool("niuniu_ask_user_question",
			mcp.WithDescription("Ask the user one or more multiple-choice questions via the niuniu chat UI. Use this INSTEAD of AskUserQuestion — AskUserQuestion has no TTY in niuniu and will error out. Blocks until the user answers or the workspace timeout fires (default 2h)."),
			mcp.WithArray("questions",
				mcp.Required(),
				mcp.Description("Array of 1-4 question objects. Each: {question: string (the prompt), header: string (short legend ≤12 chars shown above the prompt — like a fieldset legend), multiSelect: bool, options: [{label: string (1-5 words, must be unique within the question; reserved label '__other__' is forbidden), description?: string (optional explanatory text), preview?: string (optional renderable preview — code snippet/mockup/comparison shown when the option is focused), recommended?: bool (mark the suggested default — UI highlights with a star)}]}. The UI always appends an implicit 'Other' choice with free-text input — do not add it yourself."),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			rawQuestions, ok := args["questions"].([]any)
			if !ok || len(rawQuestions) == 0 {
				return mcp.NewToolResultError("questions is required and must be a non-empty array"), nil
			}
			// Pass the agent-supplied JSON through verbatim. The niuniu server
			// owns schema validation (api/mcp_ask_user.go) — duplicating it
			// here drifts.
			body, err := json.Marshal(map[string]any{"questions": rawQuestions})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			httpReq, err := http.NewRequestWithContext(ctx, "POST", api.base+"/mcp/ask-user-question", bytes.NewReader(body))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			httpReq.Header.Set("Content-Type", "application/json")
			if api.authToken != "" {
				httpReq.Header.Set("Authorization", "Bearer "+api.authToken)
			}
			resp, err := askUserClient.Do(httpReq)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer resp.Body.Close()
			respBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if resp.StatusCode != http.StatusOK {
				return mcp.NewToolResultError(fmt.Sprintf("server returned %d: %s", resp.StatusCode, string(respBytes))), nil
			}
			return mcp.NewToolResultText(string(respBytes)), nil
		},
	)
}
