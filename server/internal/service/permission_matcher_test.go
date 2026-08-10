package service

import "testing"

func TestExtractMatcherField(t *testing.T) {
	cases := []struct {
		tool   string
		input  map[string]any
		expect string
	}{
		{"Bash", map[string]any{"command": "npm test"}, "npm test"},
		{"Edit", map[string]any{"file_path": "/repo/src/a.ts"}, "/repo/src/a.ts"},
		{"Write", map[string]any{"file_path": "/repo/x.go"}, "/repo/x.go"},
		{"WebFetch", map[string]any{"url": "https://github.com/x/y"}, "https://github.com/x/y"},
		{"TodoWrite", map[string]any{}, ""},
		{"Bash", map[string]any{}, ""},
		{"Edit", map[string]any{"file_path": 42}, ""},
	}
	for _, c := range cases {
		got := extractMatcherField(c.tool, c.input)
		if got != c.expect {
			t.Errorf("tool=%s input=%v got %q want %q", c.tool, c.input, got, c.expect)
		}
	}
}

func TestMatcherMatches(t *testing.T) {
	type tc struct {
		kind, value, field string
		want               bool
	}
	cases := []tc{
		{"any", "", "anything", true},
		{"any", "", "", true},
		{"exact", "npm test", "npm test", true},
		{"exact", "npm test", "npm test:foo", false},
		{"prefix", "npm test", "npm test:foo", true},
		{"prefix", "npm test", "yarn test", false},
		{"prefix", "", "anything", true}, // empty prefix matches everything (intentional)
		{"glob", "src/**/*.ts", "src/foo/bar.ts", true},
		{"glob", "src/**/*.ts", "lib/foo.ts", false},
		{"domain", "github.com", "https://github.com/x/y", true},
		{"domain", "github.com", "https://api.github.com/x", true},
		{"domain", "github.com", "https://example.com/x", false},
		{"domain", "github.com", "not-a-url", false},
		{"domain", "github.com", "https://evil.github.com.attacker.com/", false},
		{"unknown_kind", "x", "y", false},
	}
	for _, c := range cases {
		got := matcherMatches(c.kind, c.value, c.field)
		if got != c.want {
			t.Errorf("kind=%s value=%q field=%q got %v want %v", c.kind, c.value, c.field, got, c.want)
		}
	}
}

func TestIsHighRiskTool(t *testing.T) {
	for _, tool := range []string{"Bash", "Edit", "Write", "WebFetch"} {
		if !IsHighRiskTool(tool) {
			t.Errorf("%s should be high-risk", tool)
		}
	}
	for _, tool := range []string{"TodoWrite", "WebSearch", "Task", "Read"} {
		if IsHighRiskTool(tool) {
			t.Errorf("%s should NOT be high-risk", tool)
		}
	}
}
