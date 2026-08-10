import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { api } from '@/lib/api'
import type { Column, CreateColumnData } from '@/types/api'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  projectId: number
}

const NAME_MAX = 80

export function CreateColumnDialog({ open, onOpenChange, projectId }: Props) {
  const { t } = useTranslation('projects')
  const qc = useQueryClient()
  const inputRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState('')
  const [attemptedSubmit, setAttemptedSubmit] = useState(false)

  useEffect(() => {
    if (open) {
      setName('')
      setAttemptedSubmit(false)
    }
  }, [open])

  const nameInvalid = !name.trim()
  const showNameError = attemptedSubmit && nameInvalid

  const createMut = useMutation({
    mutationFn: (data: CreateColumnData) =>
      api.post<Column>(`/projects/${projectId}/columns`, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['project-columns', projectId] })
      qc.invalidateQueries({ queryKey: ['columns', projectId] })
      toast.success(t('tabs.settings.columns.createSuccess'))
      onOpenChange(false)
    },
    onError: (e: unknown) => {
      const msg = e instanceof Error ? e.message : String(e)
      toast.error(t('tabs.settings.columns.createFailed', { message: msg }))
    },
  })

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    setAttemptedSubmit(true)
    if (nameInvalid) {
      inputRef.current?.focus()
      return
    }
    createMut.mutate({ name: name.trim() })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[440px]">
        <DialogHeader>
          <DialogTitle>{t('tabs.settings.columns.newColumn')}</DialogTitle>
          <DialogDescription>
            {t('tabs.settings.columns.newColumnDescription')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4 py-2">
          <div className="grid gap-2">
            <Label htmlFor="new-col-name">
              {t('tabs.settings.columns.nameLabel')}
              <span className="ml-1 text-destructive">*</span>
            </Label>
            <Input
              id="new-col-name"
              ref={inputRef}
              autoFocus
              maxLength={NAME_MAX}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('tabs.settings.columns.namePlaceholder')}
              aria-invalid={showNameError ? true : undefined}
              aria-errormessage={showNameError ? 'new-col-name-error' : undefined}
            />
            {showNameError && (
              <p id="new-col-name-error" className="text-xs text-destructive">
                {t('tabs.settings.columns.nameRequired')}
              </p>
            )}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={createMut.isPending}>
              {createMut.isPending
                ? t('common:actions.creating')
                : t('common:actions.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
