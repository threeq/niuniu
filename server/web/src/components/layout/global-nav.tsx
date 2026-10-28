import { useState } from 'react';
import { Link, useNavigate, useRouterState } from '@tanstack/react-router';
import { Bell, Bot, LogOut, Menu, Settings, Zap } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { cn } from '@/lib/utils';
import { openAIWindow } from '@/lib/shell';
import { IME_BRAND_FOCUS_ID } from '@/lib/ime-caret-fix';
import { useWorkspaces } from '@/lib/hooks/use-workspaces';
import { useAuthStore } from '@/stores/auth-store';
import { useConfigStore } from '@/stores/config-store';
import { usePermissionPendingTotal } from '@/stores/notification-ws-store';
import { listDashboards } from '@/lib/dashboards-api';
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { Button } from '@/components/ui/button';
import { ThemeToggle } from './theme-toggle';

const navLinks = [
  { to: '/projects', labelKey: 'menu.projects' },
  { to: '/workspaces', labelKey: 'menu.workspaces' },
  { to: '/repositories', labelKey: 'menu.repositories' },
  { to: '/knowledge-bases', labelKey: 'menu.knowledgeBases' },
  { to: '/scenes', labelKey: 'menu.scenes' },
  { to: '/settings/agents', labelKey: 'menu.agents' },
] as const;

