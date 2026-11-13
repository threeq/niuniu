import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useSearch } from '@tanstack/react-router'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ChevronDown, ChevronRight, Inbox, RefreshCw, Trash2, Zap } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { EmptyState } from '@/components/shared/empty-state'
import { useProjects } from '@/lib/hooks/use-projects'
import { useMediaQuery } from '@/hooks/use-media-query'
import {
  intakeApi,
  type IntakeDecision,
  type IntakeSourceKind,
} from '@/lib/intake-api'
import { cn } from '@/lib/utils'
import { DecisionList } from '@/components/intake/decision-list'
import { DecisionDetail } from '@/components/intake/decision-detail'
import { EnableAutoDialog } from '@/components/intake/enable-auto-dialog'
import { IgnoreReasonDialog } from '@/components/intake/ignore-reason-dialog'
import type { IgnoreReason } from '@/components/intake/ignore-reason'

type QueueView = 'pending' | 'history'

const SOURCE_KINDS: IntakeSourceKind[] = ['im_bot', 'github', 'ci', 'email', 'webhook']

const ALL_SOURCES = 'all'

function isSourceKind(value: string | undefined): value is IntakeSourceKind {
  return !!value && (SOURCE_KINDS as string[]).includes(value)
}

function decisionStatus(decision: IntakeDecision): 'pending' | 'accepted' | 'ignored' | 'consumed' {
  if (decision.status) return decision.status
  if (decision.decision === 'ignore') return 'ignored'
  if (decision.consumed) return 'consumed'
  return decision.executed ? 'accepted' : 'pending'
}

