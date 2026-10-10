import { afterEach, describe, expect, it } from 'vitest';
import { installFileDropGuard } from './file-drop-guard';

// jsdom has no DataTransfer; the guard only reads `dataTransfer.types` during
// dragover, so a minimal stand-in is enough to drive it.
function fireDragEvent(type: 'dragover' | 'drop', types?: string[]): Event {
  const e = new Event(type, { cancelable: true, bubbles: true });
  if (types) Object.defineProperty(e, 'dataTransfer', { value: { types } });
  window.dispatchEvent(e);
  return e;
}

describe('installFileDropGuard', () => {
  let cleanup: (() => void) | undefined;

  afterEach(() => {
    cleanup?.();
    cleanup = undefined;
  });

  it('prevents the browser default for file drags on both dragover and drop', () => {
    cleanup = installFileDropGuard();
    // drop's default is the navigation itself; dragover's must be prevented too
    // or the drop never fires and the renderer navigates immediately.
    expect(fireDragEvent('dragover', ['Files']).defaultPrevented).toBe(true);
    expect(fireDragEvent('drop', ['Files']).defaultPrevented).toBe(true);
  });

  it('leaves non-file drags untouched', () => {
    cleanup = installFileDropGuard();
    // Dragging selected text into an input is a normal browser affordance.
    expect(fireDragEvent('dragover', ['text/plain']).defaultPrevented).toBe(false);
    expect(fireDragEvent('drop', ['text/plain']).defaultPrevented).toBe(false);
    // A drop with no payload at all must not throw either.
    expect(fireDragEvent('drop').defaultPrevented).toBe(false);
  });

  it('stops guarding after cleanup (listeners removed)', () => {
    const stop = installFileDropGuard();
    stop();
    expect(fireDragEvent('drop', ['Files']).defaultPrevented).toBe(false);
  });
});
