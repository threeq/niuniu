import { describe, expect, it } from 'vitest';
import { targetRoute } from './notification-dispatcher';

describe('targetRoute (扩展点保护)', () => {
  it('workspace → /workspaces/$id', () => {
    expect(targetRoute({ type: 'workspace', id: 218 })).toEqual({
      to: '/workspaces/$id', params: { id: '218' },
    });
  });
  it('project → /projects/$id', () => {
    expect(targetRoute({ type: 'project', id: 42 })).toEqual({
      to: '/projects/$id', params: { id: '42' },
    });
  });
});

import { afterEach, beforeEach, vi } from 'vitest';
import type { QueryClient } from '@tanstack/react-query';

const navigateMock = vi.fn();
vi.mock('@/router', () => ({
  router: {
    navigate: (...args: unknown[]) => navigateMock(...args),
    state: { location: { pathname: '/workspaces' } },
  },
}));

const successMock = vi.fn();
const infoMock = vi.fn();
const errorMock = vi.fn();
const dismissMock = vi.fn();

vi.mock('sonner', () => ({
  toast: Object.assign(
    (msg: string, opts?: unknown) => infoMock(msg, opts),
    {
      success: (msg: string, opts?: unknown) => successMock(msg, opts),
      info: (msg: string, opts?: unknown) => infoMock(msg, opts),
      error: (msg: string, opts?: unknown) => errorMock(msg, opts),
      dismiss: (id?: unknown) => dismissMock(id),
    },
  ),
}));

vi.mock('@/lib/notification-sound', () => ({ playNotificationSound: vi.fn() }));

const settingsState = { toastEnabled: true, soundEnabled: false, soundName: 'default', volume: 1, alertScope: 'all' };
vi.mock('@/stores/notification-settings-store', () => ({
  useNotificationSettings: { getState: () => settingsState },
}));

import { dispatchNotification, __resetForTests } from './notification-dispatcher';

const qc = {
  invalidateQueries: vi.fn(),
  cancelQueries: vi.fn(),
  removeQueries: vi.fn(),
} as unknown as QueryClient;

beforeEach(() => {
  __resetForTests();
  navigateMock.mockReset();
  successMock.mockReset();
  infoMock.mockReset();
  errorMock.mockReset();
  dismissMock.mockReset();
  settingsState.toastEnabled = true;
});

afterEach(() => vi.restoreAllMocks());

describe('dispatchNotification toast — onClick navigation', () => {
  it('agent_done success toast 把 message 包成可点击节点 + 5s duration + id', () => {
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done', id: 218 });
    expect(successMock).toHaveBeenCalledTimes(1);
    const [node, opts] = successMock.mock.calls[0];
    // message 是 React 节点
    expect(node).toMatchObject({
      props: {
        children: 'Workspace !218: agent 完成，等待 review',
        onClick: expect.any(Function),
      },
    });
    // options 仅含 id 与 duration
    const o = opts as Record<string, unknown>;
    expect(o.duration).toBe(5000);
    expect(o.id).toBeTruthy();
    expect(o.onClick).toBeUndefined();
  });

  it('点击 message 节点 → router.navigate({to:"/workspaces/$id", params:{id:"218"}})', () => {
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done', id: 218 });
    const [node] = successMock.mock.calls[0];
    (node as { props: { onClick: () => void } }).props.onClick();
    expect(navigateMock).toHaveBeenCalledTimes(1);
    expect(navigateMock).toHaveBeenCalledWith({
      to: '/workspaces/$id', params: { id: '218' },
    });
  });
});

