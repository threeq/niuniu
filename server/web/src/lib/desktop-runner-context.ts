/**
 * Desktop local-runner visibility signal.
 *
 * The bottom "local executor" entry (#526·子A) is only meaningful on the
 * niuniu **desktop** app when it is showing a **remote** connection's webview:
 * the local executor bridges the remote worktree to a directory on the user's
 * own machine. A local connection (#0, kindLocal) already runs on the machine,
 * so no bridge is needed and the entry stays hidden.
 *
 * Detection is driven ENTIRELY by the desktop raw-message bridge, which the
 * desktop shell exposes to every webview it hosts — `window.chrome.webview`
 * (WebView2) on Windows, `window.webkit.messageHandlers.external` (WKWebView) on
 * macOS (see `rawBridge`). There is deliberately **no** injected
 * `window.__NIUNIU_DESKTOP__` global anymore: its ExecJS injection was observed
 * not to run reliably in remote SPAs (it raced or never fired), so it was
 * removed — the bridge is the single source of truth. Bridge present + non-local
 * host ⇒ remote desktop connection (entry shown); bridge present + local host ⇒
 * the bundled #0 server (hidden); no bridge ⇒ a plain browser (hidden). There is
 * also no localStorage override: a bypass would let the bar show while the
 * bridge is not really wired, masking real failures (it did exactly that during
 * #526 bring-up).
 */

import { useEffect, useState } from 'react';

export type ConnKind = 'local' | 'remote';

export interface DesktopRunnerContext {
  /** True when the bottom local-runner entry should be shown. */
  available: boolean;
  /** The desktop connection kind, or null when not running in the desktop shell. */
  connKind: ConnKind | null;
}

/** A raw JS→native message channel exposed by the desktop shell's webview. */
interface RawBridge {
  post: (message: string) => void;
}

/**
 * The raw-message bridge the desktop shell exposes to its webview, or null in a
 * plain browser. Two native shapes are supported, one per desktop platform:
 *   - Windows WebView2:  `window.chrome.webview.postMessage(str)`
 *   - macOS WKWebView:   `window.webkit.messageHandlers.external.postMessage(str)`
 * Both deliver the string to Go's Wails RawMessageHandler
 * (`App.HandleRawWebviewMessage`). Detection is host-independent, so it works on
 * a remote-origin SPA where the standard Wails runtime call would not.
 */
function rawBridge(): RawBridge | null {
  try {
    const w = window as unknown as {
      chrome?: { webview?: { postMessage?: (m: string) => void } };
      webkit?: {
        messageHandlers?: { external?: { postMessage?: (m: string) => void } };
      };
    };
    const wv2 = w.chrome?.webview;
    if (typeof wv2?.postMessage === 'function') {
      return { post: (m) => wv2.postMessage!(m) };
    }
    const wk = w.webkit?.messageHandlers?.external;
    if (typeof wk?.postMessage === 'function') {
      return { post: (m) => wk.postMessage!(m) };
    }
    return null;
  } catch {
    return null;
  }
}

/** Local (#0) hostnames — the bundled server; the entry stays hidden there. */
function isLocalHostname(hostname: string): boolean {
  return (
    hostname === 'localhost' ||
    hostname === '127.0.0.1' ||
    hostname === '::1' ||
    hostname === '[::1]'
  );
}

/**
 * The desktop connection key as Go's `connection.KeyFor` produces it: the base
 * URL with its scheme stripped, which is exactly `location.host`
 * (`self.niu6ai.com`, `192.168.1.5:3000`, …). Used to address raw messages back
 * to this window's connection on the Go side.
 */
function connKeyFromLocation(): string {
  try {
    return window.location.host;
  } catch {
    return '';
  }
}

/** The SPA's own origin (e.g. `https://self.niu6ai.com`) — authoritative scheme+host. */
function safeOrigin(): string {
  try {
    return window.location.origin;
  } catch {
    return '';
  }
}

