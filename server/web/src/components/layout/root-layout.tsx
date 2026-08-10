import { Suspense } from 'react';
import { Outlet } from '@tanstack/react-router';
import { Loader2 } from 'lucide-react';
import { GlobalNav } from './global-nav';
import { LicenseBanner } from './license-banner';
import { ConsentGate } from './consent-gate';
import { useNotificationWS } from '@/hooks/use-notification-ws';
import { useFocusRefetch } from '@/hooks/use-focus-refetch';
import { useReloadShortcut } from '@/hooks/use-reload-shortcut';

// Fallback shown only inside the content area while a route-split page chunk is
// being fetched. The nav shell above stays mounted, so navigation never blanks
// the whole app.
function RouteFallback() {
  return (
    <div className="flex h-full w-full items-center justify-center">
      <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
    </div>
  );
}

export function RootLayout() {
  useNotificationWS();
  useFocusRefetch();
  useReloadShortcut();

  return (
    <div className="h-screen flex flex-col">
      <LicenseBanner />
      <GlobalNav />
      <div className="flex-1 overflow-hidden">
        <Suspense fallback={<RouteFallback />}>
          <Outlet />
        </Suspense>
      </div>
      <ConsentGate />
    </div>
  );
}
