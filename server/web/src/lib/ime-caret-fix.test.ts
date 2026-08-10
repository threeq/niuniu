import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { IME_BRAND_FOCUS_ID, installIMECaretFix } from './ime-caret-fix';

const ORIGINAL_UA = navigator.userAgent;

function setUserAgent(ua: string): void {
  Object.defineProperty(navigator, 'userAgent', { value: ua, configurable: true });
}

function sinkEl(): HTMLElement | null {
  return document.querySelector('div[aria-hidden="true"][tabindex="-1"]');
}

function mountBrandEl(): HTMLElement {
  const brand = document.createElement('div');
  brand.id = IME_BRAND_FOCUS_ID;
  brand.tabIndex = -1;
  document.body.appendChild(brand);
  return brand;
}

afterEach(() => {
  setUserAgent(ORIGINAL_UA);
  document.body.innerHTML = '';
});

describe('installIMECaretFix', () => {
  describe('on non-Windows platforms', () => {
    beforeEach(() => setUserAgent('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)'));

    it('does not attach any listener or create a focus sink', () => {
      const winAdd = vi.spyOn(window, 'addEventListener');
      const docAdd = vi.spyOn(document, 'addEventListener');
      installIMECaretFix();
      expect(winAdd).not.toHaveBeenCalledWith('blur', expect.any(Function));
      expect(winAdd).not.toHaveBeenCalledWith('focus', expect.any(Function));
      expect(docAdd).not.toHaveBeenCalledWith('focusin', expect.any(Function));
      expect(sinkEl()).toBeNull();
    });

    it('returns a no-op cleanup function', () => {
      const cleanup = installIMECaretFix();
      expect(() => cleanup()).not.toThrow();
    });
  });

  describe('on Windows', () => {
    beforeEach(() => {
      setUserAgent('Mozilla/5.0 (Windows NT 10.0; Win64; x64)');
      vi.useFakeTimers();
    });
    afterEach(() => vi.useRealTimers());

    // Settle delay (RESTORE_DELAY_MS) and in-place re-arm blur dwell (REARM_DWELL_MS).
    const DELAY = 88;
    const DWELL = 40;

    it('attaches focusin + window blur/focus listeners and creates a sink', () => {
      const winAdd = vi.spyOn(window, 'addEventListener');
      const docAdd = vi.spyOn(document, 'addEventListener');
      installIMECaretFix();
      expect(docAdd).toHaveBeenCalledWith('focusin', expect.any(Function));
      expect(winAdd).toHaveBeenCalledWith('blur', expect.any(Function));
      expect(winAdd).toHaveBeenCalledWith('focus', expect.any(Function));
      expect(sinkEl()).not.toBeNull();
    });

    it('parks focus off the input on window blur, re-arms it after the delay', () => {
      const ta = document.createElement('textarea');
      document.body.appendChild(ta);
      installIMECaretFix();
      ta.focus();
      expect(document.activeElement).toBe(ta);

      window.dispatchEvent(new Event('blur'));
      expect(document.activeElement).toBe(sinkEl()); // parked onto the sink

      // With no brand mounted the park stays on the sink; the refocus lands on
      // the input only after the settle delay.
      window.dispatchEvent(new Event('focus'));
      expect(document.activeElement).toBe(sinkEl());
      vi.advanceTimersByTime(DELAY);
      expect(document.activeElement).toBe(ta);
    });

    it('parks on the brand element at reactivation, restores the input after the delay', () => {
      const brand = mountBrandEl();
      const ta = document.createElement('textarea');
      document.body.appendChild(ta);
      installIMECaretFix();
      ta.focus();

      window.dispatchEvent(new Event('blur'));
      window.dispatchEvent(new Event('focus'));
      // Parked on the brand (so WebView2 drops the stale IME attachment)…
      expect(document.activeElement).toBe(brand);
      vi.advanceTimersByTime(DELAY - 1);
      expect(document.activeElement).toBe(brand);
      // …and only after the delay does the input get focus back.
      vi.advanceTimersByTime(1);
      expect(document.activeElement).toBe(ta);
    });

    it('re-arms the clicked input in place after a direct-click activation (pointerdown before focus)', () => {
      // A direct click into an input still needs the re-arm — at activation the
      // click focuses the input pre-settle, so WebView2 may attach a stale IME.
      // We do NOT park at activation (racing the click broke it before); at
      // settle we re-arm the clicked input in place (park onto the brand, dwell,
      // refocus), ending exactly where the user clicked.
      const brand = mountBrandEl();
      const prev = document.createElement('textarea');
      const clicked = document.createElement('textarea');
      document.body.append(prev, clicked);
      installIMECaretFix();
      prev.focus(); // becomes lastEditable

      window.dispatchEvent(new Event('blur')); // backgrounded; parks onto sink
      clicked.dispatchEvent(new Event('pointerdown', { bubbles: true }));
      clicked.focus();
      const brandFocus = vi.spyOn(brand, 'focus');
      window.dispatchEvent(new Event('focus'));
      expect(document.activeElement).toBe(clicked); // no park at activation

      vi.advanceTimersByTime(DELAY + DWELL);
      expect(brandFocus).toHaveBeenCalled(); // re-armed in place via the brand
      expect(document.activeElement).toBe(clicked); // and ended back on the click
    });

    it('re-arms the clicked input in place after a direct-click activation (focus before pointerdown)', () => {
      // focus-before-pointerdown order: the window gains focus first and (not yet
      // knowing it's a click) parks on the brand + schedules the re-arm. The
      // click's pointerdown reschedules the re-arm so it targets the input the
      // click lands on — which is re-armed in place, never restored to `prev`.
      const brand = mountBrandEl();
      const prev = document.createElement('textarea');
      const clicked = document.createElement('textarea');
      document.body.append(prev, clicked);
      installIMECaretFix();
      prev.focus(); // lastEditable=prev

      window.dispatchEvent(new Event('blur'));
      window.dispatchEvent(new Event('focus')); // parks on brand, schedules re-arm
      expect(document.activeElement).toBe(brand);
      // The activating click lands during the delay and reschedules the re-arm.
      clicked.dispatchEvent(new Event('pointerdown', { bubbles: true }));
      clicked.focus();
      expect(document.activeElement).toBe(clicked);

      vi.advanceTimersByTime(DELAY + DWELL);
      expect(document.activeElement).toBe(clicked); // ends on the click, not prev
    });

    it('a re-blur before the delay elapses cancels the pending restore', () => {
      const ta = document.createElement('textarea');
      document.body.appendChild(ta);
      installIMECaretFix();
      ta.focus();

      window.dispatchEvent(new Event('blur'));
      window.dispatchEvent(new Event('focus'));
      window.dispatchEvent(new Event('blur')); // backgrounded again
      vi.advanceTimersByTime(DELAY);
      // The stale restore must not fire while backgrounded.
      expect(document.activeElement).not.toBe(ta);
    });

    it('parks onto the sink on blur even when the focused element is not editable', () => {
      // Must-reproduce path: the input blurred to a non-editable element before
      // backgrounding. The park must still run so the window backgrounds with a
      // clean focus (otherwise the next activating click into the input gets a
      // stuck, stale IME).
      mountBrandEl();
      const button = document.createElement('button');
      document.body.appendChild(button);
      installIMECaretFix();
      button.focus();
      expect(document.activeElement).toBe(button);

      window.dispatchEvent(new Event('blur'));
      expect(document.activeElement).toBe(sinkEl()); // parked off, even non-editable

      window.dispatchEvent(new Event('focus'));
      vi.advanceTimersByTime(DELAY);
      // lastEditable was null → no re-arm target → focus stays parked on the sink.
      expect(document.activeElement).toBe(sinkEl());
    });

    it('clears the saved element when focus moves to a non-editable element', () => {
      const ta = document.createElement('textarea');
      const button = document.createElement('button');
      document.body.append(ta, button);
      installIMECaretFix();
      ta.focus(); // saved
      button.focus(); // moves to non-editable -> cleared

      window.dispatchEvent(new Event('blur'));
      window.dispatchEvent(new Event('focus'));
      vi.advanceTimersByTime(DELAY);
      // Nothing should have been re-armed to the textarea.
      expect(document.activeElement).not.toBe(ta);
    });

    it('parking on the brand element does not clear the saved editable', () => {
      const ta = document.createElement('textarea');
      document.body.appendChild(ta);
      mountBrandEl();
      installIMECaretFix();
      ta.focus();

      // First round-trip parks on the brand (focusin on it must not wipe
      // lastEditable, or the second round-trip would restore nothing).
      window.dispatchEvent(new Event('blur'));
      window.dispatchEvent(new Event('focus'));
      vi.advanceTimersByTime(DELAY);
      expect(document.activeElement).toBe(ta);

      window.dispatchEvent(new Event('blur'));
      window.dispatchEvent(new Event('focus'));
      vi.advanceTimersByTime(DELAY);
      expect(document.activeElement).toBe(ta);
    });

    it('cleanup removes the listeners and the sink', () => {
      const winRemove = vi.spyOn(window, 'removeEventListener');
      const docRemove = vi.spyOn(document, 'removeEventListener');
      const cleanup = installIMECaretFix();
      expect(sinkEl()).not.toBeNull();
      cleanup();
      expect(winRemove).toHaveBeenCalledWith('blur', expect.any(Function));
      expect(winRemove).toHaveBeenCalledWith('focus', expect.any(Function));
      expect(docRemove).toHaveBeenCalledWith('focusin', expect.any(Function));
      expect(sinkEl()).toBeNull();
    });
  });
});
