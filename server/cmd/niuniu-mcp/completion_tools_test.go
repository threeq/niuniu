package main

import (
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

// TestCompletionToolsRegistered pins the §6.9 MCP surface. Project scope is
// captured at registration (pid), so a project-scoped tool must not accept a
// project_id the caller could point at another owner's project; the issue tools
// must require issue_id so they can never default to an ambiguous target.
func TestCompletionToolsRegistered(t *testing.T) {
	s := server.NewMCPServer("niuniu-test", "test")
	registerCompletionTools(s, &apiClient{}, "42")
	tools := s.ListTools()

	for _, name := range []string{
		"completion_set_policy",
		"completion_list_profiles",
		"completion_upsert_profile",
		"issue_set_completion_profile",
		"issue_get_brief",
	} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("missing MCP tool %q (have %v)", name, keysOfTools(tools))
		}
	}

	for _, name := range []string{"completion_set_policy", "completion_list_profiles", "completion_upsert_profile"} {
		if _, ok := tools[name].Tool.InputSchema.Properties["project_id"]; ok {
			t.Fatalf("%q must not accept project_id — the project is fixed at registration", name)
		}
	}

	for _, name := range []string{"issue_set_completion_profile", "issue_get_brief"} {
		if _, ok := tools[name].Tool.InputSchema.Properties["issue_id"]; !ok {
			t.Fatalf("%q must accept issue_id", name)
		}
		var required bool
		for _, r := range tools[name].Tool.InputSchema.Required {
			required = required || r == "issue_id"
		}
		if !required {
			t.Fatalf("%q must require issue_id", name)
		}
	}
}

func keysOfTools(tools map[string]*server.ServerTool) []string {
	out := make([]string, 0, len(tools))
	for name := range tools {
		out = append(out, name)
	}
	return out
}
