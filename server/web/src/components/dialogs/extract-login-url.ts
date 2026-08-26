// Extracts the Claude OAuth login URL from the raw PTY byte stream of
// `claude /login`. Kept in its own module (not the dialog component file) so it
// is unit-testable and does not trip react-refresh/only-export-components.

// Strip ANSI/OSC escape sequences so the OAuth URL can be matched from the raw
// PTY byte stream.
//
// Two OSC terminators exist and BOTH occur here: BEL (\x07) and ST (ESC \).
// The Claude CLI wraps the URL in an OSC-8 hyperlink terminated by ST, so a
// BEL-only pattern leaves `\x1b\` plus the hyperlink's own copy of the URL in
// the text — which then gets concatenated onto the visible one, yielding a
// doubled, unusable URL. Match the ST branch first so it wins over the shorter
// CSI-ish alternatives.
const ANSI_RE =
  // eslint-disable-next-line no-control-regex
  /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|[\x1b\x9b][[\]()#;?]*(?:(?:(?:;[-a-zA-Z\d/#&.:=?%@~_]+)*|[a-zA-Z\d]+(?:;[-a-zA-Z\d/#&.:=?%@~_]*)*)?\x07|(?:\d{1,4}(?:;\d{0,4})*)?[\dA-PR-TZcf-ntqry=><~])/g

// The Claude CLI renders the OAuth URL inside an ink panel sized to the (often
// narrow) login-dialog terminal. Ink HARD-wraps the URL to that width, emitting
// real line breaks (plus left/right panel padding) between segments — so in the
// raw PTY byte stream the URL is split across several physical lines, each a run
// of URL characters with no internal whitespace. Matching the start line alone
// (the old single-line regex) captured only the first segment, e.g. up to
// "...&re". We must reassemble across the wrapped lines.
//
// The host is anchored with (?:^|[^\w.-]) at the call site via a global scan:
// matching `claude.com` as a bare substring also matches the `code.claude.com`
// docs link the CLI prints a few lines earlier, which would lock the parser onto
// the wrong URL. Require the host to be exactly one of these.
const URL_START_RE =
  /https:\/\/(?:claude\.com|console\.anthropic\.com)\/(?:cai\/)?oauth\/[^\s'"]*/
// A wrap-continuation line is a single run of URL chars (no internal whitespace,
// once padding is trimmed).
const URL_CONT_RE = /^[^\s'"]+$/
// Where the URL stops and the CLI's prose begins.
//
// On a Unix PTY the prose lands on its own line (with real spaces), so a
// whitespace test alone terminated the URL. Windows ConPTY encodes inter-word
// gaps as cursor-forward escapes (\x1b[1C) instead of spaces, so after stripping
// ANSI the trailing prose COLLAPSES ONTO THE LAST URL SEGMENT as one
// whitespace-free run, e.g.:
//
//	…&state=oHkSmKT3…ke4Pastecodehereifprompted>Esctocancel
//
// Discarding such a line would drop the tail of the query string
// (code_challenge_method, state) and produce a URL the OAuth server rejects with
// "Invalid code"; keeping it whole would append the prose. So we CUT at the
// first prose marker and keep the part before it.
//
// Markers are the literal words/glyphs the CLI renders around the URL block. A
// percent-encoded OAuth URL cannot contain them: '>' and the spinner glyphs are
// not URL-safe, and the words are matched case-sensitively with a capital to
// avoid clipping legitimate lowercase parameter text.
const PROSE_CUT_RE = /[>·✶✢✻✽●]|Paste|Browser|Press|Enter|Esc|Retrying|OAuth error/

// Strip ANSI, then concatenate the line that begins the URL with the following
// wrap-continuation lines. Only return once a terminator line is seen: this
// guards against locking a half-streamed URL whose tail hasn't arrived yet.
//
// The CLI may print the URL twice in one frame (once inside an OSC-8 hyperlink
// parameter, once as visible text). Stripping OSC removes the hyperlink copy, so
// scanning for the LAST start position is not needed — but we do dedupe a URL
// that got doubled head-to-tail, which is what a missed OSC strip looks like.
export function extractLoginUrl(buffer: string): string | null {
  const lines = buffer.replace(ANSI_RE, '').split(/[\r\n]+/)
  for (let i = 0; i < lines.length; i++) {
    const start = lines[i].match(URL_START_RE)
    if (!start) continue
    // The start line itself may already carry trailing prose (ConPTY, short URL).
    let url = cut(start[0])
    let terminated = url !== start[0]
    for (let j = i + 1; !terminated && j < lines.length; j++) {
      const seg = lines[j].trim()
      if (!URL_CONT_RE.test(seg)) {
        terminated = true // blank/spaced prose line ends the URL block
        break
      }
      const kept = cut(seg)
      url += kept
      if (kept !== seg) {
        terminated = true // prose began mid-line; the URL ends here
        break
      }
    }
    if (!terminated) continue
    // Defensive: if the same URL appears concatenated to itself, keep one copy.
    const half = url.length / 2
    if (url.length % 2 === 0 && url.slice(0, half) === url.slice(half)) {
      url = url.slice(0, half)
    }
    return url
  }
  return null
}

// Truncate a segment at the first prose marker; returns it unchanged when the
// whole segment is URL text.
function cut(seg: string): string {
  const m = seg.match(PROSE_CUT_RE)
  return m ? seg.slice(0, m.index) : seg
}
