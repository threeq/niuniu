package main

import (
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

// TestDescribeSourceToolIsReadOnly asserts the discovery tool exposes no field
// an agent could use to AUTHOR a capability description. The description is
// generated server-side from the confirmed contract; the tool only selects a
// target. This is the structural half of the "an agent cannot modify the
// description" guarantee (the other half is that the HTTP route is GET-only).
func TestDescribeSourceToolIsReadOnly(t *testing.T) {
	tool := newDescribeSourceTool()
	if tool.Name != "describe_source" {
		t.Fatalf("tool name = %q", tool.Name)
	}
	props := tool.InputSchema.Properties
	if len(props) != 2 {
		t.Fatalf("describe_source must take exactly subject_kind + ref, got %v", keysOf(props))
	}
	for _, k := range []string{"subject_kind", "ref"} {
		if _, ok := props[k]; !ok {
			t.Fatalf("missing required input %q (have %v)", k, keysOf(props))
		}
	}
	forbidden := []string{"capability", "capability_doc", "agent_block", "doc", "usage", "body", "text", "description"}
	for _, f := range forbidden {
		if _, ok := props[f]; ok {
			t.Fatalf("describe_source must not accept %q — a capability description is never caller-authored", f)
		}
	}
	req := map[string]bool{}
	for _, r := range tool.InputSchema.Required {
		req[r] = true
	}
	if !req["subject_kind"] || !req["ref"] {
		t.Fatalf("subject_kind and ref must be required, got %v", tool.InputSchema.Required)
	}
}

// TestNoCapabilityAuthoringTool scans every registered integration tool and
// asserts none declares an input that could carry capability-description text.
func TestNoCapabilityAuthoringTool(t *testing.T) {
	s := server.NewMCPServer("niuniu-test", "test")
	api := &apiClient{}
	registerExternalProxyTools(s, api)
	registerDataProxyTools(s, api)
	registerCapabilityTools(s, api)

	forbidden := []string{"capability", "capability_doc", "agent_block", "capability_text", "description_text"}
	for name, st := range s.ListTools() {
		for prop := range st.Tool.InputSchema.Properties {
			for _, f := range forbidden {
				if prop == f {
					t.Fatalf("tool %q exposes input %q — a capability description must never be caller-authored", name, prop)
				}
			}
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
