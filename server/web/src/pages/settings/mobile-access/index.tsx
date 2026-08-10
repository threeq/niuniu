import { useState, useEffect, useCallback } from 'react'
import { QrCode, Lock } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { PairDialog } from './pair-dialog'
import { TrustedList } from './trusted-list'
import { RelayAccountPanel } from './relay-account-panel'
import { BillingPanel } from './billing-panel'
import { relayAPI, type TrustedMobileInfo, type RelayStatus } from './relay-api'

export function MobileAccessSettings() {
  const { t } = useTranslation('settings')
  const [pairDialogOpen, setPairDialogOpen] = useState(false)
  const [mobiles, setMobiles] = useState<TrustedMobileInfo[]>([])
  const [loadingMobiles, setLoadingMobiles] = useState(false)
  const [pairingInProgress, setPairingInProgress] = useState(false)
  const [relayStatus, setRelayStatus] = useState<RelayStatus | null>(null)

  const refreshMobiles = useCallback(async () => {
    setLoadingMobiles(true)
    try {
      const list = await relayAPI.listTrustedMobiles()
      setMobiles(list ?? [])
    } catch {
      // non-critical — silently ignore
    } finally {
      setLoadingMobiles(false)
    }
  }, [])

  const loggedIn = !!relayStatus?.logged_in

  useEffect(() => {
    if (loggedIn) {
      refreshMobiles()
    } else {
      setMobiles([])
    }
  }, [loggedIn, refreshMobiles])

  const handleOpenPairDialog = () => {
    setPairingInProgress(true)
    setPairDialogOpen(true)
  }

  const handlePairDialogChange = (open: boolean) => {
    setPairDialogOpen(open)
    if (!open) {
      setPairingInProgress(false)
    }
  }

  const handlePaired = () => {
    setPairingInProgress(false)
    refreshMobiles()
  }

  return (
    <div className="py-6 space-y-8">
      {/* Section 0 — Relay account (login / register / logged-in card) */}
      <div className="space-y-3">
        <div>
          <h2 className="text-lg font-medium text-foreground">{t('mobileAccess.relayAccount.title')}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t('mobileAccess.relayAccount.description')}
          </p>
        </div>
        <RelayAccountPanel onStatusChange={setRelayStatus} />
      </div>

      {/* Section 0.5 — Billing / plan / usage (only when logged in).  Lives
          above the pair UI because hitting the mobile-cap or daily-pairing
          cap is the most common reason pairing fails with 402; the user needs
          to revoke a stale mobile or upgrade before they can pair again. */}
      {loggedIn && (
        <div className="space-y-3">
          <div>
            <h2 className="text-lg font-medium text-foreground">{t('mobileAccess.billing.title')}</h2>
            <p className="text-sm text-muted-foreground mt-1">
              {t('mobileAccess.billing.description')}
            </p>
          </div>
          <BillingPanel onAccountChanged={refreshMobiles} />
        </div>
      )}

      {/* Section 1 — Pair new device */}
      <div className="space-y-3">
        <div>
          <h2 className="text-lg font-medium text-foreground">{t('mobileAccess.pair.section.title')}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t('mobileAccess.pair.section.description')}
          </p>
        </div>

        {loggedIn ? (
          <Button
            onClick={handleOpenPairDialog}
            disabled={pairingInProgress}
            size="lg"
            className="gap-2"
          >
            <QrCode className="h-4 w-4" />
            {t('mobileAccess.pair.section.button')}
          </Button>
        ) : (
          <div className="flex items-start gap-2 rounded-lg border border-border bg-muted px-4 py-3">
            <Lock className="h-4 w-4 text-muted-foreground shrink-0 mt-0.5" />
            <p className="text-sm text-foreground">{t('mobileAccess.pair.section.loginRequired')}</p>
          </div>
        )}
      </div>

      {/* Section 2 — Trusted devices (only visible when logged in) */}
      {loggedIn && (
        <div className="space-y-3">
          <div>
            <h2 className="text-lg font-medium text-foreground">{t('mobileAccess.trusted.title')}</h2>
            <p className="text-sm text-muted-foreground mt-1">
              {t('mobileAccess.trusted.description')}
            </p>
          </div>

          {loadingMobiles ? (
            <p className="text-sm text-muted-foreground py-4 text-center">{t('mobileAccess.trusted.loading')}</p>
          ) : (
            <TrustedList mobiles={mobiles} onRemoved={refreshMobiles} />
          )}
        </div>
      )}

      <PairDialog
        open={pairDialogOpen}
        onOpenChange={handlePairDialogChange}
        onPaired={handlePaired}
      />
    </div>
  )
}
