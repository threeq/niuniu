import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useSearch } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Inbox, RefreshCw } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { EmptyState } from '@/components/shared/empty-state'
import { useProjects } from '@/lib/hooks/use-projects'
import { useMediaQuery } from '@/hooks/use-media-query'
import { intakeApi, type IntakeDecision, type IntakeDecisionFilter } from '@/lib/intake-api'
import { cn } from '@/lib/utils'
import { DecisionList } from '@/components/intake/decision-list'
import { DecisionDetail } from '@/components/intake/decision-detail'
import { IgnoreReasonDialog } from '@/components/intake/ignore-reason-dialog'
import type { IgnoreReason } from '@/components/intake/ignore-reason'

type QueueFilter = 'all' | 'accept' | 'resume' | 'ignore'

const FILTERS: QueueFilter[] = ['all', 'accept', 'resume', 'ignore']

function filterParams(filter: QueueFilter): IntakeDecisionFilter {
  return { executed: false, decision: filter === 'all' ? undefined : filter, limit: 100 }
}

export function IntakeReviewPage() {
  const { t } = useTranslation('intake')
  const queryClient = useQueryClient()
  const isDesktop = useMediaQuery('(min-width: 768px)')
  const { projects, isLoading: projectsLoading } = useProjects()
  // Deep link from an IM chat row: preselect the project and narrow the queue to
  // that conversation (design §6.7.7).
  const search = useSearch({ strict: false }) as { project?: number; chat?: string }

  const [pickedProjectId, setPickedProjectId] = useState<number | null>(search.project ?? null)
  const [filter, setFilter] = useState<QueueFilter>('all')
  const [activeId, setActiveId] = useState<number | null>(null)
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [seenIds, setSeenIds] = useState<Set<number>>(new Set())
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [batchOpen, setBatchOpen] = useState(false)

  // Default to the deep-linked project, else the first one, without an effect:
  // the derived id is what the queries use and an explicit choice overrides it.
  const projectId = pickedProjectId ?? search.project ?? projects?.[0]?.id ?? null

  const decisionsQuery = useQuery({
    queryKey: ['intake-decisions', projectId, filter],
    queryFn: () => intakeApi.listDecisions(projectId as number, filterParams(filter)),
    enabled: projectId !== null,
    retry: 1,
    refetchInterval: 30_000,
  })

  const rulesQuery = useQuery({
    queryKey: ['intake-rules', projectId],
    queryFn: () => intakeApi.listRules(projectId as number),
    enabled: projectId !== null,
    retry: 1,
  })

  // A chat deep link narrows the queue client-side: decisions already carry the
  // originating conversation's platform id, so no extra endpoint is needed.
  const decisions = useMemo(() => {
    const all = decisionsQuery.data ?? []
    if (!search.chat) return all
    return all.filter((d) => d.payload?.ChatExtID === search.chat)
  }, [decisionsQuery.data, search.chat])

  // Keep a valid selection as the list changes (accept/ignore removes a row)
  // without an effect: falling back to the first row is derived at render time.
  const active = decisions.find((d) => d.id === activeId) ?? decisions[0] ?? null
  const activeDecisionId = active?.id ?? null

  function selectDecision(decision: IntakeDecision) {
    setActiveId(decision.id)
    setSeenIds((current) => new Set(current).add(decision.id))
    if (!isDesktop) setDrawerOpen(true)
  }

  function toggleSelected(id: number, checked: boolean) {
    setSelectedIds((current) => {
      const next = new Set(current)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  function resetSelection() {
    setSelectedIds(new Set())
  }

  const batchIgnore = useMutation({
    mutationFn: async (reason: IgnoreReason) => {
      const ids = [...selectedIds]
      for (const id of ids) {
        await intakeApi.ignore(projectId as number, id, reason.code, reason.text)
      }
      return ids.length
    },
    onSuccess: (count) => {
      setBatchOpen(false)
      resetSelection()
      toast.success(t('review.ignoredCount', { count }))
      queryClient.invalidateQueries({ queryKey: ['intake-decisions'] })
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t('review.ignoreFailed'))
      queryClient.invalidateQueries({ queryKey: ['intake-decisions'] })
    },
  })

  const listColumn = (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-1 border-b border-warm-border px-2 py-2">
        {FILTERS.map((value) => (
          <Button
            key={value}
            type="button"
            variant={filter === value ? 'secondary' : 'ghost'}
            size="sm"
            className={cn('h-7 px-2 text-xs', filter === value && 'font-semibold')}
            onClick={() => {
              setFilter(value)
              resetSelection()
            }}
          >
            {t(`review.filter.${value}`)}
          </Button>
        ))}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="ml-auto h-7 w-7"
          onClick={() => void decisionsQuery.refetch()}
          aria-label={t('review.refresh')}
          title={t('review.refresh')}
        >
          <RefreshCw className={cn('h-3.5 w-3.5', decisionsQuery.isFetching && 'animate-spin')} />
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {decisionsQuery.isLoading && <ListSkeleton />}

        {!decisionsQuery.isLoading && decisionsQuery.isError && (
          <div className="flex flex-col items-center gap-3 px-4 py-12 text-center">
            <p className="text-sm text-warm-text-muted">{t('review.loadError')}</p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void decisionsQuery.refetch()}
            >
              {t('intake.retry')}
            </Button>
          </div>
        )}

        {!decisionsQuery.isLoading && !decisionsQuery.isError && decisions.length === 0 && (
          <EmptyState
            size="compact"
            icon={<Inbox className="h-10 w-10 text-muted-foreground" />}
            title={filter === 'ignore' ? t('review.emptyIgnoredTitle') : t('review.emptyTitle')}
            description={
              filter === 'ignore' ? t('review.emptyIgnoredDescription') : t('review.emptyDescription')
            }
            action={
              filter !== 'ignore' && projectId !== null ? (
                <Button asChild variant="outline" size="sm">
                  <Link
                    to="/projects/$id"
                    params={{ id: String(projectId) }}
                    search={{ tab: 'settings', section: 'intake' }}
                  >
                    {t('review.goToSources')}
                  </Link>
                </Button>
              ) : undefined
            }
          />
        )}

        {!decisionsQuery.isLoading && !decisionsQuery.isError && decisions.length > 0 && (
          <DecisionList
            decisions={decisions}
            activeId={activeDecisionId}
            selectedIds={selectedIds}
            seenIds={seenIds}
            onSelect={selectDecision}
            onToggleSelect={toggleSelected}
          />
        )}
      </div>

      {selectedIds.size > 0 && (
        <div className="flex items-center gap-2 border-t border-warm-border bg-warm-surface px-3 py-2">
          <span className="text-xs font-medium tabular-nums">
            {t('review.selectedCount', { count: selectedIds.size })}
          </span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-7 border-destructive/50 px-2 text-xs text-destructive hover:bg-destructive/10 hover:text-destructive"
            onClick={() => setBatchOpen(true)}
          >
            {t('review.batchIgnore')}
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="ml-auto h-7 px-2 text-xs"
            onClick={resetSelection}
          >
            {t('review.clearSelection')}
          </Button>
        </div>
      )}
    </div>
  )

  const detailContent = active ? (
    <DecisionDetail
      key={active.id}
      decision={active}
      rules={rulesQuery.data ?? []}
      rulesFailed={rulesQuery.isError}
    />
  ) : (
    <div className="flex h-full items-center justify-center p-6 text-sm text-warm-text-muted">
      {t('review.selectPrompt')}
    </div>
  )

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-3 border-b border-warm-border px-4 py-3">
        <div className="min-w-0">
          <h1 className="text-lg font-semibold text-warm-text">{t('review.title')}</h1>
          <p className="text-xs text-warm-text-muted">{t('review.description')}</p>
        </div>
        <div className="ml-auto flex items-center gap-2">
          <label className="text-xs text-warm-text-muted" htmlFor="intake-project">
            {t('review.project')}
          </label>
          <Select
            value={projectId !== null ? String(projectId) : undefined}
            onValueChange={(value) => {
              setPickedProjectId(Number(value))
              setActiveId(null)
              resetSelection()
            }}
          >
            <SelectTrigger
              id="intake-project"
              className="h-8 w-44"
              aria-label={t('review.project')}
              disabled={projectsLoading || !projects?.length}
            >
              <SelectValue placeholder={t('review.projectPlaceholder')} />
            </SelectTrigger>
            <SelectContent>
              {(projects ?? []).map((project) => (
                <SelectItem key={project.id} value={String(project.id)}>
                  {project.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {search.chat && (
            <div className="flex items-center gap-2">
              <Badge variant="outline" className="text-xs font-normal text-warm-text-muted">
                {t('review.chatOnly')}
              </Badge>
              <Link
                to="/intake"
                search={projectId !== null ? { project: projectId } : {}}
                className="text-xs text-brand hover:underline"
              >
                {t('review.clearChatFilter')}
              </Link>
            </div>
          )}
        </div>
      </div>

      {projects && projects.length === 0 ? (
        <EmptyState
          icon={<Inbox className="h-12 w-12 text-muted-foreground" />}
          title={t('review.noProjectTitle')}
          description={t('review.noProjectDescription')}
        />
      ) : (
        <div className="grid min-h-0 flex-1 md:grid-cols-[minmax(0,22rem)_minmax(0,1fr)]">
          {/* Desktop: master-detail. Mobile: the list only; a row opens the
              same detail in a bottom drawer. */}
          <div className="min-h-0 md:border-r md:border-warm-border">{listColumn}</div>
          {isDesktop && <div className="min-h-0">{detailContent}</div>}
        </div>
      )}

      {!isDesktop && (
        <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
          <SheetContent side="bottom" className="flex h-[85vh] flex-col gap-0 p-0">
            <SheetHeader className="border-b border-warm-border px-4 py-3 text-left">
              <SheetTitle className="text-sm font-semibold">{t('review.title')}</SheetTitle>
            </SheetHeader>
            <div className="min-h-0 flex-1 overflow-hidden">{detailContent}</div>
          </SheetContent>
        </Sheet>
      )}

      <IgnoreReasonDialog
        open={batchOpen}
        onOpenChange={setBatchOpen}
        count={selectedIds.size}
        pending={batchIgnore.isPending}
        onConfirm={(reason) => batchIgnore.mutate(reason)}
      />
    </div>
  )
}

function ListSkeleton() {
  return (
    <div className="flex flex-col gap-4 px-3 py-3" aria-hidden>
      {[0, 1, 2, 3, 4].map((index) => (
        <div key={index} className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <div className="h-4 w-4 animate-pulse rounded bg-muted" />
            <div className="h-3 w-24 animate-pulse rounded bg-muted" />
            <div className="ml-auto h-3 w-10 animate-pulse rounded bg-muted" />
          </div>
          <div className="h-3.5 w-full animate-pulse rounded bg-muted" />
          <div className="h-3.5 w-2/3 animate-pulse rounded bg-muted" />
        </div>
      ))}
    </div>
  )
}
