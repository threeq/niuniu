import { useCallback, useEffect, useState } from 'react'
import { Check, ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { apiFetch } from '@/lib/api'
import { relayAPI, type BillingPlan } from './relay-api'

interface ChangePlanDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  currentPlanID?: string
  /**
   * Called after a successful change (either applied free-plan swap, or after
   * the Stripe checkout URL has been opened) so the parent can refresh usage.
   * The actual paid-plan upgrade is async via Stripe webhook — parent should
   * poll to pick up the change.
   */
  onPlanChanged?: () => void
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

// openExternal prefers niuniu-server's /api/shell/open-external (launches the
// host's default browser via `rundll32 url.dll,FileProtocolHandler` on Windows
// / `open` on macOS / `xdg-open` on Linux) because the embedded webview's
// window.open often opens a new Wails window instead of the system browser,
// and Stripe's checkout doesn't round-trip well inside an embedded webview.
// Falls back to window.open if the endpoint is unavailable (dev server hitting
// an older build, or a remote/hosted niuniu-server that shouldn't launch
// browsers on the remote machine).
async function openExternal(url: string) {
  try {
    await apiFetch('/shell/open-external', {
      method: 'POST',
      body: JSON.stringify({ url }),
      suppressError: true,
    })
    return
  } catch {
    // fall through
  }
  window.open(url, '_blank', 'noopener,noreferrer')
}

export function ChangePlanDialog({
  open,
  onOpenChange,
  currentPlanID,
  onPlanChanged,
}: ChangePlanDialogProps) {
  const { t } = useTranslation('settings')
  const [plans, setPlans] = useState<BillingPlan[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const list = await relayAPI.listBillingPlans()
      // Sort free → cheapest paid → most expensive so the list reads like a
      // ladder the user climbs up, not a random blob.
      list.sort((a, b) => a.price_cents_usd - b.price_cents_usd)
      setPlans(list)
    } catch (err) {
      setError((err as Error).message || t('mobileAccess.changePlan.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    if (open) {
      refresh()
      setNotice(null)
    }
  }, [open, refresh])

  const handleSelect = async (p: BillingPlan) => {
    if (p.plan_id === currentPlanID) return
    setSubmitting(p.plan_id)
    setError(null)
    setNotice(null)
    try {
      const res = await relayAPI.changeBillingPlan(p.plan_id)
      if (res.checkout_url) {
        // Paid plan: Stripe takes over. Open in system browser and explain
        // that the upgrade won't reflect until the Stripe webhook fires.
        await openExternal(res.checkout_url)
        setNotice(t('mobileAccess.changePlan.noticeStripe'))
      } else if (res.applied) {
        setNotice(t('mobileAccess.changePlan.noticeApplied', { name: p.name }))
      } else {
        setNotice(t('mobileAccess.changePlan.noticeSubmitted'))
      }
      onPlanChanged?.()
    } catch (err) {
      setError((err as Error).message || t('mobileAccess.changePlan.switchFailed'))
    } finally {
      setSubmitting(null)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t('mobileAccess.changePlan.title')}</DialogTitle>
          <DialogDescription>
            {t('mobileAccess.changePlan.description')}
          </DialogDescription>
        </DialogHeader>

        {loading ? (
          <p className="text-sm text-muted-foreground py-6 text-center">{t('mobileAccess.changePlan.loading')}</p>
        ) : error ? (
          <div className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-3">
            <p className="text-sm text-destructive flex-1">{error}</p>
            <Button size="sm" variant="outline" onClick={refresh}>
              {t('common:actions.retry')}
            </Button>
          </div>
        ) : (
          <div className="space-y-2">
            {plans.map((p) => {
              const isCurrent = p.plan_id === currentPlanID
              const isSubmitting = submitting === p.plan_id
              return (
                <div
                  key={p.plan_id}
                  className={`rounded-lg border p-4 transition-colors ${
                    isCurrent
                      ? 'border-info/40 bg-info/10'
                      : 'border-border bg-card hover:border-foreground/30'
                  }`}
                >
                  <div className="flex items-start justify-between gap-4">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <p className="text-sm font-medium text-foreground">
                          {p.name}
                        </p>
                        {isCurrent && (
                          <span className="inline-flex items-center gap-1 text-xs text-info font-medium">
                            <Check className="h-3 w-3" />
                            {t('mobileAccess.changePlan.current')}
                          </span>
                        )}
                      </div>
                      <p className="text-xs text-muted-foreground mt-1">
                        {p.description}
                      </p>
                      <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground mt-2">
                        <span>{t('mobileAccess.changePlan.stats.desktops', { n: p.max_desktops })}</span>
                        <span>·</span>
                        <span>{t('mobileAccess.changePlan.stats.mobiles', { n: p.max_mobiles })}</span>
                        <span>·</span>
                        <span>{t('mobileAccess.changePlan.stats.pairings', { n: p.max_pairings_per_day })}</span>
                        <span>·</span>
                        <span>{t('mobileAccess.changePlan.stats.bandwidth', { n: p.max_bandwidth_mb_month })}</span>
                      </div>
                    </div>
                    <div className="flex flex-col items-end gap-2 shrink-0">
                      <p className="text-sm font-medium text-foreground whitespace-nowrap">
                        {formatPrice(t, p.price_cents_usd, p.billing_interval)}
                      </p>
                      <Button
                        size="sm"
                        variant={isCurrent ? 'outline' : 'default'}
                        disabled={isCurrent || isSubmitting}
                        onClick={() => handleSelect(p)}
                        className="min-w-[80px]"
                      >
                        {isSubmitting ? t('mobileAccess.changePlan.processing') : isCurrent ? t('mobileAccess.changePlan.inUse') : p.price_cents_usd > 0 ? (
                          <span className="inline-flex items-center gap-1">
                            {t('mobileAccess.changePlan.checkout')} <ExternalLink className="h-3 w-3" />
                          </span>
                        ) : (
                          t('mobileAccess.changePlan.switch')
                        )}
                      </Button>
                    </div>
                  </div>
                </div>
              )
            })}
          </div>
        )}

        {notice && (
          <p className="text-xs text-muted-foreground bg-muted border border-border rounded px-3 py-2">
            {notice}
          </p>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('mobileAccess.changePlan.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
