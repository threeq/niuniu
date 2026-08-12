import { lazy, type ComponentType } from 'react';
import { createRootRoute, createRoute, createRouter, redirect } from '@tanstack/react-router';
import { App } from './App';
import { RootLayout } from './components/layout/root-layout';
import { useAuthStore } from './stores/auth-store';
import { isAuthEnabled } from './lib/edition';
import { shouldDivertToSystemDeps } from './lib/system-deps-gate';

// --- Eager pages -----------------------------------------------------------
// Kept in the initial bundle because they are the above-the-fold entry points
// (one of these is always the first painted screen): the team-edition login,
// the team landing (/workspaces) and the personal landing (/assistant).
import { LoginPage } from './pages/login/login-page';
import { AssistantPage } from './pages/assistant/assistant-page';
import { WorkspaceListPage } from './pages/workspaces/workspace-list-page';

// --- Lazy pages ------------------------------------------------------------
// Everything below is route-split into its own chunk and only fetched when the
// route is first visited. This keeps the first-open bundle small (important on
// iPad / slow links). Heavy editors live here transitively: the workspace IDE
// pulls in xterm, the dashboards/scenes pull in echarts, file-preview pulls in
// xlsx / mammoth / pptx, etc. — none of it is parsed on first paint.
// `lazyNamed` adapts our named page exports to React.lazy's default-export
// contract. Components suspend against the boundary inside <RootLayout>.
function lazyNamed<M, K extends keyof M>(loader: () => Promise<M>, name: K): M[K] {
  // The runtime value is a React.lazy component; we re-assert the original
  // export's type so callers keep full prop type-checking at the JSX site.
  return lazy(() =>
    loader().then((m) => ({ default: m[name] as ComponentType<unknown> })),
  ) as unknown as M[K];
}

const ImBotOnboardingPage = lazyNamed(() => import('./pages/imbot/onboarding'), 'ImBotOnboardingPage');

const WorkspacePage = lazyNamed(() => import('./pages/workspaces/workspace-page'), 'WorkspacePage');
const WorkspacesOverview = lazyNamed(() => import('./pages/workspaces/workspaces-overview'), 'WorkspacesOverview');
const WorkspaceSidebar = lazyNamed(() => import('./pages/workspaces/workspace-sidebar'), 'WorkspaceSidebar');
const ProjectKanbanPage = lazyNamed(() => import('./pages/projects/project-kanban-page'), 'ProjectKanbanPage');
const ProjectLayout = lazyNamed(() => import('./pages/projects/project-layout'), 'ProjectLayout');
const RepositoryListPage = lazyNamed(() => import('./pages/repositories/repository-list-page'), 'RepositoryListPage');
const RepositoryDetailPage = lazyNamed(() => import('./pages/repositories'), 'RepositoryDetailPage');
const RepositoryLayout = lazyNamed(() => import('./pages/repositories/repository-layout'), 'RepositoryLayout');
const SettingsPage = lazyNamed(() => import('./pages/settings'), 'SettingsPage');
const SchedulesPage = lazyNamed(() => import('./pages/schedules'), 'SchedulesPage');
const SceneListPage = lazyNamed(() => import('./pages/scenes/scene-list-page'), 'SceneListPage');
const SceneDetailPage = lazyNamed(() => import('./pages/scenes/scene-detail-page'), 'SceneDetailPage');
const SceneNewPage = lazyNamed(() => import('./pages/scenes/scene-new-page'), 'SceneNewPage');
const SceneEditPage = lazyNamed(() => import('./pages/scenes/scene-edit-page'), 'SceneEditPage');
const OrgsListPage = lazyNamed(() => import('./pages/settings/orgs'), 'OrgsListPage');
const OrgDetailPage = lazyNamed(() => import('./pages/settings/orgs/org-detail-page'), 'OrgDetailPage');
const IntegrationsPage = lazyNamed(() => import('./pages/settings/integrations'), 'IntegrationsPage');
const HarnessPage = lazyNamed(() => import('./pages/harness'), 'HarnessPage');
const AgentsPage = lazyNamed(() => import('./pages/agents'), 'AgentsPage');
const DashboardsPage = lazyNamed(() => import('./pages/dashboards/dashboards-page'), 'DashboardsPage');
const DashboardDetail = lazyNamed(() => import('./pages/dashboards/dashboard-detail'), 'DashboardDetail');

