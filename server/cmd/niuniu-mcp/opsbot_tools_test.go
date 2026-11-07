package main

import (
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

// TestOpsBotToolsRegistered pins the §12 MCP surface: every tool the design
// names must exist under its exact name, since the conversational layer and the
// docs address them by name.
func TestOpsBotToolsRegistered(t *testing.T) {
	s := server.NewMCPServer("niuniu-test", "test")
	registerOpsBotTools(s, &apiClient{})
	tools := s.ListTools()

	for _, name := range []string{
		"opsbot_create", "opsbot_list", "opsbot_get", "opsbot_update",
		"opsbot_pause", "opsbot_resume", "opsbot_kill", "opsbot_bind_chat",
		"opsbot_request_credential_link", "opsbot_browser_login",
		"opsbot_set_goals", "opsbot_set_cadence", "opsbot_set_budget", "opsbot_set_approval",
		"ops_pack_list", "ops_pack_generate", "ops_pack_validate",
		"ops_pack_test", "ops_pack_publish", "ops_pack_export", "ops_pack_import",
		"ops_approval_list", "ops_approval_approve", "ops_approval_reject",
		"ops_publish", "ops_engagement_reply", "ops_metrics", "ops_calendar",
	} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("missing MCP tool %q (have %v)", name, keysOfTools(tools))
		}
	}
}

// TestOpsBotToolsRequiredArgs keeps the error contract stable: a missing id
// argument must fail at the tool boundary instead of reaching the server as a
// /bots//pause path.
func TestOpsBotToolsRequiredArgs(t *testing.T) {
	s := server.NewMCPServer("niuniu-test", "test")
	registerOpsBotTools(s, &apiClient{})
	tools := s.ListTools()

	cases := map[string]string{
		"opsbot_get":           "bot_id",
		"opsbot_update":        "bot_id",
		"opsbot_pause":         "bot_id",
		"opsbot_set_goals":     "bot_id",
		"opsbot_set_budget":    "bot_id",
		"opsbot_bind_chat":     "chat_id",
		"ops_approval_approve": "approval_id",
		"ops_approval_reject":  "approval_id",
		"ops_publish":          "bot_id",
		"ops_engagement_reply": "engagement_id",
		"ops_metrics":          "bot_id",
		"ops_calendar":         "bot_id",
		"ops_pack_validate":    "pack_id",
		"ops_pack_export":      "dest_path",
	}
	for name, arg := range cases {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("missing MCP tool %q", name)
		}
		if _, ok := tool.Tool.InputSchema.Properties[arg]; !ok {
			t.Fatalf("%q must accept %s", name, arg)
		}
		required := false
		for _, r := range tool.Tool.InputSchema.Required {
			required = required || r == arg
		}
		if !required {
			t.Fatalf("%q must require %s", name, arg)
		}
	}
}
