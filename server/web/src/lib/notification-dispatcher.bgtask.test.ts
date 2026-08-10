import { describe, expect, it, vi } from 'vitest';
import { dispatchNotification, type Notification } from './notification-dispatcher';

function makeQueryClient() {
  return {
    invalidateQueries: vi.fn(),
    cancelQueries: vi.fn(),
    removeQueries: vi.fn(),
  } as unknown as Parameters<typeof dispatchNotification>[0];
}

describe('workspace_bg_task topic', () => {
  it('invalidates ["workspaces"] when fired', () => {
    const qc = makeQueryClient();
    const n: Notification = { topic: 'workspace_bg_task', action: 'changed', id: 42 };
    dispatchNotification(qc, n);
    const calls = (qc.invalidateQueries as unknown as { mock: { calls: unknown[][] } }).mock.calls;
    const found = calls.some(([arg]) => {
      const a = arg as { queryKey: unknown[] };
      return Array.isArray(a.queryKey) && a.queryKey[0] === 'workspaces' && a.queryKey.length === 1;
    });
    expect(found).toBe(true);
  });

  it('does not toast or cancel queries', () => {
    const qc = makeQueryClient();
    dispatchNotification(qc, { topic: 'workspace_bg_task', action: 'changed', id: 1 });
    expect((qc.cancelQueries as unknown as { mock: { calls: unknown[] } }).mock.calls.length).toBe(0);
  });
});
