import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from '@tanstack/react-router'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { useOrgStore } from '@/stores/org-store'

interface CreateOrgDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function CreateOrgDialog({ open, onOpenChange }: CreateOrgDialogProps) {
  const { t } = useTranslation('orgs')
  const navigate = useNavigate()
  const invalidate = useOrgStore((s) => s.invalidate)
  const fetch = useOrgStore((s) => s.fetch)

  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [description, setDescription] = useState('')
  const [submitting, setSubmitting] = useState(false)

  function reset() {
    setName('')
    setSlug('')
    setDescription('')
  }

  async function handleSubmit() {
    if (!name.trim()) return
    setSubmitting(true)
    try {
      const org = await api.createOrg({
        name: name.trim(),
        slug: slug.trim() || undefined,
        description: description.trim() || undefined,
      })
      invalidate()
      await fetch()
      onOpenChange(false)
      reset()
      navigate({ to: '/settings/orgs/$slug', params: { slug: org.slug } })
    } catch (err) {
      toast.error(
        t('dialogs.createOrg.createFailed', {
          error: err instanceof Error ? err.message : String(err),
        }),
      )
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('dialogs.createOrg.title')}</DialogTitle>
          <DialogDescription>{t('dialogs.createOrg.description')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div>
            <label className="text-sm font-medium text-foreground">
              {t('dialogs.createOrg.name')} <span className="text-destructive">*</span>
            </label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('dialogs.createOrg.namePlaceholder')}
              className="mt-1"
            />
          </div>
          <div>
            <label className="text-sm font-medium text-foreground">
              {t('dialogs.createOrg.slug')}
            </label>
            <Input
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder={t('dialogs.createOrg.slugPlaceholder')}
              className="mt-1"
            />
            <p className="text-xs text-muted-foreground mt-1">
              {t('dialogs.createOrg.slugHint')}
            </p>
          </div>
          <div>
            <label className="text-sm font-medium text-foreground">
              {t('dialogs.createOrg.description')}
            </label>
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t('dialogs.createOrg.descriptionPlaceholder')}
              className="mt-1"
            />
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>
            {t('dialogs.createOrg.cancel')}
          </Button>
          <Button onClick={handleSubmit} disabled={!name.trim() || submitting}>
            {submitting ? t('dialogs.createOrg.submitting') : t('dialogs.createOrg.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
