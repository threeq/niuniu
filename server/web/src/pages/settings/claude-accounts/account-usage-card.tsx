import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useEffect, useRef, useState } from 'react'
import { RefreshCw, Info, AlertTriangle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { getClaudeAccountUsage } from '@/lib/api'
import {
  type AccountUsage,
  type UsageWindow,
  type ClaudeUsageError,
  type KnownClaudeUsageErrorCode,
  isKnownClaudeUsageErrorCode,
} from '@/types/claude-usage'

const WINDOW_LABEL_KEY: Record<UsageWindow['type'], string> = {
  five_hour_billing: 'windowFiveHourBilling',
  seven_day_all: 'windowSevenDayAll',
  seven_day_sonnet: 'windowSevenDaySonnet',
  seven_day_opus: 'windowSevenDayOpus',
}

function formatNumber(n: number): string {
  return n.toLocaleString()
}

function formatUSD(amount: number): string {
  return amount.toFixed(2)
}

function formatBlockTime(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return d.toLocaleString(undefined, { hour: '2-digit', minute: '2-digit' })
}

// Visual progress bar — purely "share of total" within the window context.
// Not a quota %.
function relativeWidth(value: number, max: number): number {
  if (max <= 0) return 0
  return Math.min(100, (value / max) * 100)
}

function WindowCard({
  window,
  parentTotal,
  windows,
}: {
  window: UsageWindow
  parentTotal: number
  windows: UsageWindow[]
}) {
  const { t } = useTranslation('claude-accounts')

  // Width logic per spec §5.2:
  //   five_hour_billing → relative to (seven_day_all/33.6) baseline
  //   seven_day_all → always full bar
  //   seven_day_sonnet/opus → relative to seven_day_all
  let widthPct = 0
  if (window.type === 'seven_day_all') {
    widthPct = window.total_tokens > 0 ? 100 : 0
  } else if (window.type === 'five_hour_billing') {
    const sevenDayAll = windows.find((w) => w.type === 'seven_day_all')
    const baseline = sevenDayAll ? sevenDayAll.total_tokens / 33.6 : 0
    widthPct = relativeWidth(window.total_tokens, baseline)
  } else {
    widthPct = relativeWidth(window.total_tokens, parentTotal)
  }

  const isFiveHour = window.type === 'five_hour_billing'

  return (
    <div className="border rounded-lg p-4 space-y-3">
      <div className="flex items-baseline justify-between">
        <h3 className="font-medium">{t(`usage.${WINDOW_LABEL_KEY[window.type]}`)}</h3>
        {isFiveHour && (
          <span className="text-xs text-muted-foreground">
            {window.resets_at ? (
              // Anthropic-authoritative reset time, captured from agent stream.
              t('usage.resetsAt', {
                time: new Date(window.resets_at).toLocaleString(undefined, {
                  hour: '2-digit',
                  minute: '2-digit',
                  month: 'short',
                  day: '2-digit',
                }),
              })
            ) : window.block_start && window.block_end ? (
              <>
                {window.is_active
                  ? t('usage.blockActive')
                  : t('usage.blockInactive')}
                {' · '}
                {t('usage.blockTimeRange', {
                  start: formatBlockTime(window.block_start),
                  end: formatBlockTime(window.block_end),
                })}
              </>
            ) : null}
          </span>
        )}
      </div>

      <div className="flex items-baseline justify-between">
        <span className="font-mono text-2xl tabular-nums">
          {formatNumber(window.total_tokens)} {t('usage.tokensSuffix')}
        </span>
        <span className="font-mono text-sm text-muted-foreground">
          ${formatUSD(window.cost_usd)}
        </span>
      </div>

      <div className="h-2 rounded-full bg-muted overflow-hidden">
        <div
          className="h-full bg-info transition-all"
          style={{ width: `${widthPct}%` }}
        />
      </div>

      <div className="text-xs text-muted-foreground">
        {t('usage.tokensInputOutput', {
          input: formatNumber(window.input_tokens),
          output: formatNumber(window.output_tokens),
          cacheRead: formatNumber(window.cache_read_tokens),
          cacheCreation: formatNumber(window.cache_creation_tokens),
        })}
      </div>

      {window.models.length > 0 && (
        <details className="text-xs">
          <summary className="cursor-pointer text-muted-foreground">
            {t('usage.perModelHeader')} ({window.models.length})
          </summary>
          <ul className="mt-2 space-y-1 font-mono">
            {window.models.map((m) => (
              <li key={m.name} className="flex justify-between">
                <span className="text-foreground">{m.name}</span>
                <span className="text-muted-foreground">
                  {formatNumber(m.total_tokens)} · ${formatUSD(m.cost_usd)}
                </span>
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}

export function AccountUsageCard({ accountId }: { accountId: number }) {
  const { t } = useTranslation('claude-accounts')
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(id)
  }, [])

  // forceRef carries the user's force-refresh intent into queryFn. Cleared in
  // .finally() — NOT inside queryFn — so an interval refetch racing with a
  // user click can't eat the flag. refetchOnWindowFocus is also disabled to
  // prevent the same race from focus-triggered refetches.
  const forceRef = useRef(false)
  const query = useQuery<AccountUsage, ClaudeUsageError>({
    queryKey: ['claude-account-usage', accountId],
    queryFn: () => getClaudeAccountUsage(accountId, forceRef.current),
    enabled: accountId > 0,
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
  })
  const onForceRefresh = () => {
    forceRef.current = true
    query.refetch().finally(() => {
      forceRef.current = false
    })
  }

  if (query.isPending) return <SkeletonView />
  if (query.isError) return <ErrorView error={query.error} />

  const data = query.data
  const sevenDayAll = data.windows.find((w) => w.type === 'seven_day_all')
  const parentTotal = sevenDayAll?.total_tokens ?? 0

  const fetchedSecAgo = Math.max(
    0,
    Math.floor((now.getTime() - new Date(data.fetched_at).getTime()) / 1000),
  )
  const nextRefreshSec = Math.max(
    0,
    Math.floor((new Date(data.next_refresh_after).getTime() - now.getTime()) / 1000),
  )
  const refreshDisabled = nextRefreshSec > 0

  const tier = [data.subscription_type, data.rate_limit_tier].filter(Boolean).join(' / ')

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-2 border border-info/30 bg-info/10 rounded-lg p-3 text-sm">
        <Info className="h-4 w-4 text-info mt-0.5 shrink-0" />
        <div>
          <p>
            {t('usage.dataSourceBanner', {
              paths: data.data_source_paths.join(', '),
            })}
          </p>
          {tier && (
            <p className="text-xs text-muted-foreground mt-1">
              {t('usage.subscriptionLine', { tier })}
            </p>
          )}
        </div>
      </div>

      <div className="space-y-3">
        {data.windows.map((w) => (
          <WindowCard
            key={w.type}
            window={w}
            parentTotal={parentTotal}
            windows={data.windows}
          />
        ))}
      </div>

      <div className="flex items-center justify-between border-t pt-3 text-xs text-muted-foreground">
        <span>{t('usage.lastUpdated', { seconds: fetchedSecAgo })}</span>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={refreshDisabled}
            onClick={onForceRefresh}
          >
            <RefreshCw className="h-3 w-3 mr-1" />
            {t('usage.refreshButton')}
          </Button>
          {refreshDisabled && (
            <span className="font-mono">
              {t('usage.nextRefreshAfter', { seconds: nextRefreshSec })}
            </span>
          )}
        </div>
      </div>
    </div>
  )
}

function SkeletonView() {
  return (
    <div className="space-y-3">
      {[0, 1, 2, 3].map((i) => (
        <div key={i} className="border rounded-lg p-4 animate-pulse">
          <div className="h-4 w-32 bg-muted rounded mb-3" />
          <div className="h-8 w-48 bg-muted rounded mb-3" />
          <div className="h-2 w-full bg-muted rounded" />
        </div>
      ))}
    </div>
  )
}

// assertNever throws at runtime if reached; at the type level it forces
// callers to exhaust every case of a discriminated union — the compiler
// errors if a new union member is added without a matching case.
function assertNever(_x: never): never {
  throw new Error('unreachable error code branch')
}

function UnknownErrorView({ error }: { error: ClaudeUsageError }) {
  const { t } = useTranslation('claude-accounts')
  return (
    <div className="border border-destructive/30 bg-destructive/10 rounded-lg p-6 text-sm space-y-1">
      <p className="font-medium">{t('usage.unknownError.title')}</p>
      <p className="text-muted-foreground">
        {error.hint_zh || t('usage.unknownError.body', { detail: error.message })}
      </p>
    </div>
  )
}

function ErrorView({ error }: { error: ClaudeUsageError }) {
  const { t } = useTranslation('claude-accounts')
  // Forward-compat: any code the BE adds without a FE update lands here.
  if (!isKnownClaudeUsageErrorCode(error.code)) {
    return <UnknownErrorView error={error} />
  }
  // From this point `error.code` is the strict KnownClaudeUsageErrorCode
  // union. The switch's default calls assertNever — adding a new code to
  // KnownClaudeUsageErrorCode without a matching case here is a TS error.
  const code: KnownClaudeUsageErrorCode = error.code
  switch (code) {
    case 'projects_not_found':
      return (
        <div className="border border-warning/30 bg-warning/10 rounded-lg p-6 text-center space-y-2">
          <Info className="h-8 w-8 text-info mx-auto" />
          <h3 className="font-medium">{t('usage.empty.notLoggedInTitle')}</h3>
          <p className="text-sm text-muted-foreground">{t('usage.empty.notLoggedInBody')}</p>
        </div>
      )
    case 'jsonl_walk_failed':
      return (
        <div className="border border-destructive/30 bg-destructive/10 rounded-lg p-6 space-y-2">
          <h3 className="font-medium">{t('usage.walkFailed.title')}</h3>
          <p className="text-sm text-muted-foreground">
            {t('usage.walkFailed.body', { detail: error.message })}
          </p>
        </div>
      )
    case 'claude_data_dir_not_found':
      return (
        <div className="border border-warning/30 bg-warning/10 rounded-lg p-6 text-center space-y-2">
          <AlertTriangle className="h-8 w-8 text-warning mx-auto" />
          <h3 className="font-medium">{t('usage.noDataDir.title')}</h3>
          <p className="text-sm text-muted-foreground">{t('usage.noDataDir.body')}</p>
        </div>
      )
    case 'bad_account_id':
    case 'not_found':
    case 'authz_error':
      // These are all "this should never reach the user under normal use"
      // — show the generic envelope rather than authoring per-code copy.
      return <UnknownErrorView error={error} />
    default:
      return assertNever(code)
  }
}
