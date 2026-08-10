import { useState } from 'react'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  History, Undo2, GitCommitVertical, ShieldCheck, Flag, Camera, ChevronRight, ChevronDown,
} from 'lucide-react'
import { checkpointApi } from '@/lib/api'
import type { CheckpointListResponse, CheckpointStep, CheckpointRepo } from '@/types/api'
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { confirm } from '@/lib/confirm'
import { cn } from '@/lib/utils'

interface Props {
  workspaceId: string
}

const KIND_ICON: Record<CheckpointStep['kind'], React.ComponentType<{ className?: string }>> = {
  advance: GitCommitVertical,
  gate_pass: ShieldCheck,
  autohost_final: Flag,
  manual: Camera,
}

// Single-letter status chip, tokens only (mirrors the Changes panel).
const STATUS_BADGE: Record<string, { letter: string; cls: string }> = {
  modified: { letter: 'M', cls: 'bg-warning text-warning-foreground' },
  added: { letter: 'A', cls: 'bg-success text-success-foreground' },
  deleted: { letter: 'D', cls: 'bg-destructive text-destructive-foreground' },
  renamed: { letter: 'R', cls: 'bg-info text-info-foreground' },
  copied: { letter: 'C', cls: 'bg-info text-info-foreground' },
}

function fileName(p: string): string {
  const i = p.lastIndexOf('/')
  return i >= 0 ? p.slice(i + 1) : p
}

function formatTime(s: string): string {
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString()
}

// Autohost 安全网 checkpoint timeline (spec: hidden-ref 逐步快照与精确回退). Bound to a
// workspace: it renders the per-step snapshot history of the workspace's worktree(s)
// (refs/niuniu/<ws>/<issue>/<step>). Each step lists its changed FILES (like the
// Changes panel); clicking a file opens that step's diff for it in the central
// content viewer. A one-click "revert to this step" rewinds the worktree without
// losing later work. Rendered as a collapsible section (collapsed by default).
export function CheckpointTimeline({ workspaceId }: Props) {
  const { t } = useTranslation('projects')
  const queryClient = useQueryClient()
  const [expanded, setExpanded] = useState(false)

  const { data } = useQuery<CheckpointListResponse>({
    queryKey: ['workspace-checkpoints', workspaceId],
    queryFn: () => checkpointApi.timeline(workspaceId),
    staleTime: 15_000,
  })

  const revert = useMutation({
    mutationFn: (step: number) => checkpointApi.revert(workspaceId, step),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['workspace-checkpoints', workspaceId] })
      queryClient.invalidateQueries({ queryKey: ['workspace', workspaceId] })
    },
  })

  // Latest step first — the most recent snapshot is the most relevant.
  const steps = [...(data?.checkpoints ?? [])].reverse()
  const count = steps.length

  const onRevert = async (step: number) => {
    const ok = await confirm({
      description: t('kanban.checkpointTimeline.revertConfirm', { step }),
      destructive: true,
      confirmText: t('kanban.checkpointTimeline.revert'),
    })
    if (ok) revert.mutate(step)
  }

  return (
    <div className="border-b border-border">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-center gap-1.5 px-3 py-2 text-xs font-medium text-muted-foreground hover:bg-muted/50"
      >
        {expanded ? (
          <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />
        )}
        <History className="h-3.5 w-3.5" aria-hidden="true" />
        <span>{t('kanban.checkpointTimeline.title')}</span>
        {count > 0 && (
          <Badge variant="secondary" className="h-4 px-1 text-[10px] font-normal">
            {count}
          </Badge>
        )}
      </button>

      {expanded && (
        <div className="px-2 pb-2">
          {count === 0 ? (
            <p className="px-1 py-1 text-xs text-muted-foreground">
              {t('kanban.checkpointTimeline.empty')}
            </p>
          ) : (
            <ol className="flex flex-col gap-1.5">
              {steps.map((s) => (
                <CheckpointRow
                  key={s.step}
                  workspaceId={workspaceId}
                  step={s}
                  reverting={revert.isPending}
                  onRevert={() => onRevert(s.step)}
                />
              ))}
            </ol>
          )}
        </div>
      )}
    </div>
  )
}

interface RowProps {
  workspaceId: string
  step: CheckpointStep
  reverting: boolean
  onRevert: () => void
}

