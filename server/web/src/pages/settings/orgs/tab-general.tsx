import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/api'
import { useOrgStore } from '@/stores/org-store'
import type { Org } from '@/types/org'

interface TabGeneralProps {
  org: Org
  role: string | undefined
}

export function TabGeneral({ org, role }: TabGeneralProps) {
  const { t } = useTranslation('orgs')
  const invalidate = useOrgStore((s) => s.invalidate)
  const fetchOrgs = useOrgStore((s) => s.fetch)

  const canEdit = role === 'owner' || role === 'admin'

  const [name, setName] = useState(org.name)
  const [slug, setSlug] = useState(org.slug)
  const [description, setDescription] = useState(org.description ?? '')
  const [saving, setSaving] = useState(false)

  // Reset form if org changes
  useEffect(() => {
    setName(org.name)
    setSlug(org.slug)
    setDescription(org.description ?? '')
  }, [org.id, org.name, org.slug, org.description])

  async function handleSave() {
    if (!name.trim()) return
    setSaving(true)
    try {
      await api.updateOrg(org.id, {
        name: name.trim(),
        slug: slug.trim() || undefined,
        description: description.trim(),
      })
      invalidate()
      await fetchOrgs()
      toast.success(t('detail.general.saved'))
    } catch (err) {
      toast.error(
        t('detail.general.saveFailed', {
          error: err instanceof Error ? err.message : String(err),
        }),
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="py-6 space-y-6 max-w-lg">
      <div>
        <h2 className="text-base font-medium text-foreground mb-4">
          {t('detail.general.sectionTitle')}
        </h2>
        <div className="space-y-4">
          <div>
            <label className="text-sm font-medium text-foreground">
              {t('detail.general.name')} <span className="text-destructive">*</span>
            </label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              disabled={!canEdit}
              className="mt-1"
            />
          </div>
          <div>
            <label className="text-sm font-medium text-foreground">
              {t('detail.general.slug')}
            </label>
            <Input
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              disabled={!canEdit}
              className="mt-1 font-mono"
            />
            <p className="text-xs text-muted-foreground mt-1">
              {t('detail.general.slugHint', { slug: slug || t('detail.general.slugPlaceholder') })}
            </p>
          </div>
          <div>
            <label className="text-sm font-medium text-foreground">
              {t('detail.general.description')}
            </label>
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              disabled={!canEdit}
              placeholder={t('detail.general.descriptionPlaceholder')}
              className="mt-1"
            />
          </div>
        </div>
      </div>

      {canEdit && (
        <Button onClick={handleSave} disabled={!name.trim() || saving} size="sm">
          {saving ? t('common:actions.saving') : t('common:actions.save')}
        </Button>
      )}

      {!canEdit && (
        <p className="text-xs text-muted-foreground">{t('detail.general.readOnlyHint')}</p>
      )}
    </div>
  )
}
