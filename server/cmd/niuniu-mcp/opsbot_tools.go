// opsbot_tools.go — registers the Ops Bot MCP tools (design 2026-11-06 §12).
//
// Every tool is a thin client over the /mcp/ops tunnel, which shares handlers
// with the /api/ops console: identical paths, authorization and response
// envelopes on both entry points. Ownership is resolved server-side from the
// MCP token's user; an explicit `owner` argument only narrows the view.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerOpsBotTools(s *server.MCPServer, api *apiClient) {
	registerOpsBotCRUD(api, s)
	registerOpsBotConfig(api, s)
	registerOpsPackTools(api, s)
	registerOpsApprovalTools(api, s)
	registerOpsActionTools(api, s)
}

func registerOpsBotCRUD(api *apiClient, s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("opsbot_list",
			mcp.WithDescription("List Ops Bots owned by the caller (directors first). `owner` optionally narrows "+
				"to user:<id> or org:<slug|id>; absent lists the caller's personal space."),
			mcp.WithString("owner", mcp.Description("Optional owner filter: user:<id> | org:<slug|id>")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path := "/mcp/ops/bots"
			if owner, _ := req.GetArguments()["owner"].(string); owner != "" {
				path += "?owner=" + url.QueryEscape(owner)
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("opsbot_get",
			mcp.WithDescription("Read one Ops Bot: configuration, lifecycle status and provisioning ids."),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.get("/mcp/ops/bots/" + id)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("opsbot_create",
			mcp.WithDescription("Create an Ops Bot. role=account requires pack_id + account_ref (one bot per "+
				"platform account; a second account on the same pack is allowed, the same account is 409). "+
				"role=director creates the brand-level director. project_id optionally provisions the control "+
				"Epic + workspace."),
			mcp.WithString("role", mcp.Description("account (default) | director")),
			mcp.WithString("pack_id", mcp.Description("Platform Pack id, e.g. wechat_mp / xiaohongshu")),
			mcp.WithNumber("pack_version", mcp.Description("Pin a pack version; 0 (default) follows the latest published")),
			mcp.WithString("account_ref", mcp.Description("Platform account identifier; unique per owner+pack")),
			mcp.WithString("display_name", mcp.Description("Human-readable name")),
			mcp.WithNumber("director_bot_id", mcp.Description("Optional director this account reports to")),
			mcp.WithNumber("project_id", mcp.Description("Optional project used to provision the control Epic + workspace")),
			mcp.WithObject("persona_json", mcp.Description("Persona configuration object")),
			mcp.WithObject("credential_refs_json", mcp.Description("Credential references {app, account} — never secrets")),
			mcp.WithObject("knowledge_scope_json", mcp.Description("Knowledge scope object")),
			mcp.WithObject("goals_json", mcp.Description("Goals object")),
			mcp.WithObject("cadence_json", mcp.Description("Cadence object (posts per week, hot-topic top-ups)")),
			mcp.WithObject("approval_policy_json", mcp.Description("Approval switches: {publish, reply, video} booleans")),
			mcp.WithObject("budget_json", mcp.Description("Budget object, e.g. {monthly_limit: 100, currency: \"CNY\"}")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			payload := opsCopyArgs(req.GetArguments(),
				"role", "pack_id", "pack_version", "account_ref", "display_name",
				"director_bot_id", "project_id", "persona_json", "credential_refs_json",
				"knowledge_scope_json", "goals_json", "cadence_json",
				"approval_policy_json", "budget_json")
			if _, ok := payload["role"]; !ok {
				payload["role"] = "account"
			}
			data, err := api.post("/mcp/ops/bots", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("opsbot_update",
			mcp.WithDescription("Patch an Ops Bot's configuration. Only the supplied fields change."),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
			mcp.WithString("display_name", mcp.Description("New display name")),
			mcp.WithNumber("pack_version", mcp.Description("Pin a new pack version")),
			mcp.WithObject("persona_json", mcp.Description("Persona configuration object")),
			mcp.WithObject("credential_refs_json", mcp.Description("Credential references — never secrets")),
			mcp.WithObject("knowledge_scope_json", mcp.Description("Knowledge scope object")),
			mcp.WithObject("goals_json", mcp.Description("Goals object")),
			mcp.WithObject("cadence_json", mcp.Description("Cadence object")),
			mcp.WithObject("approval_policy_json", mcp.Description("Approval switches")),
			mcp.WithObject("budget_json", mcp.Description("Budget object")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			payload := opsCopyArgs(req.GetArguments(),
				"display_name", "pack_version", "persona_json", "credential_refs_json",
				"knowledge_scope_json", "goals_json", "cadence_json",
				"approval_policy_json", "budget_json")
			data, err := api.patch("/mcp/ops/bots/"+id, payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	for _, sp := range []struct{ name, action, desc string }{
		{"opsbot_pause", "pause", "Pause a bot's automatic actions."},
		{"opsbot_resume", "resume", "Resume a paused bot."},
		{"opsbot_kill", "disable", "Disable a bot (kill switch): also cancels its pending approvals."},
	} {
		sp := sp
		s.AddTool(
			mcp.NewTool(sp.name,
				mcp.WithDescription(sp.desc),
				mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
			),
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				id, errRes := opsIDArg(req.GetArguments(), "bot_id")
				if errRes != nil {
					return errRes, nil
				}
				data, err := api.post("/mcp/ops/bots/"+id+"/"+sp.action, nil)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(string(data)), nil
			},
		)
	}
}

func registerOpsBotConfig(api *apiClient, s *server.MCPServer) {
	for _, f := range []struct{ name, field, desc string }{
		{"opsbot_set_goals", "goals_json", "Replace the bot's goals object."},
		{"opsbot_set_cadence", "cadence_json", "Replace the bot's cadence object."},
		{"opsbot_set_approval", "approval_policy_json", "Replace the bot's approval switches ({publish, reply, video})."},
	} {
		f := f
		s.AddTool(
			mcp.NewTool(f.name,
				mcp.WithDescription(f.desc),
				mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
				mcp.WithObject("value", mcp.Description("The new configuration object"), mcp.Required()),
			),
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				id, errRes := opsIDArg(req.GetArguments(), "bot_id")
				if errRes != nil {
					return errRes, nil
				}
				value, ok := req.GetArguments()["value"]
				if !ok {
					return mcp.NewToolResultError("value is required"), nil
				}
				data, err := api.patch("/mcp/ops/bots/"+id, map[string]any{f.field: value})
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(string(data)), nil
			},
		)
	}

	s.AddTool(
		mcp.NewTool("opsbot_set_budget",
			mcp.WithDescription("Set the account's monthly budget. A limit already exceeded by this month's "+
				"spend is rejected with 409 (the bot would be immediately over budget)."),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
			mcp.WithNumber("monthly_limit", mcp.Description("Monthly limit; 0 = unlimited"), mcp.Required()),
			mcp.WithString("currency", mcp.Description("ISO currency code; default USD")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			limit, errRes := requireNumber(req.GetArguments(), "monthly_limit")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{"monthly_limit": limit}
			if currency, ok := req.GetArguments()["currency"].(string); ok && currency != "" {
				payload["currency"] = currency
			}
			data, err := api.post("/mcp/ops/bots/"+id+"/budget", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("opsbot_request_credential_link",
			mcp.WithDescription("Mint a one-time secure link for the bot's platform credential. The user fills "+
				"the secret in on the standalone page; it never enters the conversation."),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
			mcp.WithNumber("project_id", mcp.Description("Project to bind the link to (defaults to the bot's control-Epic project)")),
			mcp.WithString("title", mcp.Description("Label shown on the credential page")),
			mcp.WithArray("fields", mcp.Description("Credential field names, e.g. [app_id, app_secret]")),
			mcp.WithString("auth_mode", mcp.Description("Optional auth mode for the target credential")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			payload := opsCopyArgs(req.GetArguments(), "project_id", "title", "fields", "auth_mode")
			data, err := api.post("/mcp/ops/bots/"+id+"/credential-link", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("opsbot_browser_status",
			mcp.WithDescription("Read the bot's browser session: login_status (valid|expired|unknown), whether the "+
				"Pack needs an interactive login, the last login error, and a machine-readable next_action."),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.get("/mcp/ops/bots/" + id + "/browser")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("opsbot_browser_profile",
			mcp.WithDescription("Register (or return) the bot's browser profile directory. This does NOT log in; "+
				"call opsbot_browser_login to start the interactive login."),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
			mcp.WithString("backend", mcp.Description("local (default) | cloud")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{}
			if backend, ok := req.GetArguments()["backend"].(string); ok && backend != "" {
				payload["backend"] = backend
			}
			data, err := api.post("/mcp/ops/bots/"+id+"/browser-login", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("opsbot_browser_login",
			mcp.WithDescription("Start the bot's interactive login now and return immediately with state=running "+
				"(the operator scans the QR code in the browser window). Poll opsbot_browser_status until "+
				"login_status=valid. Channels that authenticate with a credential return state=not_required."),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.post("/mcp/ops/bots/"+id+"/login", map[string]any{})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("opsbot_bind_chat",
			mcp.WithDescription("Route an IM chat to an Ops Bot. The bot must belong to the same owner as the chat's channel."),
			mcp.WithNumber("chat_id", mcp.Description("IM chat ID"), mcp.Required()),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID; 0 clears the binding"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			chatID, errRes := opsIDArg(req.GetArguments(), "chat_id")
			if errRes != nil {
				return errRes, nil
			}
			botID, errRes := requireNumber(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.patch("/mcp/ops/chats/"+chatID, map[string]any{"ops_bot_id": int64(botID)})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

func registerOpsPackTools(api *apiClient, s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("ops_pack_list",
			mcp.WithDescription("List Platform Packs available to the caller (built-in seeds are installed on first use)."),
			mcp.WithString("owner", mcp.Description("Optional owner filter: user:<id> | org:<slug|id>")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path := "/mcp/ops/packs"
			if owner, _ := req.GetArguments()["owner"].(string); owner != "" {
				path += "?owner=" + url.QueryEscape(owner)
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	versionTool := func(name, path, desc string) {
		s.AddTool(
			mcp.NewTool(name,
				mcp.WithDescription(desc),
				mcp.WithString("pack_id", mcp.Description("Pack id"), mcp.Required()),
				mcp.WithNumber("version", mcp.Description("Pack version"), mcp.Required()),
			),
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				packID, errRes := requireString(req.GetArguments(), "pack_id")
				if errRes != nil {
					return errRes, nil
				}
				version, errRes := requireNumber(req.GetArguments(), "version")
				if errRes != nil {
					return errRes, nil
				}
				data, err := api.post("/mcp/ops/packs/"+path, map[string]any{
					"pack_id": packID,
					"version": int64(version),
				})
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(string(data)), nil
			},
		)
	}
	versionTool("ops_pack_validate", "validate", "Validate a pack version (manifest + every bound file).")
	versionTool("ops_pack_test", "test", "Dry-run a pack: validate it and report the capability bindings that would be used.")
	versionTool("ops_pack_publish", "publish", "Publish a validated pack version so new bots can pin it (draft → published).")

	s.AddTool(
		mcp.NewTool("ops_pack_generate",
			mcp.WithDescription("Write a new draft pack version from an explicit manifest and file contents. "+
				"manifest: {id, version, display_name, capabilities, bindings, ...}; files: {relative_path: content}."),
			mcp.WithObject("manifest", mcp.Description("Pack manifest object"), mcp.Required()),
			mcp.WithObject("files", mcp.Description("Pack files keyed by relative path")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			manifest, ok := req.GetArguments()["manifest"]
			if !ok {
				return mcp.NewToolResultError("manifest is required"), nil
			}
			payload := map[string]any{"manifest": manifest}
			if files, ok := req.GetArguments()["files"]; ok {
				payload["files"] = files
			}
			data, err := api.post("/mcp/ops/packs", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("ops_pack_export",
			mcp.WithDescription("Export a pack version to a local zip file. Returns the written path."),
			mcp.WithString("pack_id", mcp.Description("Pack id"), mcp.Required()),
			mcp.WithNumber("version", mcp.Description("Pack version"), mcp.Required()),
			mcp.WithString("dest_path", mcp.Description("Local zip path to write"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			packID, errRes := requireString(req.GetArguments(), "pack_id")
			if errRes != nil {
				return errRes, nil
			}
			version, errRes := requireNumber(req.GetArguments(), "version")
			if errRes != nil {
				return errRes, nil
			}
			dest, errRes := requireString(req.GetArguments(), "dest_path")
			if errRes != nil {
				return errRes, nil
			}
			// The agent chooses the path; require it to be explicit and to name
			// a file (not a directory) so a typo can never truncate something
			// unexpected. The local agent already has file-write capability, so
			// this is a guardrail, not a sandbox.
			if !filepath.IsAbs(dest) {
				return mcp.NewToolResultError("dest_path must be an absolute path"), nil
			}
			if info, statErr := os.Stat(dest); statErr == nil && info.IsDir() {
				return mcp.NewToolResultError("dest_path must be a file, not a directory"), nil
			}
			data, err := api.get(fmt.Sprintf("/mcp/ops/packs/%s/%d/export", url.PathEscape(packID), int64(version)))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("write %s: %v", dest, err)), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf(`{"exported":true,"path":%q,"bytes":%d}`, dest, len(data))), nil
		},
	)

	s.AddTool(
		mcp.NewTool("ops_pack_import",
			mcp.WithDescription("Import a pack zip exported from another owner. The server reads the local path."),
			mcp.WithString("zip_path", mcp.Description("Local zip path to import"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			zipPath, errRes := requireString(req.GetArguments(), "zip_path")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.post("/mcp/ops/packs/import", map[string]any{"path": zipPath})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

func registerOpsApprovalTools(api *apiClient, s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("ops_approval_list",
			mcp.WithDescription("List the caller's pending Ops approvals (publish / reply / video cards)."),
			mcp.WithString("owner", mcp.Description("Optional owner filter: user:<id> | org:<slug|id>")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path := "/mcp/ops/approvals?status=pending"
			if owner, _ := req.GetArguments()["owner"].(string); owner != "" {
				path += "&owner=" + url.QueryEscape(owner)
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	for _, sp := range []struct{ name, action, desc string }{
		{"ops_approval_approve", "approve", "Approve a pending Ops approval card."},
		{"ops_approval_reject", "reject", "Reject a pending Ops approval card (a reason is recorded)."},
	} {
		sp := sp
		s.AddTool(
			mcp.NewTool(sp.name,
				mcp.WithDescription(sp.desc+" A second decision on the same card returns 409."),
				mcp.WithNumber("approval_id", mcp.Description("Approval ID from ops_approval_list"), mcp.Required()),
				mcp.WithString("reason", mcp.Description("Optional decision note")),
			),
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				id, errRes := opsIDArg(req.GetArguments(), "approval_id")
				if errRes != nil {
					return errRes, nil
				}
				payload := map[string]any{}
				if reason, ok := req.GetArguments()["reason"].(string); ok && reason != "" {
					payload["reason"] = reason
				}
				data, err := api.post("/mcp/ops/approvals/"+id+"/"+sp.action, payload)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(string(data)), nil
			},
		)
	}
}

func registerOpsActionTools(api *apiClient, s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("ops_publish",
			mcp.WithDescription("Record a publish intent for an issue under this bot. Idempotent on "+
				"idempotency_key: repeating the same key returns the existing record. The publish "+
				"orchestrator dispatches the pending record."),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
			mcp.WithNumber("issue_id", mcp.Description("Content issue ID"), mcp.Required()),
			mcp.WithString("idempotency_key", mcp.Description("Stable key for this publish attempt"), mcp.Required()),
			mcp.WithNumber("content_version", mcp.Description("Content version; default 1")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			payload := opsCopyArgs(req.GetArguments(), "issue_id", "idempotency_key", "content_version")
			data, err := api.post("/mcp/ops/bots/"+id+"/publish", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("ops_engagement_reply",
			mcp.WithDescription("Record an approved reply for a comment/DM and queue it for dispatch. "+
				"Dispatch (and the sent receipt) is performed by the engagement service."),
			mcp.WithNumber("engagement_id", mcp.Description("Engagement ID"), mcp.Required()),
			mcp.WithString("reply_text", mcp.Description("Reply body; defaults to the suggested reply")),
			mcp.WithNumber("approval_request_id", mcp.Description("Approval card that authorized this reply")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "engagement_id")
			if errRes != nil {
				return errRes, nil
			}
			payload := opsCopyArgs(req.GetArguments(), "reply_text", "approval_request_id")
			data, err := api.post("/mcp/ops/engagements/"+id+"/reply", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("ops_metrics",
			mcp.WithDescription("Read an account's daily metrics (normalized namespace) for a date window."),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
			mcp.WithString("from", mcp.Description("Start date YYYY-MM-DD; default 29 days ago")),
			mcp.WithString("to", mcp.Description("End date YYYY-MM-DD; default today")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			query := url.Values{}
			if from, ok := req.GetArguments()["from"].(string); ok && from != "" {
				query.Set("from", from)
			}
			if to, ok := req.GetArguments()["to"].(string); ok && to != "" {
				query.Set("to", to)
			}
			path := "/mcp/ops/bots/" + id + "/metrics"
			if encoded := query.Encode(); encoded != "" {
				path += "?" + encoded
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("ops_calendar",
			mcp.WithDescription("Read the bot's cadence plus the scheduled work under its control Epic."),
			mcp.WithNumber("bot_id", mcp.Description("Ops Bot ID"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, errRes := opsIDArg(req.GetArguments(), "bot_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.get("/mcp/ops/bots/" + id + "/calendar")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

// opsIDArg reads a required positive numeric argument as a path segment.
func opsIDArg(args map[string]any, key string) (string, *mcp.CallToolResult) {
	v, errRes := requireNumber(args, key)
	if errRes != nil {
		return "", errRes
	}
	id := int64(v)
	if id <= 0 {
		return "", mcp.NewToolResultError(key + " must be a positive integer")
	}
	return strconv.FormatInt(id, 10), nil
}

// opsCopyArgs copies the named arguments that are present, so omitted optional
// fields keep their server-side default instead of being zeroed.
func opsCopyArgs(args map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := args[k]; ok {
			out[k] = v
		}
	}
	return out
}
