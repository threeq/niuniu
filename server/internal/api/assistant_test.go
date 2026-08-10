package api

import "testing"

func TestDeriveTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"first line only", "做一份周报\n要包含图表", "做一份周报"},
		{"trims whitespace", "  整理客户名单  ", "整理客户名单"},
		{"empty falls back", "   ", "牛牛助手任务"},
		{"short stays intact", "帮我写封邮件", "帮我写封邮件"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveTitle(c.in); got != c.want {
				t.Fatalf("deriveTitle(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	// Long single-line input is clipped to 40 runes + ellipsis.
	long := ""
	for i := 0; i < 60; i++ {
		long += "字"
	}
	got := deriveTitle(long)
	gotRunes := []rune(got)
	if len(gotRunes) != 41 || gotRunes[40] != '…' {
		t.Fatalf("deriveTitle(long) = %q (len %d runes), want 40 runes + ellipsis", got, len(gotRunes))
	}
}