export function IntakeReviewPage() {
  const { t } = useTranslation('intake')
  const queryClient = useQueryClient()
  const isDesktop = useMediaQuery('(min-width: 768px)')
  const { projects } = useProjects()
  // Deep link from an IM chat row: preselect the project and narrow the queue to
  // that conversation (design §6.7.7). Mission control drills here with
  // ?source=<kind> for a per-source view.
  const search = useSearch({ strict: false }) as {
    project?: number
    chat?: string
    source?: string
  }

  const [view, setView] = useState<QueueView>('pending')
  const [source, setSource] = useState<string>(search.source ?? ALL_SOURCES)
  const [activeId, setActiveId] = useState<number | null>(null)
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [seenIds, setSeenIds] = useState<Set<number>>(new Set())
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [batchOpen, setBatchOpen] = useState(false)
  const [enableOpen, setEnableOpen] = useState(false)

  // No project selector: the queue spans every accessible project and groups by
  // project. A deep link (?project=) still narrows it.
  const projectFilter = search.project ?? null
  const [collapsed, setCollapsed] = useState<Set<number>>(new Set())
  function toggleGroup(projectId: number) {
    setCollapsed((current) => {
      const next = new Set(current)
      if (next.has(projectId)) next.delete(projectId)
      else next.add(projectId)
      return next
    })
  }
  const projectIds = useMemo(
    () => (projectFilter !== null ? [projectFilter] : (projects ?? []).map((p) => p.id)),
    [projectFilter, projects],
  )
  const projectName = useMemo(() => {
    const map = new Map<number, string>()
    for (const project of projects ?? []) map.set(project.id, project.name)
    return map
  }, [projects])

  const decisionQueries = useQueries({
    queries: projectIds.map((pid) => ({
      queryKey: ['intake-decisions', pid, view, source],
      queryFn: () =>
        intakeApi.listDecisions(pid, {
          executed: view === 'pending' ? false : undefined,
          source_kind: isSourceKind(source) ? source : undefined,
          limit: 100,
        }),
      retry: 1,
      refetchInterval: 30_000,
    })),
  })

  const isLoading = decisionQueries.some((query) => query.isLoading)
  const isError = decisionQueries.length > 0 && decisionQueries.every((query) => query.isError)
  const isFetching = decisionQueries.some((query) => query.isFetching)

  // The history view is everything that is no longer pending (accepted or
  // ignored); the pending view keeps only actionable rows.
  const decisions = useMemo(() => {
    const all = decisionQueries.flatMap((query) => query.data ?? [])
    const scoped = search.chat
      ? all.filter((d) => d.payload?.ChatExtID === search.chat)
      : all
    return scoped.filter((d) =>
      view === 'pending' ? decisionStatus(d) === 'pending' : decisionStatus(d) !== 'pending',
    )
  }, [decisionQueries, search.chat, view])

  // Group by project in the sidebar order; projects without rows are omitted.
  const groups = useMemo(() => {
    const byProject = new Map<number, IntakeDecision[]>()
    for (const decision of decisions) {
      const list = byProject.get(decision.project_id) ?? []
      list.push(decision)
      byProject.set(decision.project_id, list)
    }
    const ordered = projectIds.filter((id) => byProject.has(id))
    for (const id of byProject.keys()) if (!ordered.includes(id)) ordered.push(id)
    return ordered.map((id) => ({
      projectId: id,
      name: projectName.get(id) ?? `#${id}`,
      items: byProject.get(id) ?? [],
    }))
  }, [decisions, projectIds, projectName])

  const active = decisions.find((d) => d.id === activeId) ?? decisions[0] ?? null
  const activeDecisionId = active?.id ?? null

  // Rules and calibration belong to a single project. When "all projects" is
  // shown, follow the selected decision; fall back to the picked project.
  const contextProjectId = active?.project_id ?? projectFilter ?? null

  const rulesQuery = useQuery({
    queryKey: ['intake-rules', contextProjectId],
    queryFn: () => intakeApi.listRules(contextProjectId as number),
    enabled: contextProjectId !== null,
    retry: 1,
  })

  const calibrationQuery = useQuery({
    queryKey: ['intake-calibration', contextProjectId],
    queryFn: () => intakeApi.getCalibration(contextProjectId as number),
    enabled: contextProjectId !== null,
    retry: 1,
    refetchInterval: 30_000,
  })

  const settingsQuery = useQuery({
    queryKey: ['intake-settings', contextProjectId],
    queryFn: () => intakeApi.getSettings(contextProjectId as number),
    enabled: contextProjectId !== null,
    retry: 1,
  })

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

  // "Select all" covers exactly the rows currently visible (view + source +
  // chat filters), so selecting in the pending view never sweeps in history.
  const visibleIds = useMemo(() => decisions.map((d) => d.id), [decisions])
  const allSelected = visibleIds.length > 0 && visibleIds.every((id) => selectedIds.has(id))
  const someSelected = visibleIds.some((id) => selectedIds.has(id))
  function toggleSelectAll(checked: boolean) {
    setSelectedIds((current) => {
      const next = new Set(current)
      for (const id of visibleIds) {
        if (checked) next.add(id)
        else next.delete(id)
      }
      return next
    })
  }

  function refreshAll() {
    for (const query of decisionQueries) void query.refetch()
  }

  const batchIgnore = useMutation({
    mutationFn: async (reason: IgnoreReason) => {
      const ids = [...selectedIds]
      const byProject = new Map(decisions.map((d) => [d.id, d.project_id]))
      for (const id of ids) {
        const pid = byProject.get(id)
        if (pid === undefined) continue
        await intakeApi.ignore(pid, id, reason.code, reason.text)
      }
      return ids.length
    },
    onSuccess: (count) => {
      setBatchOpen(false)
      resetSelection()
      toast.success(t('review.ignoredCount', { count }))
      queryClient.invalidateQueries({ queryKey: ['intake-decisions'] })
      queryClient.invalidateQueries({ queryKey: ['intake-calibration'] })
      queryClient.invalidateQueries({ queryKey: ['intake-pending-count'] })
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t('review.ignoreFailed'))
      queryClient.invalidateQueries({ queryKey: ['intake-decisions'] })
      queryClient.invalidateQueries({ queryKey: ['intake-pending-count'] })
    },
  })

  // Accept the selected suggestions with their stored briefs (no per-item edit
  // in batch mode). Each accept runs the normal pipeline, so this creates and
  // starts the matching tasks/workspaces. Sequential on purpose: a burst of
  // agent starts is heavier than the reviewer expects from one click.
  const batchAccept = useMutation({
    mutationFn: async () => {
      const byProject = new Map(decisions.map((d) => [d.id, d.project_id]))
      // Rows that vanished from the current view (already handled elsewhere)
      // are simply skipped rather than reported as failures.
      const ids = [...selectedIds].filter((id) => byProject.has(id))
      let accepted = 0
      let failed = 0
      for (const id of ids) {
        try {
          await intakeApi.accept(byProject.get(id) as number, id)
          accepted++
        } catch {
          failed++
        }
      }
      return { accepted, failed }
    },
    onSuccess: ({ accepted, failed }) => {
      resetSelection()
      if (failed === 0) {
        toast.success(t('review.acceptedCount', { count: accepted }))
      } else {
        toast.warning(t('review.batchPartial', { ok: accepted, failed }))
      }
      queryClient.invalidateQueries({ queryKey: ['intake-decisions'] })
      queryClient.invalidateQueries({ queryKey: ['intake-calibration'] })
      queryClient.invalidateQueries({ queryKey: ['intake-pending-count'] })
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t('review.acceptFailed'))
      queryClient.invalidateQueries({ queryKey: ['intake-decisions'] })
      queryClient.invalidateQueries({ queryKey: ['intake-pending-count'] })
    },
  })

  const batchDelete = useMutation({
    mutationFn: async () => {
      const ids = [...selectedIds]
      const byProject = new Map(decisions.map((d) => [d.id, d.project_id]))
      for (const id of ids) {
        const pid = byProject.get(id)
        if (pid === undefined) continue
        await intakeApi.deleteDecision(pid, id)
      }
      return ids.length
    },
    onSuccess: (count) => {
      resetSelection()
      toast.success(t('review.deletedCount', { count }))
      queryClient.invalidateQueries({ queryKey: ['intake-decisions'] })
      queryClient.invalidateQueries({ queryKey: ['intake-pending-count'] })
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t('review.deleteFailed'))
      queryClient.invalidateQueries({ queryKey: ['intake-decisions'] })
      queryClient.invalidateQueries({ queryKey: ['intake-pending-count'] })
    },
  })

  const enableAuto = useMutation({
    mutationFn: (sourceKinds: string[]) => {
      const current = settingsQuery.data
      if (!current || contextProjectId === null) {
        throw new Error(t('intake.calibration.dialog.settingsUnavailable'))
      }
      const rows = current.settings
        .filter((setting) => sourceKinds.includes(setting.source_kind))
        .map((setting) => ({
          source_kind: setting.source_kind as IntakeSourceKind,
          enabled: true,
          mode: 'auto' as const,
          daily_cap: setting.daily_cap,
          allow_list: setting.allow_list,
          bot_list: setting.bot_list,
        }))
      return intakeApi.updateSettings(contextProjectId, rows)
    },
    onSuccess: () => {
      setEnableOpen(false)
      toast.success(t('intake.calibration.dialog.enabledToast'))
      queryClient.invalidateQueries({ queryKey: ['intake-calibration', contextProjectId] })
      queryClient.invalidateQueries({ queryKey: ['intake-settings', contextProjectId] })
      queryClient.invalidateQueries({ queryKey: ['intake-decisions'] })
    },
    onError: (error) => {
      toast.error(
        t('intake.calibration.dialog.enabledFailed', {
          message: error instanceof Error ? error.message : String(error),
        }),
      )
    },
  })

  const calibration = calibrationQuery.data
  const ready = calibration?.ready === true
  const calibratedKinds = (calibration?.sources ?? []).filter((s) => s.calibrated > 0)
  const allCalibratedOnAuto =
    calibratedKinds.length > 0 &&
    calibratedKinds.every(
      (sourceEntry) =>
        settingsQuery.data?.settings.find((s) => s.source_kind === sourceEntry.source_kind)?.mode ===
        'auto',
    )

  const listColumn = (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-1 border-b border-warm-border px-2 py-2">
        {(['pending', 'history'] as QueueView[]).map((value) => (
          <Button
            key={value}
            type="button"
            variant={view === value ? 'secondary' : 'ghost'}
            size="sm"
            className={cn('h-7 px-2 text-xs', view === value && 'font-semibold')}
            onClick={() => {
              setView(value)
              setActiveId(null)
              resetSelection()
            }}
          >
            {t(`review.view.${value}`)}
          </Button>
        ))}
        {decisions.length > 0 && (
          <label className="flex cursor-pointer select-none items-center gap-1.5 text-xs text-warm-text-muted">
            <Checkbox
              checked={allSelected ? true : someSelected ? 'indeterminate' : false}
              onCheckedChange={(checked) => toggleSelectAll(checked === true)}
              aria-label={t('review.selectAll')}
            />
            {t('review.selectAll')}
          </label>
        )}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="ml-auto h-7 w-7"
          onClick={refreshAll}
          aria-label={t('review.refresh')}
          title={t('review.refresh')}
        >
          <RefreshCw className={cn('h-3.5 w-3.5', isFetching && 'animate-spin')} />
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading && <ListSkeleton />}

        {!isLoading && isError && (
          <div className="flex flex-col items-center gap-3 px-4 py-12 text-center">
            <p className="text-sm text-warm-text-muted">{t('review.loadError')}</p>
            <Button type="button" variant="outline" size="sm" onClick={refreshAll}>
              {t('intake.retry')}
            </Button>
          </div>
        )}

        {!isLoading && !isError && decisions.length === 0 && (
          <EmptyState
            size="compact"
            icon={<Inbox className="h-10 w-10 text-muted-foreground" />}
            title={view === 'history' ? t('review.emptyHistoryTitle') : t('review.emptyTitle')}
            description={
              view === 'history'
                ? t('review.emptyHistoryDescription')
                : t('review.emptyDescription')
            }
            action={
              view === 'pending' && projectFilter !== null ? (
                <Button asChild variant="outline" size="sm">
                  <Link
                    to="/projects/$id"
                    params={{ id: String(projectFilter) }}
                    search={{ tab: 'settings', section: 'intake' }}
                  >
                    {t('review.goToSources')}
                  </Link>
                </Button>
              ) : undefined
            }
          />
        )}

        {!isLoading && !isError && decisions.length > 0 && (
          <div>
            {groups.map((group) => {
              const isCollapsed = collapsed.has(group.projectId)
              return (
                <section key={group.projectId}>
                  <button
                    type="button"
                    onClick={() => toggleGroup(group.projectId)}
                    aria-expanded={!isCollapsed}
                    className="sticky top-0 z-10 flex w-full items-center gap-1.5 border-b border-warm-border bg-warm-surface px-3 py-1.5 text-left hover:bg-accent"
                  >
                    {isCollapsed ? (
                      <ChevronRight className="h-3.5 w-3.5 text-warm-text-muted" aria-hidden />
                    ) : (
                      <ChevronDown className="h-3.5 w-3.5 text-warm-text-muted" aria-hidden />
                    )}
                    <span className="truncate text-xs font-semibold text-warm-text">
                      {group.name}
                    </span>
                    <span className="text-xs text-warm-text-muted tabular-nums">
                      {group.items.length}
                    </span>
                  </button>
                  {!isCollapsed && (
                    <DecisionList
                      decisions={group.items}
                      activeId={activeDecisionId}
                      selectedIds={selectedIds}
                      seenIds={seenIds}
                      onSelect={selectDecision}
                      onToggleSelect={toggleSelected}
                    />
                  )}
                </section>
              )
            })}
          </div>
        )}
      </div>

      {selectedIds.size > 0 && (
        <div className="flex items-center gap-2 border-t border-warm-border bg-warm-surface px-3 py-2">
          <span className="text-xs font-medium tabular-nums">
            {t('review.selectedCount', { count: selectedIds.size })}
          </span>
          {view === 'pending' && (
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button
                  type="button"
                  size="sm"
                  className="h-7 gap-1 px-2 text-xs"
                  disabled={batchAccept.isPending}
                >
                  <Zap className="h-3.5 w-3.5" aria-hidden />
                  {batchAccept.isPending ? t('review.batchAccepting') : t('review.batchAccept')}
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>
                    {t('review.batchAcceptTitle', { count: selectedIds.size })}
                  </AlertDialogTitle>
                  <AlertDialogDescription>{t('review.batchAcceptDescription')}</AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>{t('review.cancel')}</AlertDialogCancel>
                  <AlertDialogAction onClick={() => batchAccept.mutate()}>
                    {t('review.batchAccept')}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          )}
          {view === 'pending' && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-7 border-destructive/50 px-2 text-xs text-destructive-text hover:bg-destructive/10 hover:text-destructive-text"
              onClick={() => setBatchOpen(true)}
            >
              {t('review.batchIgnore')}
            </Button>
          )}
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 gap-1 px-2 text-xs text-destructive-text hover:bg-destructive/10 hover:text-destructive-text"
                disabled={batchDelete.isPending}
              >
                <Trash2 className="h-3.5 w-3.5" aria-hidden />
                {t('review.batchDelete')}
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>
                  {t('review.deleteSelectedTitle', { count: selectedIds.size })}
                </AlertDialogTitle>
                <AlertDialogDescription>{t('review.deleteDescription')}</AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{t('review.cancel')}</AlertDialogCancel>
                <AlertDialogAction
                  className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                  onClick={() => batchDelete.mutate()}
                >
                  {t('review.delete')}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
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
          <Select
            value={source}
            onValueChange={(value) => {
              setSource(value)
              setActiveId(null)
              resetSelection()
            }}
          >
            <SelectTrigger
              id="intake-source"
              className="h-8 w-36"
              aria-label={t('review.source')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_SOURCES}>{t('review.sourceAll')}</SelectItem>
              {SOURCE_KINDS.map((kind) => (
                <SelectItem key={kind} value={kind}>
                  {t(`source.${kind}`, { defaultValue: kind })}
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
                search={projectFilter !== null ? { project: projectFilter } : {}}
                className="text-xs text-brand hover:underline"
              >
                {t('review.clearChatFilter')}
              </Link>
            </div>
          )}
        </div>
      </div>

      {contextProjectId !== null && calibration ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-warm-border bg-warm-surface px-4 py-2">
          <span className="text-xs font-medium text-warm-text tabular-nums">
            {t('intake.calibration.summary', {
              count: calibration.calibrated,
              rate: Math.round(calibration.acceptance_rate * 100),
            })}
          </span>
          <span className="text-xs text-warm-text-muted tabular-nums">
            {t('intake.calibration.pendingCount', { count: calibration.pending })}
          </span>
          {ready && allCalibratedOnAuto ? (
            <span className="text-xs text-warm-text-muted">{t('intake.calibration.activeHint')}</span>
          ) : ready ? (
            <>
              <span className="text-xs text-warm-text-muted">{t('intake.calibration.readyHint')}</span>
              <Button
                type="button"
                size="sm"
                variant="secondary"
                className="ml-auto h-7 px-2 text-xs"
                onClick={() => setEnableOpen(true)}
              >
                <Zap className="mr-1 h-3.5 w-3.5" aria-hidden="true" />
                {t('intake.calibration.readyCta')}
              </Button>
            </>
          ) : (
            <span className="text-xs text-warm-text-muted tabular-nums">
              {calibration.calibrated < calibration.min_calibrated
                ? t('intake.calibration.needCount', {
                    count: calibration.min_calibrated - calibration.calibrated,
                  })
                : t('intake.calibration.needRate', {
                    rate: Math.round(calibration.min_acceptance_rate * 100),
                  })}
            </span>
          )}
        </div>
      ) : null}

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
          <SheetContent side="bottom" className="flex h-5/6 flex-col gap-0 p-0">
            <SheetHeader className="border-b border-warm-border px-4 py-3 text-left">
              <SheetTitle className="text-sm font-semibold">{t('review.title')}</SheetTitle>
              <SheetDescription className="sr-only">{t('review.description')}</SheetDescription>
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

      {calibration && contextProjectId !== null ? (
        <EnableAutoDialog
          open={enableOpen}
          onOpenChange={setEnableOpen}
          calibration={calibration}
          settings={settingsQuery.data}
          rules={rulesQuery.data ?? []}
          pending={enableAuto.isPending}
          onConfirm={(sourceKinds) => enableAuto.mutate(sourceKinds)}
        />
      ) : null}
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
