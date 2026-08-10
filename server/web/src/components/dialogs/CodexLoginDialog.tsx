import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
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

interface CodexLoginDialogProps {
  accountId: number
  wsToken: string
  accountName: string
  onClose: () => void
}

// Mirror of ClaudeLoginDialog but routed to /ws/codex-accounts/:id/login-pty.
// Bridges the user's browser to a `codex auth login` PTY running on the
// server so the OAuth flow completes inside niuniu instead of requiring
// shell access.
export function CodexLoginDialog({
  accountId,
  wsToken,
  accountName,
  onClose,
}: CodexLoginDialogProps) {
  const { t } = useTranslation('codex-accounts')
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)
  const [open, setOpen] = useState(true)
  const termInstanceRef = useRef<Terminal | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const resizeObserverRef = useRef<ResizeObserver | null>(null)
  const rafIdRef = useRef<number | null>(null)
  const fitTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const teardown = useCallback(() => {
    if (rafIdRef.current !== null) cancelAnimationFrame(rafIdRef.current)
    if (fitTimerRef.current !== null) clearTimeout(fitTimerRef.current)
    resizeObserverRef.current?.disconnect()
    wsRef.current?.close()
    termInstanceRef.current?.dispose()
    rafIdRef.current = null
    fitTimerRef.current = null
    resizeObserverRef.current = null
    wsRef.current = null
    termInstanceRef.current = null
    fitAddonRef.current = null
  }, [])

  const termContainerRef = useCallback(
    (node: HTMLDivElement | null) => {
      if (!node) {
        teardown()
        return
      }
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

      const params = new URLSearchParams({ ws_token: wsToken })
      const jwt = getAccessToken()
      if (jwt) params.set('token', jwt)
      const wsUrl = `/ws/codex-accounts/${accountId}/login-pty?${params.toString()}`

      const ws = new WebSocket(wsUrl)
      ws.binaryType = 'arraybuffer'
      wsRef.current = ws

      ws.onopen = () => {
        ws.send(JSON.stringify({
          type: 'resize', cols: terminal.cols, rows: terminal.rows,
        }))
      }
      ws.onmessage = (evt) => {
        if (evt.data instanceof ArrayBuffer) {
          terminal.write(new TextDecoder().decode(evt.data))
        } else if (typeof evt.data === 'string') {
          terminal.write(evt.data)
        }
      }
      ws.onclose = () => { terminal.write('\r\n[disconnected]\r\n') }
      ws.onerror = () => { terminal.write('\r\n[connection error]\r\n') }
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

  useEffect(() => () => teardown(), [teardown])

  function handleOpenChange(v: boolean) {
    if (!v) {
      wsRef.current?.close()
      setOpen(false)
      onClose()
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('loginDialog.title', { name: accountName })}</DialogTitle>
          <DialogDescription>{t('loginDialog.instruction')}</DialogDescription>
        </DialogHeader>
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