export function GlobalNav() {
  const { t } = useTranslation('nav');
  const { t: tWs } = useTranslation('workspaces');
  const { t: tDash } = useTranslation('dashboards');
  const routerState = useRouterState();
  const currentPath = routerState.location.pathname;
  const { workspaces } = useWorkspaces();
  const { user, isAuthenticated, logout } = useAuthStore();
  const navigate = useNavigate();
  const pendingPermTotal = usePermissionPendingTotal();
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  // The "数据看板" entry only appears once the user has pinned at least one
  // chart (i.e. has >=1 dashboard). The query is invalidated on pin, so the
  // entry surfaces immediately after the first pin. (plan task E5 step 2)
  const { data: dashboards } = useQuery({
    queryKey: ['dashboards'],
    queryFn: listDashboards,
    staleTime: 30_000,
  });
  const showDashboards = (dashboards?.length ?? 0) > 0;

  // Edition detection for the AI 直达 entry: gate on configLoaded so team
  // edition doesn't flash it before /api/health resolves (personalMode
  // defaults true until then).
  const personalMode = useConfigStore((s) => s.personalMode);
  const configLoaded = useConfigStore((s) => s.loaded);

  function handleLogout() {
    const refreshToken = useAuthStore.getState().refreshToken;
    if (refreshToken) {
      fetch('/api/auth/logout', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: refreshToken }),
      }).catch(() => {});
    }
    logout();
    navigate({ to: '/login' });
  }

  const activeStatuses = ['attention', 'needs_review', 'created', 'running'];
  const activeWorkspaces = (workspaces ?? []).filter((ws) =>
    activeStatuses.includes(ws.status)
  );
  const activeCount = activeWorkspaces.length;

  // Badge color priority: attention > needs_review > running > default
  // Each option bundles background + text color for WCAG AA compliance.
  // amber requires dark text (text-white on amber fails AA ~2:1).
  const badgeClass = activeWorkspaces.some((ws) => ws.status === 'attention')
    ? 'bg-red-500 text-white dark:bg-red-400 dark:text-white'
    : activeWorkspaces.some((ws) => ws.status === 'needs_review' || ws.status === 'created')
      ? 'bg-amber-500 text-neutral-950 dark:bg-amber-400 dark:text-neutral-950'
      : activeWorkspaces.some((ws) => ws.status === 'running')
        ? 'bg-emerald-500 text-white dark:bg-emerald-400 dark:text-neutral-950'
        : 'bg-emerald-500 text-white dark:bg-emerald-400 dark:text-neutral-950';

  return (
    <header className="flex h-10 shrink-0 items-center justify-between border-b bg-background px-2 sm:px-4">
      <div className="flex min-w-0 flex-1 items-center gap-2 md:gap-6">
        {/* Mobile: the inline nav is replaced by a slide-over menu so the
            header stays usable on phone widths. */}
        <Button
          variant="ghost"
          size="icon"
          onClick={() => setMobileMenuOpen(true)}
          className="h-10 w-10 shrink-0 md:hidden"
          aria-label={t('actions.menu')}
          title={t('actions.menu')}
        >
          <Menu className="h-5 w-5" />
        </Button>
        {/* tabIndex=-1: programmatically focusable only — the IME caret fix
            parks focus here on window reactivation (see lib/ime-caret-fix.ts). */}
        <div id={IME_BRAND_FOCUS_ID} tabIndex={-1} className="flex items-center gap-2 outline-none shrink-0">
          <img src="/favicon.svg" alt="" aria-hidden className="h-5 w-5 dark:hidden" />
          <img src="/favicon-dark.svg" alt="" aria-hidden className="h-5 w-5 hidden dark:block" />
          <span className="text-sm font-semibold text-foreground">{t('brand.name')}</span>
        </div>

        {/* min-w-0 + overflow-x-auto: on iPad / narrow widths the nav scrolls
            horizontally instead of truncating its items (brand + right-side
            controls stay fixed). Touch scrollbars auto-hide.
            py-1.5 / -my-1.5: overflow-x-auto forces overflow-y to `auto` (CSS
            can't mix visible + auto on the two axes), which would clip the
            corner count/permission badges that poke -top-1 / -bottom-1 out of
            each link. The vertical padding gives them room inside the scroll
            box; the matching negative margin keeps the header height unchanged. */}
        <nav className="hidden min-w-0 items-center gap-1 overflow-x-auto py-1.5 -my-1.5 md:flex">
          {navLinks.map(({ to, labelKey }) => {
            const isActive = currentPath === to || currentPath.startsWith(to + '/');
            const showBadge = to === '/workspaces' && activeCount > 0;
            const showPermBell = to === '/workspaces' && pendingPermTotal > 0;
            return (
              <Link
                key={to}
                to={to}
                className={cn(
                  'relative shrink-0 px-3 py-1 rounded-md text-sm font-medium transition-colors',
                  isActive
                    ? 'bg-accent text-foreground'
                    : 'text-muted-foreground hover:bg-accent hover:text-foreground'
                )}
              >
                {t(labelKey)}
                {showBadge && (
                  <span className={cn(
                    'absolute -top-1 -right-1 text-[9px] font-bold min-w-[16px] h-4 rounded-full flex items-center justify-center px-1',
                    badgeClass
                  )}>
                    {activeCount}
                  </span>
                )}
                {showPermBell && (
                  <span
                    className="absolute -bottom-1 -right-1 flex items-center gap-0.5 rounded-full bg-amber-500 px-1 h-4 text-[9px] font-bold text-neutral-950 dark:bg-amber-400 dark:text-neutral-950"
                    title={tWs('permission.indicator.pendingCount', { count: pendingPermTotal })}
                    aria-label={tWs('permission.indicator.pendingCount', { count: pendingPermTotal })}
                  >
                    <Bell className="w-2.5 h-2.5" />
                    {pendingPermTotal}
                  </span>
                )}
              </Link>
            );
          })}
          {showDashboards && (
            <Link
              to="/dashboards"
              className={cn(
                'relative px-3 py-1 rounded-md text-sm font-medium transition-colors',
                currentPath === '/dashboards' ||
                  currentPath.startsWith('/dashboards/')
                  ? 'bg-accent text-foreground'
                  : 'text-muted-foreground hover:bg-accent hover:text-foreground',
              )}
            >
              {tDash('nav.dashboards')}
            </Link>
          )}
        </nav>
      </div>

      <div className="flex items-center gap-1 shrink-0">
        {/* AI 直达 — personal/desktop edition only. Always visible so the native
            AI aggregation window has a reliable entry point that doesn't depend
            on the (easily-conflicting) global hotkey. Signals the desktop shell
            via the backend (see lib/shell.openAIWindow). */}
        {configLoaded && personalMode && (
          <button
            type="button"
            onClick={() => { void openAIWindow(); }}
            className="hidden items-center gap-1 rounded-md px-2.5 py-1 text-xs font-medium text-info transition-colors hover:bg-info/10 sm:flex"
            title={t('menu.aiHub')}
          >
            <Bot className="w-3.5 h-3.5" />
            {t('menu.aiHub')}
          </button>
        )}
        <Link
          to="/schedules"
          className={cn(
            'flex items-center gap-1 rounded-md px-1.5 py-1 text-xs font-medium transition-colors sm:px-2.5',
            currentPath === '/schedules'
              ? 'bg-info/15 text-info'
              : 'text-info hover:bg-info/10'
          )}
        >
          <Zap className="w-3.5 h-3.5" />
          <span className="hidden sm:inline">{t('actions.autoAi')}</span>
        </Link>
        <ThemeToggle />
        <Link
          to="/settings"
          className={cn(
            'p-1.5 rounded-md transition-colors',
            currentPath === '/settings'
              ? 'bg-accent text-foreground'
              : 'text-muted-foreground hover:bg-accent hover:text-foreground'
          )}
          aria-label={t('actions.settings')}
        >
          <Settings className="w-4 h-4" />
        </Link>
        {isAuthenticated && user && (
          <>
            <span className="hidden sm:inline-block align-middle text-xs text-muted-foreground ml-2 max-w-32 truncate">{user.display_name || user.username}</span>
            <button
              onClick={handleLogout}
              className="p-1.5 rounded-md text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
              aria-label={t('actions.logout')}
              title={t('actions.logout')}
            >
              <LogOut className="w-3.5 h-3.5" />
            </button>
          </>
        )}
      </div>

      {/* Mobile navigation sheet. Only reachable from the <md hamburger. */}
      <Sheet open={mobileMenuOpen} onOpenChange={setMobileMenuOpen}>
        <SheetContent side="left" className="w-4/5 max-w-xs gap-0 p-0">
          <SheetHeader className="border-b border-warm-border px-4 py-3 text-left">
            <SheetTitle className="flex items-center gap-2 text-sm font-semibold text-foreground">
              <img src="/favicon.svg" alt="" aria-hidden className="h-5 w-5 dark:hidden" />
              <img src="/favicon-dark.svg" alt="" aria-hidden className="hidden h-5 w-5 dark:block" />
              {t('brand.name')}
            </SheetTitle>
          </SheetHeader>
          <nav className="flex flex-col gap-0.5 p-2">
            {navLinks.map(({ to, labelKey }) => {
              const isActive = currentPath === to || currentPath.startsWith(to + '/');
              return (
                <Link
                  key={to}
                  to={to}
                  onClick={() => setMobileMenuOpen(false)}
                  className={cn(
                    'flex h-10 items-center justify-between rounded-md px-3 text-sm font-medium transition-colors',
                    isActive
                      ? 'bg-accent text-foreground'
                      : 'text-muted-foreground hover:bg-accent hover:text-foreground'
                  )}
                >
                  <span className="truncate">{t(labelKey)}</span>
                  {to === '/workspaces' && activeCount > 0 && (
                    <span className={cn('ml-2 min-w-4 rounded-full px-1 text-center text-[10px] font-semibold tabular-nums', badgeClass)}>
                      {activeCount}
                    </span>
                  )}
                  {to === '/workspaces' && pendingPermTotal > 0 && (
                    <span className="ml-1 flex items-center gap-0.5 rounded-full bg-amber-500 px-1 text-[10px] font-semibold text-neutral-950 dark:bg-amber-400 dark:text-neutral-950">
                      <Bell className="h-2.5 w-2.5" />
                      {pendingPermTotal}
                    </span>
                  )}
                </Link>
              );
            })}
            {showDashboards && (
              <Link
                to="/dashboards"
                onClick={() => setMobileMenuOpen(false)}
                className={cn(
                  'flex h-10 items-center rounded-md px-3 text-sm font-medium transition-colors',
                  currentPath === '/dashboards' || currentPath.startsWith('/dashboards/')
                    ? 'bg-accent text-foreground'
                    : 'text-muted-foreground hover:bg-accent hover:text-foreground',
                )}
              >
                {tDash('nav.dashboards')}
              </Link>
            )}

            <div className="my-2 h-px bg-border" />

            <Link
              to="/schedules"
              onClick={() => setMobileMenuOpen(false)}
              className={cn(
                'flex h-10 items-center gap-2 rounded-md px-3 text-sm font-medium transition-colors',
                currentPath === '/schedules'
                  ? 'bg-info/15 text-info'
                  : 'text-info hover:bg-info/10'
              )}
            >
              <Zap className="h-4 w-4" />
              {t('actions.autoAi')}
            </Link>
            {configLoaded && personalMode && (
              <Button
                variant="ghost"
                onClick={() => {
                  setMobileMenuOpen(false);
                  void openAIWindow();
                }}
                className="h-10 justify-start gap-2 px-3 text-sm font-medium text-info hover:bg-info/10"
              >
                <Bot className="h-4 w-4" />
                {t('menu.aiHub')}
              </Button>
            )}
          </nav>
        </SheetContent>
      </Sheet>
    </header>
  );
}
