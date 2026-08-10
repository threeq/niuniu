package main

import "testing"

func TestParseToolGroups(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]bool
	}{
		{"", map[string]bool{}},
		{"   ", map[string]bool{}},
		{"multi-agent", map[string]bool{"multi-agent": true}},
		{"multi-agent,harness", map[string]bool{"multi-agent": true, "harness": true}},
		{" multi-agent , , harness ", map[string]bool{"multi-agent": true, "harness": true}},
	}
	for _, c := range cases {
		got := parseToolGroups(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("parseToolGroups(%q): len %d, want %d (%v)", c.in, len(got), len(c.want), got)
		}
		for k := range c.want {
			if !got[k] {
				t.Fatalf("parseToolGroups(%q): missing %q (got %v)", c.in, k, got)
			}
		}
	}
}