/**
 * The desktop raw-message bridge scoped to a remote connection, present iff the
 * SPA is running inside the niuniu desktop shell's webview (WebView2 on Windows
 * or WKWebView on macOS — see `rawBridge`). This is the sole desktop signal —
 * the SPA drives registration itself off this bridge. Returns null on a local
 * (#0) host so the bundled server stays hidden.
 */
function desktopBridge(): { connKey: string; post: (message: string) => void } | null {
  try {
    const bridge = rawBridge();
    if (!bridge) return null;
    // Local connection #0 (bundled server on localhost) must stay hidden.
    if (isLocalHostname(window.location.hostname)) return null;
    return { connKey: connKeyFromLocation(), post: bridge.post };
  } catch {
    return null;
  }
}

function postDesktopRunnerMessage(payload: Record<string, unknown>): boolean {
  const ctx = desktopBridge();
  if (!ctx) return false;
  try {
    ctx.post(JSON.stringify({ ...payload, connKey: ctx.connKey }));
    return true;
  } catch {
    return false;
  }
}

/** Read the persisted JWT the desktop reverse channel authenticates with. */
function readAccessToken(): string {
  try {
    const raw = localStorage.getItem('niuniu-auth-storage');
    if (!raw) return '';
    const st = JSON.parse(raw) as { state?: { accessToken?: string } };
    return st?.state?.accessToken ?? '';
  } catch {
    return '';
  }
}

/**
 * Register a workspace's local-runner config with the desktop shell so it opens
 * the reverse channel and comes online — the "保存即连接" bridge, driven from the
 * SPA (which can reliably reach Go) rather than from desktop-injected JS (which
 * could not). Posts the auth token first so the runner can authenticate, then
 * the config. No-op outside the desktop shell.
 */
export function harvestRunnerConfigToDesktop(
  workspaceId: string,
  localDir: string,
  workspaceName = '',
): void {
  if (!localDir) return;
  // The SPA's own origin is authoritative for scheme+host (the desktop's
  // recorded connection URL may be http:// even when the server is actually
  // https, which would make the reverse channel dial ws:// and fail the TLS
  // handshake). Send it so Go uses wss://+https for this connection.
  const origin = safeOrigin();
  const token = readAccessToken();
  if (token) {
    postDesktopRunnerMessage({ type: 'niuniu-runner-token', token, origin });
  }
  postDesktopRunnerMessage({
    type: 'niuniu-runner-config',
    workspaceId,
    localDir,
    origin,
    // Human label for the manager UI; omitted when unknown so the desktop keeps
    // its existing value rather than blanking it.
    ...(workspaceName ? { workspaceName } : {}),
  });
}

/** Tell the desktop shell a workspace was unbound (解绑) → stop + remove. */
export function unbindRunnerFromDesktop(workspaceId: string): void {
  postDesktopRunnerMessage({ type: 'niuniu-runner-unbind', workspaceId });
}

/**
 * Push the current auth token to the desktop shell so the reverse channel
 * re-authenticates with a fresh JWT on its next reconnect. The SPA rotates its
 * access token; the desktop reuses a snapshot for the WS handshake, so without
 * this every reconnect after expiry fails with 401. Call on every token change
 * (see main.tsx). No-op outside the desktop shell or on an empty token.
 */
export function postDesktopRunnerToken(token: string): void {
  if (!token) return;
  postDesktopRunnerMessage({ type: 'niuniu-runner-token', token, origin: safeOrigin() });
}

/**
 * True when the desktop can service a native directory picker: we're in a remote
 * desktop webview AND the WebView2 raw-message bridge is present. The config
 * dialog uses this to show/hide the "browse" affordance.
 */
export function desktopDirPickAvailable(): boolean {
  return desktopBridge() !== null;
}

/**
 * Ask the desktop shell to open a native folder picker for the local working
 * directory. The chosen path arrives asynchronously as a `niuniu:runner-dir-picked`
 * CustomEvent on `window` (dispatched by Go via ExecJS). Returns false when no
 * desktop bridge is available (plain browser).
 */
