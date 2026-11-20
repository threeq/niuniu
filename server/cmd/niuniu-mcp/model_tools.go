// model_tools.go — registers the ten shared-model-library MCP tools (design
// §11.2). Every tool is a thin tunnel over the project's own HTTP API
// (/api/models, /api/model-usage/summary, /api/model-budget), so argument names
// and the upstream error code/body reach the conversation unchanged.
//
// The model library is owner-scoped rather than project-scoped, so these tools
// are registered for every workspace. The optional owner_type/owner_id pair
// mirrors the API's owner override: omit it for the caller's personal owner, or
// pass an org owner (team edition) when the caller has access.
//
// Tool surface:
//
//	model_list             — system + owner models (disabled hidden by default)
//	model_create           — add/overwrite an owner row (enabled defaults to true)
//	model_update           — patch display_name and/or enabled
//	model_delete           — delete an owner row (system rows answer 400)
//	model_refresh_preview  — list a provider's catalogue without importing
//	model_import           — merge candidate names; returns {added, skipped}
//	model_usage_summary    — aggregate usage over a window, optional group_by
//	model_budget_get       — owner hard cap, month usage and project soft caps
//	model_budget_set       — set the owner monthly token cap (0 = unlimited)
//	model_budget_set_project — set a project's advisory soft cap
package main

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerModelTools(s *server.MCPServer, api *apiClient, pid string) {
	s.AddTool(
		mcp.NewTool("model_list",
			mcp.WithDescription("List the shared model library: system built-ins merged with the owner's rows. "+
				"Disabled rows are hidden unless include_disabled is true. Omit owner_type/owner_id for the "+
				"caller's personal library; pass an org owner (team edition) to read that org's library."),
			mcp.WithBoolean("include_disabled", mcp.Description("Include disabled rows (management view). Default false.")),
			mcp.WithString("owner_type", mcp.Description("Optional owner override: user | org")),
			mcp.WithNumber("owner_id", mcp.Description("Owner ID paired with owner_type")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			q := modelOwnerQuery(args)
			if v, ok := args["include_disabled"].(bool); ok && v {
				q.Set("all", "1")
			}
			data, err := api.get(modelWithQuery("/api/models", q))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("model_create",
			mcp.WithDescription("Add a model to the owner's library. A duplicate name updates the existing owner "+
				"row (200) instead of erroring; a new row answers 201. enabled defaults to true."),
			mcp.WithString("name", mcp.Description("Model name as used by the provider, e.g. glm-5.1"), mcp.Required()),
			mcp.WithString("display_name", mcp.Description("Optional human-readable label")),
			mcp.WithBoolean("enabled", mcp.Description("Whether the model is selectable. Default true.")),
			mcp.WithString("owner_type", mcp.Description("Optional owner override: user | org")),
			mcp.WithNumber("owner_id", mcp.Description("Owner ID paired with owner_type")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			name, errRes := requireString(args, "name")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{"name": strings.TrimSpace(name)}
			if v, ok := args["display_name"].(string); ok && v != "" {
				payload["display_name"] = v
			}
			if v, ok := args["enabled"].(bool); ok {
				payload["enabled"] = v
			}
			if owner := modelOwnerBody(args); owner != nil {
				payload["owner"] = owner
			}
			data, err := api.post("/api/models", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("model_update",
			mcp.WithDescription("Patch an owner model row's display_name and/or enabled. System rows cannot be "+
				"modified; create an owner row with enabled=false to hide one instead."),
			mcp.WithNumber("id", mcp.Description("Owner model row ID"), mcp.Required()),
			mcp.WithString("display_name", mcp.Description("New display name")),
			mcp.WithBoolean("enabled", mcp.Description("Enable or disable the model")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			id, errRes := requirePositiveID(args, "id")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{}
			if v, ok := args["display_name"].(string); ok {
				payload["display_name"] = v
			}
			if v, ok := args["enabled"].(bool); ok {
				payload["enabled"] = v
			}
			if len(payload) == 0 {
				return mcp.NewToolResultError("at least one of display_name or enabled is required"), nil
			}
			data, err := api.patch("/api/models/"+strconv.FormatInt(id, 10), payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("model_delete",
			mcp.WithDescription("Delete an owner model row. System built-in rows answer 400 (hide them with an "+
				"owner row instead). Deleting a model in use by a provider does not change the saved provider value."),
			mcp.WithNumber("id", mcp.Description("Owner model row ID"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := requirePositiveID(req.GetArguments(), "id")
			if errRes != nil {
				return errRes, nil
			}
			if _, err := api.delete("/api/models/" + strconv.FormatInt(id, 10)); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(`{"deleted":true,"id":` + strconv.FormatInt(id, 10) + `}`), nil
		},
	)

	s.AddTool(
		mcp.NewTool("model_refresh_preview",
			mcp.WithDescription("Fetch a provider's model catalogue (GET /models) without importing it. Returns the "+
				"candidate array with already_in_library flags. 422 means the provider has no /models listing — ask "+
				"the user to enter model names manually; 502 carries the provider endpoint and failure reason."),
			mcp.WithNumber("provider_id", mcp.Description("Environment provider ID to refresh"), mcp.Required()),
			mcp.WithString("owner_type", mcp.Description("Optional owner override: user | org")),
			mcp.WithNumber("owner_id", mcp.Description("Owner ID paired with owner_type")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			providerID, errRes := requirePositiveID(args, "provider_id")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{"provider_id": providerID}
			if owner := modelOwnerBody(args); owner != nil {
				payload["owner"] = owner
			}
			data, err := api.post("/api/models/refresh-preview", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("model_import",
			mcp.WithDescription("Merge model names into the owner's library. Existing names are skipped. Returns "+
				"{added, skipped} counts."),
			mcp.WithArray("names", mcp.Description("Model names to import"),
				mcp.Items(map[string]any{"type": "string"}), mcp.Required()),
			mcp.WithString("owner_type", mcp.Description("Optional owner override: user | org")),
			mcp.WithNumber("owner_id", mcp.Description("Owner ID paired with owner_type")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			names, errRes := requireStringList(args, "names")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{"names": names}
			if owner := modelOwnerBody(args); owner != nil {
				payload["owner"] = owner
			}
			data, err := api.post("/api/models/import", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("model_usage_summary",
			mcp.WithDescription("Aggregate model usage for the owner over [from, to). from/to accept RFC3339 or "+
				"YYYY-MM-DD (UTC); the default window is the current UTC month through now. group_by adds one "+
				"breakdown dimension; omit it for totals only."),
			mcp.WithString("from", mcp.Description("Window start: RFC3339 or YYYY-MM-DD")),
			mcp.WithString("to", mcp.Description("Window end (exclusive for a date-only value): RFC3339 or YYYY-MM-DD")),
			mcp.WithString("group_by", mcp.Description("Breakdown dimension: model | provider | project | workspace | cli")),
			mcp.WithString("owner_type", mcp.Description("Optional owner override: user | org")),
			mcp.WithNumber("owner_id", mcp.Description("Owner ID paired with owner_type")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			q := modelOwnerQuery(args)
			for _, key := range []string{"from", "to", "group_by"} {
				if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
					q.Set(key, strings.TrimSpace(v))
				}
			}
			data, err := api.get(modelWithQuery("/api/model-usage/summary", q))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("model_budget_get",
			mcp.WithDescription("Read the owner's monthly token hard cap, this month's used tokens and every "+
				"project soft cap."),
			mcp.WithString("owner_type", mcp.Description("Optional owner override: user | org")),
			mcp.WithNumber("owner_id", mcp.Description("Owner ID paired with owner_type")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := api.get(modelWithQuery("/api/model-budget", modelOwnerQuery(req.GetArguments())))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("model_budget_set",
			mcp.WithDescription("Set the caller's monthly token hard cap. 0 means unlimited. A positive cap at or "+
				"below this month's usage answers 409 with used_tokens/monthly_tokens/period — raise the limit or "+
				"wait for the next period."),
			mcp.WithNumber("monthly_tokens", mcp.Description("Monthly token cap; 0 = unlimited"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			monthly, errRes := requireNonNegativeNumber(req.GetArguments(), "monthly_tokens")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.put("/api/model-budget", map[string]any{"monthly_tokens": monthly})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("model_budget_set_project",
			mcp.WithDescription("Set a project's advisory soft token cap. Soft caps only warn, so they may sit below "+
				"current usage and never answer 409. project_id defaults to the workspace's project."),
			mcp.WithNumber("project_id", mcp.Description("Project ID; defaults to this workspace's project")),
			mcp.WithNumber("monthly_tokens", mcp.Description("Monthly token soft cap; 0 = unlimited"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			projectID, errRes := modelProjectID(args, pid)
			if errRes != nil {
				return errRes, nil
			}
			monthly, errRes := requireNonNegativeNumber(args, "monthly_tokens")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.put("/api/model-budget/projects/"+strconv.FormatInt(projectID, 10),
				map[string]any{"monthly_tokens": monthly})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

// modelOwnerQuery maps owner_type/owner_id to the API's query parameters. An
// absent or empty owner_type means "caller's personal owner".
func modelOwnerQuery(args map[string]any) url.Values {
	q := url.Values{}
	ownerType, _ := args["owner_type"].(string)
	if ownerType = strings.TrimSpace(ownerType); ownerType == "" {
		return q
	}
	q.Set("owner_type", ownerType)
	if id, ok := args["owner_id"].(float64); ok {
		q.Set("owner_id", strconv.FormatInt(int64(id), 10))
	}
	return q
}

// modelOwnerBody maps owner_type/owner_id to the API's body owner object.
// Returns nil when no explicit owner was supplied.
func modelOwnerBody(args map[string]any) map[string]any {
	ownerType, _ := args["owner_type"].(string)
	if ownerType = strings.TrimSpace(ownerType); ownerType == "" {
		return nil
	}
	var id int64
	if v, ok := args["owner_id"].(float64); ok {
		id = int64(v)
	}
	return map[string]any{"type": ownerType, "id": id}
}

func modelWithQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// requirePositiveID extracts a required positive integer ID argument.
func requirePositiveID(args map[string]any, key string) (int64, *mcp.CallToolResult) {
	v, ok := args[key].(float64)
	if !ok || v <= 0 || v != float64(int64(v)) {
		return 0, mcp.NewToolResultError(key + " is required and must be a positive integer")
	}
	return int64(v), nil
}

// requireNonNegativeNumber extracts a required number >= 0 and returns it as an
// int64 token count (the budget endpoints take integers).
func requireNonNegativeNumber(args map[string]any, key string) (int64, *mcp.CallToolResult) {
	v, ok := args[key].(float64)
	if !ok || v < 0 || v != float64(int64(v)) {
		return 0, mcp.NewToolResultError(key + " is required and must be a non-negative integer")
	}
	return int64(v), nil
}

func requireStringList(args map[string]any, key string) ([]string, *mcp.CallToolResult) {
	raw, ok := args[key].([]any)
	if !ok || len(raw) == 0 {
		return nil, mcp.NewToolResultError(key + " is required and must be a non-empty array of strings")
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, mcp.NewToolResultError(key + " must contain only non-empty strings")
		}
		out = append(out, strings.TrimSpace(s))
	}
	return out, nil
}

// modelProjectID resolves the soft-cap target: an explicit project_id wins,
// otherwise the workspace's project (pid) is used.
func modelProjectID(args map[string]any, pid string) (int64, *mcp.CallToolResult) {
	if v, ok := args["project_id"].(float64); ok && v > 0 {
		if v != float64(int64(v)) {
			return 0, mcp.NewToolResultError("project_id must be a positive integer")
		}
		return int64(v), nil
	}
	if id, err := strconv.ParseInt(strings.TrimSpace(pid), 10, 64); err == nil && id > 0 {
		return id, nil
	}
	return 0, mcp.NewToolResultError("project_id is required (this workspace is not bound to a project)")
}
