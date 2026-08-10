import { describe, expect, it } from 'vitest';
import { isIMEComposing } from './ime';

describe('isIMEComposing', () => {
  it('returns false for plain Enter keydown', () => {
    const e = new KeyboardEvent('keydown', { key: 'Enter' });
    expect(isIMEComposing(e)).toBe(false);
  });

  it('returns true when nativeEvent.isComposing is true', () => {
    const native = new KeyboardEvent('keydown', { key: 'Enter' });
    Object.defineProperty(native, 'isComposing', { value: true });
    expect(isIMEComposing(native)).toBe(true);
  });

  it('returns true for legacy keyCode 229', () => {
    const native = new KeyboardEvent('keydown', { key: 'Process' });
    Object.defineProperty(native, 'keyCode', { value: 229 });
    expect(isIMEComposing(native)).toBe(true);
  });

  it('unwraps React synthetic event via nativeEvent', () => {
    const native = new KeyboardEvent('keydown', { key: 'Enter' });
    Object.defineProperty(native, 'isComposing', { value: true });
    const synthetic = { nativeEvent: native } as unknown as React.KeyboardEvent;
    expect(isIMEComposing(synthetic)).toBe(true);
  });

  it('returns false when not composing and no legacy keyCode', () => {
    const e = new KeyboardEvent('keydown', { key: ' ' });
    expect(isIMEComposing(e)).toBe(false);
  });
});
