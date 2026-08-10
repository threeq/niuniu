import { it, expect } from 'vitest'
import { extractLoginUrl } from './extract-login-url'

const FULL_URL =
  'https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference+user%3Asessions%3Aclaude_code+user%3Amcp_servers+user%3Afile_upload&code_challenge=4t_TTCY46Trl9wPOS2XwW9BioAeFVPQ-4ZNEVWLPpLQ&code_challenge_method=S256&state=PaBmDOWNCtAUKPkz_auKItMs_3yxeVjxW7Zo7LKzqUk'

// The CLI hard-wraps the URL to the narrow login terminal, exactly as captured
// in the bug report screenshot (5 physical lines), each padded + ANSI-decorated.
function wrap(url: string, width: number, pad = '  '): string {
  const out: string[] = []
  for (let i = 0; i < url.length; i += width) {
    // \x1b[2K = clear line, \x1b[39m = default fg — the kind of ANSI ink emits.
    out.push(`\x1b[2K${pad}\x1b[39m${url.slice(i, i + width)}`)
  }
  return out.join('\r\n')
}

it('reassembles an OAuth URL hard-wrapped across multiple lines', () => {
  const buffer =
    `\x1b[2K  Browser didn't open? Use the url below to sign in (c to copy)\r\n\r\n` +
    wrap(FULL_URL, 90) +
    `\r\n\r\n\x1b[2K  Paste code here if prompted >\r\n`
  // Sanity: the URL is genuinely fragmented in the buffer (the old single-line
  // regex would only ever recover the first wrapped segment).
  expect(buffer).not.toContain(FULL_URL)
  expect(extractLoginUrl(buffer)).toBe(FULL_URL)
})

it('handles a URL that fits on a single line', () => {
  const buffer = `\x1b[2K  ${FULL_URL}\r\n\r\n  Paste code here if prompted >\r\n`
  expect(extractLoginUrl(buffer)).toBe(FULL_URL)
})

it('handles the console.anthropic.com host', () => {
  const url = 'https://console.anthropic.com/oauth/authorize?code=true&client_id=abc&state=xyz'
  const buffer = `${wrap(url, 40)}\r\n\r\nPaste code here >\r\n`
  expect(extractLoginUrl(buffer)).toBe(url)
})

it('does not over-join trailing prose into the URL', () => {
  const buffer = `${wrap(FULL_URL, 90)}\r\n\r\nPaste code here if prompted >\r\n`
  const result = extractLoginUrl(buffer)
  expect(result).toBe(FULL_URL)
  expect(result).not.toContain('Paste')
})

it('returns null while the wrapped URL is still streaming (no terminator yet)', () => {
  // First two wrapped segments arrived, but the tail + prose terminator have not.
  const partial = wrap(FULL_URL.slice(0, 180), 90)
  expect(extractLoginUrl(partial)).toBeNull()
})

it('returns null when no login URL is present', () => {
  expect(extractLoginUrl('\x1b[2K  some unrelated terminal output\r\n')).toBeNull()
})
