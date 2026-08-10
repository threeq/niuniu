import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, ApiError } from '@/lib/api'
import type { GitIdentity } from '@/types/api'

interface Props {
  initial?: GitIdentity
  onSaved: () => void
}

export function GitIdentityPanel({ initial, onSaved }: Props) {
  const { t } = useTranslation('settings')
  const configured = !!initial?.configured
  const [editing, setEditing] = useState(!configured)
  const [name, setName] = useState(initial?.name ?? '')
  const [email, setEmail] = useState(initial?.email ?? '')
  const [saving, setSaving] = useState(false)

  async function save() {
    setSaving(true)
    try {
      await api.setGitIdentity(name.trim(), email.trim())
      toast.success(t('systemDeps.gitIdentity.saved'))
      onSaved()
      setEditing(false)
    } catch (err) {
      let code = ''
      if (err instanceof ApiError) {
        const body = err.body as Record<string, unknown> | null | undefined;
        code = (body?.error as Record<string, unknown> | undefined)?.code as string ?? ''
      }
      if (code === 'INVALID_GIT_IDENTITY') {
        toast.error(t('systemDeps.gitIdentity.invalid'))
      } else {
        const detail = err instanceof Error ? err.message : String(err)
        toast.error(t('systemDeps.gitIdentity.saveFailed', { detail }))
      }
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mt-2 flex flex-col gap-2">
      {/* Always-visible disambiguation: this panel configures the OS-global
          git identity (`git config --global`), NOT niuniu's per-user
          commit author. Both exist; new users repeatedly conflate them. */}
      <p className="text-xs text-muted-foreground">
        {t('systemDeps.gitIdentity.disambiguation')}
      </p>
      {!editing && configured ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <span>
            {initial!.name} &lt;{initial!.email}&gt;
          </span>
          <Button size="sm" variant="ghost" onClick={() => setEditing(true)}>
            {t('systemDeps.gitIdentity.edit')}
          </Button>
        </div>
      ) : (
        <>
          {!configured && (
            <div className="text-sm text-amber-600">
              {t('systemDeps.gitIdentity.unconfiguredHint')}
            </div>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <Input
              placeholder={t('systemDeps.gitIdentity.namePlaceholder')}
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="max-w-xs"
            />
            <Input
              placeholder={t('systemDeps.gitIdentity.emailPlaceholder')}
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className="max-w-xs"
            />
            <Button size="sm" disabled={saving || !name.trim() || !email.trim()} onClick={save}>
              {t('systemDeps.gitIdentity.save')}
            </Button>
            {configured && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setName(initial?.name ?? '')
                  setEmail(initial?.email ?? '')
                  setEditing(false)
                }}
              >
                {t('systemDeps.gitIdentity.cancel')}
              </Button>
            )}
          </div>
        </>
      )}
    </div>
  )
}
