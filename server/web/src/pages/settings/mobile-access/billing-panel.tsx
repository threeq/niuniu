import { useCallback, useEffect, useState } from 'react'
import { Check, Copy, CreditCard, Monitor, RefreshCw, Smartphone, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
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
import { ChangePlanDialog } from './change-plan-dialog'
import {
  relayAPI,
  type BillingPlan,
  type BillingUsage,
  type RemoteDesktopDevice,
  type RemoteMobileDevice,
} from './relay-api'

// Usage meter: compact progress bar + "used / limit" label. Turns amber at
// 80% and red at 100% so the user notices quota pressure without having to
// parse the numbers.
function UsageBar({
  label,
  used,
  limit,
  unit = '',
}: {
  label: string
  used: number
  limit: number
  unit?: string
}) {
  const pct = limit <= 0 ? 0 : Math.min(100, Math.round((used / limit) * 100))
  const color =
    pct >= 100 ? 'bg-destructive' : pct >= 80 ? 'bg-warning' : 'bg-info'
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between text-xs">
        <span className="text-foreground">{label}</span>
        <span className="text-muted-foreground font-mono">
          {used}
          {unit} / {limit}
          {unit}
        </span>
      </div>
      <div className="h-1.5 rounded-full bg-muted overflow-hidden">
        <div
          className={`h-full ${color} transition-all`}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  )
}

// DeviceIdLine — render a device id full-width on its own row, monospace,
// with a one-tap copy-to-clipboard button. Used by both the mobile and
// desktop sections of the device list so the user can always read the
// full UUID (the previous "platform · id" single-line layout truncated
// the right half of the id with ellipsis on narrow panels) and paste it
// into a support request without manual selection.
function DeviceIdLine({ id }: { id: string }) {
  const { t } = useTranslation('settings')
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(id)
      setCopied(true)
      // 1.5s feedback window — long enough to register the change without
      // needing a stateful checkmark queue if the user mashes the button.
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard permission denied / non-secure context — silently
      // ignore; the id is still visible for manual selection.
    }
  }
  return (
    <div className="flex items-center gap-1.5 mt-1">
      <code className="text-xs text-muted-foreground font-mono break-all select-all flex-1 leading-tight">
        {id}
      </code>
      <button
        type="button"
        onClick={copy}
        title={copied ? t('mobileAccess.billing.copyId.copied') : t('mobileAccess.billing.copyId.copy')}
        className="shrink-0 p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
      >
        {copied ? <Check className="h-3 w-3 text-success" /> : <Copy className="h-3 w-3" />}
      </button>
    </div>
  )
}

function formatPrice(t: (k: string) => string, cents: number, interval: string): string {
  if (cents === 0) return t('mobileAccess.billing.free')
  const dollars = (cents / 100).toFixed(cents % 100 === 0 ? 0 : 2)
  const intervalLabel =
    interval === 'monthly' ? t('mobileAccess.billing.intervalMonthly')
    : interval === 'yearly' ? t('mobileAccess.billing.intervalYearly')
    : interval
  return `$${dollars} / ${intervalLabel}`
}

interface BillingPanelProps {
  /**
   * Called after a revoke so the parent can refresh the trusted-mobiles
   * allowlist (which is separate but typically cleaned up together).
   */
  onAccountChanged?: () => void
}

