import { useColorScheme } from 'react-native';
import { lightColors, darkColors, ThemeColors } from './tokens';
import { create } from 'zustand';
import AsyncStorage from '@react-native-async-storage/async-storage';

type ThemePreference = 'system' | 'light' | 'dark';

interface ThemeStore {
  preference: ThemePreference;
  setPreference: (pref: ThemePreference) => Promise<void>;
  loadPreference: () => Promise<void>;
}

export const useThemeStore = create<ThemeStore>((set) => ({
  preference: 'system',
  setPreference: async (pref) => {
    set({ preference: pref });
    await AsyncStorage.setItem('theme_preference', pref);
  },
  loadPreference: async () => {
    const saved = await AsyncStorage.getItem('theme_preference');
    if (saved === 'light' || saved === 'dark' || saved === 'system') {
      set({ preference: saved });
    }
  },
}));

export function useThemeColors(): ThemeColors {
  const systemScheme = useColorScheme();
  const preference = useThemeStore((s) => s.preference);

  const effectiveScheme = preference === 'system' ? systemScheme : preference;
  return (effectiveScheme === 'dark' ? darkColors : lightColors) as ThemeColors;
}

export function useIsDark(): boolean {
  const systemScheme = useColorScheme();
  const preference = useThemeStore((s) => s.preference);
  const effectiveScheme = preference === 'system' ? systemScheme : preference;
  return effectiveScheme === 'dark';
}
