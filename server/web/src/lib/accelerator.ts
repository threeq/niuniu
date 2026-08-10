/**
 * Global-hotkey accelerator helpers for the desktop 通用设置 keybinding UI.
 *
 * The accelerator string format ("Ctrl+Shift+Z" / "Cmd+Shift+Z") is the exact
 * contract the desktop shell parses in Go (internal/hotkey.ParseAccelerator):
 * "+"-joined modifier tokens (Ctrl / Cmd / Alt / Option / Shift) followed by a
 * single key (A–Z, 0–9, or Space). Keep the token names in sync with the Go
 * parser on both platforms.
 */

/** True when running on macOS — decides Cmd/Option vs Ctrl/Alt token names. */
export function isMacPlatform(): boolean {
  if (typeof navigator === 'undefined') return false;
  const p = `${navigator.platform} ${navigator.userAgent}`.toLowerCase();
  return p.includes('mac');
}

/** Modifier keys that must not be captured as the accelerator's main key. */
const MODIFIER_KEYS = new Set(['Control', 'Shift', 'Alt', 'Meta', 'OS']);

/**
 * Map a KeyboardEvent's non-modifier key to a canonical accelerator key token,
 * or null when it isn't a bindable key (letters, digits and Space only — the set
 * the Go parser accepts). Uses `event.code` so the physical key is stable across
 * Shift/keyboard layouts (Shift+Z stays "Z", not the shifted glyph).
 */
export function keyTokenFromEvent(e: Pick<KeyboardEvent, 'code' | 'key'>): string | null {
  const code = e.code || '';
  if (/^Key[A-Z]$/.test(code)) return code.slice(3); // KeyZ -> Z
  if (/^Digit[0-9]$/.test(code)) return code.slice(5); // Digit9 -> 9
  if (code === 'Space') return 'Space';
  // Fall back to a single printable character from `key`.
  const k = e.key || '';
  if (/^[a-zA-Z0-9]$/.test(k)) return k.toUpperCase();
  return null;
}

/**
 * Build an accelerator string from a keydown event, or null when the event has
 * no non-modifier key yet (user is still holding modifiers) or the key is not
 * bindable. Requires at least one modifier — a bare key is an unsafe global
 * binding and is rejected (returns null).
 */
export function formatAcceleratorFromEvent(
  e: Pick<KeyboardEvent, 'code' | 'key' | 'ctrlKey' | 'shiftKey' | 'altKey' | 'metaKey'>,
  mac = isMacPlatform(),
): string | null {
  const key = e.key || '';
  if (MODIFIER_KEYS.has(key)) return null; // modifier-only, keep waiting
  const token = keyTokenFromEvent(e);
  if (!token) return null;

  const mods: string[] = [];
  if (e.ctrlKey) mods.push('Ctrl');
  if (e.metaKey) mods.push(mac ? 'Cmd' : 'Ctrl');
  if (e.altKey) mods.push(mac ? 'Option' : 'Alt');
  if (e.shiftKey) mods.push('Shift');
  // De-dup (metaKey → Ctrl on non-mac could collide with ctrlKey).
  const uniq = mods.filter((m, i) => mods.indexOf(m) === i);
  if (uniq.length === 0) return null; // bare key — refuse

  return [...uniq, token].join('+');
}