function requireAuth() {
  return async () => {
    const authEnabled = await isAuthEnabled();
    if (!authEnabled) return;
    const { isAuthenticated } = useAuthStore.getState();
    if (!isAuthenticated) {
      throw redirect({ to: '/login' });
    }
  };
}

const rootRoute = createRootRoute({ component: App });

// Public token-authed page — mounted directly on rootRoute so it is accessible
// without a session (no requireAuth beforeLoad).
const imbotOnboardingRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/imbot/onboarding/$token',
  component: () => {
    const { token } = imbotOnboardingRoute.useParams();
    return <ImBotOnboardingPage token={token} />;
  },
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  component: LoginPage,
  beforeLoad: async () => {
    const authEnabled = await isAuthEnabled();
    if (!authEnabled) {
      throw redirect({ to: '/workspaces' });
    }
    const { isAuthenticated } = useAuthStore.getState();
    if (isAuthenticated) {
      throw redirect({ to: '/workspaces' });
    }
  },
});

const layoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'layout',
  component: RootLayout,
  beforeLoad: requireAuth(),
});

// Initial landing decision. In personal mode, if the minimum system deps to
// run an agent (node+git+claude) aren't installed, divert to the System
// Dependencies settings tab to guide installation; otherwise (deps satisfied,
// team edition, or probe failure) land on /workspaces as before. Guidance
// only — global nav stays available so the user isn't hard-blocked.
const indexRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/',
  beforeLoad: async () => {
    if (await shouldDivertToSystemDeps()) {
      throw redirect({ to: '/settings', search: { tab: 'system-deps' } });
    }
    // Personal edition is assistant-first: land non-technical users on the
    // conversational entry. Team edition keeps the kanban/workspaces home.
    if (!(await isAuthEnabled())) {
      throw redirect({ to: '/assistant' });
    }
    throw redirect({ to: '/workspaces' });
  },
});

// Conversational office-assistant entry point (#388). Pinned above the kanban
// nav; reuses agent-sse-store to stream the agent's plan + artifacts.
const assistantRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/assistant',
  component: AssistantPage,
});

const workspacesRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/workspaces',
  component: WorkspaceListPage,
});

// Cross-workspace overview. Declared before the dynamic /$id route so
// the literal "overview" segment wins path matching.
const workspacesOverviewRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/workspaces/overview',
  component: () => (
    <div className="flex h-full">
      <WorkspaceSidebar />
      <WorkspacesOverview />
    </div>
  ),
});

const workspaceDetailRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/workspaces/$id',
  component: () => {
    const { id } = workspaceDetailRoute.useParams();
    return <WorkspacePage workspaceId={id} />;
  },
});

const projectsLayoutRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/projects',
  component: ProjectLayout,
});

const projectDetailRoute = createRoute({
  getParentRoute: () => projectsLayoutRoute,
  path: '/$id',
  component: () => {
    const { id } = projectDetailRoute.useParams();
    return <ProjectKanbanPage projectId={id} />;
  },
});

const projectIndexRoute = createRoute({
  getParentRoute: () => projectsLayoutRoute,
  path: '/',
  component: () => {
    // Empty - the layout itself shows "select a project" message
    return null;
  },
});

const repositoriesLayoutRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/repositories',
  component: RepositoryLayout,
});

const repositoriesIndexRoute = createRoute({
  getParentRoute: () => repositoriesLayoutRoute,
  path: '/',
  component: RepositoryListPage,
});