export function BillingPanel({ onAccountChanged }: BillingPanelProps) {
  const { t } = useTranslation('settings')
  const [usage, setUsage] = useState<BillingUsage | null>(null)
  const [plan, setPlan] = useState<BillingPlan | null>(null)
  const [remoteMobiles, setRemoteMobiles] = useState<RemoteMobileDevice[]>([])
  const [remoteDesktops, setRemoteDesktops] = useState<RemoteDesktopDevice[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [changePlanOpen, setChangePlanOpen] = useState(false)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [u, p, m, d] = await Promise.all([
        relayAPI.getBillingUsage(),
        relayAPI.getBillingPlan(),
        relayAPI.listRemoteMobileDevices(),
        relayAPI.listRemoteDesktopDevices(),
      ])
      setUsage(u)
      setPlan(p)
      setRemoteMobiles(m ?? [])
      setRemoteDesktops(d ?? [])
    } catch (err) {
      setError((err as Error).message || t('mobileAccess.billing.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    refresh()
  }, [refresh])

  const handleRevoke = async (id: string) => {
    try {
      await relayAPI.revokeRemoteMobileDevice(id)
      await refresh()
      onAccountChanged?.()
    } catch (err) {
      setError((err as Error).message || t('mobileAccess.billing.remoteDevices.revokeFailed'))
    }
  }

  const handleRevokeDesktop = async (id: string) => {
    try {
      await relayAPI.revokeRemoteDesktopDevice(id)
      await refresh()
      onAccountChanged?.()
    } catch (err) {
      setError((err as Error).message || t('mobileAccess.billing.remoteDesktops.revokeFailed'))
    }
  }

  const handlePlanChanged = async () => {
    // A free-plan swap applies immediately on the server; paid plans require
    // Stripe webhook + our poll. Either way, re-fetch so the UI reflects
    // whatever the server now says is current.
    await refresh()
  }

  if (loading && !usage) {
    return <p className="text-sm text-muted-foreground py-4 text-center">{t('mobileAccess.billing.loading')}</p>
  }

  if (error && !usage) {
    return (
      <div className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-3">
        <p className="text-sm text-destructive flex-1">{error}</p>
        <Button size="sm" variant="outline" onClick={refresh}>
          {t('common:actions.retry')}
        </Button>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {/* Current plan card + refresh + change-plan trigger */}
      <div className="rounded-lg border border-border bg-card p-4 space-y-4">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <CreditCard className="h-4 w-4 text-muted-foreground" />
              <p className="text-sm font-medium text-foreground">
                {t('mobileAccess.billing.currentPlan')}&nbsp;{plan?.name ?? usage?.plan_id ?? '-'}
              </p>
            </div>
            {plan && (
              <p className="text-xs text-muted-foreground mt-1">
                {plan.description} · {formatPrice(t, plan.price_cents_usd, plan.billing_interval)}
              </p>
            )}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <Button
              size="sm"
              variant="outline"
              onClick={refresh}
              disabled={loading}
              title={t('mobileAccess.billing.refresh')}
            >
              <RefreshCw
                className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`}
              />
            </Button>
            <Button size="sm" onClick={() => setChangePlanOpen(true)}>
              {t('mobileAccess.billing.upgradePlan')}
            </Button>
          </div>
        </div>

        {/* Usage bars: each one the frontend learns matters when a 402 hits. */}
        {usage && (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 pt-2 border-t border-border">
            <UsageBar
              label={t('mobileAccess.billing.usage.mobiles')}
              used={usage.mobiles_used}
              limit={usage.mobiles_limit}
            />
            <UsageBar
              label={t('mobileAccess.billing.usage.pairings')}
              used={usage.pairings_today}
              limit={usage.pairings_daily_limit}
            />
            <UsageBar
              label={t('mobileAccess.billing.usage.desktops')}
              used={usage.desktops_used}
              limit={usage.desktops_limit}
            />
            <UsageBar
              label={t('mobileAccess.billing.usage.bandwidth')}
              used={usage.bandwidth_mb_used}
              limit={usage.bandwidth_mb_limit}
              unit=" MB"
            />
          </div>
        )}
      </div>

      {/* Remote mobile devices — distinct from the local trusted-mobiles list. */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <p className="text-sm font-medium text-foreground">{t('mobileAccess.billing.remoteDevices.title')}</p>
          <p className="text-xs text-muted-foreground">
            {remoteMobiles.length} / {usage?.mobiles_limit ?? '-'}
          </p>
        </div>
        <p className="text-xs text-muted-foreground">
          {t('mobileAccess.billing.remoteDevices.description')}
        </p>
        {remoteMobiles.length === 0 ? (
          <p className="text-sm text-muted-foreground py-4 text-center border border-dashed border-border rounded-lg">
            {t('mobileAccess.billing.remoteDevices.empty')}
          </p>
        ) : (
          <div className="space-y-2">
            {remoteMobiles.map((m) => (
              <div
                key={m.id}
                className="flex items-center justify-between py-3 px-4 border border-border rounded-lg bg-card"
              >
                <div className="flex items-start gap-3 min-w-0 flex-1">
                  <Smartphone className="h-5 w-5 text-muted-foreground shrink-0 mt-0.5" />
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-medium text-foreground truncate">
                      {m.name || m.id}
                    </p>
                    <p className="text-xs text-muted-foreground mt-0.5">
                      {m.platform || '-'}
                    </p>
                    <DeviceIdLine id={m.id} />
                  </div>
                </div>
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button
                      size="sm"
                      variant="outline"
                      className="shrink-0 ml-3 text-destructive border-destructive/30 hover:bg-destructive/10 hover:text-destructive"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>{t('mobileAccess.billing.remoteDevices.revokeTitle')}</AlertDialogTitle>
                      <AlertDialogDescription>
                        {t('mobileAccess.billing.remoteDevices.revokeDescription', { name: m.name || m.id })}
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>{t('common:actions.cancel')}</AlertDialogCancel>
                      <AlertDialogAction
                        onClick={() => handleRevoke(m.id)}
                        className="bg-destructive hover:bg-destructive/90 text-white"
                      >
                        {t('mobileAccess.billing.remoteDevices.revoke')}
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Remote desktops — same shape as mobiles, exposes the per-account
          desktops table on the relay. The user's *current* desktop process
          shows up here too (when it has been registered with the relay);
          it's safe to leave that one alone. The list is mostly useful for
          spotting stale desktops left over from earlier installs / dev
          builds — a mobile that's still pointing at a revoked desktop_id
          surfaces as "fetch POST .../d/<id>/rpc failed" with an id that
          doesn't appear in any of these lists. */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <p className="text-sm font-medium text-foreground">{t('mobileAccess.billing.remoteDesktops.title')}</p>
          <p className="text-xs text-muted-foreground">
            {remoteDesktops.length}
          </p>
        </div>
        <p className="text-xs text-muted-foreground">
          {t('mobileAccess.billing.remoteDesktops.description')}
        </p>
        {remoteDesktops.length === 0 ? (
          <p className="text-sm text-muted-foreground py-4 text-center border border-dashed border-border rounded-lg">
            {t('mobileAccess.billing.remoteDesktops.empty')}
          </p>
        ) : (
          <div className="space-y-2">
            {remoteDesktops.map((d) => (
              <div
                key={d.id}
                className="flex items-center justify-between py-3 px-4 border border-border rounded-lg bg-card"
              >
                <div className="flex items-start gap-3 min-w-0 flex-1">
                  <Monitor className="h-5 w-5 text-muted-foreground shrink-0 mt-0.5" />
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-medium text-foreground truncate">
                      {d.name || d.id}
                    </p>
                    <p className="text-xs text-muted-foreground mt-0.5">
                      {[d.os, d.arch].filter(Boolean).join('/') || '-'}
                    </p>
                    <DeviceIdLine id={d.id} />
                  </div>
                </div>
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button
                      size="sm"
                      variant="outline"
                      className="shrink-0 ml-3 text-destructive border-destructive/30 hover:bg-destructive/10 hover:text-destructive"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>{t('mobileAccess.billing.remoteDesktops.revokeTitle')}</AlertDialogTitle>
                      <AlertDialogDescription>
                        {t('mobileAccess.billing.remoteDesktops.revokeDescription', { name: d.name || d.id })}
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>{t('common:actions.cancel')}</AlertDialogCancel>
                      <AlertDialogAction
                        onClick={() => handleRevokeDesktop(d.id)}
                        className="bg-destructive hover:bg-destructive/90 text-white"
                      >
                        {t('mobileAccess.billing.remoteDesktops.revoke')}
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </div>
            ))}
          </div>
        )}
      </div>

      {error && usage && (
        <p className="text-xs text-destructive">{error}</p>
      )}

      <ChangePlanDialog
        open={changePlanOpen}
        onOpenChange={setChangePlanOpen}
        currentPlanID={plan?.plan_id ?? usage?.plan_id}
        onPlanChanged={handlePlanChanged}
      />
    </div>
  )
}
