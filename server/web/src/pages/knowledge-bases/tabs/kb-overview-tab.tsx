import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { FileText, Layers, Clock, FolderOpen, Boxes } from 'lucide-react'
import { api } from '@/lib/api'
import { KB_SOURCE_ICON } from '@/components/knowledge/kb-source-meta'
import type { Project } from '@/types/api'
import type { KnowledgeBase } from '@/lib/kb-api'

/**
 * At-a-glance summary of one KB: corpus size, where it came from, when it was
 * last indexed, and which projects can see it.
 *
 * The old list row showed doc/chunk counts and nothing else — `last_indexed_at`
 * and the resolved binding names were already in the API response but had no
 * surface to appear on, so "is this stale?" and "who can actually reach this?"
 * were unanswerable in the UI.
 */
export function KBOverviewTab({ kb }: { kb: KnowledgeBase }) {
  const { t, i18n } = useTranslation('knowledge')
  const SourceIcon = KB_SOURCE_ICON[kb.source_kind]

  // Resolve bound project ids to names. Cached and shared with the dialog's
  // binding editor, so switching tabs does not refetch.
  const projects = useQuery({
    queryKey: ['projects', 'active'],
    queryFn: () => api.get<Project[]>('/projects', { params: { status: 'active' } }),
  })

  const boundNames = kb.bindings.map((b) => {
    const hit = (projects.data ?? []).find((p) => p.id === b.target_id)
    return hit ? hit.name : `#${b.target_id}`
  })

  const lastIndexed = kb.last_indexed_at
    ? new Intl.DateTimeFormat(i18n.language, {
        dateStyle: 'medium',
        timeStyle: 'short',
      }).format(new Date(kb.last_indexed_at))
    : t('overview.neverIndexed')

  return (
    <div className="h-full overflow-y-auto p-4">
      <div className="flex flex-col gap-4 max-w-3xl">
        {kb.description && (
          <p className="text-sm text-muted-foreground">{kb.description}</p>
        )}

        <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
          <StatCard
            icon={FileText}
            label={t('overview.documents')}
            value={kb.doc_count.toLocaleString(i18n.language)}
          />
          <StatCard
            icon={Layers}
            label={t('overview.chunks')}
            value={kb.chunk_count.toLocaleString(i18n.language)}
          />
          <StatCard
            icon={Clock}
            label={t('overview.lastIndexed')}
            value={lastIndexed}
          />
        </div>

        <section className="rounded-lg border border-warm-border bg-warm-surface p-3 flex flex-col gap-2">
          <h2 className="text-sm font-medium flex items-center gap-1.5">
            <SourceIcon className="size-4 text-muted-foreground" aria-hidden />
            {t('overview.source')}
          </h2>
          <dl className="flex flex-col gap-1.5 text-sm">
            <div className="flex gap-2">
              <dt className="text-muted-foreground w-24 shrink-0">
                {t('dialog.sourceKindLabel')}
              </dt>
              <dd>{t(`sourceKind.${kb.source_kind}`)}</dd>
            </div>
            {kb.source_location && (
              <div className="flex gap-2 min-w-0">
                <dt className="text-muted-foreground w-24 shrink-0">
                  {t('overview.location')}
                </dt>
                <dd className="font-mono text-xs break-all min-w-0">
                  {kb.source_location}
                </dd>
              </div>
            )}
          </dl>
        </section>

        <section className="rounded-lg border border-warm-border bg-warm-surface p-3 flex flex-col gap-2">
          <h2 className="text-sm font-medium flex items-center gap-1.5">
            <Boxes className="size-4 text-muted-foreground" aria-hidden />
            {t('bindings.title')}
          </h2>
          {boundNames.length === 0 ? (
            <p className="text-xs text-muted-foreground">
              {t('overview.unboundWarning')}
            </p>
          ) : (
            <ul className="flex flex-wrap gap-1.5">
              {boundNames.map((name) => (
                <li
                  key={name}
                  className="inline-flex items-center gap-1 rounded bg-warm-muted px-1.5 py-0.5 text-xs text-warm-text-muted"
                >
                  <FolderOpen className="size-3" aria-hidden />
                  {name}
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </div>
  )
}

function StatCard({
  icon: Icon,
  label,
  value,
}: {
  icon: typeof FileText
  label: string
  value: string
}) {
  return (
    <div className="rounded-lg border border-warm-border bg-warm-surface p-3 flex flex-col gap-1">
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <Icon className="size-3.5" aria-hidden />
        {label}
      </span>
      <span className="text-lg font-medium tabular-nums truncate">{value}</span>
    </div>
  )
}
