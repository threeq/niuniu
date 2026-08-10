import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, ApiError } from '@/lib/api'

interface RepositoryIdentity {
  repository_id: number
  name: string
  email: string
  resolved_name: string
  resolved_email: string
  has_override: boolean
}

/**
 * Per-(user, repository) git author override panel — rendered inside the
 * Repository detail page's "Settings" tab. The settings page itself only
 * carries the user's global default; this section is where you say
 * "use a different author/email when committing to *this* repository".
 *
 * Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
 *   v5.1 §3.1.6
 */
export function RepoGitIdentitySection({ repoId }: { repoId: string }) {
  const { t } = useTranslation('repositories')
  const queryClient = useQueryClient()
  const id = Number(repoId)

  const queryKey = ['repository-identity', id] as const

  const { data, isLoading, isError } = useQuery({
    queryKey,
    queryFn: () => api.get<RepositoryIdentity>(`/me/repository-identities/${id}`),
    enabled: !Number.isNaN(id) && id > 0,
  })

  const [nameDraft, setNameDraft] = useState('')
  const [emailDraft, setEmailDraft] = useState('')
  const [prevServer, setPrevServer] = useState<RepositoryIdentity | undefined>(undefined)
  // Compare-during-render: sync drafts when the server response first arrives
  // (or changes after a save / delete).
  if (data && data !== prevServer) {
    setPrevServer(data)
    setNameDraft(data.name)
    setEmailDraft(data.email)
  }

  const putMutation = useMutation({
    mutationFn: (body: { name: string; email: string }) =>
      api.put<RepositoryIdentity>(`/me/repository-identities/${id}`, body),
    onSuccess: (resp) => {
      queryClient.setQueryData(queryKey, resp)
      setPrevServer(resp)
      setNameDraft(resp.name)
      setEmailDraft(resp.email)
      toast.success(t('detail.settings.gitIdentity.saved'))
    },
    onError: (err) => {
      let code = ''
      if (err instanceof ApiError) {
        const body = err.body as Record<string, unknown> | null | undefined
        code = ((body?.error as Record<string, unknown> | undefined)?.code as string) ?? ''
      }
      if (code === 'ERR_INVALID_EMAIL') {
        toast.error(t('detail.settings.gitIdentity.invalidEmail'))
      } else if (code === 'ERR_INVALID_NAME') {
        toast.error(t('detail.settings.gitIdentity.invalidName'))
      } else {
        const detail = err instanceof Error ? err.message : String(err)
        toast.error(t('detail.settings.gitIdentity.saveFailed', { detail }))
      }
    },
  })

  const deleteMutation = useMutation({
    mutationFn: () => api.delete<void>(`/me/repository-identities/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey })
      setNameDraft('')
      setEmailDraft('')
      toast.success(t('detail.settings.gitIdentity.cleared'))
    },
    onError: (err) => {
      const detail = err instanceof Error ? err.message : String(err)
      toast.error(t('detail.settings.gitIdentity.clearFailed', { detail }))
    },
  })

  if (isError) {
    return (
      <div>
        <h3 className="text-sm font-semibold text-foreground mb-2">
          {t('detail.settings.gitIdentity.title')}
        </h3>
        <p className="text-sm text-destructive">{t('detail.settings.gitIdentity.loadFailed')}</p>
      </div>
    )
  }

  const dirty =
    nameDraft.trim() !== (data?.name ?? '').trim() ||
    emailDraft.trim() !== (data?.email ?? '').trim()
  const saveDisabled = putMutation.isPending || isLoading || !dirty
  const canClear = data?.has_override === true && !deleteMutation.isPending

  const resolvedLine =
    data?.resolved_name && data?.resolved_email
      ? `${data.resolved_name} <${data.resolved_email}>`
      : ''

  return (
    <div className="space-y-4">
      <div>
        <h3 className="text-sm font-semibold text-foreground">
          {t('detail.settings.gitIdentity.title')}
        </h3>
        <p className="text-xs text-muted-foreground mt-1">
          {t('detail.settings.gitIdentity.description')}
        </p>
      </div>

      <div className="space-y-2">
        <label htmlFor="repo-git-identity-name" className="block text-sm font-medium text-foreground">
          {t('detail.settings.gitIdentity.name')}
        </label>
        <Input
          id="repo-git-identity-name"
          value={nameDraft}
          onChange={(e) => setNameDraft(e.target.value)}
          placeholder={t('detail.settings.gitIdentity.namePlaceholder')}
          disabled={putMutation.isPending}
        />
      </div>

      <div className="space-y-2">
        <label htmlFor="repo-git-identity-email" className="block text-sm font-medium text-foreground">
          {t('detail.settings.gitIdentity.email')}
        </label>
        <Input
          id="repo-git-identity-email"
          type="email"
          value={emailDraft}
          onChange={(e) => setEmailDraft(e.target.value)}
          placeholder={t('detail.settings.gitIdentity.emailPlaceholder')}
          disabled={putMutation.isPending}
        />
        <p className="text-xs text-muted-foreground/70">
          {t('detail.settings.gitIdentity.fieldsHint')}
        </p>
      </div>

      {resolvedLine && (
        <div className="rounded-md border border-border bg-muted/40 px-4 py-3 space-y-1">
          <p className="text-xs uppercase tracking-wide text-muted-foreground">
            {t('detail.settings.gitIdentity.previewLabel')}
          </p>
          <p className="text-sm font-mono text-foreground">{resolvedLine}</p>
          {!data?.has_override && (
            <p className="text-xs text-muted-foreground/70">
              {t('detail.settings.gitIdentity.usingGlobal')}
            </p>
          )}
        </div>
      )}

      <div className="flex items-center gap-2">
        <Button
          size="sm"
          onClick={() =>
            putMutation.mutate({ name: nameDraft.trim(), email: emailDraft.trim() })
          }
          disabled={saveDisabled}
        >
          {putMutation.isPending
            ? t('detail.settings.gitIdentity.saving')
            : t('detail.settings.gitIdentity.save')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => deleteMutation.mutate()}
          disabled={!canClear}
        >
          {deleteMutation.isPending
            ? t('detail.settings.gitIdentity.clearing')
            : t('detail.settings.gitIdentity.clear')}
        </Button>
      </div>
    </div>
  )
}
