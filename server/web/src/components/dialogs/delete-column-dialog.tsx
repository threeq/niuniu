import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
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
import { api } from '@/lib/api'
import type { ColumnExtension } from '@/lib/project-template-api'

interface Props {
  open: boolean
  column: ColumnExtension | null
  projectId: number
  onOpenChange: (b: boolean) => void
}

export function DeleteColumnDialog({ open, column, projectId, onOpenChange }: Props) {
  const { t } = useTranslation('projects')
  const qc = useQueryClient()

  const mut = useMutation({
    mutationFn: () => api.delete<void>(`/columns/${column!.id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-columns', projectId] })
      qc.invalidateQueries({ queryKey: ['columns', projectId] })
      toast.success(t('tabs.settings.columns.deleteSuccess'))
      onOpenChange(false)
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : String(e)
      toast.error(t('tabs.settings.columns.deleteFailed', { message: msg }))
    },
  })

  if (!column) return null

  return (
    <AlertDialog
      open={open}
      onOpenChange={(o) => {
        if (mut.isPending) return
        onOpenChange(o)
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t('tabs.settings.columns.deleteConfirm.title', { name: column.name })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t('tabs.settings.columns.deleteConfirm.description')}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={mut.isPending}>
            {t('tabs.settings.columns.deleteConfirm.cancel')}
          </AlertDialogCancel>
          <AlertDialogAction
            className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            onClick={() => mut.mutate()}
            disabled={mut.isPending}
          >
            {mut.isPending
              ? t('common:actions.deleting')
              : t('tabs.settings.columns.deleteConfirm.confirm')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