function CheckpointRow({ workspaceId, step, reverting, onRevert }: RowProps) {
  const { t } = useTranslation('projects')
  const [open, setOpen] = useState(false)
  const Icon = KIND_ICON[step.kind] ?? GitCommitVertical
  const passed = step.gate_status === 'pass'

  return (
    <li className="rounded-md border border-border">
      <div className="flex items-center gap-2 px-2 py-1.5">
        <span
          className={cn(
            'flex h-5 w-5 flex-shrink-0 items-center justify-center rounded-full border',
            passed
              ? 'border-success/30 bg-success/10 text-success'
              : 'border-border bg-muted text-muted-foreground',
          )}
        >
          <Icon className="h-3 w-3" aria-hidden="true" />
        </span>

        <div className="min-w-0 flex-1">
          <p className="break-words text-xs text-foreground">
            <span className="tabular-nums text-muted-foreground">#{step.step}</span>{' '}
            {step.label || t(`kanban.checkpointTimeline.kind.${step.kind}`)}
          </p>
          <p className="flex flex-wrap items-center gap-1 text-[10px] tabular-nums text-muted-foreground">
            <Badge variant="secondary" className="h-4 px-1 text-[10px] font-normal">
              {t(`kanban.checkpointTimeline.kind.${step.kind}`)}
            </Badge>
            {passed && (
              <Badge variant="outline" className="h-4 border-success/40 px-1 text-[10px] font-normal text-success">
                {t('kanban.checkpointTimeline.gatePass')}
              </Badge>
            )}
            <span>· {formatTime(step.created_at)}</span>
          </p>
        </div>

        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-1.5 text-[10px]"
          onClick={() => setOpen((o) => !o)}
        >
          {open ? t('kanban.checkpointTimeline.hideFiles') : t('kanban.checkpointTimeline.viewFiles')}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 px-1.5 text-[10px]"
          disabled={reverting}
          onClick={onRevert}
        >
          <Undo2 className="mr-0.5 h-3 w-3" aria-hidden="true" />
          {t('kanban.checkpointTimeline.revert')}
        </Button>
      </div>

      {open && (
        <div className="flex flex-col gap-1 border-t border-border p-1.5">
          {step.repos.map((r) => (
            <CheckpointRepoFiles key={r.id} workspaceId={workspaceId} repo={r} step={step.step} />
          ))}
        </div>
      )}
    </li>
  )
}

interface RepoFilesProps {
  workspaceId: string
  repo: CheckpointRepo
  step: number
}

// Renders one repo's changed-file list for a checkpoint step. Clicking a file opens
// its diff (that step's parent..snapshot) in the central content viewer.
function CheckpointRepoFiles({ workspaceId, repo, step }: RepoFilesProps) {
  const { t } = useTranslation('projects')
  const openViewer = useWorkspacePanelStore((s) => s.openContentViewer)
  const viewerTarget = useWorkspacePanelStore((s) => s.contentViewer[workspaceId] ?? null)

  const { data, isLoading } = useQuery({
    queryKey: ['checkpoint-diff', workspaceId, repo.id],
    queryFn: () => checkpointApi.diff(workspaceId, repo.id),
    staleTime: 60_000,
  })

  if (isLoading) {
    return <p className="px-1 text-[10px] text-muted-foreground">{t('kanban.checkpointTimeline.loading')}</p>
  }
  const files = data?.files ?? []
  if (files.length === 0) {
    return <p className="px-1 text-[10px] text-muted-foreground">{t('kanban.checkpointTimeline.noChanges')}</p>
  }

  return (
    <div className="flex flex-col">
      {repo.repo_name && (
        <p className="px-1 py-0.5 text-[10px] font-medium text-muted-foreground">{repo.repo_name}</p>
      )}
      {files.map((f) => {
        const badge = STATUS_BADGE[f.status] ?? STATUS_BADGE.modified
        const active =
          viewerTarget?.kind === 'checkpoint-diff' &&
          viewerTarget.checkpointId === repo.id &&
          viewerTarget.path === f.path
        return (
          <button
            key={f.path}
            type="button"
            title={f.path}
            onClick={() =>
              openViewer(workspaceId, {
                kind: 'checkpoint-diff',
                checkpointId: repo.id,
                path: f.path,
                repoName: repo.repo_name,
                step,
              })
            }
            className={cn(
              'flex w-full items-center gap-1.5 rounded px-1 py-0.5 text-left text-[11px] transition-colors',
              active ? 'bg-brand-soft' : 'hover:bg-accent',
            )}
          >
            <span
              className={cn(
                'grid h-[15px] w-[15px] shrink-0 place-items-center rounded text-[9px] font-semibold',
                badge.cls,
              )}
            >
              {badge.letter}
            </span>
            <span className={cn('flex-1 truncate', active ? 'font-semibold text-brand' : 'text-foreground')}>
              {fileName(f.path)}
            </span>
            <span className="flex shrink-0 items-center gap-1 font-mono text-[10px]">
              {f.additions > 0 && <span className="text-success">+{f.additions}</span>}
              {f.deletions > 0 && <span className="text-destructive">−{f.deletions}</span>}
            </span>
          </button>
        )
      })}
    </div>
  )
}
