import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useAgentSSEStore } from '@/stores/agent-sse-store';
import { getFileContentUrl } from '@/lib/workspace-file-url';
import { ArtifactPreviewPanel, type ArtifactFile, type ArtifactPanelVariant } from './artifact-preview-panel';

interface ArtifactPanelContainerProps {
  workspaceId: string;
  /** `viewer` (default, workspace) opens clicks in the content viewer; `inline`
   *  previews the selected deliverable below the list. */
  variant?: ArtifactPanelVariant;
}

// The agent declares which files are user-facing products by maintaining this
// manifest at the workspace root. Whether a file is a deliverable is the AI's
// decision — extension is only used to pick a renderer, never to decide
// membership.
const ARTIFACT_MANIFEST_PATH = '.niuniu/artifacts.json';

function basename(path: string): string {
  return path.split('/').pop() || path;
}

// fetchManifest returns the AI-declared product list, or [] when there's no
// (valid) manifest yet. The manifest is the sole source of truth — a file is a
// product only if the AI declared it.
async function fetchManifest(workspaceId: string): Promise<ArtifactFile[]> {
  try {
    const res = await fetch(getFileContentUrl(workspaceId, ARTIFACT_MANIFEST_PATH, 'raw'));
    if (!res.ok) return [];
    const data = JSON.parse(await res.text()) as unknown;
    const arr = Array.isArray(data)
      ? data
      : (data as { artifacts?: unknown }).artifacts;
    if (!Array.isArray(arr)) return [];
    return arr
      .filter((a): a is { path: string; title?: string } =>
        !!a && typeof (a as { path?: unknown }).path === 'string')
      .map((a) => ({ path: a.path, name: (a.title || basename(a.path)).trim() || a.path }));
  } catch {
    return [];
  }
}

// ArtifactPanelContainer surfaces the conversation's deliverables, sourced
// solely from the agent-maintained manifest (the AI decides what's a product).
export function ArtifactPanelContainer({ workspaceId, variant }: ArtifactPanelContainerProps) {
  const { t } = useTranslation('workspaces');
  const queryClient = useQueryClient();
  const queryKey = ['workspace-artifacts', workspaceId];
  const { data, isLoading } = useQuery({
    queryKey,
    queryFn: () => fetchManifest(workspaceId),
    staleTime: 30_000,
  });

  // The agent writes/declares deliverables mid-conversation, so refresh
  // (debounced) whenever it touches files or finishes a turn.
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    const refresh = () => {
      if (timerRef.current) clearTimeout(timerRef.current);
      timerRef.current = setTimeout(() => {
        queryClient.invalidateQueries({ queryKey: ['workspace-artifacts', workspaceId] });
      }, 800);
    };
    const unsub = useAgentSSEStore.getState().addHandler(workspaceId, (msg) => {
      if (msg.type === 'tool_use' || msg.type === 'idle' || msg.type === 'done') refresh();
    });
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
      unsub();
    };
  }, [workspaceId, queryClient]);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full text-xs text-muted-foreground">
        {t('filePreview.loading')}
      </div>
    );
  }

  return <ArtifactPreviewPanel workspaceId={workspaceId} artifacts={data ?? []} variant={variant} />;
}
