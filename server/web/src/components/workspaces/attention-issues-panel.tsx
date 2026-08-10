import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Inbox } from 'lucide-react'
import { epicApi } from '@/lib/api'
import type { Issue } from '@/types/api'
import { ExecStatusBadge } from '@/components/shared/kanban/exec-status-badge'

// "需要我处理的" overview (spec section 19): issues in a terminal needs-human
// exec_status (blocked-needs-human / waiting-user-input / abandoned) across every
// owner the caller can access. Hidden entirely when nothing needs attention, so it
// never adds noise to the overview.
export function AttentionIssuesPanel() {
  const { t } = useTranslation('projects')
  const navigate = useNavigate()
  const { data } = useQuery<Issue[]>({
    queryKey: ['attention-issues'],
    queryFn: () => epicApi.attentionIssues(),
    staleTime: 15_000,
  })

  const issues = data ?? []
  if (issues.length === 0) return null

  return (
    <div className="rounded-lg border border-warm-border bg-warm-surface">
      <div className="flex items-center justify-between border-b border-warm-border px-4 py-2">
        <span className="flex items-center gap-1.5 text-sm font-medium text-warm-text">
          <Inbox className="h-4 w-4 text-destructive" aria-hidden="true" />
          {t('kanban.attention.title')}
        </span>
        <span className="rounded bg-destructive/10 px-1.5 py-0.5 text-[10px] font-medium tabular-nums text-destructive">
          {issues.length}
        </span>
      </div>
      <ul className="divide-y divide-warm-border/60">
        {issues.map((issue) => (
          <li key={issue.id}>
            <button
              type="button"
              className="flex w-full items-center gap-2 px-4 py-2 text-left hover:bg-warm-muted/50"
              onClick={() => navigate({ to: '/projects/$id', params: { id: String(issue.project_id) } })}
            >
              <span className="text-[11px] tabular-nums text-warm-text-muted">#{issue.id}</span>
              <span className="min-w-0 flex-1 truncate text-sm text-warm-text">{issue.title}</span>
              {issue.exec_status && <ExecStatusBadge status={issue.exec_status} />}
            </button>
            {issue.exec_status_reason && (
              <p className="px-4 pb-2 text-[11px] text-warm-text-muted">{issue.exec_status_reason}</p>
            )}
          </li>
        ))}
      </ul>
    </div>
  )
}
