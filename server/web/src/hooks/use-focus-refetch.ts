import { useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';

// Refetch active queries whenever the niuniu window regains focus or its tab
// becomes visible again. Wails webview's focus/visibility events don't always
// drive TanStack Query's built-in refetchOnWindowFocus reliably, so this hook
// invalidates explicitly. UI state (scroll, dialogs, terminal) is preserved —
// only data is re-fetched in the background.
export function useFocusRefetch() {
  const queryClient = useQueryClient();

  useEffect(() => {
    const refresh = () => {
      if (document.visibilityState === 'visible') {
        queryClient.invalidateQueries();
      }
    };

    document.addEventListener('visibilitychange', refresh);
    window.addEventListener('focus', refresh);

    return () => {
      document.removeEventListener('visibilitychange', refresh);
      window.removeEventListener('focus', refresh);
    };
  }, [queryClient]);
}
