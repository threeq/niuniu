import { useTranslation } from 'react-i18next'
import { Circle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { isKBBusy, type KnowledgeBase } from '@/lib/kb-api'

type Tone = 'success' | 'warning' | 'destructive' | 'muted'

/**
 * Resolves a KB's ingest + enabled state into one display state.
 *
 * Deliberately collapses two orthogonal fields (`status`, `ingest_status`) into a
 * single badge: a disabled-but-ready KB and an enabled-but-failed KB are both
 * "not currently usable", and showing two competing badges made the row unreadable.
 * Precedence is failure > in-progress > disabled > ready.
 */
function resolveState(kb: KnowledgeBase): { key: string; tone: Tone } {
  if (kb.ingest_status === 'failed') return { key: 'failed', tone: 'destructive' }
  if (isKBBusy(kb)) return { key: kb.ingest_status, tone: 'warning' }
  if (kb.status === 'disabled') return { key: 'disabled', tone: 'muted' }
  return { key: 'ready', tone: 'success' }
}

const TONE_DOT: Record<Tone, string> = {
  success: 'text-success',
  warning: 'text-warning',
  destructive: 'text-destructive',
  muted: 'text-muted-foreground',
}

const TONE_BADGE: Record<Tone, string> = {
  success: 'bg-success/15 text-success border-success/30',
  warning: 'bg-warning/15 text-warning border-warning/30',
  destructive: 'bg-destructive/15 text-destructive border-destructive/30',
  muted: 'bg-muted text-muted-foreground border-border',
}

function labelFor(
  kb: KnowledgeBase,
  t: (k: string, o?: Record<string, unknown>) => string,
): string {
  const { key } = resolveState(kb)
  if (key === 'disabled') return t('row.disabledBadge')
  return t(`ingest.${key}`)
}

/**
 * Compact state indicator for dense rows (the sidebar). Carries an accessible
 * name so the state is not conveyed by color alone.
 */
export function KBStatusDot({ kb }: { kb: KnowledgeBase }) {
  const { t } = useTranslation('knowledge')
  const { tone } = resolveState(kb)
  const label = labelFor(kb, t)
  return (
    <Circle
      className={cn('w-2 h-2 shrink-0 fill-current', TONE_DOT[tone])}
      role="img"
      aria-label={label}
    />
  )
}

/** Full text badge for the detail header, where there is room for a label. */
export function KBStatusBadge({ kb }: { kb: KnowledgeBase }) {
  const { t } = useTranslation('knowledge')
  const { tone } = resolveState(kb)
  const label = labelFor(kb, t)
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-xs',
        TONE_BADGE[tone],
      )}
    >
      <Circle className="w-1.5 h-1.5 fill-current" aria-hidden />
      {label}
    </span>
  )
}

/** Token-only progress bar. Width is an inline style (a computed percentage
 *  cannot be a Tailwind class), everything else goes through tokens. */
export function KBProgressBar({ value }: { value: number }) {
  const pct = Math.max(0, Math.min(100, value))
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-warm-muted">
      <div
        className="h-full rounded-full bg-info transition-[width] duration-500"
        style={{ width: `${pct}%` }}
      />
    </div>
  )
}
