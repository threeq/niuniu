import { AppState, AppStateStatus } from 'react-native';
import { useTransportStore } from '../stores/transportStore';

/**
 * registerForegroundHandler wires AppState changes to tear down / re-establish
 * active transports. Call once at app startup (e.g. from _layout.tsx).
 *
 * When the app goes to background or becomes inactive, all cached Noise KK
 * cipher sessions are cleared so that stale sessions don't linger while the
 * app is not in use (battery + security).  On next foreground activation
 * smartFetch will lazily re-handshake on the next RPC call.
 *
 * Returns a cleanup function that removes the AppState listener.
 */
export function registerForegroundHandler(): () => void {
  const onChange = (state: AppStateStatus) => {
    if (state === 'active') {
      // foreground: nothing proactive — smartFetch will handshake lazily on next call
      return;
    }
    if (state === 'background' || state === 'inactive') {
      useTransportStore.getState().clearAllCiphers();
    }
  };
  const sub = AppState.addEventListener('change', onChange);
  return () => sub.remove();
}
