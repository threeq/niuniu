import React, { useEffect } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Group, Panel, Separator } from 'react-resizable-panels';
import { useTranslation } from 'react-i18next';
import { PanelLeftOpen } from 'lucide-react';
import { api } from '@/lib/api';
import type { Workspace, Issue } from '@/types/api';
import { useWorkspacePanelStore, type PanelId } from '@/stores/workspace-panel-store';
import { useAgentSSEStore } from '@/stores/agent-sse-store';
import { useAppStore } from '@/stores/app';
import { useRunSSE } from '@/hooks/use-run-sse';
import { WorkspaceToolbar } from './workspace-toolbar';
import { WorkspaceSidebar } from './workspace-sidebar';
import { WorkspaceQuickActions } from './workspace-quick-actions';
import { ChatPanel } from './panels/chat-panel';
import { TerminalPanel } from './panels/terminal-panel';
import { FileTreePanel } from './panels/file-tree-panel';
import { ChangesPanel } from './panels/changes-panel';
import { IssuePanel } from './panels/issue-panel';
import { PinnedMessagesPanel } from './panels/pinned-messages-panel';
import { ArtifactPanelContainer } from './panels/artifact-panel-container';
import { ContentViewerPanel } from './panels/content-viewer-panel';
import { ArchivedPlaceholder } from './panels/archived-placeholder';
import { WorkspaceProjectionBanner } from './panels/workspace-projection-banner';
import { StudioDraftBanner } from './panels/studio-draft-banner';
import { LocalRunnerBar } from './local-runner/local-runner-bar';
import { useDesktopRunnerAvailable } from '@/lib/desktop-runner-context';

interface WorkspacePageProps {
  workspaceId: string;
}

const FILESYSTEM_PANELS = new Set<PanelId>(['files', 'changes', 'terminal', 'artifact']);

function PanelContent({ panelId, workspaceId, workspace, isArchived }: { panelId: PanelId; workspaceId: string; workspace: Workspace; isArchived: boolean }) {
  if (isArchived && FILESYSTEM_PANELS.has(panelId)) {
    return <ArchivedPlaceholder />;
  }
  switch (panelId) {
    case 'files':
      return <FileTreePanel workspaceId={workspaceId} />;
    case 'changes':
      return <ChangesPanel workspaceId={workspaceId} />;
    case 'terminal':
      return <TerminalPanel workspaceId={workspaceId} />;
    case 'issue':
      return <IssuePanel workspace={workspace} readOnly={isArchived} />;
    case 'pinned':
      return <PinnedMessagesPanel workspaceId={workspaceId} />;
    case 'artifact':
      return <ArtifactPanelContainer workspaceId={workspaceId} />;
    default:
      return null;
  }
}