describe('dispatchNotification toast — group lifecycle', () => {
  it('点击 → 同 group 全部 toast id 都 dismiss', () => {
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done', id: 218 });
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done', id: 218 });
    const a = successMock.mock.calls[0][0] as { props: { onClick: () => void } };
    const aId = (successMock.mock.calls[0][1] as { id: string }).id;
    const bId = (successMock.mock.calls[1][1] as { id: string }).id;
    a.props.onClick();
    const dismissed = dismissMock.mock.calls.map((c) => c[0]);
    expect(dismissed).toContain(aId);
    expect(dismissed).toContain(bId);
  });

  it('auto-close 触发后旧 id 不再被批量 dismiss', () => {
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done', id: 218 });
    const firstId = (successMock.mock.calls[0][1] as { id: string }).id;
    const firstAutoClose = (successMock.mock.calls[0][1] as { onAutoClose: () => void }).onAutoClose;
    firstAutoClose();
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done', id: 218 });
    const secondNode = successMock.mock.calls[1][0] as { props: { onClick: () => void } };
    const secondId = (successMock.mock.calls[1][1] as { id: string }).id;
    secondNode.props.onClick();
    const dismissed = dismissMock.mock.calls.map((c) => c[0]);
    expect(dismissed).toContain(secondId);
    expect(dismissed).not.toContain(firstId);
  });

  it('不同 workspace 的 toast 互不干扰', () => {
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done', id: 218 });
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done', id: 219 });
    const a = successMock.mock.calls[0][0] as { props: { onClick: () => void } };
    const aId = (successMock.mock.calls[0][1] as { id: string }).id;
    const bId = (successMock.mock.calls[1][1] as { id: string }).id;
    a.props.onClick();
    const dismissed = dismissMock.mock.calls.map((c) => c[0]);
    expect(dismissed).toContain(aId);
    expect(dismissed).not.toContain(bId);
  });

  it('toastEnabled=false → 不调 sonner，group map 不污染', () => {
    settingsState.toastEnabled = false;
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done', id: 218 });
    expect(successMock).not.toHaveBeenCalled();
    settingsState.toastEnabled = true;
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done', id: 218 });
    const node = successMock.mock.calls[0][0] as { props: { onClick: () => void } };
    const id = (successMock.mock.calls[0][1] as { id: string }).id;
    node.props.onClick();
    expect(dismissMock.mock.calls).toEqual([[id]]);
  });
});

describe('dispatchNotification toast — remaining paths', () => {
  it('agent_done attention → error 路径附 onClick', () => {
    dispatchNotification(qc, {
      topic: 'workspace', action: 'agent_done', id: 219, extra: { status: 'attention' },
    });
    expect(errorMock).toHaveBeenCalledTimes(1);
    const [node, opts] = errorMock.mock.calls[0];
    expect(node).toMatchObject({
      props: {
        children: 'Workspace !219: agent 出错，需要关注',
        onClick: expect.any(Function),
      },
    });
    const o = opts as Record<string, unknown>;
    expect(o.id).toBeTruthy();
    expect(o.duration).toBe(5000);
  });

  it('status_changed needs_review → info 路径附 onClick', () => {
    dispatchNotification(qc, {
      topic: 'workspace', action: 'status_changed', id: 220, extra: { status: 'needs_review' },
    });
    expect(infoMock).toHaveBeenCalledTimes(1);
    const [node, opts] = infoMock.mock.calls[0];
    expect(node).toMatchObject({
      props: {
        children: 'Workspace !220: needs_review',
        onClick: expect.any(Function),
      },
    });
    const o = opts as Record<string, unknown>;
    expect(o.id).toBeTruthy();
    expect(o.duration).toBe(5000);
  });

  it('n.id 缺失 → 不弹任何 toast（无可点击目标，跳过 toast）', () => {
    dispatchNotification(qc, { topic: 'workspace', action: 'agent_done' });
    expect(successMock).not.toHaveBeenCalled();
    expect(infoMock).not.toHaveBeenCalled();
    expect(errorMock).not.toHaveBeenCalled();
  });

  it('workspace.deleted 不弹任何 toast（防 404 跳转回归）', () => {
    dispatchNotification(qc, { topic: 'workspace', action: 'deleted', id: 218 });
    expect(successMock).not.toHaveBeenCalled();
    expect(infoMock).not.toHaveBeenCalled();
    expect(errorMock).not.toHaveBeenCalled();
  });

  it('workspace.archived 不弹任何 toast', () => {
    dispatchNotification(qc, { topic: 'workspace', action: 'archived', id: 218 });
    expect(successMock).not.toHaveBeenCalled();
    expect(infoMock).not.toHaveBeenCalled();
    expect(errorMock).not.toHaveBeenCalled();
  });
});

describe('dispatchNotification invalidation map', () => {
  it('pinned.changed → 失效 ["pinned-messages", workspaceId]（自动 pin 实时刷新面板）', () => {
    (qc.invalidateQueries as ReturnType<typeof vi.fn>).mockReset();
    dispatchNotification(qc, { topic: 'pinned', action: 'changed', id: 218 });
    expect(qc.invalidateQueries).toHaveBeenCalledWith({ queryKey: ['pinned-messages', '218'] });
  });
});
