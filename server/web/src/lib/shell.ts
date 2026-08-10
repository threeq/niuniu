import { api } from './api'

// Reveals a workspace/repository path in the host OS file manager. Personal
// edition only — relies on the niuniu-server process running on the same
// machine as the user. Backend rejects relative or non-existent paths.
export async function openInExplorer(path: string): Promise<void> {
  await api.post('/shell/open-path', { path })
}

// Opens a new OS-native terminal window in the user's home directory and runs
// `claude` so the user can complete the CLI login flow. Personal edition only;
// in hosted deployments the backend returns 403 not_supported_in_hosted_mode.
// Frontend should additionally hide the button via info.personal_mode.
export async function openClaudeLoginTerminal(): Promise<void> {
  await api.post('/shell/claude-login', {})
}

// Codex twin: opens a terminal in the user's home directory and runs
// `codex login`. Same personal-mode constraints as openClaudeLoginTerminal.
export async function openCodexLoginTerminal(): Promise<void> {
  await api.post('/shell/codex-login', {})
}

// Opens an http/https URL in the user's default OS browser via the backend
// shell. Only meaningful in personal/bundled mode where niuniu-server runs
// locally; team/hosted callers should use window.open(url, '_blank') instead
// so the URL opens in the user's browser, not on the server host.
export async function openExternalUrl(url: string): Promise<void> {
  await api.post('/shell/open-external', { url })
}

// Raises the native "AI 直达" aggregation window (personal/desktop edition only).
// The SPA runs inside the Wails webview but can't call the Wails runtime
// directly, so we signal the local server, which publishes an event the desktop
// shell's SSE listener acts on. Returns 403 in hosted mode (button is hidden there).
export async function openAIWindow(): Promise<void> {
  await api.post('/shell/open-ai-window', {})
}

// Opens an external URL the right way for the current runtime.
//
// In personal/bundled mode the app runs inside a Wails webview (macOS WKWebView)
// that has no browser chrome: window.open(url, '_blank') and <a target="_blank">
// are silently swallowed, so links appear "dead" — clicking does nothing. We
// must round-trip through the backend shell so the OS default browser opens.
// In hosted/browser mode window.open is correct (the backend can't open a
// browser on the server host). Falls back to window.open if the shell call
// fails, so a backend hiccup still gives the user a chance to navigate.
export async function openExternalSmart(url: string, personalMode: boolean): Promise<void> {
  if (!personalMode) {
    window.open(url, '_blank', 'noopener,noreferrer')
    return
  }
  try {
    await openExternalUrl(url)
  } catch {
    window.open(url, '_blank', 'noopener,noreferrer')
  }
}
