import { useTranslation } from 'react-i18next'
import { Bot, Github, Inbox } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { cn } from '@/lib/utils'
import type { IntakeDecision } from '@/lib/intake-api'
import { useRelativeTime } from './use-relative-time'

export function SourceIcon({ source }: { source: string }) {
  if (source === 'github') return <Github className="h-4 w-4" aria-hidden />
  if (source === 'im_bot') return <Bot className="h-4 w-4" aria-hidden />
  return <Inbox className="h-4 w-4" aria-hidden />
}

export function StrengthBadge({ strength }: { strength: string }) {
  const { t } = useTranslation('intake')
  const label =
    strength === 'explicit' || strength === 'inferred' || strength === 'denied'
      ? t(`review.strength.${strength}`)
      : strength
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center rounded-md px-1.5 py-0.5 text-xs font-medium',
        strength === 'explicit'
          ? 'bg-brand-soft text-brand'
          : strength === 'denied'
            ? 'bg-destructive/10 text-destructive-text'
            : 'bg-warm-muted text-warm-text-muted',
      )}
    >
      {label}
    </span>
  )
}

/** Actor label for the list: a display name when the platform provided one,
 *  otherwise a shortened external id. The raw open_id is never shown in full —
 *  it is machine noise in a scanning list. */
function actorLabel(decision: IntakeDecision, unknown: string): string {
  const name = (decision.actor_name ?? '').trim()
  if (name) return name
  const ext = (decision.actor_ext_id ?? '').trim()
  if (!ext) return unknown
  return ext.length > 12 ? `${ext.slice(0, 10)}…` : ext
}

/** Queue state badge: pending / accepted / ignored. The accept-vs-resume
 *  distinction is intentionally not shown — the reviewer picks the target when
 *  accepting, and the internal classification is noise in a list. */
export function StatusBadge({ decision }: { decision: IntakeDecision }) {
  const { t } = useTranslation('intake')
  const status =
    decision.status ??
    (decision.decision === 'ignore'
      ? 'ignored'
      : decision.consumed
        ? 'consumed'
        : decision.executed
          ? 'accepted'
          : 'pending')
  const tone =
    status === 'accepted'
      ? 'bg-brand-soft text-brand'
      : status === 'ignored' || status === 'consumed'
        ? 'bg-warm-muted text-warm-text-muted'
        : 'bg-warm-muted text-warm-text'
  return (
    <span className={cn('inline-flex shrink-0 items-center rounded-md px-1.5 py-0.5 text-xs font-medium', tone)}>
      {t(`review.status.${status}`)}
    </span>
  )
}

interface DecisionListProps {
  decisions: IntakeDecision[]
  activeId: number | null
  selectedIds: Set<number>
  seenIds: Set<number>
  onSelect: (decision: IntakeDecision) => void
  onToggleSelect: (id: number, checked: boolean) => void
}

export function DecisionList({
  decisions,
  activeId,
  selectedIds,
  seenIds,
  onSelect,
  onToggleSelect,
}: DecisionListProps) {
  const { t } = useTranslation('intake')
  const relative = useRelativeTime()

  return (
    <ul className="divide-y divide-warm-border">
      {decisions.map((decision) => {
        const brief = decision.would_do?.brief
        const summary =
          (brief?.Goal ?? '').trim() ||
          (decision.payload?.Text ?? '').trim() ||
          t('review.noSummary')
        const thread = decision.thread_ext_id || decision.payload?.ThreadExtID || ''
        return (
          <li key={decision.id} className="relative flex items-start gap-2 px-3 py-3">
            {!seenIds.has(decision.id) && (
              <>
                <span className="absolute inset-y-2 left-0 w-0.5 rounded-full bg-brand" aria-hidden="true" />
                <span className="sr-only">{t('review.newDecision')}</span>
              </>
            )}
            <Checkbox
              checked={selectedIds.has(decision.id)}
              onCheckedChange={(checked) => onToggleSelect(decision.id, checked === true)}
              className="mt-1 shrink-0"
              aria-label={t('review.selectDecision', { id: decision.id })}
            />
            <Button
              type="button"
              variant="ghost"
              onClick={() => onSelect(decision)}
              className={cn(
                'h-auto min-w-0 flex-1 flex-col items-start justify-start gap-0 whitespace-normal rounded-md p-0 text-left font-normal hover:bg-accent',
                activeId === decision.id && 'bg-brand-soft/60 hover:bg-brand-soft/60',
              )}
            >
              {/* The message is what the reviewer scans; everything else is
                  secondary metadata on one quiet line below it. */}
              <p className="line-clamp-2 text-sm font-medium break-words text-warm-text">
                {summary}
              </p>
              <div className="mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-warm-text-muted">
                <span className="tabular-nums">{relative(decision.created_at)}</span>
                <StrengthBadge strength={decision.strength} />
                <StatusBadge decision={decision} />
                <span className="inline-flex min-w-0 items-center gap-1">
                  <SourceIcon source={decision.source_kind} />
                  <span className="truncate">{actorLabel(decision, t('review.unknownActor'))}</span>
                </span>
                {thread && (
                  <>
                    <span aria-hidden>·</span>
                    <span className="truncate">{thread}</span>
                  </>
                )}
              </div>
            </Button>
          </li>
        )
      })}
    </ul>
  )
}
