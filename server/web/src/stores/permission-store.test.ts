import { describe, it, expect, beforeEach } from 'vitest';
import { usePermissionStore } from './permission-store';

beforeEach(() => usePermissionStore.setState({ byWorkspace: new Map() }));

describe('permission-store', () => {
  it('ingests permission_request SSE event (camelCase wire shape)', () => {
    usePermissionStore.getState().ingestEvent({
      type: 'permission_request',
      workspaceId: 1,
      ts: 1746099296000,
      permissionRequest: {
        requestId: 7,
        sessionId: 's',
        toolName: 'Bash',
        toolInput: { command: 'x' },
        requestedAt: 1746099296000,
        expiresAt: 1746106496000,
      },
    });
    const list = usePermissionStore.getState().byWorkspace.get(1) ?? [];
    expect(list).toHaveLength(1);
    expect(list[0].tool_name).toBe('Bash');
    expect(list[0].id).toBe(7);
    expect(list[0].status).toBe('pending');
    expect(list[0].workspace_id).toBe(1);
    expect(list[0].session_id).toBe('s');
    expect(list[0].tool_input).toEqual({ command: 'x' });
    expect(list[0].requested_at).toBe(1746099296000);
    expect(list[0].expires_at).toBe(1746106496000);
  });

  it('marks request status non-pending on permission_decided event', () => {
    usePermissionStore.setState({
      byWorkspace: new Map([
        [
          1,
          [
            {
              id: 7,
              workspace_id: 1,
              session_id: 's',
              tool_name: 'Bash',
              tool_input: {},
              requested_at: 0,
              expires_at: 0,
              status: 'pending',
            },
          ],
        ],
      ]),
    });
    usePermissionStore.getState().ingestEvent({
      type: 'permission_decided',
      workspaceId: 1,
      ts: 1746099500000,
      permissionDecided: {
        requestId: 7,
        status: 'allowed',
        decidedById: 9,
        decisionSource: 'user',
      },
    });
    const list = usePermissionStore.getState().byWorkspace.get(1) ?? [];
    expect(list.find((r) => r.status === 'pending')).toBeUndefined();
    expect(list.find((r) => r.id === 7)?.status).toBe('allowed');
  });
});
