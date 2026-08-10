import { Trash2, Smartphone } from 'lucide-react'
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
import { relayAPI, type TrustedMobileInfo } from './relay-api'

interface TrustedListProps {
  mobiles: TrustedMobileInfo[]
  onRemoved: () => void
}

function formatRelativeTime(t: (k: string, p?: Record<string, unknown>) => string, iso: string): string {
  if (!iso || iso.startsWith('0001')) return t('mobileAccess.trusted.never')
  try {
    const diff = Date.now() - new Date(iso).getTime()
    const minutes = Math.floor(diff / 60_000)
    if (minutes < 1) return t('common:time.now')
    if (minutes < 60) return t('mobileAccess.trusted.minutesAgo', { n: minutes })
    const hours = Math.floor(minutes / 60)
    if (hours < 24) return t('mobileAccess.trusted.hoursAgo', { n: hours })
    const days = Math.floor(hours / 24)
    if (days < 30) return t('mobileAccess.trusted.daysAgo', { n: days })
    const months = Math.floor(days / 30)
    if (months < 12) return t('mobileAccess.trusted.monthsAgo', { n: months })
    return t('mobileAccess.trusted.yearsAgo', { n: Math.floor(months / 12) })
  } catch {
    return iso
  }
}

function MobileRow({
  mobile,
  onRemoved,
}: {
  mobile: TrustedMobileInfo
  onRemoved: () => void
}) {
  const { t } = useTranslation('settings')
  const handleRemove = async () => {
    await relayAPI.removeTrustedMobile(mobile.xpub_hex)
    onRemoved()
  }

  return (
    <div className="flex items-center justify-between py-3 px-4 border border-border rounded-lg bg-card">
      <div className="flex items-center gap-3 min-w-0">
        <Smartphone className="h-5 w-5 text-info shrink-0" />
        <div className="min-w-0">
          <p className="text-sm font-medium text-foreground truncate">{mobile.name}</p>
          <div className="flex items-center gap-2 text-xs text-muted-foreground mt-0.5">
            <span>{mobile.platform}</span>
            <span>·</span>
            <span>{t('mobileAccess.trusted.pairedAt', { when: formatRelativeTime(t, mobile.paired_at) })}</span>
            {mobile.last_seen_at && !mobile.last_seen_at.startsWith('0001') && (
              <>
                <span>·</span>
                <span>{t('mobileAccess.trusted.lastSeen', { when: formatRelativeTime(t, mobile.last_seen_at) })}</span>
              </>
            )}
          </div>
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
            <AlertDialogTitle>{t('mobileAccess.trusted.removeTitle')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('mobileAccess.trusted.removeDescription', { name: mobile.name })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common:actions.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleRemove}
              className="bg-destructive hover:bg-destructive/90 text-white"
            >
              {t('mobileAccess.trusted.remove')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

export function TrustedList({ mobiles, onRemoved }: TrustedListProps) {
  const { t } = useTranslation('settings')
  if (mobiles.length === 0) {
    return (
      <p className="text-sm text-muted-foreground py-6 text-center">
        {t('mobileAccess.trusted.empty')}
      </p>
    )
  }

  return (
    <div className="space-y-2">
      {mobiles.map((m) => (
        <MobileRow key={m.xpub_hex} mobile={m} onRemoved={onRemoved} />
      ))}
    </div>
  )
}
