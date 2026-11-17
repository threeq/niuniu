import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { opsApi } from '@/lib/api'
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

interface OpsDeleteBotDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  botId: number
  botName: string
  onDeleted: () => void
}

export function OpsDeleteBotDialog({
  open,
  onOpenChange,
  botId,
  botName,
  onDeleted,
}: OpsDeleteBotDialogProps) {
  const { t } = useTranslation('ops')
  const [deleting, setDeleting] = useState(false)

  const handleDelete = async () => {
    setDeleting(true)
    try {
      const result = await opsApi.deleteBot(botId)
      if (result.cleanup_errors?.length) {
        toast.error(t('deleteBot.cleanupFailed', { message: result.cleanup_errors.join('; ') }))
      } else {
        toast.success(t('deleteBot.deleted', { name: botName }))
      }
      onOpenChange(false)
      onDeleted()
    } catch (err) {
      toast.error(
        t('toast.actionFailed', {
          message: err instanceof Error ? err.message : String(err),
        }),
      )
    } finally {
      setDeleting(false)
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t('deleteBot.title')}</AlertDialogTitle>
          <AlertDialogDescription>{t('deleteBot.description', { name: botName })}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={deleting}>{t('deleteBot.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => void handleDelete()}
            disabled={deleting}
            className="bg-destructive hover:bg-destructive/90"
          >
            {deleting ? t('deleteBot.deleting') : t('deleteBot.delete')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
