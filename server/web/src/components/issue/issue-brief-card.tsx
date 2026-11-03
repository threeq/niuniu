import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowUpRight,
  ChevronDown,
  ChevronRight,
  History,
  Info,
  Pencil,
  Plus,
  RotateCcw,
  Save,
  Trash2,
  X,
} from 'lucide-react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { confirm } from '@/lib/confirm'
import { cn } from '@/lib/utils'
import { useIssueChecklists } from '@/lib/hooks/use-issue-checklists'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { SourceIcon, StrengthBadge } from '@/components/intake/decision-list'
import type {
  BriefProvenanceEvent,
  IssueBriefResponse,
  IssueChecklist,
  TaskBriefDelivery,
  TaskBriefInput,
  TaskBriefVersion,
} from '@/types/api'

interface IssueBriefCardProps {
  issueId: number
  readOnly?: boolean
}

type BriefDraft = {
  goal: string
  acceptance: string[]
  scopeIn: string
  scopeOut: string
  delivery: TaskBriefDelivery
}

function toDraft(v: TaskBriefVersion): BriefDraft {
  return {
    goal: v.goal,
    acceptance: v.acceptance.length > 0 ? [...v.acceptance] : [''],
    scopeIn: v.scope_in.join('\n'),
    scopeOut: v.scope_out.join('\n'),
    delivery: { ...v.delivery },
  }
}

function splitLines(value: string): string[] {
  return value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
}

function toInput(draft: BriefDraft): TaskBriefInput {
  return {
    goal: draft.goal.trim(),
    acceptance: draft.acceptance.map((item) => item.trim()).filter(Boolean),
    scope_in: splitLines(draft.scopeIn),
    scope_out: splitLines(draft.scopeOut),
    delivery: draft.delivery,
  }
}

function formatTime(value: string): string {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString()
}

/**
 * IssueBriefCard — the Task Brief surface on the Issue detail panel
 * (design §6.4 / §6.7.6).
 *
 * The card is hidden entirely for issues without a Brief (not created through
 * Intake). Acceptance rows are the Brief's materialized `kind='acceptance'`
 * checklist rows, so checking one updates the same row the completion gate
 * reads. Manual `kind='task'` rows stay in IssueChecklist untouched.
 */
