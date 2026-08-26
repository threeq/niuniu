import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Check } from 'lucide-react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { getAccessToken } from '@/stores/auth-store'
import { useThemeStore } from '@/stores/theme-store'
import { LIGHT_TERMINAL_THEME, DARK_TERMINAL_THEME } from '@/lib/terminal-themes'
import { extractLoginUrl } from '@/components/dialogs/extract-login-url'

/**
 * CLILoginTerminalDialog hosts the interactive `claude` / `codex login` flow in
 * an xterm.js terminal wired to a server-side PTY (GET /ws/cli-login/:tool/terminal).
 *
 * This is the team-edition login path (#677). Personal mode can pop a native
 * terminal window on the user's own desktop, but the team server runs in a
 * container with no display, so previously there was no way to authenticate the
 * CLIs at all — the buttons were hidden and /api/shell/*-login returned 403.
 *
 * Deliberately NOT reusing useTerminal/terminal-store: those key connections by
 * workspace id and keep the socket alive across unmounts so scrollback survives
 * tab switches. A login session wants the opposite — one socket per dialog,
 * closed on dismiss, so a half-finished OAuth prompt never lingers server-side.
 */
export function CLILoginTerminalDialog(props: {
  tool: 'claude' | 'codex' | null
  onClose: () => void
  /** Called after the dialog closes so the caller can re-probe login status. */
  onFinished?: () => void
}) {
  const { tool, onClose, onFinished } = props
  const { t } = useTranslation('settings')
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)
  const [loginUrl, setLoginUrl] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const roRef = useRef<ResizeObserver | null>(null)
  const rafRef = useRef<number | null>(null)
  const copiedTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  // Accumulated PTY text, scanned to reassemble the OAuth URL that the CLI
  // hard-wraps across several lines. Capped so a long session can't grow it
  // without bound.
  const bufRef = useRef('')
  const urlFoundRef = useRef(false)
  // `t` is read inside the connect callback but must not re-trigger it:
  // i18next hands back a new function identity on store events, and
  // reconnecting would drop a live PTY mid-OAuth.
  const tRef = useRef(t)
  tRef.current = t

  const teardown = useCallback(() => {
    if (rafRef.current !== null) cancelAnimationFrame(rafRef.current)
    if (copiedTimerRef.current !== null) clearTimeout(copiedTimerRef.current)
    roRef.current?.disconnect()
    // Close the socket before disposing the terminal: the server tears the PTY
    // down when the socket drops, so this also kills a login left mid-flow.
    wsRef.current?.close()
    termRef.current?.dispose()
    rafRef.current = null
    copiedTimerRef.current = null
    roRef.current = null
    wsRef.current = null
    termRef.current = null
    fitRef.current = null
    bufRef.current = ''
    urlFoundRef.current = false
  }, [])

  // Callback ref rather than useEffect + hostRef: Radix Dialog renders content
  // inside a Portal driven by Presence, so the inner div mounts on a LATER
  // commit than the dialog's first render. An effect reading ref.current can
  // therefore see null and bail out permanently, leaving a blank terminal. A
  // callback ref fires the instant React attaches the node.
  const termContainerRef = useCallback(
    (node: HTMLDivElement | null) => {
      if (!node) {
        teardown()
        return
      }
      if (termRef.current) return // already initialized for this mount cycle
      if (!tool) return

      const terminal = new Terminal({
        cursorBlink: true,
        fontSize: 13,
        fontFamily:
          '"JetBrains Mono", "Fira Code", Consolas, "Microsoft YaHei UI", "PingFang SC", "Hiragino Sans GB", "Noto Sans Mono CJK SC", "Noto Sans CJK SC", monospace',
        theme: resolvedTheme === 'dark' ? DARK_TERMINAL_THEME : LIGHT_TERMINAL_THEME,
      })
      const fit = new FitAddon()
      terminal.loadAddon(fit)
      terminal.loadAddon(new WebLinksAddon())
      terminal.open(node)
      termRef.current = terminal
      fitRef.current = fit

      const sendResize = () => {
        try {
          fit.fit()
        } catch {
          /* layout not ready */
        }
        if (wsRef.current?.readyState === WebSocket.OPEN) {
          wsRef.current.send(
            JSON.stringify({ type: 'resize', cols: terminal.cols, rows: terminal.rows }),
          )
        }
      }

      // Defer the first fit: the dialog's mount animation can report 0×0 in the
      // same frame the node attaches, which would size the PTY to 0 columns.
      rafRef.current = requestAnimationFrame(sendResize)

      const token = getAccessToken()
      const tokenParam = token ? `?token=${encodeURIComponent(token)}` : ''
      const ws = new WebSocket(`/ws/cli-login/${tool}/terminal${tokenParam}`)
      ws.binaryType = 'arraybuffer'
      wsRef.current = ws

      ws.onopen = () => sendResize()

      const ingest = (text: string) => {
        terminal.write(text)
        if (urlFoundRef.current) return
        bufRef.current = (bufRef.current + text).slice(-32768)
        const url = extractLoginUrl(bufRef.current)
        if (url) {
          urlFoundRef.current = true
          setLoginUrl(url)
        }
      }

      ws.onmessage = (ev) => {
        if (ev.data instanceof ArrayBuffer) ingest(new TextDecoder().decode(ev.data))
        else if (typeof ev.data === 'string') ingest(ev.data)
      }
      ws.onerror = () => {
        terminal.write(`\r\n\x1b[31m${tRef.current('systemDeps.tool.loginTerminalError')}\x1b[0m\r\n`)
      }
      ws.onclose = () => {
        terminal.write(`\r\n\x1b[33m${tRef.current('systemDeps.tool.loginTerminalClosed')}\x1b[0m\r\n`)
      }

      terminal.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) ws.send(data)
      })

      const ro = new ResizeObserver(sendResize)
      ro.observe(node)
      roRef.current = ro
    },
    // resolvedTheme is captured at open time on purpose: re-running this would
    // reconnect the PTY and lose an in-flight login.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [tool, teardown],
  )

  // The callback ref's null branch does not fire on every unmount path, so
  // guarantee cleanup here too.
  useEffect(() => () => teardown(), [teardown])

  async function handleCopy() {
    if (!loginUrl) return
    try {
      await navigator.clipboard.writeText(loginUrl)
    } catch {
      return // insecure context / no clipboard permission; the URL is selectable
    }
    setCopied(true)
    if (copiedTimerRef.current !== null) clearTimeout(copiedTimerRef.current)
    copiedTimerRef.current = setTimeout(() => setCopied(false), 2000)
  }

  const label = tool === 'claude' ? 'Claude Code CLI' : 'Codex CLI'

  return (
    <Dialog
      open={!!tool}
      onOpenChange={(open) => {
        if (!open) {
          setLoginUrl(null)
          setCopied(false)
          onClose()
          onFinished?.()
        }
      }}
    >
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('systemDeps.tool.loginTerminalTitle', { tool: label })}</DialogTitle>
          <DialogDescription>{t('systemDeps.tool.loginTerminalHint')}</DialogDescription>
        </DialogHeader>
        {/* The CLI hard-wraps the OAuth URL to the terminal width, so copying it
            out of the xterm by hand yields a broken link. Surface the
            reassembled URL as one clickable/copyable line. */}
        {loginUrl && (
          <div className="flex items-center gap-2 rounded border bg-muted/40 p-2">
            <a
              href={loginUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="flex-1 truncate text-xs text-blue-600 hover:underline"
            >
              {loginUrl}
            </a>
            <Button size="sm" variant="outline" onClick={handleCopy}>
              {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
              <span className="ml-1">{t('systemDeps.tool.loginCopyUrl')}</span>
            </Button>
          </div>
        )}
        <div ref={termContainerRef} className="h-80 w-full rounded border bg-black p-2" />
      </DialogContent>
    </Dialog>
  )
}
