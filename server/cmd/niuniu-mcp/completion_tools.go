// completion_tools.go — registers the five Completion (自动完成) MCP tools (design §6.9).
// Project-scoped tools capture pid at registration time; the issue tools address
// an issue by id and authorize through the issue's project on the server.
package main

import (
	"context"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerCompletionTools(s *server.MCPServer, api *apiClient, pid string) {
	s.AddTool(
		mcp.NewTool("completion_set_policy",
			mcp.WithDescription("Update this project's default completion (自动完成) policy. Partial: omitted fields "+
				"keep their current value. All five fields are shown; autohost_enabled=false disables unattended "+
				"continuation for issues that inherit the project default."),
			mcp.WithBoolean("autohost_enabled", mcp.Description("Keep the agent working until the goal condition is met")),
			mcp.WithNumber("max_rounds", mcp.Description("Maximum autohost continuation rounds")),
			mcp.WithNumber("token_budget", mcp.Description("Token budget per issue; 0 = unlimited")),
			mcp.WithBoolean("stop_on_decision", mcp.Description("Stop the turn when a design decision needs a human")),
			mcp.WithBoolean("require_review", mcp.Description("Send completed work to human review instead of done")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			payload := completionPolicyPayload(req.GetArguments())
			data, err := api.put("/mcp/projects/"+pid+"/completion/settings", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("completion_list_profiles",
			mcp.WithDescription("List this project's named completion profiles. Issues may reference a profile "+
				"(issue_set_completion_profile); resolution is issue profile > project default."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get("/mcp/projects/" + pid + "/completion/profiles")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("completion_upsert_profile",
			mcp.WithDescription("Create a named completion profile, or update one when profile_id is given. "+
				"Names are unique per project. Only the supplied policy fields change on update."),
			mcp.WithString("name", mcp.Description("Profile name, e.g. 标准 / 快速 / 深度 / 高风险需审查")),
			mcp.WithNumber("profile_id", mcp.Description("Existing profile ID to update; omit to create")),
			mcp.WithBoolean("autohost_enabled", mcp.Description("Keep the agent working until the goal condition is met")),
			mcp.WithNumber("max_rounds", mcp.Description("Maximum autohost continuation rounds")),
			mcp.WithNumber("token_budget", mcp.Description("Token budget per issue; 0 = unlimited")),
			mcp.WithBoolean("stop_on_decision", mcp.Description("Stop the turn when a design decision needs a human")),
			mcp.WithBoolean("require_review", mcp.Description("Send completed work to human review instead of done")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			payload := completionPolicyPayload(args)
			if v, ok := args["name"].(string); ok && v != "" {
				payload["name"] = v
			}
			if v, ok := args["profile_id"].(float64); ok && v > 0 {
				payload["profile_id"] = int64(v)
			}
			data, err := api.post("/mcp/projects/"+pid+"/completion/profiles", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("issue_set_completion_profile",
			mcp.WithDescription("Set or clear an issue's completion profile override. Omit profile_id (or pass 0) "+
				"to clear it and inherit the project default. The profile must belong to the issue's project."),
			mcp.WithNumber("issue_id", mcp.Description("Issue ID"), mcp.Required()),
			mcp.WithNumber("profile_id", mcp.Description("Profile ID from completion_list_profiles; omit or 0 to inherit the project default")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueID, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			var profileID *int64
			if v, ok := args["profile_id"].(float64); ok && v > 0 {
				id := int64(v)
				profileID = &id
			}
			payload := map[string]any{"profile_id": profileID}
			data, err := api.put("/mcp/issues/"+strconv.FormatInt(int64(issueID), 10)+"/completion-profile", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("issue_get_brief",
			mcp.WithDescription("Read an issue's Task Brief: the current version (goal, acceptance criteria, scope, "+
				"delivery, provenance) plus the full version history, newest first. brief is null for issues that "+
				"did not come from Intake."),
			mcp.WithNumber("issue_id", mcp.Description("Issue ID"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			issueID, errRes := requireNumber(args, "issue_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.get("/mcp/issues/" + strconv.FormatInt(int64(issueID), 10) + "/brief")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

// completionPolicyPayload copies only the policy fields the agent supplied, so
// an omitted field means "keep current" rather than "reset to zero".
func completionPolicyPayload(args map[string]any) map[string]any {
	payload := map[string]any{}
	for _, key := range []string{"autohost_enabled", "max_rounds", "token_budget", "stop_on_decision", "require_review"} {
		if v, ok := args[key]; ok && v != nil {
			payload[key] = v
		}
	}
	return payload
}
