import { beforeEach, describe, expect, it, vi } from 'vitest';
import { backfillPendingPermissions } from './backfill-pending-permissions';
import { api, listPermissionRequests } from '@/lib/api';
import { useNotificationWSStore } from '@/stores/notification-ws-store';

vi.mock('@/lib/api', () => ({
  api: { get: vi.fn() },
  listPermissionRequests: vi.fn(),
}));

const ws = (id: string, overrides: Record<string, unknown> = {}) => ({
  id,
  status: 'running',
  is_archived: 0,
  ...overrides,
});

describe('backfillPendingPermissions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useNotificationWSStore.setState({ permissionPendingByWorkspace: {} });
  });

  it('seeds pending request ids for active workspaces only', async () => {
    vi.mocked(api.get).mockResolvedValueOnce([
      ws('1'),
      ws('2', { status: 'completed' }),
      ws('3', { is_archived: 1 }),
    ] as never);
    vi.mocked(listPermissionRequests).mockResolvedValueOnce([
      { id: 11, status: 'pending' },
      { id: 12, status: 'allowed' },
    ] as never);

    await backfillPendingPermissions();

    // Completed/archived sessions cannot block on a request — never queried.
    expect(listPermissionRequests).toHaveBeenCalledTimes(1);
    expect(listPermissionRequests).toHaveBeenCalledWith(1);
    expect(
      useNotificationWSStore.getState().permissionPendingByWorkspace[1],
    ).toEqual(new Set([11]));
  });

  it('clears a stale pending set when the snapshot is empty', async () => {
    useNotificationWSStore.setState({
      permissionPendingByWorkspace: { 1: new Set([99]) },
    });
    vi.mocked(api.get).mockResolvedValueOnce([ws('1')] as never);
    vi.mocked(listPermissionRequests).mockResolvedValueOnce([] as never);

    await backfillPendingPermissions();

    expect(
      useNotificationWSStore.getState().permissionPendingByWorkspace[1],
    ).toBeUndefined();
  });

  it('keeps the live map intact when the snapshot fetch fails', async () => {
    useNotificationWSStore.setState({
      permissionPendingByWorkspace: { 1: new Set([7]) },
    });
    vi.mocked(api.get).mockRejectedValueOnce(new Error('offline'));

    await backfillPendingPermissions();

    expect(
      useNotificationWSStore.getState().permissionPendingByWorkspace[1],
    ).toEqual(new Set([7]));
  });
});
