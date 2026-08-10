import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { queryDesktopHotkey, setDesktopHotkey } from './desktop-hotkey'

// desktop-hotkey posts over the same raw bridge as the runner (WebView2 on
// Windows / WKWebView on macOS). These tests lock the SPA→desktop message
// contract the Go handler (runnerwin.go HandleRawWebviewMessage) parses.

function installBridge(posted: unknown[]) {
  ;(window as unknown as { chrome?: unknown }).chrome = {
    webview: { postMessage: (m: string) => posted.push(JSON.parse(m)) },
  }
}

function removeBridge() {
  delete (window as unknown as { chrome?: unknown }).chrome
  delete (window as unknown as { webkit?: unknown }).webkit
}

describe('desktop-hotkey bridge', () => {
  let posted: unknown[]
  beforeEach(() => {
    posted = []
    installBridge(posted)
  })
  afterEach(removeBridge)

  it('queryDesktopHotkey defaults to the ai target', () => {
    expect(queryDesktopHotkey()).toBe(true)
    expect(posted).toContainEqual({ type: 'niuniu-hotkey-query', target: 'ai' })
  })

  it('queryDesktopHotkey carries an explicit window target', () => {
    expect(queryDesktopHotkey('window')).toBe(true)
    expect(posted).toContainEqual({ type: 'niuniu-hotkey-query', target: 'window' })
  })

  it('setDesktopHotkey posts target + enabled + accelerator', () => {
    expect(setDesktopHotkey(true, 'Ctrl+Shift+Z')).toBe(true)
    expect(posted).toContainEqual({
      type: 'niuniu-hotkey-set',
      target: 'ai',
      enabled: true,
      accelerator: 'Ctrl+Shift+Z',
    })
  })

  it('setDesktopHotkey carries the window target', () => {
    expect(setDesktopHotkey(true, 'Ctrl+Shift+N', 'window')).toBe(true)
    expect(posted).toContainEqual({
      type: 'niuniu-hotkey-set',
      target: 'window',
      enabled: true,
      accelerator: 'Ctrl+Shift+N',
    })
  })

  it('setDesktopHotkey carries a disable request', () => {
    expect(setDesktopHotkey(false, 'Cmd+Shift+Z')).toBe(true)
    expect(posted).toContainEqual({
      type: 'niuniu-hotkey-set',
      target: 'ai',
      enabled: false,
      accelerator: 'Cmd+Shift+Z',
    })
  })

  it('returns false with no desktop bridge (plain browser)', () => {
    removeBridge()
    expect(queryDesktopHotkey()).toBe(false)
    expect(setDesktopHotkey(true, 'Ctrl+Shift+Z')).toBe(false)
  })
})
