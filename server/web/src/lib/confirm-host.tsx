import { useSyncExternalStore } from 'react'
import { useTranslation } from 'react-i18next'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { getConfirmSnapshot, settleConfirm, subscribeConfirm } from '@/lib/confirm'

/** Mounted once at the app root; renders whichever confirm() request is active. */
export function ConfirmHost() {
  const { t } = useTranslation()
  const req = useSyncExternalStore(subscribeConfirm, getConfirmSnapshot)
  const open = req !== null

  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        // Closing via Esc / overlay counts as cancel.
        if (!next) settleConfirm(false)
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{req?.title ?? t('actions.confirm')}</AlertDialogTitle>
          <AlertDialogDescription className="whitespace-pre-line">
            {req?.description}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={() => settleConfirm(false)}>
            {req?.cancelText ?? t('actions.cancel')}
          </AlertDialogCancel>
          <AlertDialogAction
            className={
              req?.destructive
                ? 'bg-destructive text-destructive-foreground hover:bg-destructive/90'
                : undefined
            }
            onClick={() => settleConfirm(true)}
          >
            {req?.confirmText ?? t('actions.ok')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