export function WorkspacePage({ workspaceId }: WorkspacePageProps) {
  const { t } = useTranslation('workspaces');
  const { openPanels, isSidebarOpen, toggleSidebar } = useWorkspacePanelStore();
  const contentViewer = useWorkspacePanelStore((s) => s.contentViewer[workspaceId] ?? null);

  const { data: workspace, isLoading } = useQuery({
    queryKey: ['workspace', workspaceId],
    // Fix 5: api.get<T> returns T directly (apiFetch returns JSON.parse(text) as T)
    queryFn: () => api.get<Workspace>(`/workspaces/${workspaceId}`),
    retry: 1,
  });

  const isArchived = workspace?.is_archived === 1;
  const issueId = workspace?.issue_id ?? null;
  const workspaceLoaded = !!workspace;

  // Fetch the linked issue to get project_id for SSE invalidation routing.
  // If the workspace has no issue (issueId is null), project_id stays 0 and
  // useRunSSE will skip project-scoped query invalidations gracefully.
  const { data: linkedIssue } = useQuery({
    queryKey: ['issue', issueId],
    queryFn: () => api.get<Issue>(`/issues/${issueId}`),
    enabled: !!issueId && !isArchived,
    staleTime: 60_000,
  });
  const projectId = linkedIssue?.project_id ?? 0;

  useEffect(() => {
    if (!workspaceLoaded || isArchived) return;
    const store = useAgentSSEStore.getState();
    store.addWorkspace(workspaceId);
    return () => {
      store.removeWorkspace(workspaceId);
    };
  }, [workspaceId, workspaceLoaded, isArchived]);

  // Subscribe to Phase 2 harness SSE events and route them to TanStack Query cache.
  useRunSSE({ workspaceId: Number(workspaceId), projectId });

  // #526·子A: the bottom local-runner entry shows only on the desktop app's
  // remote connection (or the dev bypass). The desktop injects the signal
  // AFTER first paint (Windows drops the document-created inject, so it lands
  // via a post-navigation ExecJS), so this must be reactive — a one-shot read
  // would race the injection and hide the entry forever. Declared before the
  // early returns below to keep hook order stable.
  const runnerAvailable = useDesktopRunnerAvailable();

  const setLastOpenedWorkspace = useAppStore((s) => s.setLastOpenedWorkspace);
  useEffect(() => {
    if (!workspace || workspace.is_archived === 1) return;
    setLastOpenedWorkspace(workspaceId);
  }, [workspaceId, workspace, setLastOpenedWorkspace]);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
        {t('common:actions.loading')}
      </div>
    );
  }

  if (!workspace) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
        {t('detail.notFound')}
      </div>
    );
  }

  const sidePanels = openPanels.filter(
    (p) => p !== 'chat' && (p !== 'issue' || !!workspace.issue_id)
  );

  // Central content viewer (file / diff / diagram). Archived workspaces have no
  // live filesystem to read, so the viewer is suppressed there.
  const viewer = isArchived ? null : contentViewer;
  const hasViewer = !!viewer;
  const hasSide = sidePanels.length > 0;

  return (
    <div className="flex h-full">
      {isSidebarOpen ? (
        <WorkspaceSidebar />
      ) : (
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label={t('sidebar.collapse.expand')}
          title={t('sidebar.collapse.expand')}
          className="self-start mt-2 ml-1 p-1 rounded text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
        >
          <PanelLeftOpen className="w-4 h-4" />
        </button>
      )}

      <div className="flex flex-col flex-1 min-w-0">
        <WorkspaceToolbar workspace={workspace} />

        {/* empty:hidden collapses the wrapper (and its pt-2) when the banner
            renders null — otherwise a dead 8px band sits atop the chat flow. */}
        {!isArchived && (
          <div className="px-3 pt-2 shrink-0 empty:hidden">
            <WorkspaceProjectionBanner workspaceId={Number(workspaceId)} />
          </div>
        )}

        {/* Studio delivery hint (#238): drafts live in the workspace; the
            user's original directory only changes on "deliver". Studio-only. */}
        {!isArchived && workspace.is_studio === 1 && (
          <div className="px-3 pt-2 shrink-0 empty:hidden">
            <StudioDraftBanner workspaceId={workspaceId} />
          </div>
        )}

        <div className="flex flex-1 min-h-0">
          <div className="flex-1 min-w-0">
            {!hasViewer && !hasSide ? (
              <ChatPanel key={workspaceId} workspace={workspace} readOnly={isArchived} />
            ) : (
              // Stable per-panel `id`s are required so react-resizable-panels
              // correctly re-associates each panel's size when the viewer/side
              // panels are toggled in and out — without them the library
              // mis-maps sizes and can squash the chat to a sliver. The chat has
              // no maxSize so it stays freely draggable via the wider handles.
              // Chat is the primary workspace: it dominates by default and only
              // yields space when the content viewer opens (so the user can
              // review/comment on content and keep chatting side by side). The
              // right-hand list panels (files/changes/…) are just pickers, so
              // they stay a narrow column. Stable `id`s are required so v4
              // re-associates sizes correctly as panels toggle in/out.
              <Group orientation="horizontal" className="h-full">
                <Panel id="chat" defaultSize={hasViewer ? 42 : 76} minSize={30}>
                  <ChatPanel key={workspaceId} workspace={workspace} readOnly={isArchived} />
                </Panel>

                {/* Central content viewer — between chat and the right panels.
                    This is where reviewing / editing / commenting happens, so it
                    takes the main content space when open. */}
                {hasViewer && (
                  <>
                    <Separator
                      id="sep-viewer"
                      className="w-1 shrink-0 bg-border transition-colors hover:bg-brand/40"
                    />
                    <Panel id="viewer" defaultSize={hasSide ? 46 : 58} minSize={30}>
                      <ContentViewerPanel workspaceId={workspaceId} target={viewer} />
                    </Panel>
                  </>
                )}

                {/* Right-side toggled panels (files list, changes list, …) —
                    navigation pickers, kept to a narrow column. */}
                {hasSide && (
                  <>
                    <Separator
                      id="sep-side"
                      className="w-1 shrink-0 bg-border transition-colors hover:bg-brand/40"
                    />
                    <Panel id="side" defaultSize={22} minSize={14}>
                      <Group orientation="vertical" className="h-full">
                        {sidePanels.map((panelId, index) => (
                          <React.Fragment key={panelId}>
                            {index > 0 && (
                              <Separator
                                id={`sep-${panelId}`}
                                className="h-1 shrink-0 bg-border transition-colors hover:bg-brand/40"
                              />
                            )}
                            <Panel
                              id={panelId}
                              defaultSize={Math.floor(100 / sidePanels.length)}
                              minSize={10}
                            >
                              <PanelContent panelId={panelId} workspaceId={workspaceId} workspace={workspace} isArchived={isArchived} />
                            </Panel>
                          </React.Fragment>
                        ))}
                      </Group>
                    </Panel>
                  </>
                )}
              </Group>
            )}
          </div>

          {!isArchived && <WorkspaceQuickActions workspace={workspace} />}
        </div>

        {/* #526·子A: per-workspace local executor entry. Desktop-remote only. */}
        {runnerAvailable && !isArchived && (
          <div className="flex h-8 shrink-0 border-t border-warm-border bg-warm-muted">
            <LocalRunnerBar workspaceId={workspaceId} workspaceName={workspace.name} />
          </div>
        )}
      </div>
    </div>
  );
}
