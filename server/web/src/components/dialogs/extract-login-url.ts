// Extracts the Claude OAuth login URL from the raw PTY byte stream of
// `claude /login`. Kept in its own module (not the dialog component file) so it
// is unit-testable and does not trip react-refresh/only-export-components.

// Strip ANSI/OSC escape sequences so the OAuth URL can be matched from the raw
// PTY byte stream. Standard ansi-regex pattern — the OSC branch terminates at
// BEL/ST and so cannot greedily swallow the URL.
const ANSI_RE =
  // eslint-disable-next-line no-control-regex
  /[\x1b\x9b][[\]()#;?]*(?:(?:(?:;[-a-zA-Z\d/#&.:=?%@~_]+)*|[a-zA-Z\d]+(?:;[-a-zA-Z\d/#&.:=?%@~_]*)*)?\x07|(?:\d{1,4}(?:;\d{0,4})*)?[\dA-PR-TZcf-ntqry=><~])/g

// The Claude CLI renders the OAuth URL inside an ink panel sized to the (often
// narrow) login-dialog terminal. Ink HARD-wraps the URL to that width, emitting
// real line breaks (plus left/right panel padding) between segments — so in the
// raw PTY byte stream the URL is split across several physical lines, each a run
// of URL characters with no internal whitespace. Matching the start line alone
// (the old single-line regex) captured only the first segment, e.g. up to
// "...&re". We must reassemble across the wrapped lines.
const URL_START_RE = /https:\/\/(?:claude\.com|console\.anthropic\.com)\/[^\s'"]*/
// A wrap-continuation line is a single run of URL chars (no internal whitespace,
// once padding is trimmed). A blank or prose line — which always follows the URL
// block ("Paste code here if prompted") — has internal spaces / is empty, so it
// fails this and terminates the URL.
const URL_CONT_RE = /^[^\s'"]+$/

// Strip ANSI, then concatenate the line that begins the URL with the following
// wrap-continuation lines. Only return once a terminator line is seen: this
// guards against locking a half-streamed URL whose tail hasn't arrived yet.
export function extractLoginUrl(buffer: string): string | null {
  const lines = buffer.replace(ANSI_RE, '').split(/[\r\n]+/)
  for (let i = 0; i < lines.length; i++) {
    const start = lines[i].match(URL_START_RE)
    if (!start) continue
    let url = start[0]
    let terminated = false
    for (let j = i + 1; j < lines.length; j++) {
      const seg = lines[j].trim()
      if (!URL_CONT_RE.test(seg)) {
        terminated = true // blank/prose line ends the URL block
        break
      }
      url += seg
    }
    if (terminated) return url
  }
  return null
}
