const TEXT_INPUT_SELECTOR = [
  'textarea',
  'input[type="text"]',
  'input[type="search"]',
  'input[type="email"]',
  'input[type="url"]',
  'input[type="tel"]',
  'input[type="password"]',
  'input:not([type])',
  '[contenteditable="true"]',
  '[contenteditable=""]',
  '[contenteditable="plaintext-only"]',
].join(',');

function isTextInput(el: EventTarget | null): el is HTMLElement {
  return el instanceof HTMLElement && el.matches(TEXT_INPUT_SELECTOR);
}

/**
 * id of the top-left 牛牛 brand element in the global nav. On window
 * reactivation, focus is parked here first (a real, visible element) before
 * being handed back to the last active input — see installIMECaretFix.
 */
export const IME_BRAND_FOCUS_ID = 'niuniu-brand';

/**
 * Global workaround for WebView2 (Windows desktop) IME context staleness on
 * window reactivation. Call once at app boot.
 *
 * Symptom: Alt-Tab away from niuniu and back, and intermittently the chat
 * <textarea> won't accept Chinese — it may stay in English (holding whatever
 * conversion mode the other app left), or drop input entirely. WebView2 keeps
 * the editable element "focused" the whole time the window is backgrounded, so
 * on return it never re-attaches a fresh IME input context. Manually clicking
 * out of and back into the input fixes it (that re-attaches the context) — the
 * tell-tale of a focus-regain IME re-arm bug, which this code automates.
 *
 * Host-mode verification (ws-683, 2026-06-25): the desktop app runs WebView2 in
 * WINDOWED hosting — go-webview2 v1.0.23 only ever calls
 * CreateCoreWebView2Controller(hwnd); it has no composition controller, and
 * niuniu sets no frameless/transparency/backdrop window options. So the earlier
 * "composition / visual hosting" hypothesis is ruled out, and Microsoft's
 * visual-hosting focus-regain IME release-note fix does NOT apply here. The real
 * upstream gap is in Wails v3: on WM_ACTIVATE/WA_ACTIVE (Alt-Tab) it emits
 * WindowActive but never re-issues controller.MoveFocus — only WM_SETFOCUS on
 * the frame does — so the webview's IME context is not reliably re-armed on
 * reactivation. This DOM-level fix compensates without an upstream patch.
 *
 * Strategy (split the blur/focus across the real window lifecycle):
 *   1. On `focusin`, remember the focused editable element (`lastEditable`);
 *      clear it when focus moves to a non-editable element.
 *   2. On window `blur` (the window is losing focus), move DOM focus onto an
 *      offscreen focus sink — ALWAYS, whatever was focused — so the window is
 *      always backgrounded with a clean, non-input focus and WebView2 genuinely
 *      detaches any IME context. (Parking only when an input was focused left a
 *      must-reproduce hole: input blurred on the page first → no park → the next
 *      activating click into it came up with a stuck, stale IME.)
 *   3. On window `focus` (reactivation), re-arm the IME with a genuine focus
 *      transition after the window settles (~RESTORE_DELAY_MS; a focus fired too
 *      early does NOT re-attach WebView2's IME). The re-arm is keyed off the live
 *      focus at settle time, so it serves BOTH activation paths:
 *        • Alt-Tab / keyboard: at activation, park focus on the 牛牛 brand
 *          element (a real visible element the offscreen sink alone could not
 *          replace; sink fallback). At settle, focus is still parked there →
 *          refocus `lastEditable`. That post-settle focus is the re-arm.
 *        • Direct click into an input: the click focuses the input itself, but
 *          at activation (pre-settle) WebView2 may attach a stale IME that won't
 *          type and stays stuck. So the click STILL needs the re-arm — but we do
 *          NOT park at activation (parking before the click lands, racing it, is
 *          what broke this before). At settle the clicked input is focused, so we
 *          re-arm it IN PLACE: park off onto the brand, dwell REARM_DWELL_MS,
 *          then refocus it — ending exactly where the user clicked, IME fresh.
 *
 *      An earlier build STOOD DOWN on clicks (assuming the click self-arms); real
 *      hardware showed clicking back into the window then failed intermittently,
 *      so clicks get the re-arm too — just deferred past the click, not racing
 *      it. `clickActivating` (set on pointerdown, reset on blur) only decides
 *      whether to park at activation, not whether to re-arm.
 *
 * Doing the blur on window-blur and the focus on window-focus (rather than a
 * single in-place blur/focus bounce) means we never toggle focus while the
 * window is active, so this cannot interrupt in-app key handling such as the
 * Shift CN/EN toggle. Text inputs preserve selectionStart/End across blur+
 * focus, so the caret position is kept.
 *
 * Returns a cleanup function (used by tests; not called in production).
 * No-op on non-Windows since the bug is WebView2-specific.
 */
