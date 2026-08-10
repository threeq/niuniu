/**
 * Desktop global-hotkey bridge for the two configurable window toggles:
 * the LOCAL main window ("本地牛牛窗口") and the AI-aggregation window ("AI 直达").
 *
 * The 通用设置 page uses this to read and change each global shortcut, addressed
 * by a `target` ("window" | "ai"). Messages travel over the same raw bridge as
 * the local runner (WebView2 on Windows / WKWebView on macOS — see
 * desktop-runner-context.ts). The desktop replies by dispatching a
 * `niuniu:hotkey-config` CustomEvent (carrying `target`) into the window (see
 * hotkeywin.go).
 */

import { postDesktopShellMessage } from './desktop-runner-context';
import { isMacPlatform } from './accelerator';

/** CustomEvent name the desktop dispatches with the current/updated hotkey state. */
export const DESKTOP_HOTKEY_EVENT = 'niuniu:hotkey-config';

/** Which global hotkey a query/set/config message addresses. */
export type DesktopHotkeyTarget = 'window' | 'ai';

/** The platform-conventional default combo for a target (Cmd on macOS). */
export function defaultAccelerator(target: DesktopHotkeyTarget): string {
  const mac = isMacPlatform();
  if (target === 'window') return mac ? 'Cmd+Shift+N' : 'Ctrl+Shift+N';
  return mac ? 'Cmd+Shift+Z' : 'Ctrl+Shift+Z';
}

/**
 * The desktop shell injects the current hotkey config as `window.__NIUNIU_HOTKEYS__`
 * via a document-created script (reliable, unlike the async query→ExecJS echo — see
 * hotkeywin.go hotkeyBootstrapJS). This is the primary source for the settings UI.
 */
interface HotkeyBootstrapEntry {
  enabled: boolean;
  accelerator: string;
}

/** Read the injected bootstrap state for a target, or null when absent (browser / old shell). */
export function readBootstrapHotkey(target: DesktopHotkeyTarget): HotkeyBootstrapEntry | null {
  try {
    const g = (window as unknown as { __NIUNIU_HOTKEYS__?: Record<string, HotkeyBootstrapEntry> })
      .__NIUNIU_HOTKEYS__;
    const v = g?.[target];
    if (v && typeof v.accelerator === 'string') {
      return { enabled: !!v.enabled, accelerator: v.accelerator };
    }
  } catch {
    /* ignore */
  }
  return null;
}

/** Keep the injected bootstrap global in sync after a change, so a same-session reopen echoes it. */
export function writeBootstrapHotkey(target: DesktopHotkeyTarget, enabled: boolean, accelerator: string): void {
  try {
    const w = window as unknown as { __NIUNIU_HOTKEYS__?: Record<string, HotkeyBootstrapEntry> };
    if (!w.__NIUNIU_HOTKEYS__) w.__NIUNIU_HOTKEYS__ = {};
    w.__NIUNIU_HOTKEYS__[target] = { enabled, accelerator };
  } catch {
    /* ignore */
  }
}

/** Shape of the `niuniu:hotkey-config` CustomEvent detail from the desktop shell. */
export interface DesktopHotkeyConfig {
  /** Which hotkey this state describes (defaults to "ai" from older shells). */
  target: DesktopHotkeyTarget;
  /** Whether this global hotkey is currently registered. */
  enabled: boolean;
  /** The configured accelerator string, e.g. "Cmd+Shift+Z". */
  accelerator: string;
  /** Human label of the combo that actually bound (may differ on OS conflict). */
  label: string;
  /** False when a set request was rejected (e.g. invalid accelerator). */
  ok: boolean;
  /** Reason when ok is false. */
  error: string;
}

/**
 * Ask the desktop shell for a target's hotkey state. The answer arrives
 * asynchronously as a DESKTOP_HOTKEY_EVENT CustomEvent on window. Returns false
 * in a plain browser (no desktop bridge).
 */
export function queryDesktopHotkey(target: DesktopHotkeyTarget = 'ai'): boolean {
  return postDesktopShellMessage({ type: 'niuniu-hotkey-query', target });
}

/**
 * Change / enable / disable a target's global hotkey. `accelerator` is a
 * "+"-joined string like "Ctrl+Shift+Z"; pass the current one when only toggling
 * `enabled`. The result (including validation failures) arrives as a
 * DESKTOP_HOTKEY_EVENT. Returns false in a plain browser.
 */
export function setDesktopHotkey(
  enabled: boolean,
  accelerator: string,
  target: DesktopHotkeyTarget = 'ai',
): boolean {
  return postDesktopShellMessage({
    type: 'niuniu-hotkey-set',
    target,
    enabled,
    accelerator,
  });
}
