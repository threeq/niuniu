import { describe, expect, it } from 'vitest';
import { computeUsagePillTone } from './chat-input-helpers';

describe('computeUsagePillTone', () => {
  it('returns muted (always shown) when context is known but low and rate limit is OK', () => {
    expect(computeUsagePillTone(0, { status: 'allowed' })).toBe('muted');
    expect(computeUsagePillTone(74.9, { status: 'allowed' })).toBe('muted');
    expect(computeUsagePillTone(50, undefined)).toBe('muted');
  });

  it('returns null only when there is no context data and no rate alert', () => {
    expect(computeUsagePillTone(undefined, undefined)).toBeNull();
    expect(computeUsagePillTone(null, null)).toBeNull();
    expect(computeUsagePillTone(undefined, { status: 'allowed' })).toBeNull();
  });

  it('returns info at 75% context (inclusive lower bound)', () => {
    expect(computeUsagePillTone(75, undefined)).toBe('info');
    expect(computeUsagePillTone(79.99, undefined)).toBe('info');
  });

  it('returns warning at 80% context (inclusive lower bound)', () => {
    expect(computeUsagePillTone(80, undefined)).toBe('warning');
    expect(computeUsagePillTone(94.99, undefined)).toBe('warning');
  });

  it('returns critical at 95% context (inclusive lower bound)', () => {
    expect(computeUsagePillTone(95, undefined)).toBe('critical');
    expect(computeUsagePillTone(100, undefined)).toBe('critical');
  });

  it('escalates to critical when rate limit is rejected, regardless of context', () => {
    expect(computeUsagePillTone(0, { status: 'rejected' })).toBe('critical');
    expect(computeUsagePillTone(undefined, { status: 'rejected' })).toBe('critical');
    expect(computeUsagePillTone(50, { status: 'rejected' })).toBe('critical');
  });

  it('escalates to warning when rate limit is allowed_warning and context is low', () => {
    expect(computeUsagePillTone(0, { status: 'allowed_warning' })).toBe('warning');
    expect(computeUsagePillTone(undefined, { status: 'allowed_warning' })).toBe('warning');
  });

  it('lets context override rate-limit when context is more severe', () => {
    // 95% context wins over allowed_warning rate status
    expect(computeUsagePillTone(95, { status: 'allowed_warning' })).toBe('critical');
  });

  it('treats unknown rate-limit status values as no alert (context drives tone)', () => {
    // Unknown rate status contributes nothing; low context still shows as muted.
    expect(computeUsagePillTone(50, { status: 'something_unexpected' })).toBe('muted');
    expect(computeUsagePillTone(75, { status: 'something_unexpected' })).toBe('info');
    // No context + unknown rate status -> nothing to show.
    expect(computeUsagePillTone(undefined, { status: 'something_unexpected' })).toBeNull();
  });
});
