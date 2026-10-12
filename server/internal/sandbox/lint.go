package sandbox

import (
	"fmt"
	"regexp"
	"strings"
)

// Static check for AI-authored function code (issue #739, "禁用模式 lint").
//
// This is a REVIEW AID, not the security boundary. The boundary is the OS fence
// plus the capability layer: even a function that slips past every rule here
// still has no filesystem, no sockets and no way to name a URL. The lint exists
// so that the human approving a draft is shown the constructs that usually mean
// the generated code is doing something the declarative path should have done
// instead — and so an obviously malicious draft never reaches the review queue.

// LintSeverity ranks a finding. Only SeverityError blocks a publish.
type LintSeverity string

const (
	SeverityError   LintSeverity = "error"
	SeverityWarning LintSeverity = "warning"
)

// LintFinding is one hit.
type LintFinding struct {
	Rule     string       `json:"rule"`
	Severity LintSeverity `json:"severity"`
	Line     int          `json:"line"`
	Snippet  string       `json:"snippet"`
	Message  string       `json:"message"`
	Fix      string       `json:"fix,omitempty"`
}

type lintRule struct {
	id       string
	re       *regexp.Regexp
	severity LintSeverity
	message  string
	fix      string
}

var lintRules = []lintRule{
	{
		id: "no-eval", severity: SeverityError,
		re:      regexp.MustCompile(`\b(eval|Function)\s*\(`),
		message: "dynamic code evaluation is not allowed",
		fix:     "write the logic directly instead of building code at runtime",
	},
	{
		id: "no-require", severity: SeverityError,
		re:      regexp.MustCompile(`\b(require|module|exports|__dirname|__filename|process|globalThis|global|Buffer|Deno)\b`),
		message: "the function runs with no module system and no host globals",
		fix:     "operation functions have no npm access and no Node API; declare the operation in the contract and use http_request, or ask the platform to add the capability",
	},
	{
		// Named network APIs only. Bare words like `net` or `tls` were in an
		// earlier revision and blocked legitimate code that merely used them as
		// identifiers, which teaches a reviewer to click past the warnings.
		id: "no-host-network", severity: SeverityError,
		re:      regexp.MustCompile(`\b(fetch|XMLHttpRequest|WebSocket|EventSource)\b|\bnet\s*\.|\btls\s*\.|\bdgram\s*\.`),
		message: "the function has no network API of its own; http_request is the only egress",
		fix:     "use http_request({operation, params, body}) so the call goes through the proxy (scope, credentials and audit included)",
	},
	{
		id: "no-async", severity: SeverityError,
		re:      regexp.MustCompile(`\b(async|await|Promise|setTimeout|setInterval|setImmediate|queueMicrotask)\b`),
		message: "operation functions are synchronous: there is no event loop to resolve promises or timers",
		fix:     "call http_request directly and use its return value; it blocks until the response arrives",
	},
	{
		id: "no-infinite-loop", severity: SeverityError,
		re:      regexp.MustCompile(`while\s*\(\s*(true|1)\s*\)|for\s*\(\s*;\s*;\s*\)`),
		message: "an unconditional loop cannot terminate within the function's budget",
		fix:     "bound the loop with a counter or a condition",
	},
	{
		id: "no-proto-access", severity: SeverityError,
		re:      regexp.MustCompile(`__proto__|\.\s*constructor\b|prototype\s*\[`),
		message: "prototype-chain access is a sandbox-escape idiom and has no use in an operation function",
		fix:     "read the fields you need directly off the input object",
	},
	{
		id: "no-absolute-url", severity: SeverityError,
		re:      regexp.MustCompile(`["'` + "`" + `]https?://`),
		message: "the function may not name a URL: egress is authorized by operation, not by address",
		fix:     "pass the operation name declared in the contract; the host resolves the method, path and host",
	},
	{
		id: "no-import", severity: SeverityError,
		re:      regexp.MustCompile(`^\s*import\s|^\s*export\s|\bimport\s*\(`),
		message: "imports are unavailable — there is no module loader in the box",
		fix:     "inline what you need; if a library is genuinely required, ask for the packaged-dependency runtime tier",
	},
	{
		id: "no-child-process", severity: SeverityWarning,
		re:      regexp.MustCompile(`child_process|spawnSync|\bexecSync\b`),
		message: "process spawning is denied by the OS fence and by the syscall filter",
		fix:     "remove the call; this runs nowhere",
	},
}

// Lint reports the blocking and advisory findings for a function body.
func Lint(code string) []LintFinding {
	var out []LintFinding
	lines := strings.Split(code, "\n")
	for i, raw := range lines {
		line := stripStringLiteralsExceptURLs(raw)
		for _, r := range lintRules {
			loc := r.re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			out = append(out, LintFinding{
				Rule:     r.id,
				Severity: r.severity,
				Line:     i + 1,
				Snippet:  strings.TrimSpace(raw),
				Message:  r.message,
				Fix:      r.fix,
			})
		}
	}
	return out
}

// LintBlocks reports whether any finding forbids publishing.
func LintBlocks(findings []LintFinding) bool {
	for _, f := range findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// LintSummary renders the findings as a compact human-readable list. The
// confirmation card shows this verbatim, so it is written for a person, not a
// parser.
func LintSummary(findings []LintFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	for i, f := range findings {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "line %d [%s] %s", f.Line, f.Rule, f.Message)
		if f.Fix != "" {
			b.WriteString(" — " + f.Fix)
		}
	}
	return b.String()
}

// stripStringLiteralsExceptURLs blanks out string literal CONTENT so a banned
// token inside a message (e.g. an error string mentioning "process") does not
// trip a rule — while leaving absolute URLs visible, since naming a host is
// itself the thing being checked for.
var urlInLiteral = regexp.MustCompile(`["'` + "`" + `]https?://[^"'` + "`" + `]*`)

func stripStringLiteralsExceptURLs(s string) string {
	if !strings.ContainsAny(s, `"'`+"`") {
		return s
	}
	// Keep the whole line when it carries an absolute URL so the
	// no-absolute-url rule still sees it.
	if urlInLiteral.MatchString(s) {
		return s
	}
	var b strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote == 0 && (r == '"' || r == '\'' || r == '`'):
			quote = r
			b.WriteRune(r)
		case quote != 0 && r == quote:
			quote = 0
			b.WriteRune(r)
		case quote != 0:
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
