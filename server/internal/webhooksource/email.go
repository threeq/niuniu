package webhooksource

// Shared email normalization rules (design §6.2–§6.3). The IMAP poller and the
// inbound-parse webhook must agree on what counts as a task: the same ignore
// rules, the same thread root, and the same event id. Keeping the pure functions
// here (rather than in either transport) is what makes that agreement testable.
//
// Everything is data-only: a header or body can never grant authority.

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// EmailBodyLimit caps the stored body (design §6.2: 64KB). A longer body is
// truncated on a rune boundary so the result is still valid UTF-8.
const EmailBodyLimit = 64 << 10

// EmailHeaders carries the RFC 5322 headers the ignore rules inspect. The
// inbound adapter fills it from the provider's header collection (Mailgun
// message-headers, SendGrid raw headers, Postmark Headers); the IMAP adapter
// fills it from the parsed message.
type EmailHeaders struct {
	AutoSubmitted string
	Precedence    string
	ReturnPath    string
}

// EmailIgnoreReason applies the design's default ignore rules: system
// auto-replies, bulk/list mail, bounces, and the binding's own address (a
// message we sent would otherwise create work for ourselves). It returns "" when
// the message should be processed.
func EmailIgnoreReason(headers EmailHeaders, fromAddress, selfAddress string) string {
	if v := strings.TrimSpace(headers.AutoSubmitted); v != "" && !strings.EqualFold(v, "no") {
		return "auto-submitted"
	}
	switch strings.ToLower(strings.TrimSpace(headers.Precedence)) {
	case "bulk", "junk", "list":
		return "precedence"
	}
	if strings.TrimSpace(headers.ReturnPath) == "<>" {
		return "bounce"
	}
	if selfAddress != "" && fromAddress != "" &&
		strings.EqualFold(strings.TrimSpace(fromAddress), strings.TrimSpace(selfAddress)) {
		return "self"
	}
	return ""
}

// TrimMessageID strips the angle brackets and surrounding whitespace that
// Message-ID / In-Reply-To carry in the wire format.
func TrimMessageID(v string) string {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.TrimSpace(s)
}

// FirstMessageID returns the first Message-ID in a header value. In-Reply-To
// normally holds one id; taking the first keeps a malformed multi-id header from
// becoming a thread key.
func FirstMessageID(v string) string {
	if ids := MessageIDList(v); len(ids) > 0 {
		return ids[0]
	}
	return TrimMessageID(v)
}

// MessageIDList extracts Message-IDs from a References header, which is a
// space-separated list of angle-bracketed ids.
func MessageIDList(v string) []string {
	s := strings.TrimSpace(v)
	if s == "" {
		return nil
	}
	original := s
	var out []string
	for {
		start := strings.Index(s, "<")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], ">")
		if end < 0 {
			break
		}
		if id := TrimMessageID(s[start : start+end+1]); id != "" {
			out = append(out, id)
		}
		s = s[start+end+1:]
	}
	if len(out) == 0 {
		// Tolerate servers that omit the angle brackets.
		for _, f := range strings.Fields(original) {
			if id := TrimMessageID(f); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

// EmailThreadRoot resolves the conversation key: the first References entry,
// else In-Reply-To, else the message's own Message-ID. A reply therefore
// resolves to the task its root created, exactly as on the IMAP path.
func EmailThreadRoot(references []string, inReplyTo, messageID string) string {
	if len(references) > 0 {
		if id := strings.TrimSpace(references[0]); id != "" {
			return id
		}
	}
	if id := strings.TrimSpace(inReplyTo); id != "" {
		return id
	}
	return strings.TrimSpace(messageID)
}

var emailQuoteMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^on\b.{0,300}\bwrote:\s*$`),
	regexp.MustCompile(`^在.{0,200}写道[:：]\s*$`),
	regexp.MustCompile(`(?i)^-{2,}\s*original message\s*-{2,}$`),
	regexp.MustCompile(`^-{2,}\s*原始邮件\s*-{2,}$`),
}

// StripQuotedReply removes the quoted history a mail client appends: the
// `> …` lines and everything from an "On … wrote:" / "在 … 写道:" marker down.
// A reply becomes just what the person actually wrote, which is what the agent
// needs to act on.
func StripQuotedReply(body string) string {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isQuoteMarker(trimmed) {
			break
		}
		if strings.HasPrefix(trimmed, ">") {
			continue
		}
		kept = append(kept, line)
	}
	return normalizeBlankLines(strings.Join(kept, "\n"))
}

func isQuoteMarker(line string) bool {
	for _, re := range emailQuoteMarkers {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

func normalizeBlankLines(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// TruncateUTF8 cuts s to at most max bytes without splitting a rune.
func TruncateUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// HTMLToText renders an HTML part as plain text. It is intentionally crude:
// block elements become line breaks, script/style/title are dropped, and blank
// runs collapse. Anything fancier would only add noise to a task prompt.
func HTMLToText(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return strings.TrimSpace(s)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head", "title", "noscript":
				return
			case "br", "p", "div", "tr", "li", "ul", "ol", "table", "blockquote",
				"h1", "h2", "h3", "h4", "h5", "h6", "section", "article":
				b.WriteString("\n")
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return normalizeBlankLines(b.String())
}
