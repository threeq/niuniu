import { useNotificationStore, type AgentNotification } from '../notificationStore';

beforeEach(() => {
  useNotificationStore.setState({ notifications: [], unreadCount: 0 });
});

describe('notificationStore', () => {
  describe('upsertAgent', () => {
    it('adds a new notification', () => {
      useNotificationStore.getState().upsertAgent({
        workspaceId: 'ws-1',
        workspaceName: 'Test WS',
        status: 'running',
        summary: 'Working on task',
        updatedAt: '2026-01-01T00:00:00Z',
      });

      const { notifications, unreadCount } = useNotificationStore.getState();
      expect(notifications).toHaveLength(1);
      expect(notifications[0].read).toBe(false);
      expect(unreadCount).toBe(1);
    });

    it('updates existing notification for same workspace', () => {
      const store = useNotificationStore.getState();
      store.upsertAgent({
        workspaceId: 'ws-1',
        workspaceName: 'Test WS',
        status: 'running',
        summary: 'Working',
        updatedAt: '2026-01-01T00:00:00Z',
      });
      store.upsertAgent({
        workspaceId: 'ws-1',
        workspaceName: 'Test WS',
        status: 'completed',
        summary: 'Done!',
        updatedAt: '2026-01-01T00:01:00Z',
      });

      const { notifications } = useNotificationStore.getState();
      expect(notifications).toHaveLength(1);
      expect(notifications[0].status).toBe('completed');
      expect(notifications[0].summary).toBe('Done!');
    });

    it('sorts failed notifications first', () => {
      const store = useNotificationStore.getState();
      store.upsertAgent({
        workspaceId: 'ws-1',
        workspaceName: 'WS1',
        status: 'completed',
        summary: 'OK',
        updatedAt: '2026-01-01T00:02:00Z',
      });
      store.upsertAgent({
        workspaceId: 'ws-2',
        workspaceName: 'WS2',
        status: 'failed',
        summary: 'Error!',
        updatedAt: '2026-01-01T00:01:00Z',
      });

      const { notifications } = useNotificationStore.getState();
      expect(notifications[0].workspaceId).toBe('ws-2');
      expect(notifications[0].status).toBe('failed');
    });
  });

  describe('markRead', () => {
    it('marks specific notification as read', () => {
      useNotificationStore.getState().upsertAgent({
        workspaceId: 'ws-1',
        workspaceName: 'WS1',
        status: 'completed',
        summary: 'Done',
        updatedAt: '2026-01-01T00:00:00Z',
      });

      useNotificationStore.getState().markRead('ws-1');

      const { notifications, unreadCount } = useNotificationStore.getState();
      expect(notifications[0].read).toBe(true);
      expect(unreadCount).toBe(0);
    });
  });

  describe('markAllRead', () => {
    it('marks all notifications as read', () => {
      const store = useNotificationStore.getState();
      store.upsertAgent({
        workspaceId: 'ws-1',
        workspaceName: 'WS1',
        status: 'running',
        summary: 'a',
        updatedAt: '2026-01-01T00:00:00Z',
      });
      store.upsertAgent({
        workspaceId: 'ws-2',
        workspaceName: 'WS2',
        status: 'completed',
        summary: 'b',
        updatedAt: '2026-01-01T00:01:00Z',
      });

      useNotificationStore.getState().markAllRead();

      const { unreadCount } = useNotificationStore.getState();
      expect(unreadCount).toBe(0);
    });
  });
});