export function installIMECaretFix(): () => void {
  if (typeof navigator === 'undefined' || !navigator.userAgent.includes('Windows')) {
    return () => {};
  }

  // The editable element to restore focus to on reactivation.
  let lastEditable: HTMLElement | null = null;
  // Deferred re-arm timers: restoreTimer waits for the window to settle after
  // activation; reArmTimer is the brief blur-dwell of the in-place re-arm taken
  // when a click has left an editable focused.
  let restoreTimer: ReturnType<typeof setTimeout> | undefined;
  let reArmTimer: ReturnType<typeof setTimeout> | undefined;
  // Settle beat after window activation before the re-arm. A focus fired too
  // early (pre-settle) does NOT re-attach WebView2's IME on the device. Tuned on
  // real hardware (ws-683).
  const RESTORE_DELAY_MS = 88;
  // Blur dwell of the in-place re-arm: how long the clicked input stays parked
  // off before being refocused, so WebView2 registers a genuine unfocus→refocus.
  const REARM_DWELL_MS = 40;
  // True once a pointerdown has happened since the window was last backgrounded
  // — i.e. this reactivation is being driven by a direct click into the page.
  // Reset on window blur.
  let clickActivating = false;

  // Offscreen, programmatically-focusable sink. Parking focus here on window
  // blur takes it off the editable element so the IME context detaches.
  // tabIndex -1 keeps it out of Tab order; it is invisible and inert.
  const sink = document.createElement('div');
  sink.tabIndex = -1;
  sink.setAttribute('aria-hidden', 'true');
  sink.style.cssText =
    'position:fixed;left:-9999px;top:0;width:1px;height:1px;opacity:0;pointer-events:none;';

  const onFocusIn = (e: FocusEvent): void => {
    const target = e.target;
    if (target === sink) return; // our own park focus — don't disturb lastEditable
    if (target instanceof HTMLElement && target.id === IME_BRAND_FOCUS_ID) {
      return; // reactivation park on the brand element — don't disturb lastEditable
    }
    lastEditable = isTextInput(target) ? target : null;
  };

  // The park element for the re-arm: the 牛牛 brand element (a real, visible
  // element that makes WebView2 drop the stale IME attachment where the
  // offscreen sink alone did not); sink fallback if the nav isn't mounted.
  const getParkTarget = (): HTMLElement => document.getElementById(IME_BRAND_FOCUS_ID) ?? sink;

  // Schedule the post-settle re-arm. At fire time (RESTORE_DELAY_MS after
  // activation, i.e. once WebView2 has settled) it looks at what actually holds
  // focus and re-arms the IME with a genuine focus transition:
  //   • An editable is focused — a CLICK landed on it (it was focused at
  //     activation, before settle, so its IME may be stale). Re-arm it IN PLACE:
  //     park off onto the park element, dwell a beat, then refocus it. Ends
  //     exactly where the user clicked, now with a fresh IME context.
  //   • Focus is still parked where we put it (ALT-TAB — nobody grabbed it) →
  //     hand it to the saved input. That post-settle focus is the re-arm.
  const scheduleReArm = (): void => {
    if (restoreTimer !== undefined) clearTimeout(restoreTimer);
    if (reArmTimer !== undefined) {
      clearTimeout(reArmTimer);
      reArmTimer = undefined;
    }
    restoreTimer = setTimeout(() => {
      restoreTimer = undefined;
      const active = document.activeElement;
      const parkTarget = getParkTarget();
      if (isTextInput(active) && active.isConnected) {
        const el = active;
        parkTarget.focus({ preventScroll: true });
        reArmTimer = setTimeout(() => {
          reArmTimer = undefined;
          if (el.isConnected) el.focus({ preventScroll: true });
        }, REARM_DWELL_MS);
      } else if (active === parkTarget && lastEditable && lastEditable.isConnected) {
        lastEditable.focus({ preventScroll: true });
      }
    }, RESTORE_DELAY_MS);
  };

  // A pointerdown between backgrounding and reactivation marks the activation as
  // click-driven. The click focuses the input itself, but at activation time
  // (pre-settle) WebView2 may attach a stale IME context — so the click still
  // needs the re-arm, just done AFTER it settles on the clicked input (running
  // it early, parking before the click lands, is what broke it before). If the
  // window-focus handler already scheduled a re-arm (focus-before-pointerdown
  // order), reschedule so it targets the input the click lands on.
  const onPointerDown = (): void => {
    clickActivating = true;
    if (restoreTimer !== undefined) scheduleReArm();
  };

  const onWindowBlur = (): void => {
    // Each backgrounding resets click tracking; only a click that drives the
    // *next* reactivation should retarget the re-arm.
    clickActivating = false;
    // Any pending re-arm from a previous activation is now stale — drop it.
    if (restoreTimer !== undefined) {
      clearTimeout(restoreTimer);
      restoreTimer = undefined;
    }
    if (reArmTimer !== undefined) {
      clearTimeout(reArmTimer);
      reArmTimer = undefined;
    }
    // ALWAYS park focus onto the offscreen sink while backgrounded, regardless
    // of what currently holds focus, so the window is consistently backgrounded
    // with a clean, non-input focus and WebView2 detaches any IME context.
    //
    // Must-reproduce path this fixes: focus the input, then click a non-editable
    // area on the page so the input blurs (lastEditable→null), switch to another
    // app, then click straight back into the input to activate niuniu. Without
    // an unconditional park, the window was backgrounded with focus still on a
    // page element, and the activating click attached a STALE IME context that
    // would not type and stayed stuck for later clicks too ("feels marked"). The
    // working direct-click path differs only in that the input had been focused
    // (so the old conditional park ran); parking unconditionally equalises them.
    if (document.activeElement && document.activeElement !== sink) {
      sink.focus({ preventScroll: true });
    }
  };

  // On reactivation, re-arm the IME after the window settles (see scheduleReArm).
  // BOTH activation paths need the re-arm — real-device testing showed that
  // clicking back into the window is just as unreliable as Alt-Tab without it
  // (an earlier "stand down on click" attempt left clicks failing intermittently
  // / "feels marked"). The re-arm is keyed off the live focus at settle time, so
  // it adapts: a click that has landed on an input gets that input re-armed in
  // place; an Alt-Tab with focus still parked gets the saved input restored.
  const onWindowFocus = (): void => {
    if (!clickActivating) {
      // Alt-Tab / keyboard (or a click whose pointerdown hasn't fired yet): park
      // off now so the saved input is re-armed from a parked state at settle.
      if (!lastEditable) return; // nothing editable in play — leave focus alone
      getParkTarget().focus({ preventScroll: true });
    }
    // Click activation does NOT park here — parking before the click lands is
    // what broke it before. scheduleReArm re-arms the clicked input in place
    // once it has settled.
    scheduleReArm();
  };

  document.body.appendChild(sink);
  document.addEventListener('focusin', onFocusIn);
  // Capture phase so we observe the click before its focus side effects settle.
  document.addEventListener('pointerdown', onPointerDown, true);
  window.addEventListener('blur', onWindowBlur);
  window.addEventListener('focus', onWindowFocus);

  return () => {
    if (restoreTimer !== undefined) clearTimeout(restoreTimer);
    if (reArmTimer !== undefined) clearTimeout(reArmTimer);
    document.removeEventListener('focusin', onFocusIn);
    document.removeEventListener('pointerdown', onPointerDown, true);
    window.removeEventListener('blur', onWindowBlur);
    window.removeEventListener('focus', onWindowFocus);
    sink.remove();
  };
}
