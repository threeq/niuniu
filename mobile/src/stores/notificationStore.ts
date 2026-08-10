import { create } from 'zustand';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useServerStore } from './serverStore';

export interface AgentNotification {
  workspaceId: string;
  workspaceName: string;
  status: 'running' | 'completed' | 'failed' | 'idle';
  summary: string;
  costUsd?: number;
  turns?: number;
  updatedAt: string;
  read: boolean;
}

interface NotificationState {
  notifications: AgentNotification[];
  unreadCount: number;
  loadNotifications: () => Promise<void>;
  upsertAgent: (notif: Omit<AgentNotification, 'read'>) => void;
  markRead: (workspaceId: string) => void;
  markAllRead: () => void;
}

function storageKey() {
  const serverId = useServerStore.getState().activeServerId;
  return `agent_notifications_${serverId ?? 'default'}`;
}

export const useNotificationStore = create<NotificationState>((set, get) => ({
  notifications: [],
  unreadCount: 0,

  loadNotifications: async () => {
    try {
      const raw = await AsyncStorage.getItem(storageKey());
      const notifications: AgentNotification[] = raw ? JSON.parse(raw) : [];
      const unreadCount = notifications.filter((n) => !n.read).length;
      set({ notifications, unreadCount });
    } catch {
      set({ notifications: [], unreadCount: 0 });
    }
  },

  upsertAgent: (notif) => {
    const { notifications } = get();
    const existing = notifications.findIndex((n) => n.workspaceId === notif.workspaceId);
    let updated: AgentNotification[];
    if (existing >= 0) {
      updated = [...notifications];
      updated[existing] = { ...notif, read: false };
    } else {
      updated = [{ ...notif, read: false }, ...notifications];
    }
    updated.sort((a, b) => {
      if (a.status === 'failed' && b.status !== 'failed') return -1;
      if (b.status === 'failed' && a.status !== 'failed') return 1;
      return new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime();
    });
    const unreadCount = updated.filter((n) => !n.read).length;
    set({ notifications: updated, unreadCount });
    AsyncStorage.setItem(storageKey(), JSON.stringify(updated));
  },

  markRead: (workspaceId) => {
    const { notifications } = get();
    const updated = notifications.map((n) =>
      n.workspaceId === workspaceId ? { ...n, read: true } : n
    );
    const unreadCount = updated.filter((n) => !n.read).length;
    set({ notifications: updated, unreadCount });
    AsyncStorage.setItem(storageKey(), JSON.stringify(updated));
  },

  markAllRead: () => {
    const { notifications } = get();
    const updated = notifications.map((n) => ({ ...n, read: true }));
    set({ notifications: updated, unreadCount: 0 });
    AsyncStorage.setItem(storageKey(), JSON.stringify(updated));
  },
}));
