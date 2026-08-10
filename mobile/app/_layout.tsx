import { Stack } from 'expo-router';
import { useEffect } from 'react';
import { GestureHandlerRootView } from 'react-native-gesture-handler';
import { BottomSheetModalProvider } from '@gorhom/bottom-sheet';
import { useAuthStore } from '../src/stores/authStore';
import { useNotificationStore } from '../src/stores/notificationStore';
import { useThemeStore } from '../src/theme/useTheme';
import { useSSENotifications } from '../src/hooks/useSSE';
import { ErrorBoundary } from '../src/components/ErrorBoundary';
import { registerForegroundHandler } from '../src/transport/foreground';
import Constants from 'expo-constants';
import '../src/i18n';

const isExpoGo = Constants.appOwnership === 'expo';

if (!isExpoGo) {
  const Notifications = require('expo-notifications');
  Notifications.setNotificationHandler({
    handleNotification: async () => ({ shouldShowAlert: true, shouldPlaySound: false, shouldSetBadge: false, shouldShowBanner: true, shouldShowList: true }),
  });
}

export default function RootLayout() {
  const loadTokens = useAuthStore((s) => s.loadTokens);
  useSSENotifications();

  useEffect(() => {
    loadTokens();
    useThemeStore.getState().loadPreference();
    if (!isExpoGo) {
      const Notifications = require('expo-notifications');
      Notifications.requestPermissionsAsync().catch(() => {});
    }
    useNotificationStore.getState().loadNotifications();
    // Tear down Noise KK cipher sessions when app backgrounds to save battery
    // and clear stale session state. smartFetch re-handshakes lazily on resume.
    const unregisterForeground = registerForegroundHandler();
    return unregisterForeground;
  }, [loadTokens]);

  return (
    <GestureHandlerRootView style={{ flex: 1 }}>
      <ErrorBoundary>
        <BottomSheetModalProvider>
          <Stack screenOptions={{ headerShown: false }}>
            <Stack.Screen name="(auth)" />
            <Stack.Screen name="(tabs)" />
            <Stack.Screen name="settings" options={{ title: 'Settings', headerShown: true }} />
            <Stack.Screen name="settings/trusted-relays" options={{ title: 'Trusted Relays', headerShown: true }} />
            <Stack.Screen name="settings/connection-info" options={{ title: 'Connection Info', headerShown: true }} />
            <Stack.Screen name="settings/paired-desktops" options={{ title: 'Paired Desktops', headerShown: true }} />
            <Stack.Screen name="workspace" options={{ title: 'Workspaces', headerShown: true }} />
            <Stack.Screen name="workspace/[id]" options={{ title: 'Workspace', headerShown: true }} />
            <Stack.Screen name="repository" options={{ title: 'Repositories', headerShown: true }} />
          </Stack>
        </BottomSheetModalProvider>
      </ErrorBoundary>
    </GestureHandlerRootView>
  );
}
