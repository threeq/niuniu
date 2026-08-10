import * as SecureStore from 'expo-secure-store';
import { create } from 'zustand';
import { useServerStore } from './serverStore';

interface AuthStore {
  accessToken: string | null;
  refreshToken: string | null;
  isAuthenticated: boolean;
  isLoading: boolean;

  setTokens: (access: string, refresh: string) => Promise<void>;
  clearTokens: () => Promise<void>;
  loadTokens: () => Promise<void>;
}

function tokenKey(suffix: string): string {
  const serverId = useServerStore.getState().activeServerId ?? 'default';
  return `niuniu_${serverId}_${suffix}`;
}

export const useAuthStore = create<AuthStore>()((set) => ({
  accessToken: null,
  refreshToken: null,
  isAuthenticated: false,
  isLoading: true,

  setTokens: async (access, refresh) => {
    await SecureStore.setItemAsync(tokenKey('access'), access);
    await SecureStore.setItemAsync(tokenKey('refresh'), refresh);
    set({ accessToken: access, refreshToken: refresh, isAuthenticated: true });
  },

  clearTokens: async () => {
    await SecureStore.deleteItemAsync(tokenKey('access'));
    await SecureStore.deleteItemAsync(tokenKey('refresh'));
    set({ accessToken: null, refreshToken: null, isAuthenticated: false });
  },

  loadTokens: async () => {
    try {
      const access = await SecureStore.getItemAsync(tokenKey('access'));
      const refresh = await SecureStore.getItemAsync(tokenKey('refresh'));
      set({
        accessToken: access,
        refreshToken: refresh,
        isAuthenticated: !!access,
        isLoading: false,
      });
    } catch {
      set({ isLoading: false });
    }
  },
}));
