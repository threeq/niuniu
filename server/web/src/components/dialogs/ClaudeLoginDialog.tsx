import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Check, ExternalLink } from 'lucide-react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogFooter,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { getAccessToken } from '@/stores/auth-store'
import { useThemeStore } from '@/stores/theme-store'
import { LIGHT_TERMINAL_THEME, DARK_TERMINAL_THEME } from '@/lib/terminal-themes'
import { extractLoginUrl } from './extract-login-url'

interface ClaudeLoginDialogProps {
  accountId: number
  wsToken: string
  accountName: string
  onClose: () => void
}

// Callback-ref pattern (not useEffect+useRef): Radix Dialog renders content
// inside a Portal that uses Presence; the inner div mounts on a different
// commit pass than the dialog's first render, so a useEffect dependent on
// termRef.current can fire while it is still null. A callback ref fires
// the instant React attaches the DOM node, guaranteeing initialization runs.
export function ClaudeLoginDialog({
  accountId,
  wsToken,
  accountName,
  onClose,
}: ClaudeLoginDialogProps) {
  const { t } = useTranslation('claude-accounts')
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)
  const [open, setOpen] = useState(true)
  const [loginUrl, setLoginUrl] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const termInstanceRef = useRef<Terminal | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const resizeObserverRef = useRef<ResizeObserver | null>(null)
  const rafIdRef = useRef<number | null>(null)
  const fitTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  // Accumulates decoded PTY output (capped) so the URL can be reassembled even
  // when it spans multiple WS frames; once found we stop scanning.
  const outputBufferRef = useRef('')
  // Guards re-scanning the buffer once the URL has been captured.
  const loginUrlFoundRef = useRef(false)
  const copiedTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const teardown = useCallback(() => {
    if (rafIdRef.current !== null) cancelAnimationFrame(rafIdRef.current)
    if (fitTimerRef.current !== null) clearTimeout(fitTimerRef.current)
    if (copiedTimerRef.current !== null) clearTimeout(copiedTimerRef.current)
    resizeObserverRef.current?.disconnect()
    wsRef.current?.close()
    termInstanceRef.current?.dispose()
    rafIdRef.current = null
    fitTimerRef.current = null
    copiedTimerRef.current = null
    resizeObserverRef.current = null
    wsRef.current = null
    termInstanceRef.current = null
    fitAddonRef.current = null
    outputBufferRef.current = ''
  }, [])

  // Callback ref: fires with the node when attached, with null on detach.
  const termContainerRef = useCallback(
    (node: HTMLDivElement | null) => {
      console.debug('[ClaudeLoginDialog] termContainerRef', { hasNode: !!node })
      if (!node) {
        teardown()
        return
      }
      // Already initialized for this mount cycle.
      if (termInstanceRef.current) return

      const terminal = new Terminal({
        cursorBlink: true,
        fontSize: 13,
        fontFamily: '"JetBrains Mono", "Fira Code", Consolas, monospace',
        theme: resolvedTheme === 'dark' ? DARK_TERMINAL_THEME : LIGHT_TERMINAL_THEME,
      })
      const fitAddon = new FitAddon()
      const webLinksAddon = new WebLinksAddon()
      terminal.loadAddon(fitAddon)
      terminal.loadAddon(webLinksAddon)
      terminal.open(node)
      termInstanceRef.current = terminal
      fitAddonRef.current = fitAddon
      console.debug('[ClaudeLoginDialog] terminal.open done')

      // Defer fit() — Dialog mount-anim can yield 0×0 right after open.
      rafIdRef.current = requestAnimationFrame(() => {
        try { fitAddon.fit() } catch { /* layout not ready */ }
        if (wsRef.current?.readyState === WebSocket.OPEN) {
          wsRef.current.send(JSON.stringify({
            type: 'resize', cols: terminal.cols, rows: terminal.rows,
          }))
        }
      })
      fitTimerRef.current = setTimeout(() => {
        try { fitAddon.fit() } catch { /* layout not ready */ }
      }, 100)

      // Relative URL — same idiom as workspace terminal (terminal-store.ts).
      const params = new URLSearchParams({ ws_token: wsToken })
      const jwt = getAccessToken()
      if (jwt) params.set('token', jwt)
      const wsUrl = `/ws/claude-accounts/${accountId}/login-pty?${params.toString()}`

      console.debug('[ClaudeLoginDialog] opening WS', wsUrl)
      const ws = new WebSocket(wsUrl)
      ws.binaryType = 'arraybuffer'
      wsRef.current = ws

      ws.onopen = () => {
        console.debug('[ClaudeLoginDialog] WS open')
        ws.send(JSON.stringify({
          type: 'resize', cols: terminal.cols, rows: terminal.rows,
        }))
      }

      const ingest = (text: string) => {
        terminal.write(text)
        // Scan only until the URL is found; cap the buffer to bound memory.
        if (loginUrlFoundRef.current) return
        outputBufferRef.current = (outputBufferRef.current + text).slice(-32768)
        const url = extractLoginUrl(outputBufferRef.current)
        if (url) {
          loginUrlFoundRef.current = true
          setLoginUrl(url)
        }
      }

      ws.onmessage = (evt) => {
        if (evt.data instanceof ArrayBuffer) {
          const decoder = new TextDecoder()
          ingest(decoder.decode(evt.data))
        } else if (typeof evt.data === 'string') {
          ingest(evt.data)
        }
      }

      ws.onclose = (e) => {
        console.debug('[ClaudeLoginDialog] WS close', { code: e.code, reason: e.reason })
        terminal.write('\r\n[disconnected]\r\n')
      }

      ws.onerror = (e) => {
        console.error('[ClaudeLoginDialog] WS error', e)
        terminal.write('\r\n[connection error]\r\n')
      }

      terminal.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) ws.send(data)
      })

      const ro = new ResizeObserver(() => {
        try { fitAddon.fit() } catch { /* layout not ready */ }
        if (ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({
            type: 'resize', cols: terminal.cols, rows: terminal.rows,
          }))
        }
      })
      ro.observe(node)
      resizeObserverRef.current = ro
    },
    [accountId, wsToken, resolvedTheme, teardown],
  )

  // Fallback: if the component unmounts while node is still attached,
  // teardown won't fire via callback ref → ensure cleanup runs.
  useEffect(() => () => teardown(), [teardown])

  function handleOpenChange(v: boolean) {
    if (!v) {
      wsRef.current?.close()
      setOpen(false)
      onClose()
    }
  }

  async function handleCopy() {
    if (!loginUrl) return
    try {
      await navigator.clipboard.writeText(loginUrl)
    } catch {
      // Clipboard API unavailable (insecure context): fall back to selecting
      // the text so the user can copy manually.
      const el = document.getElementById('claude-login-url')
      const sel = window.getSelection()
      if (el && sel) {
        const range = document.createRange()
        range.selectNodeContents(el)
        sel.removeAllRanges()
        sel.addRange(range)
      }
      return
    }
    setCopied(true)
    if (copiedTimerRef.current !== null) clearTimeout(copiedTimerRef.current)
    copiedTimerRef.current = setTimeout(() => setCopied(false), 2000)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('loginDialog.title', { name: accountName })}</DialogTitle>
          <DialogDescription>{t('loginDialog.instruction')}</DialogDescription>
        </DialogHeader>

        {loginUrl && (
          <div className="rounded-md border border-border bg-muted/50 p-3">
            <div className="mb-2 text-xs font-medium text-muted-foreground">
              {t('loginDialog.urlLabel')}
            </div>
            <code
              id="claude-login-url"
              className="block max-h-24 overflow-y-auto break-all select-all font-mono text-xs text-foreground"
            >
              {loginUrl}
            </code>
            <div className="mt-3 flex gap-2">
              <Button size="sm" variant="secondary" onClick={handleCopy}>
                {copied ? (
                  <Check className="size-4" />
                ) : (
                  <Copy className="size-4" />
                )}
                {copied ? t('loginDialog.copied') : t('loginDialog.copyUrl')}
              </Button>
              <Button size="sm" variant="ghost" asChild>
                <a href={loginUrl} target="_blank" rel="noreferrer noopener">
                  <ExternalLink className="size-4" />
                  {t('loginDialog.openInBrowser')}
                </a>
              </Button>
            </div>
          </div>
        )}

        <div
          ref={termContainerRef}
          className="w-full rounded border border-border overflow-hidden"
          style={{
            height: 420,
            backgroundColor: resolvedTheme === 'dark' ? '#0c0c0c' : '#ffffff',
          }}
        />

        <DialogFooter>
          <Button variant="ghost" onClick={() => handleOpenChange(false)}>
            {t('loginDialog.cancel')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
