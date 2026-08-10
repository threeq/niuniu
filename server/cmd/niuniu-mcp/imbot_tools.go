// imbot_tools.go — registers the five IM Bot onboarding MCP tools (Epic #555 T4).
// All tools are project-scoped: the pid closure from main.go is captured at
// registration time. No project_id argument is exposed to the agent.
//
// Tool surface:
//   imbot_request_credential_link — generate a one-time credential submission URL
//   imbot_test_channel            — verify channel connectivity
//   imbot_list_pending_chats      — list chats awaiting approval
//   imbot_approve_chat            — approve a pending chat (records the caller)
//   imbot_channel_status          — get current status of a channel
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerIMBotTools(s *server.MCPServer, api *apiClient, pid string) {
	// 1. imbot_request_credential_link
	// Call this first during onboarding: it issues a 15-minute one-time link the
	// user opens in the browser to submit their IM platform credentials (AppID,
	// AppSecret, etc.). Show the URL to the user immediately; do not store or log it.
	s.AddTool(
		mcp.NewTool("imbot_request_credential_link",
			mcp.WithDescription("Generate a one-time credential submission link for IM Bot onboarding. "+
				"The user opens the returned URL in their browser and fills in the platform credentials "+
				"(AppID, AppSecret, etc.). The link expires in 15 minutes. "+
				"Call this after the user has chosen a platform and bot name. "+
				"For the 'wechat' platform the page shows a QR code the user scans with the WeChat "+
				"app instead of a credential form. "+
				"Show the URL to the user immediately. Never store or log the URL."),
			mcp.WithString("platform", mcp.Description("IM platform: lark | dingtalk | telegram | wework | wechat"), mcp.Required()),
			mcp.WithString("name", mcp.Description("Human-readable channel name, e.g. '飞书-研发群机器人'"), mcp.Required()),
			mcp.WithString("connection_mode", mcp.Description("stream (default, long-connection, works without public IP) | webhook (requires public URL)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			platform, errRes := requireString(args, "platform")
			if errRes != nil {
				return errRes, nil
			}
			name, errRes := requireString(args, "name")
			if errRes != nil {
				return errRes, nil
			}
			mode := "stream"
			if v, ok := args["connection_mode"].(string); ok && v != "" {
				mode = v
			}
			payload := map[string]any{
				"platform":        platform,
				"name":            name,
				"connection_mode": mode,
			}
			data, err := api.post("/mcp/projects/"+pid+"/imbot/onboarding-token", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// 2. imbot_test_channel
	// After the credential link is submitted, call this to verify the platform
	// connection is live before proceeding to chat pairing.
	s.AddTool(
		mcp.NewTool("imbot_test_channel",
			mcp.WithDescription("Test connectivity for an IM Bot channel. "+
				"Call this after credentials have been submitted via the onboarding link to confirm "+
				"the channel is live. Returns {ok:true} on success or an error message."),
			mcp.WithNumber("channel_id", mcp.Description("Channel ID (from imbot_channel_status or the credential submission response)"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			cidF, errRes := requireNumber(args, "channel_id")
			if errRes != nil {
				return errRes, nil
			}
			cid := strconv.FormatInt(int64(cidF), 10)
			data, err := api.post("/mcp/projects/"+pid+"/imbot/channels/"+cid+"/test", nil)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// 3. imbot_list_pending_chats
	// Once the bot is added to a group/chat, the platform pushes an inbound event
	// and the chat appears in the pending list. Call this to see which chats are
	// waiting for approval. Optionally filter by channel_id.
	s.AddTool(
		mcp.NewTool("imbot_list_pending_chats",
			mcp.WithDescription("List group chats awaiting pairing approval. "+
				"After the bot is added to a chat, the chat appears here as 'pending'. "+
				"Use imbot_approve_chat to admit it. Optionally filter by channel_id. "+
				"Set owner_scope=true to list pending chats across ALL of the owner's bots "+
				"(shared-bot: one bot may serve several projects)."),
			mcp.WithNumber("channel_id", mcp.Description("Optional: filter to a specific channel's pending chats")),
			mcp.WithBoolean("owner_scope", mcp.Description("Optional: list pending chats across all of the owner's bots, not just this project")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			path := "/mcp/projects/" + pid + "/imbot/pending-chats"
			var q []string
			if cidF, ok := args["channel_id"].(float64); ok && cidF > 0 {
				q = append(q, fmt.Sprintf("channel_id=%d", int64(cidF)))
			}
			if osc, ok := args["owner_scope"].(bool); ok && osc {
				q = append(q, "scope=owner")
			}
			if len(q) > 0 {
				path += "?" + strings.Join(q, "&")
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// 4. imbot_approve_chat
	// Approves a pending chat so the bot can exchange messages with it.
	// The approval is attributed to the MCP caller's user id (resolved server-side).
	s.AddTool(
		mcp.NewTool("imbot_approve_chat",
			mcp.WithDescription("Approve a pending chat so the IM Bot can interact with it. "+
				"After approval, the bot sends a welcome message to the chat. "+
				"Use imbot_list_pending_chats to find the chat_id. "+
				"Optionally pass project_id to route the chat to a specific (same-owner) "+
				"project; when omitted it is routed to the current project."),
			mcp.WithNumber("chat_id", mcp.Description("Chat ID to approve (from imbot_list_pending_chats)"), mcp.Required()),
			mcp.WithNumber("project_id", mcp.Description("Optional: target project id to route this chat to (defaults to the current project)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			chatIDF, errRes := requireNumber(args, "chat_id")
			if errRes != nil {
				return errRes, nil
			}
			chatID := strconv.FormatInt(int64(chatIDF), 10)
			var payload map[string]any
			if pf, ok := args["project_id"].(float64); ok && pf > 0 {
				payload = map[string]any{"project_id": int64(pf)}
			}
			data, err := api.post("/mcp/projects/"+pid+"/imbot/chats/"+chatID+"/approve", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// 5. imbot_channel_status
	// Check the current state of a channel (status, credential presence, mode).
	// Use this to verify configuration after the onboarding flow or to diagnose issues.
	s.AddTool(
		mcp.NewTool("imbot_channel_status",
			mcp.WithDescription("Get the current status of an IM Bot channel. "+
				"Returns id, name, channel_type, connection_mode, status, and has_credential. "+
				"Use this to verify the channel is active and has credentials before testing."),
			mcp.WithNumber("channel_id", mcp.Description("Channel ID to inspect"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			cidF, errRes := requireNumber(args, "channel_id")
			if errRes != nil {
				return errRes, nil
			}
			cid := strconv.FormatInt(int64(cidF), 10)
			data, err := api.get("/mcp/projects/" + pid + "/imbot/channels/" + cid + "/status")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			// Parse to confirm the response is valid JSON; re-emit as text.
			var obj map[string]any
			if jsonErr := json.Unmarshal(data, &obj); jsonErr != nil {
				return mcp.NewToolResultText(string(data)), nil
			}
			out, _ := json.Marshal(obj)
			return mcp.NewToolResultText(string(out)), nil
		},
	)
}
