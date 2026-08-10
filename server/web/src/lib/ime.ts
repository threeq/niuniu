import type { KeyboardEvent as ReactKeyboardEvent } from 'react';

/**
 * Returns true if the keyboard event is part of an active IME composition
 * (Chinese / Japanese / Korean text being composed before commit).
 *
 * Detection covers two signals:
 *   1. KeyboardEvent.isComposing — set by Chromium/Firefox/Safari during
 *      composition. React forwards this on `event.nativeEvent.isComposing`.
 *   2. KeyboardEvent.keyCode === 229 — legacy "Process" key code that some
 *      Windows IMEs still emit when isComposing fails to land. Cheap fallback.
 */
export function isIMEComposing(
  e: ReactKeyboardEvent | KeyboardEvent,
): boolean {
  const native: KeyboardEvent = 'nativeEvent' in e ? e.nativeEvent : e;
  if (native.isComposing) return true;
  // eslint-disable-next-line deprecation/deprecation
  if (native.keyCode === 229) return true;
  return false;
}