export function requestDesktopDirPick(): boolean {
  return postDesktopRunnerMessage({ type: 'niuniu-runner-pick-dir' });
}

/** Event name Go dispatches into the webview with the picked directory path. */
export const DESKTOP_DIR_PICKED_EVENT = 'niuniu:runner-dir-picked';

/**
 * Post a raw bridge message to the desktop shell regardless of host (local #0
 * OR remote). Unlike `postDesktopRunnerMessage`, this is NOT gated to remote
 * connections — the "在 OpenPencil 中打开" launch is meaningful on the bundled
 * local server too. Returns false in a plain browser (no bridge).
 */
export function postDesktopShellMessage(payload: Record<string, unknown>): boolean {
  const bridge = rawBridge();
  if (!bridge) return false;
  try {
    bridge.post(JSON.stringify(payload));
    return true;
  } catch {
    return false;
  }
}

/** CustomEvent name Go dispatches back with the OpenPencil launch result. */
export const OPEN_PENCIL_RESULT_EVENT = 'niuniu:open-pencil-result';

/** Shape of the `niuniu:open-pencil-result` CustomEvent detail from Go. */
export interface OpenPencilResult {
  ok: boolean;
  /** e.g. 'not_installed' when the app could not be located. */
  reason: string;
}

/**
 * Ask the desktop shell to launch the OpenPencil desktop app (pencil-design
 * scene). Optionally passes a design file path to open. The outcome arrives
 * asynchronously as an `OPEN_PENCIL_RESULT_EVENT` CustomEvent on `window`
 * (dispatched by Go). Returns false when not running in the desktop shell (plain
 * browser) — the caller should then show a "desktop only" hint.
 */
export function requestOpenInPencil(filePath?: string): boolean {
  return postDesktopShellMessage({
    type: 'niuniu-open-pencil',
    ...(filePath ? { filePath } : {}),
  });
}

/**
 * True when the SPA is running inside the niuniu desktop shell's webview (the
 * raw-message bridge is present — WebView2 on Windows or WKWebView on macOS),
 * regardless of host. Used to gate desktop-only wiring like the reverse-channel
 * token keep-alive.
 */
export function isDesktopShell(): boolean {
  return rawBridge() !== null;
}

/**
 * Resolve whether the local-runner bottom entry is available, plus the
 * connection kind. Detection is bridge-only:
 *
 *  1. WebView2 bridge present + non-local host → remote desktop (entry shown).
 *  2. Bridge present + local host → the bundled #0 server (hidden).
 *  3. No bridge → a plain browser (hidden).
 */
export function getDesktopRunnerContext(): DesktopRunnerContext {
  if (desktopBridge()) return { available: true, connKind: 'remote' };
  // Bridge present but local host is filtered out inside desktopBridge().
  if (isDesktopShell()) return { available: false, connKind: 'local' };
  return { available: false, connKind: null };
}

/**
 * Reactive variant of `getDesktopRunnerContext().available` for React views.
 *
 * The desktop bridge (WebView2's `window.chrome.webview` on Windows, WKWebView's
 * `window.webkit.messageHandlers.external` on macOS) is a native object present
 * before any page script runs, so the mount-time read is normally already
 * correct. This hook keeps a bounded poll purely as cheap defense against any
 * webview timing quirk: once available it stops polling and never flips back.
 */
export function useDesktopRunnerAvailable(
  pollMs = 250,
  maxWaitMs = 15000,
): boolean {
  const [available, setAvailable] = useState(
    () => getDesktopRunnerContext().available,
  );
  useEffect(() => {
    if (available) return;
    let waited = 0;
    const timer = window.setInterval(() => {
      waited += pollMs;
      if (getDesktopRunnerContext().available) {
        setAvailable(true);
        window.clearInterval(timer);
      } else if (waited >= maxWaitMs) {
        window.clearInterval(timer);
      }
    }, pollMs);
    return () => window.clearInterval(timer);
  }, [available, pollMs, maxWaitMs]);
  return available;
}