const repositoryDetailRoute = createRoute({
  getParentRoute: () => repositoriesLayoutRoute,
  path: '/$id',
  component: RepositoryDetailPage,
});

// Legacy ?tab= values for tabs that were promoted out of /settings to their
// own top-level routes. Old bookmarks / desktop tray entries land on the new
// home instead of silently falling back to "general".
const promotedSettingsTabs: Record<string, string> = {
  harness: '/settings/harness',
  agent: '/settings/agents',
  'agent-registry': '/settings/agents',
};

const settingsRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/settings',
  validateSearch: (search: Record<string, unknown>): { tab?: string } => ({
    tab: typeof search.tab === 'string' ? search.tab : undefined,
  }),
  beforeLoad: ({ search }) => {
    const target = search.tab ? promotedSettingsTabs[search.tab] : undefined;
    if (target) throw redirect({ to: target });
  },
  component: SettingsPage,
});

const schedulesRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/schedules',
  component: SchedulesPage,
});

// /scenes/new declared BEFORE /scenes/$id so the literal "new" wins
// over the dynamic $id segment in TanStack Router path matching.
const scenesRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/scenes',
  component: SceneListPage,
});

const sceneNewRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/scenes/new',
  component: SceneNewPage,
});

// /scenes/$id/edit declared BEFORE /scenes/$id so the more specific child
// route is registered ahead of the bare detail route.
const sceneEditRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/scenes/$id/edit',
  component: () => {
    const { id } = sceneEditRoute.useParams();
    return <SceneEditPage sceneId={id} />;
  },
});

const sceneDetailRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/scenes/$id',
  component: () => {
    const { id } = sceneDetailRoute.useParams();
    return <SceneDetailPage sceneId={id} />;
  },
});

const orgsListRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/settings/orgs',
  beforeLoad: async () => {
    if (!(await isAuthEnabled())) {
      throw redirect({ to: '/settings' });
    }
  },
  component: () => (
    <SettingsPage orgsActive>
      <OrgsListPage />
    </SettingsPage>
  ),
});

const orgDetailRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/settings/orgs/$slug',
  beforeLoad: async () => {
    if (!(await isAuthEnabled())) {
      throw redirect({ to: '/settings' });
    }
  },
  component: () => {
    const { slug } = orgDetailRoute.useParams();
    return (
      <SettingsPage orgsActive>
        <OrgDetailPage slug={slug} />
      </SettingsPage>
    );
  },
});

const integrationsRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/settings/integrations',
  component: IntegrationsPage,
});

const harnessRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/settings/harness',
  component: HarnessPage,
});

const agentsRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/settings/agents',
  component: AgentsPage,
});

const dashboardsRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/dashboards',
  component: DashboardsPage,
});

const dashboardDetailRoute = createRoute({
  getParentRoute: () => layoutRoute,
  path: '/dashboards/$id',
  component: () => {
    const { id } = dashboardDetailRoute.useParams();
    return <DashboardDetail dashboardId={id} />;
  },
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  imbotOnboardingRoute,
  layoutRoute.addChildren([
    indexRoute,
    assistantRoute,
    workspacesRoute,
    workspacesOverviewRoute,
    workspaceDetailRoute,
    projectsLayoutRoute.addChildren([
      projectIndexRoute,
      projectDetailRoute,
    ]),
    repositoriesLayoutRoute.addChildren([
      repositoriesIndexRoute,
      repositoryDetailRoute,
    ]),
    settingsRoute,
    schedulesRoute,
    scenesRoute,
    sceneNewRoute,
    sceneEditRoute,
    sceneDetailRoute,
    orgsListRoute,
    orgDetailRoute,
    integrationsRoute,
    harnessRoute,
    agentsRoute,
    dashboardsRoute,
    dashboardDetailRoute,
  ]),
]);

export const router = createRouter({ routeTree });

declare module '@tanstack/react-router' {
  interface Register { router: typeof router; }
}
