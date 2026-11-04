// intake_tools.go — registers the five Intake (任务来源) MCP tools (design §6.9).
// All are project-scoped: the pid closure from main.go is captured at
// registration time, so no project_id argument is exposed to the agent.
//
// Tool surface:
//   intake_get_settings     — read per-source mode / daily cap / allow lists
//   intake_set_mode         — change one source's mode (off | observe | auto)
//   intake_list_pending     — dry-run decisions awaiting calibration
//   intake_accept_decision  — execute a pending decision (creates the task)
//   intake_ignore_decision  — reject a pending decision with a reason
package main

import (
	"context"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerIntakeTools(s *server.MCPServer, api *apiClient, pid string) {
	s.AddTool(
		mcp.NewTool("intake_get_settings",
			mcp.WithDescription("Read this project's intake (任务来源) settings: one row per source kind "+
				"(im_bot/github/ci/email/webhook) with mode (off|observe|auto), daily_cap and allow/bot lists. "+
				"Sources without a stored row are returned with defaults (observe, cap 4)."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get("/mcp/projects/" + pid + "/intake/settings")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("intake_set_mode",
			mcp.WithDescription("Change how much initiative the project takes for one intake source. "+
				"off = only explicit requests; observe = record dry-run decisions for human calibration; "+
				"auto = create tasks within the rules and daily cap."),
			mcp.WithString("source_kind", mcp.Description("Intake source: im_bot | github | ci | email | webhook"), mcp.Required()),
			mcp.WithString("mode", mcp.Description("off | observe | auto"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			sourceKind, errRes := requireString(args, "source_kind")
			if errRes != nil {
				return errRes, nil
			}
			mode, errRes := requireString(args, "mode")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.post("/mcp/projects/"+pid+"/intake/mode", map[string]any{
				"source_kind": sourceKind,
				"mode":        mode,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("intake_list_pending",
			mcp.WithDescription("List dry-run intake decisions awaiting calibration (executed=false, decision=accept). "+
				"Each item carries the source, actor, thread, strength, matched rules and the would-do payload "+
				"(suggested Task Brief) so it can be reviewed before accepting."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get("/mcp/projects/" + pid + "/intake/decisions/pending")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("intake_accept_decision",
			mcp.WithDescription("Accept a pending dry-run decision: executes it through the intake pipeline "+
				"(creates the issue/workspace from the suggested Task Brief). Returns 501 while the pipeline "+
				"is not wired — do not retry in a loop."),
			mcp.WithNumber("decision_id", mcp.Description("Decision ID from intake_list_pending"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			id, errRes := requireNumber(args, "decision_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.post("/mcp/decisions/"+strconv.FormatInt(int64(id), 10)+"/accept", nil)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("intake_ignore_decision",
			mcp.WithDescription("Reject a pending dry-run decision and record why. A reason is mandatory — it is "+
				"what makes the accept-rate metric meaningful."),
			mcp.WithNumber("decision_id", mcp.Description("Decision ID from intake_list_pending"), mcp.Required()),
			mcp.WithString("reason", mcp.Description("Why this decision should not become a task"), mcp.Required()),
			mcp.WithString("reason_code", mcp.Description("Optional machine-readable reason code (e.g. not_actionable, duplicate, out_of_scope)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			id, errRes := requireNumber(args, "decision_id")
			if errRes != nil {
				return errRes, nil
			}
			reason, errRes := requireString(args, "reason")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{"reason": reason}
			if code, ok := args["reason_code"].(string); ok && code != "" {
				payload["reason_code"] = code
			}
			data, err := api.post("/mcp/decisions/"+strconv.FormatInt(int64(id), 10)+"/ignore", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}
