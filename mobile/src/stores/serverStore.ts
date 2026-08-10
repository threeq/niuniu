import AsyncStorage from '@react-native-async-storage/async-storage';
import { create } from 'zustand';
import { createJSONStorage, persist } from 'zustand/middleware';
import type { ServerConfig } from '../api/types';

interface ServerStore {
  servers: ServerConfig[];
  activeServerId: string | null;

  addServer: (server: Omit<ServerConfig, 'id'>) => string;
  removeServer: (id: string) => void;
  updateServer: (id: string, updates: Partial<ServerConfig>) => void;
  setActive: (id: string) => void;

  // Computed
  getActiveServer: () => ServerConfig | undefined;
  getBaseUrl: () => string | null;
}

let idCounter = 0;
function generateId(): string {
  return `srv_${Date.now()}_${++idCounter}`;
}

export const useServerStore = create<ServerStore>()(
  persist(
    (set, get) => ({
      servers: [],
      activeServerId: null,

      addServer: (server) => {
        const id = generateId();
        const newServer: ServerConfig = { ...server, id };
        set((state) => ({
          servers: [...state.servers, newServer],
          activeServerId: state.activeServerId ?? id,
        }));
        return id;
      },

      removeServer: (id) => {
        set((state) => ({
          servers: state.servers.filter((s) => s.id !== id),
          activeServerId: state.activeServerId === id ? null : state.activeServerId,
        }));
      },

      updateServer: (id, updates) => {
        set((state) => ({
          servers: state.servers.map((s) => (s.id === id ? { ...s, ...updates } : s)),
        }));
      },

      setActive: (id) => {
        set({ activeServerId: id });
      },

      getActiveServer: () => {
        const { servers, activeServerId } = get();
        return servers.find((s) => s.id === activeServerId);
      },

      getBaseUrl: () => {
        const server = get().getActiveServer();
        if (!server) return null;
        return `${server.scheme}://${server.host}:${server.port}`;
      },
    }),
    {
      name: 'niuniu-servers',
      storage: createJSONStorage(() => AsyncStorage),
    },
  ),
);
