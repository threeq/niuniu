import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { useLicenseStore } from '@/stores/license-store'

const REFETCH_INTERVAL_MS = 5 * 60 * 1000
// Where "联系授权 / Contact for license" sends the user.
const CONTACT_EMAIL = 'three3q@qq.com'

export function LicenseBanner() {
  const { t } = useTranslation('common')
  const navigate = useNavigate()
  const status = useLicenseStore((s) => s.status)

  useEffect(() => {
    void useLicenseStore.getState().fetch()
    const id = setInterval(() => {
      void useLicenseStore.getState().fetch()
    }, REFETCH_INTERVAL_MS)
    return () => clearInterval(id)
  }, [])

  if (!status || status.state === 'active') {
    return null
  }

  if (status.state === 'expiring') {
    return (
      <div className="w-full py-1.5 px-4 text-xs text-center border-b border-warning/30 bg-warning/10 text-warning">
        {t('license.banner.expiring', { days: status.days_remaining })}
      </div>
    )
  }

  let message: string
  if (status.state === 'clock_tampered') {
    message = t('license.banner.tampered')
  } else if (status.state === 'unlicensed') {
    message = t('license.banner.unlicensed')
  } else {
    message = t('license.banner.expired')
  }

  return (
    <div className="w-full py-1.5 px-4 text-xs text-center border-b border-destructive/30 bg-destructive/10 text-destructive">
      <span className="inline-flex flex-wrap items-center justify-center gap-x-3 gap-y-1">
        <span>{message}</span>
        <Button
          variant="link"
          className="h-auto p-0 text-xs font-medium text-destructive underline"
          onClick={() => navigate({ to: '/settings', search: { tab: 'license' } })}
        >
          {t('license.banner.uploadNow')}
        </Button>
        <Button
          asChild
          variant="link"
          className="h-auto p-0 text-xs font-medium text-destructive underline"
        >
          <a href={`mailto:${CONTACT_EMAIL}`}>{t('license.banner.contactAuth')}</a>
        </Button>
      </span>
    </div>
  )
}
