package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerManagedTaskTool exposes create_managed_task — the one-call entry the
// conversational office assistant uses to stand up a recurring "managed task"
// from a natural-language request. The agent itself translates the user's
// wording into a cron expression and the per-run instruction, so the user never
// fills in a technical field. The server provisions the backing issue + no-repo
// workspace + bound cron schedule in one shot (POST /mcp/managed-tasks).
func registerManagedTaskTool(s *server.MCPServer, api *apiClient) {
	s.AddTool(
		mcp.NewTool("create_managed_task",
			mcp.WithDescription("Create a recurring managed task from a natural-language request, in one call. "+
				"Use this whenever the user wants something done automatically on a schedule (每天/每周/每月/到点 自动做某事), "+
				"e.g. 「每周一早上9点整理下载文件夹」「每月1号生成上月开支表」. You translate the timing into a standard "+
				"5-field cron expression yourself — never ask the user for cron/technical fields. The server provisions a "+
				"dedicated managed workspace + binds the cron schedule; at each tick the task runs to completion on its own "+
				"and fires an in-app + OS notification. Returns the created schedule_id / workspace_id. "+
				"The task is then visible (and pausable/deletable) on the 定时任务 (/schedules) page."),
			mcp.WithString("description", mcp.Description(
				"Imperative instruction run on every tick — what the task should DO each time "+
					"(e.g. \"整理下载文件夹中的文件并生成整理报告\"). Not the timing."), mcp.Required()),
			mcp.WithString("cron_expr", mcp.Description(
				"Standard 5-field cron you derived from the user's timing: \"minute hour day-of-month month day-of-week\". "+
					"Examples: every Monday 09:00 = \"0 9 * * 1\"; every day 08:00 = \"0 8 * * *\"; "+
					"1st of every month 09:00 = \"0 9 1 * *\"."), mcp.Required()),
			mcp.WithString("name", mcp.Description("Short human-readable task name (optional; derived from description when omitted).")),
			mcp.WithString("goal_condition", mcp.Description(
				"Completion criterion for a single run, used for self-judged completion (optional; defaults to description).")),
			mcp.WithBoolean("attach_to_current", mcp.Description(
				"Decide from the user's intent + the task you're currently working on: set TRUE to make the CURRENT "+
					"task recurring (the user wants what you're doing now to repeat on the schedule); set FALSE (default) "+
					"to create a NEW task for it (a separate piece of recurring work — it becomes a subtask of the current "+
					"conversation). When in doubt or when the recurring job is distinct from the current task, use false.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			description, errRes := requireString(args, "description")
			if errRes != nil {
				return errRes, nil
			}
			cronExpr, errRes := requireString(args, "cron_expr")
			if errRes != nil {
				return errRes, nil
			}
			body := map[string]any{
				"description": description,
				"cron_expr":   cronExpr,
			}
			if name, ok := args["name"].(string); ok && name != "" {
				body["name"] = name
			}
			if goal, ok := args["goal_condition"].(string); ok && goal != "" {
				body["goal_condition"] = goal
			}
			if attach, ok := args["attach_to_current"].(bool); ok && attach {
				body["attach_to_current"] = true
			}
			data, err := api.post("/mcp/managed-tasks", body)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}
