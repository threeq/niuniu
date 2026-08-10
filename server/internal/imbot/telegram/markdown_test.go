package telegram

import "testing"

func TestRenderTelegramHTML(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain passes through", "hello world", "hello world"},
		{"escapes angle brackets & amp", "a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{"bold", "**做完了**", "<b>做完了</b>"},
		{"inline code", "run `go test`", "run <code>go test</code>"},
		{"link", "see [docs](https://x.io/a)", `see <a href="https://x.io/a">docs</a>`},
		{"heading to bold", "# 怎么用", "<b>怎么用</b>"},
		{"task ref hash is not a heading", "#12 标题", "#12 标题"},
		{"code is not restyled", "`a**b**c`", "<code>a**b**c</code>"},
		{"angle brackets inside code escaped", "`<div>`", "<code>&lt;div&gt;</code>"},
		{"underscores left intact", "some_var_name", "some_var_name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderTelegramHTML(c.in); got != c.want {
				t.Errorf("renderTelegramHTML(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestRenderTelegramHTML_FencedCode(t *testing.T) {
	got := renderTelegramHTML("before\n```go\nfmt.Println(\"x\")\n```\nafter")
	want := "before\n<pre>fmt.Println(\"x\")\n</pre>\nafter"
	if got != want {
		t.Errorf("fenced code = %q, want %q", got, want)
	}
}
