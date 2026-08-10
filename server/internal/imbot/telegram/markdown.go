package telegram

import (
	"regexp"
	"strconv"
	"strings"
)

// This file renders the CommonMark-flavored text the dispatcher emits (**bold**,
// `code`, fenced blocks, [links], # headings) into the small HTML subset Telegram
// accepts with parse_mode=HTML (<b>/<i>/<code>/<pre>/<a>). Telegram does NOT
// render CommonMark, and its MarkdownV2 mode 400s on unescaped `.`/`-`/`!` etc.,
// so HTML is the reliable rich-text path.
//
// Safety: every non-code character is HTML-escaped first, and only balanced,
// self-generated tags are injected, so the output is always valid, balanced HTML
// — Telegram cannot reject it for bad entities. Push still keeps a plain-text
// fallback as a belt-and-suspenders guard. Single `*`/`_` italics are
// deliberately NOT converted: `_` and `*` pepper code, filenames and identifiers,
// so converting them would mangle far more than it helps.

var (
	fencedCodeRe = regexp.MustCompile("(?s)```[a-zA-Z0-9]*\\n?(.*?)```")
	inlineCodeRe = regexp.MustCompile("`([^`\\n]+)`")
	boldRe       = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	linkRe       = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
	headingRe    = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.+?)[ \t]*$`)
	placeholderRe = regexp.MustCompile("\x00(\\d+)\x00")
)

// renderTelegramHTML converts the CommonMark subset to Telegram HTML. Plain text
// with no markup passes through unchanged (bar HTML escaping), so it is safe to
// run on every outbound message.
func renderTelegramHTML(md string) string {
	// 1. Stash code spans/blocks as placeholders BEFORE escaping so inline styling
	//    never rewrites code contents, and code is escaped exactly once.
	var codes []string
	stash := func(html string) string {
		codes = append(codes, html)
		return "\x00" + strconv.Itoa(len(codes)-1) + "\x00"
	}
	md = fencedCodeRe.ReplaceAllStringFunc(md, func(m string) string {
		inner := fencedCodeRe.FindStringSubmatch(m)[1]
		return stash("<pre>" + escapeHTML(inner) + "</pre>")
	})
	md = inlineCodeRe.ReplaceAllStringFunc(md, func(m string) string {
		inner := inlineCodeRe.FindStringSubmatch(m)[1]
		return stash("<code>" + escapeHTML(inner) + "</code>")
	})

	// 2. Escape the remaining prose (placeholders are NUL+digits, left intact).
	out := escapeHTML(md)

	// 3. Inline styling on the escaped prose.
	out = headingRe.ReplaceAllString(out, "<b>$1</b>")
	out = boldRe.ReplaceAllString(out, "<b>$1</b>")
	out = linkRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := linkRe.FindStringSubmatch(m)
		label, href := sub[1], sub[2]
		href = strings.ReplaceAll(href, `"`, "&quot;")
		return `<a href="` + href + `">` + label + `</a>`
	})

	// 4. Restore the stashed code.
	out = placeholderRe.ReplaceAllStringFunc(out, func(m string) string {
		idx, _ := strconv.Atoi(placeholderRe.FindStringSubmatch(m)[1])
		if idx >= 0 && idx < len(codes) {
			return codes[idx]
		}
		return m
	})
	return out
}

// escapeHTML escapes the three characters Telegram's HTML parser treats as
// markup. Quotes are handled at the href boundary, not here.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
