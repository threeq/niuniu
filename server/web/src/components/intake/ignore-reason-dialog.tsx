import { useState } from 'react'
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
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  IGNORE_REASON_CODES,
  type IgnoreReason,
  type IgnoreReasonCode,
} from './ignore-reason'

interface IgnoreReasonDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  count: number
  pending: boolean
  onConfirm: (reason: IgnoreReason) => void
}

export function IgnoreReasonDialog({
  open,
  onOpenChange,
  count,
  pending,
  onConfirm,
}: IgnoreReasonDialogProps) {
  const { t } = useTranslation('intake')
  const title =
    count > 1 ? t('review.ignoreBatchTitle', { count }) : t('review.ignoreTitle')

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            {count > 1 ? t('review.ignoreBatchDescription') : t('review.ignoreDescription')}
          </DialogDescription>
        </DialogHeader>
        {/* Mounted only while the dialog is open, so the form state resets on
            every open without an effect. */}
        <IgnoreReasonForm pending={pending} onConfirm={onConfirm} onCancel={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

function IgnoreReasonForm({
  pending,
  onConfirm,
  onCancel,
}: {
  pending: boolean
  onConfirm: (reason: IgnoreReason) => void
  onCancel: () => void
}) {
  const { t } = useTranslation('intake')
  const [code, setCode] = useState<IgnoreReasonCode | ''>('')
  const [text, setText] = useState('')
  const [error, setError] = useState('')

  function submit() {
    if (!code) {
      setError(t('review.ignoreReasonRequired'))
      return
    }
    if (code === 'other' && text.trim() === '') {
      setError(t('review.ignoreReasonTextRequired'))
      return
    }
    onConfirm({ code, text: text.trim() })
  }

  return (
    <>
      <div className="flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <label className="text-xs font-medium text-warm-text-muted" htmlFor="ignore-reason">
            {t('review.ignoreReason')}
          </label>
          <Select
            value={code}
            onValueChange={(value) => {
              setCode(value as IgnoreReasonCode)
              setError('')
            }}
          >
            <SelectTrigger id="ignore-reason" aria-label={t('review.ignoreReason')}>
              <SelectValue placeholder={t('review.ignoreReasonPlaceholder')} />
            </SelectTrigger>
            <SelectContent>
              {IGNORE_REASON_CODES.map((value) => (
                <SelectItem key={value} value={value}>
                  {t(`review.reason.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {code === 'other' && (
          <Input
            value={text}
            onChange={(e) => {
              setText(e.target.value)
              setError('')
            }}
            placeholder={t('review.ignoreReasonOtherPlaceholder')}
            aria-label={t('review.ignoreReasonOther')}
          />
        )}

        {error && <p className="text-xs text-destructive-text">{error}</p>}
      </div>

      <DialogFooter>
        <Button variant="ghost" onClick={onCancel} disabled={pending}>
          {t('review.cancel')}
        </Button>
        <Button variant="destructive" onClick={submit} disabled={pending}>
          {pending ? t('review.saving') : t('review.confirmIgnore')}
        </Button>
      </DialogFooter>
    </>
  )
}
