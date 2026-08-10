package workitem

import "testing"

func TestInjectTypeLabel(t *testing.T) {
	got := InjectTypeLabel("需求", []string{"p0", "frontend"})
	if len(got) != 3 || got[0] != "type:需求" {
		t.Fatalf("expected type label at index 0, got %v", got)
	}
	// idempotent
	got = InjectTypeLabel("需求", got)
	if len(got) != 3 {
		t.Fatalf("expected idempotent (still 3 items), got %v", got)
	}
}

func TestInjectTypeLabel_EmptyType(t *testing.T) {
	got := InjectTypeLabel("", []string{"x"})
	if len(got) != 1 || got[0] != "x" {
		t.Fatalf("expected no injection for empty type, got %v", got)
	}
}

func TestFormatRef(t *testing.T) {
	cases := []struct{ source, extID, want string }{
		{"github", "owner/repo#123", "github:owner/repo#123"},
		{"jira", "PROJ-456", "jira:PROJ-456"},
		{"tapd", "ws-1/789", "tapd:ws-1/789"},
	}
	for _, c := range cases {
		got := FormatRef(c.source, c.extID)
		if got != c.want {
			t.Errorf("FormatRef(%s, %s) = %s, want %s", c.source, c.extID, got, c.want)
		}
	}
}

func TestParseRef(t *testing.T) {
	source, id, ok := ParseRef("github:owner/repo#1")
	if !ok || source != "github" || id != "owner/repo#1" {
		t.Fatalf("bad parse: source=%s id=%s ok=%v", source, id, ok)
	}
	if _, _, ok := ParseRef("no-colon"); ok {
		t.Fatal("expected failure on missing colon")
	}
}