export function IssueBriefCard({ issueId, readOnly }: IssueBriefCardProps) {
  const { t } = useTranslation('issues')
  const queryClient = useQueryClient()

  const { data, isLoading } = useQuery<IssueBriefResponse>({
    queryKey: ['issue-brief', issueId],
    queryFn: () => api.getIssueBrief(issueId),
    staleTime: 15_000,
  })
  const { checklists, updateChecklist } = useIssueChecklists(issueId)

  const [open, setOpen] = useState(true)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<BriefDraft | null>(null)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [compareVersion, setCompareVersion] = useState<number | null>(null)

  const brief = data?.brief ?? null
  const versions = data?.versions ?? []

  const acceptanceRows = useMemo(
    () => checklists.filter((item) => item.kind === 'acceptance'),
    [checklists],
  )

  const saveMutation = useMutation({
    mutationFn: (input: TaskBriefInput) => api.putIssueBrief(issueId, input),
    onSuccess: (view) => {
      queryClient.setQueryData(['issue-brief', issueId], view)
      queryClient.invalidateQueries({ queryKey: ['issue-checklists', issueId] })
      queryClient.invalidateQueries({ queryKey: ['issue', String(issueId)] })
      queryClient.invalidateQueries({ queryKey: ['issues'] })
      queryClient.invalidateQueries({ queryKey: ['all-issues'] })
      setEditing(false)
      setDraft(null)
      toast.success(t('brief.saved', { version: view.brief?.version ?? 0 }))
    },
    onError: () => toast.error(t('brief.saveFailed')),
  })

  const rollbackMutation = useMutation({
    mutationFn: (version: TaskBriefVersion) =>
      api.putIssueBrief(issueId, {
        goal: version.goal,
        acceptance: version.acceptance,
        scope_in: version.scope_in,
        scope_out: version.scope_out,
        delivery: version.delivery,
      }),
    onSuccess: (view) => {
      queryClient.setQueryData(['issue-brief', issueId], view)
      queryClient.invalidateQueries({ queryKey: ['issue-checklists', issueId] })
      queryClient.invalidateQueries({ queryKey: ['issue', String(issueId)] })
      setCompareVersion(null)
      toast.success(t('brief.rollbackDone', { version: view.brief?.version ?? 0 }))
    },
    onError: () => toast.error(t('brief.rollbackFailed')),
  })

  if (isLoading) return null
  if (!brief) return null

  const toggleAcceptance = (index: number, text: string) => {
    if (readOnly) return
    const row =
      acceptanceRows[index] ?? acceptanceRows.find((item) => item.title === text)
    if (!row) return
    updateChecklist({
      id: Number(row.id),
      title: row.title,
      is_completed: row.is_completed === 1 ? 0 : 1,
    })
  }

  const startEdit = () => {
    setDraft(toDraft(brief))
    setEditing(true)
    setOpen(true)
  }

  const cancelEdit = () => {
    setEditing(false)
    setDraft(null)
  }

  const submitEdit = () => {
    if (!draft) return
    if (!draft.goal.trim()) {
      toast.error(t('brief.goalRequired'))
      return
    }
    saveMutation.mutate(toInput(draft))
  }

  const handleRollback = async (version: TaskBriefVersion) => {
    const ok = await confirm({
      title: t('brief.rollbackConfirmTitle', { version: version.version }),
      description: t('brief.rollbackConfirmDesc'),
      confirmText: t('brief.rollbackConfirm'),
    })
    if (ok) rollbackMutation.mutate(version)
  }

  const sourceKind =
    data?.provenance_event?.source_kind || brief.delivery.source_kind || 'webhook'

  return (
    <div className="border-b border-border px-5 py-4">
      <div className="rounded-lg border border-warm-border bg-warm-surface">
        <div className="flex flex-wrap items-center gap-2 px-3 py-2">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 gap-1.5 px-2 text-xs"
            aria-expanded={open}
            onClick={() => setOpen((prev) => !prev)}
          >
            {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
            <span className="font-medium">{t('brief.title')}</span>
            <span className="text-warm-text-muted">v{brief.version}</span>
          </Button>

          <div className="ml-auto flex items-center gap-1.5">
            <ProvenancePopover
              brief={brief}
              event={data?.provenance_event ?? null}
              sourceKind={sourceKind}
            />
            {!readOnly && !editing && (
              <Button
                type="button"
                variant="secondary"
                size="sm"
                className="h-7 gap-1.5 px-2 text-xs"
                onClick={startEdit}
              >
                <Pencil className="h-3.5 w-3.5" />
                {t('brief.edit')}
              </Button>
            )}
          </div>
        </div>

        {open && (
          <div className="flex flex-col gap-4 border-t border-warm-border px-3 py-3">
            {editing && draft ? (
              <BriefEditor
                draft={draft}
                onChange={setDraft}
                onCancel={cancelEdit}
                onSave={submitEdit}
                saving={saveMutation.isPending}
              />
            ) : (
              <BriefView
                brief={brief}
                acceptanceRows={acceptanceRows}
                onToggleAcceptance={toggleAcceptance}
                readOnly={readOnly}
              />
            )}

            <div className="flex flex-col gap-2 border-t border-warm-border pt-2">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 w-fit gap-1.5 px-2 text-xs text-warm-text-muted"
                aria-expanded={historyOpen}
                onClick={() => setHistoryOpen((prev) => !prev)}
              >
                <History className="h-3.5 w-3.5" />
                {t('brief.history')}
                <span className="tabular-nums">{versions.length}</span>
              </Button>
              {historyOpen && (
                <VersionHistory
                  versions={versions}
                  currentVersion={brief.version}
                  compareVersion={compareVersion}
                  onToggleCompare={(v) => setCompareVersion((prev) => (prev === v ? null : v))}
                  onRollback={handleRollback}
                  rollingBack={rollbackMutation.isPending}
                  readOnly={readOnly}
                />
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function BriefView({
  brief,
  acceptanceRows,
  onToggleAcceptance,
  readOnly,
}: {
  brief: TaskBriefVersion
  acceptanceRows: IssueChecklist[]
  onToggleAcceptance: (index: number, text: string) => void
  readOnly?: boolean
}) {
  const { t } = useTranslation('issues')

  return (
    <div className="flex flex-col gap-3">
      <section className="flex flex-col gap-1">
        <span className="text-xs font-medium text-warm-text-muted">{t('brief.goal')}</span>
        <p className="whitespace-pre-wrap break-words text-sm">{brief.goal}</p>
      </section>

      <section className="flex flex-col gap-1">
        <span className="text-xs font-medium text-warm-text-muted">{t('brief.acceptance')}</span>
        {brief.acceptance.length === 0 ? (
          <p className="text-sm text-warm-text-muted">{t('brief.acceptanceEmpty')}</p>
        ) : (
          <ul className="flex flex-col gap-1">
            {brief.acceptance.map((item, index) => {
              const row =
                acceptanceRows[index] ??
                acceptanceRows.find((candidate) => candidate.title === item)
              const checked = row?.is_completed === 1
              return (
                <li key={`${index}-${item}`} className="flex items-start gap-2">
                  <Checkbox
                    checked={checked}
                    disabled={readOnly || !row}
                    onCheckedChange={() => onToggleAcceptance(index, item)}
                    className="mt-0.5"
                    aria-label={item}
                  />
                  <span
                    className={cn(
                      'text-sm break-words',
                      checked && 'text-warm-text-muted line-through',
                    )}
                  >
                    {item}
                  </span>
                </li>
              )
            })}
          </ul>
        )}
      </section>

      <div className="grid gap-3 sm:grid-cols-2">
        <BriefList label={t('brief.scopeIn')} items={brief.scope_in} empty={t('brief.scopeEmpty')} />
        <BriefList label={t('brief.scopeOut')} items={brief.scope_out} empty={t('brief.scopeEmpty')} />
      </div>

      <section className="flex flex-col gap-1">
        <span className="text-xs font-medium text-warm-text-muted">{t('brief.delivery')}</span>
        <DeliveryText delivery={brief.delivery} />
      </section>
    </div>
  )
}

function BriefList({ label, items, empty }: { label: string; items: string[]; empty: string }) {
  return (
    <section className="flex flex-col gap-1">
      <span className="text-xs font-medium text-warm-text-muted">{label}</span>
      {items.length === 0 ? (
        <span className="text-sm text-warm-text-muted">{empty}</span>
      ) : (
        <div className="flex flex-wrap gap-1">
          {items.map((item, index) => (
            <span
              key={`${index}-${item}`}
              className="rounded-md bg-warm-muted px-1.5 py-0.5 text-xs break-words"
            >
              {item}
            </span>
          ))}
        </div>
      )}
    </section>
  )
}

function DeliveryText({ delivery }: { delivery: TaskBriefDelivery }) {
  const { t } = useTranslation(['issues', 'intake'])
  if (!delivery.source_kind && !delivery.thread_ext_id && !delivery.reply_mode) {
    return <span className="text-sm text-warm-text-muted">{t('brief.deliveryNone')}</span>
  }
  const mode =
    delivery.reply_mode === 'thread' || delivery.reply_mode === 'chat' || delivery.reply_mode === 'pr_comment'
      ? t(`brief.replyMode.${delivery.reply_mode}`)
      : delivery.reply_mode
  return (
    <span className="flex flex-wrap items-center gap-1.5 text-sm">
      <span>
        {t(`intake.source.${delivery.source_kind}`, {
          ns: 'intake',
          defaultValue: delivery.source_kind || '—',
        })}
      </span>
      {delivery.thread_ext_id && (
        <span className="text-warm-text-muted">· {delivery.thread_ext_id}</span>
      )}
      {mode && <span className="text-warm-text-muted">· {mode}</span>}
    </span>
  )
}

function BriefEditor({
  draft,
  onChange,
  onCancel,
  onSave,
  saving,
}: {
  draft: BriefDraft
  onChange: (next: BriefDraft) => void
  onCancel: () => void
  onSave: () => void
  saving: boolean
}) {
  const { t } = useTranslation('issues')

  const setAcceptance = (index: number, value: string) => {
    const next = [...draft.acceptance]
    next[index] = value
    onChange({ ...draft, acceptance: next })
  }

  const removeAcceptance = (index: number) => {
    const next = draft.acceptance.filter((_, i) => i !== index)
    onChange({ ...draft, acceptance: next.length > 0 ? next : [''] })
  }

  return (
    <div className="flex flex-col gap-3">
      <section className="flex flex-col gap-1.5">
        <label className="text-xs font-medium text-warm-text-muted" htmlFor="brief-goal-edit">
          {t('brief.goal')}
        </label>
        <Textarea
          id="brief-goal-edit"
          value={draft.goal}
          onChange={(e) => onChange({ ...draft, goal: e.target.value })}
          rows={2}
          className="min-h-0 resize-none"
        />
      </section>

      <section className="flex flex-col gap-1.5">
        <span className="text-xs font-medium text-warm-text-muted">{t('brief.acceptance')}</span>
        <div className="flex flex-col gap-1.5">
          {draft.acceptance.map((item, index) => (
            <div key={index} className="flex items-center gap-1.5">
              <Input
                value={item}
                onChange={(e) => setAcceptance(index, e.target.value)}
                placeholder={t('brief.acceptancePlaceholder')}
                className="h-8"
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="h-8 w-8 shrink-0 text-warm-text-muted"
                onClick={() => removeAcceptance(index)}
                aria-label={t('brief.removeAcceptance')}
                title={t('brief.removeAcceptance')}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            </div>
          ))}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 w-fit gap-1 px-2 text-xs"
            onClick={() => onChange({ ...draft, acceptance: [...draft.acceptance, ''] })}
          >
            <Plus className="h-3.5 w-3.5" />
            {t('brief.addAcceptance')}
          </Button>
        </div>
      </section>

      <div className="grid gap-3 sm:grid-cols-2">
        <section className="flex flex-col gap-1.5">
          <label className="text-xs font-medium text-warm-text-muted" htmlFor="brief-scope-in">
            {t('brief.scopeIn')}
          </label>
          <Textarea
            id="brief-scope-in"
            value={draft.scopeIn}
            onChange={(e) => onChange({ ...draft, scopeIn: e.target.value })}
            placeholder={t('brief.scopePlaceholder')}
            rows={3}
            className="min-h-0 resize-none font-mono text-xs"
          />
        </section>
        <section className="flex flex-col gap-1.5">
          <label className="text-xs font-medium text-warm-text-muted" htmlFor="brief-scope-out">
            {t('brief.scopeOut')}
          </label>
          <Textarea
            id="brief-scope-out"
            value={draft.scopeOut}
            onChange={(e) => onChange({ ...draft, scopeOut: e.target.value })}
            placeholder={t('brief.scopePlaceholder')}
            rows={3}
            className="min-h-0 resize-none font-mono text-xs"
          />
        </section>
      </div>

      <section className="grid gap-3 sm:grid-cols-3">
        <div className="flex flex-col gap-1.5">
          <label className="text-xs font-medium text-warm-text-muted" htmlFor="brief-delivery-source">
            {t('brief.deliverySource')}
          </label>
          <Input
            id="brief-delivery-source"
            value={draft.delivery.source_kind}
            onChange={(e) =>
              onChange({ ...draft, delivery: { ...draft.delivery, source_kind: e.target.value } })
            }
            className="h-8"
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <label className="text-xs font-medium text-warm-text-muted" htmlFor="brief-delivery-thread">
            {t('brief.deliveryThread')}
          </label>
          <Input
            id="brief-delivery-thread"
            value={draft.delivery.thread_ext_id}
            onChange={(e) =>
              onChange({ ...draft, delivery: { ...draft.delivery, thread_ext_id: e.target.value } })
            }
            className="h-8"
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <label className="text-xs font-medium text-warm-text-muted" htmlFor="brief-delivery-mode">
            {t('brief.deliveryMode')}
          </label>
          <Input
            id="brief-delivery-mode"
            value={draft.delivery.reply_mode}
            onChange={(e) =>
              onChange({ ...draft, delivery: { ...draft.delivery, reply_mode: e.target.value } })
            }
            placeholder={t('brief.deliveryModePlaceholder')}
            className="h-8"
          />
        </div>
      </section>

      <div className="flex items-center justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={saving}>
          <X className="h-3.5 w-3.5" />
          {t('brief.cancel')}
        </Button>
        <Button type="button" size="sm" onClick={onSave} disabled={saving}>
          <Save className="h-3.5 w-3.5" />
          {saving ? t('brief.saving') : t('brief.save')}
        </Button>
      </div>
    </div>
  )
}

function ProvenancePopover({
  brief,
  event,
  sourceKind,
}: {
  brief: TaskBriefVersion
  event: BriefProvenanceEvent | null
  sourceKind: string
}) {
  const { t } = useTranslation(['issues', 'intake'])
  const provenance = brief.provenance
  const actor = event?.actor_name || event?.actor_ext_id || provenance.actor_ext_id
  const separator = t('brief.listSeparator')
  const ruleLabel =
    event && event.rule_names.length > 0
      ? event.rule_names.join(separator)
      : provenance.rule_ids.length > 0
        ? provenance.rule_ids.map((id) => `#${id}`).join(separator)
        : t('brief.provenance.noRules')

  const rawText =
    event?.text ||
    (event && typeof event.payload?.Text === 'string' ? (event.payload.Text as string) : '')
  const attachments =
    event && Array.isArray(event.payload?.Attachments)
      ? (event.payload.Attachments as { Name?: string }[])
      : []
  const eventLabel = event
    ? t(`githubEvent.${event.event_kind}`, {
        ns: 'intake',
        defaultValue: event.event_kind,
      })
    : ''
  const threadLabel = event?.thread_ext_id ? `#${event.thread_ext_id}` : ''
  // The API already filters to http(s); keep the guard here too, since this
  // value becomes an href.
  const safeURL = event?.url && /^https?:\/\//i.test(event.url) ? event.url : ''

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-7 gap-1.5 px-2 text-xs text-warm-text-muted"
          aria-label={t('brief.provenance.title')}
        >
          <SourceIcon source={sourceKind} />
          <span>{t(`intake.source.${sourceKind}`, { ns: 'intake', defaultValue: sourceKind })}</span>
          <Info className="h-3 w-3" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 max-h-96 overflow-y-auto">
        <div className="flex flex-col gap-2">
          <h4 className="text-sm font-medium">{t('brief.provenance.title')}</h4>
          <dl className="flex flex-col gap-1.5 text-xs">
            <ProvenanceRow
              label={t('brief.provenance.source')}
              value={t(`intake.source.${sourceKind}`, { ns: 'intake', defaultValue: sourceKind })}
            />
            {event?.source_ref && (
              <ProvenanceRow
                label={t('brief.provenance.repository')}
                value={event.source_ref}
              />
            )}
            <ProvenanceRow
              label={t('brief.provenance.thread')}
              value={threadLabel || '—'}
            />
            {event && (
              <>
                <ProvenanceRow
                  label={t('brief.provenance.event')}
                  value={eventLabel || '—'}
                />
                <ProvenanceRow
                  label={t('brief.provenance.eventId')}
                  value={event.event_ext_id || '—'}
                />
              </>
            )}
            <ProvenanceRow label={t('brief.provenance.actor')} value={actor || '—'} />
            <div className="flex items-start gap-2">
              <dt className="w-20 shrink-0 text-warm-text-muted">{t('brief.provenance.strength')}</dt>
              <dd>{provenance.strength ? <StrengthBadge strength={provenance.strength} /> : '—'}</dd>
            </div>
            <ProvenanceRow label={t('brief.provenance.rules')} value={ruleLabel} />
            <ProvenanceRow
              label={t('brief.provenance.policyVersion')}
              value={String(provenance.policy_version)}
            />
            <ProvenanceRow
              label={t('brief.provenance.decisionId')}
              value={provenance.decision_id > 0 ? `#${provenance.decision_id}` : '—'}
            />
          </dl>

          <div className="flex flex-col gap-1 border-t border-warm-border pt-2">
            <span className="text-xs font-medium text-warm-text-muted">
              {t('brief.provenance.originalEvent')}
            </span>
            {rawText || attachments.length > 0 ? (
              <div className="flex max-h-40 flex-col gap-1.5 overflow-y-auto rounded-md bg-warm-muted px-2 py-1.5">
                {rawText && (
                  <p className="whitespace-pre-wrap break-words text-xs">{rawText}</p>
                )}
                {attachments.length > 0 && (
                  <div className="flex flex-wrap gap-1">
                    {attachments.map((attachment, index) => (
                      <span
                        key={index}
                        className="rounded-md border border-warm-border px-1.5 py-0.5 text-xs text-warm-text-muted"
                      >
                        {attachment.Name || t('brief.provenance.attachment')}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            ) : (
              <span className="text-xs text-warm-text-muted">{t('brief.provenance.noEvent')}</span>
            )}
            {event && (
              <span className="flex flex-wrap items-center gap-2 text-xs text-warm-text-muted">
                <span>{t('brief.provenance.receivedAt', { time: formatTime(event.received_at) })}</span>
                {safeURL && (
                  <a
                    href={safeURL}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-0.5 text-brand hover:underline"
                  >
                    {t('brief.provenance.openSource')}
                    <ArrowUpRight className="h-3 w-3" aria-hidden />
                  </a>
                )}
              </span>
            )}
          </div>
        </div>
      </PopoverContent>
    </Popover>
  )
}

function ProvenanceRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-start gap-2">
      <dt className="w-20 shrink-0 text-warm-text-muted">{label}</dt>
      <dd className="break-words">{value}</dd>
    </div>
  )
}

type DiffEntry = { field: string; value: string; kind: 'added' | 'removed' }
type DiffChange = { field: string; from: string; to: string }

function diffVersions(
  prev: TaskBriefVersion | null,
  next: TaskBriefVersion,
  labels: { goal: string; acceptance: string; scopeIn: string; scopeOut: string; delivery: string },
): { changes: DiffChange[]; entries: DiffEntry[] } {
  const changes: DiffChange[] = []
  const entries: DiffEntry[] = []
  if (prev && prev.goal !== next.goal) {
    changes.push({ field: labels.goal, from: prev.goal, to: next.goal })
  }
  const diffList = (field: string, before: string[], after: string[]) => {
    const prevSet = new Set(before)
    const nextSet = new Set(after)
    for (const item of after) if (!prevSet.has(item)) entries.push({ field, value: item, kind: 'added' })
    for (const item of before) if (!nextSet.has(item)) entries.push({ field, value: item, kind: 'removed' })
  }
  diffList(labels.acceptance, prev?.acceptance ?? [], next.acceptance)
  diffList(labels.scopeIn, prev?.scope_in ?? [], next.scope_in)
  diffList(labels.scopeOut, prev?.scope_out ?? [], next.scope_out)
  if (prev) {
    const deliveryChanged =
      prev.delivery.source_kind !== next.delivery.source_kind ||
      prev.delivery.thread_ext_id !== next.delivery.thread_ext_id ||
      prev.delivery.reply_mode !== next.delivery.reply_mode
    if (deliveryChanged) {
      changes.push({
        field: labels.delivery,
        from: `${prev.delivery.source_kind} · ${prev.delivery.thread_ext_id} · ${prev.delivery.reply_mode}`,
        to: `${next.delivery.source_kind} · ${next.delivery.thread_ext_id} · ${next.delivery.reply_mode}`,
      })
    }
  }
  return { changes, entries }
}

function VersionHistory({
  versions,
  currentVersion,
  compareVersion,
  onToggleCompare,
  onRollback,
  rollingBack,
  readOnly,
}: {
  versions: TaskBriefVersion[]
  currentVersion: number
  compareVersion: number | null
  onToggleCompare: (version: number) => void
  onRollback: (version: TaskBriefVersion) => void
  rollingBack: boolean
  readOnly?: boolean
}) {
  const { t } = useTranslation('issues')
  const labels = {
    goal: t('brief.goal'),
    acceptance: t('brief.acceptance'),
    scopeIn: t('brief.scopeIn'),
    scopeOut: t('brief.scopeOut'),
    delivery: t('brief.delivery'),
  }

  return (
    <ol className="flex flex-col gap-1.5">
      {versions.map((version) => {
        const previous = versions.find((candidate) => candidate.version === version.version - 1) ?? null
        const isCurrent = version.version === currentVersion
        const comparing = compareVersion === version.version
        const diff = diffVersions(previous, version, labels)
        const operator = version.created_by
          ? t('brief.actorUser', { id: version.created_by })
          : version.provenance.actor_ext_id || t('brief.actorSystem')
        return (
          <li key={version.id} className="rounded-md border border-warm-border px-2 py-1.5">
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <span className="font-medium">v{version.version}</span>
              {isCurrent && (
                <span className="rounded-md bg-brand-soft px-1.5 py-0.5 text-xs font-medium text-brand">
                  {t('brief.current')}
                </span>
              )}
              <span className="text-warm-text-muted">{operator}</span>
              <span className="tabular-nums text-warm-text-muted">{formatTime(version.created_at)}</span>
              <div className="ml-auto flex items-center gap-1">
                {previous && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-6 px-1.5 text-xs"
                    aria-expanded={comparing}
                    onClick={() => onToggleCompare(version.version)}
                  >
                    {comparing ? t('brief.hideCompare') : t('brief.compare')}
                  </Button>
                )}
                {!readOnly && !isCurrent && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-6 gap-1 px-1.5 text-xs"
                    disabled={rollingBack}
                    onClick={() => onRollback(version)}
                  >
                    <RotateCcw className="h-3 w-3" />
                    {t('brief.rollback')}
                  </Button>
                )}
              </div>
            </div>

            {comparing && (
              <div className="mt-1.5 flex flex-col gap-1 border-t border-warm-border pt-1.5 text-xs">
                {diff.changes.length === 0 && diff.entries.length === 0 && (
                  <span className="text-warm-text-muted">{t('brief.noChanges')}</span>
                )}
                {diff.changes.map((change, index) => (
                  <div key={`change-${index}`} className="flex flex-col gap-0.5">
                    <span className="text-warm-text-muted">{change.field}</span>
                    <span className="line-through text-warm-text-muted break-words">{change.from || '—'}</span>
                    <span className="break-words">{change.to || '—'}</span>
                  </div>
                ))}
                {diff.entries.map((entry, index) => (
                  <div key={`entry-${index}`} className="flex items-start gap-1.5">
                    <span
                      className={cn(
                        'shrink-0 rounded px-1 text-xs font-medium',
                        entry.kind === 'added'
                          ? 'bg-success/10 text-success'
                          : 'bg-destructive/10 text-destructive-text',
                      )}
                    >
                      {entry.kind === 'added' ? t('brief.added') : t('brief.removed')}
                    </span>
                    <span className="text-warm-text-muted">{entry.field}</span>
                    <span className="break-words">{entry.value}</span>
                  </div>
                ))}
              </div>
            )}
          </li>
        )
      })}
    </ol>
  )
}
