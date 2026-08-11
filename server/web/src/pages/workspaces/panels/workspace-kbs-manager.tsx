import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Library, Plus, Trash2, RefreshCw, Loader2, FolderOpen } from 'lucide-react'
import { toast } from 'sonner'
import { confirm } from '@/lib/confirm'
import { Button } from '@/components/ui/button'
import {
  listKnowledgeBases,
  listWorkspaceKnowledgeBases,
  mountWorkspaceKnowledgeBase,
  syncWorkspaceKnowledgeBase,
  unmountWorkspaceKnowledgeBase,
  type WorkspaceKBMount,
} from '@/lib/kb-api'

// Per-workspace knowledge-base mounting (KB as a first-class citizen, mirroring
// worktrees). Mounting a KB materializes its content read-only into
// <workspace>/datasets/<name>/ (visible in the workspace file tree) and
// auto-ingests it. Rendered inside the workspace settings dialog next to the
// repository/worktree management it complements.

interface WorkspaceKBsManagerProps {
  workspaceId: string
}

export function WorkspaceKBsManager({ workspaceId }: WorkspaceKBsManagerProps) {
  const { t } = useTranslation('knowledge')
  const queryClient = useQueryClient()
  const [selectedKbId, setSelectedKbId] = useState('')

  const queryKey = ['workspace-kbs', workspaceId]
  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey })
    // The mounted dataset dirs live in the workspace file tree, so refresh it.
    queryClient.invalidateQueries({ queryKey: ['workspace-tree', workspaceId] })
  }

  const { data: mounts = [], isLoading } = useQuery({
    queryKey,
    queryFn: () => listWorkspaceKnowledgeBases(workspaceId),
  })
  const { data: allKbs = [] } = useQuery({
    queryKey: ['knowledge-bases'],
    queryFn: listKnowledgeBases,
  })

  const mountedIds = new Set(mounts.map((m) => m.kb_id))
  const available = allKbs.filter((kb) => !mountedIds.has(kb.id))

  const mount = useMutation({
    mutationFn: (kbId: number) => mountWorkspaceKnowledgeBase(workspaceId, kbId),
    onSuccess: () => {
      setSelectedKbId('')
      invalidate()
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })
  const sync = useMutation({
    mutationFn: (kbId: number) => syncWorkspaceKnowledgeBase(workspaceId, kbId),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })
  const unmount = useMutation({
    mutationFn: (kbId: number) => unmountWorkspaceKnowledgeBase(workspaceId, kbId),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  })

  const handleMount = () => {
    if (!selectedKbId) return
    mount.mutate(Number(selectedKbId))
  }

  const handleUnmount = async (m: WorkspaceKBMount) => {
    if (
      !(await confirm({
        description: t('workspaceMount.unmountConfirm', { name: m.name }),
        destructive: true,
      }))
    ) {
      return
    }
    unmount.mutate(m.kb_id)
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <label className="block text-sm font-medium text-foreground">
          {t('workspaceMount.title')}
        </label>
        <FolderOpen className="h-3.5 w-3.5 text-muted-foreground/70" aria-hidden />
      </div>
      <p className="text-xs text-muted-foreground mb-2">
        {t('workspaceMount.description')}
      </p>

      {isLoading ? (
        <p className="text-xs text-muted-foreground italic">
          {t('common:actions.loading')}
        </p>
      ) : mounts.length === 0 ? (
        <p className="text-xs text-muted-foreground italic">
          {t('workspaceMount.none')}
        </p>
      ) : (
        <div className="rounded-md border border-border divide-y divide-border">
          {mounts.map((m) => (
            <div key={m.kb_id} className="px-3 py-2">
              <div className="flex items-center gap-2">
                <Library className="h-3.5 w-3.5 text-muted-foreground/70 shrink-0" aria-hidden />
                <span className="text-sm font-medium text-foreground truncate">
                  {m.name}
                </span>
                <div className="ml-auto flex items-center gap-1 shrink-0">
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-label={t('workspaceMount.sync')}
                    disabled={sync.isPending && sync.variables === m.kb_id}
                    onClick={() => sync.mutate(m.kb_id)}
                  >
                    <RefreshCw
                      className={`h-3.5 w-3.5 ${sync.isPending && sync.variables === m.kb_id ? 'animate-spin' : ''}`}
                    />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-label={t('workspaceMount.unmount')}
                    disabled={unmount.isPending && unmount.variables === m.kb_id}
                    onClick={() => handleUnmount(m)}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </div>
              <p className="flex items-center gap-1 text-xs text-muted-foreground/70 mt-0.5 pl-6 font-mono truncate">
                <span className="shrink-0 rounded bg-muted px-1 py-px text-xs not-italic">
                  {t('workspaceMount.readOnly')}
                </span>
                {t('workspaceMount.datasetPath', {
                  name: datasetDirName(m.dataset_path),
                })}
              </p>
            </div>
          ))}
        </div>
      )}

      <div className="mt-2 flex items-center gap-2">
        <select
          aria-label={t('workspaceMount.pick')}
          value={selectedKbId}
          onChange={(e) => setSelectedKbId(e.target.value)}
          disabled={available.length === 0 || mount.isPending}
          className="h-8 flex-1 min-w-0 rounded border border-border bg-background px-2 text-sm focus:outline-none focus:ring-1 focus:ring-info"
        >
          <option value="">
            {available.length === 0
              ? t('workspaceMount.allMounted')
              : t('workspaceMount.pick')}
          </option>
          {available.map((kb) => (
            <option key={kb.id} value={String(kb.id)}>
              {kb.name}
            </option>
          ))}
        </select>
        <Button
          size="sm"
          onClick={handleMount}
          disabled={!selectedKbId || mount.isPending}
        >
          {mount.isPending ? (
            <Loader2 className="h-3.5 w-3.5 mr-1 animate-spin" />
          ) : (
            <Plus className="h-3.5 w-3.5 mr-1" />
          )}
          {t('workspaceMount.mount')}
        </Button>
      </div>
    </div>
  )
}

/** Last path segment of a mounted dataset dir (the KB's sanitized name). */
function datasetDirName(datasetPath: string): string {
  const parts = datasetPath.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] ?? datasetPath
}