import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/api'
import { useOrgStore } from '@/stores/org-store'
import {
  listClaudeAccounts,
  getActiveClaudeAccount,
  setActiveClaudeAccount,
} from '@/lib/claude-account-api'
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

  // Claude active account for this org
  const { data: claudeAccounts = [] } = useQuery({
    queryKey: ['claude-accounts'],
    queryFn: listClaudeAccounts,
    enabled: canEdit,
  })
  const { data: orgActiveResp, refetch: refetchOrgActive } = useQuery({
    queryKey: ['claude-active-account', 'org', org.id],
    queryFn: () => getActiveClaudeAccount('org', org.id),
    enabled: canEdit,
  })
  const orgActiveAccountId = orgActiveResp?.account?.id ?? null

  const activeClaudeAccounts = claudeAccounts.filter(
    (a) => a.config_dir !== '' && a.status === 'active'
  )
  const orgActiveInList = claudeAccounts.find((a) => a.id === orgActiveAccountId)
  const orgActiveIsInvisible = orgActiveAccountId !== null && !orgActiveInList

  async function handleOrgActiveChange(accountId: number | null) {
    try {
      await setActiveClaudeAccount({
        owner_type: 'org',
        owner_id: org.id,
        account_id: accountId,
      })
      refetchOrgActive()
    } catch {
      // apiFetch shows toast
    }
  }

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

      {/* Org active Claude account */}
      {canEdit && (
        <div className="border-t border-border pt-4">
          <label className="text-sm font-medium text-foreground">
            {t('detail.general.claudeActiveAccount')}
          </label>
          <select
            value={orgActiveAccountId ?? ''}
            onChange={(e) => handleOrgActiveChange(e.target.value === '' ? null : Number(e.target.value))}
            className="mt-1 w-full h-9 rounded border border-border bg-background px-3 text-sm focus:outline-none focus:ring-1 focus:ring-info"
          >
            <option value="">{t('claude-accounts:active.followDefault')}</option>
            {orgActiveIsInvisible && (
              <option value={String(orgActiveAccountId)} disabled>
                {t('claude-accounts:picker.privateNotVisible')}
              </option>
            )}
            {activeClaudeAccounts.map((a) => (
              <option key={a.id} value={String(a.id)}>
                {a.name} {a.email ? `(${a.email})` : ''}
              </option>
            ))}
          </select>
        </div>
      )}

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
