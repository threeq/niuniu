import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, ApiError } from '@/lib/api'

interface GitIdentityResponse {
  email: string
  author: string
  address: string
}

const QUERY_KEY = ['me-git-identity']

export function GitIdentitySettings() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()

  const { data, isLoading, isError } = useQuery({
    queryKey: QUERY_KEY,
    queryFn: () => api.get<GitIdentityResponse>('/me/git-identity'),
  })

  // Local "draft" email — initialized from the server response the first time
  // it arrives. Compare-during-render keeps the input editable without a
  // useEffect.
  const [emailDraft, setEmailDraft] = useState('')
  const [prevServerEmail, setPrevServerEmail] = useState<string | undefined>(undefined)
  if (data && data.email !== prevServerEmail) {
    setPrevServerEmail(data.email)
    setEmailDraft(data.email)
  }

  const saveMutation = useMutation({
    mutationFn: (email: string) =>
      api.put<GitIdentityResponse>('/me/git-identity', { email }),
    onSuccess: (resp) => {
      queryClient.setQueryData(QUERY_KEY, resp)
      setEmailDraft(resp.email)
      setPrevServerEmail(resp.email)
      toast.success(t('gitIdentity.saved'))
    },
    onError: (err) => {
      let code = ''
      if (err instanceof ApiError) {
        const body = err.body as Record<string, unknown> | null | undefined
        code = ((body?.error as Record<string, unknown> | undefined)?.code as string) ?? ''
      }
      if (code === 'ERR_INVALID_EMAIL') {
        toast.error(t('gitIdentity.invalidEmail'))
      } else {
        const detail = err instanceof Error ? err.message : String(err)
        toast.error(t('gitIdentity.saveFailed', { detail }))
      }
    },
  })

  if (isError) {
    return (
      <div className="py-6">
        <p className="text-sm text-destructive">{t('gitIdentity.loadFailed')}</p>
      </div>
    )
  }

  const previewAuthor = data?.author ?? ''
  const previewAddress = data?.address ?? ''
  const previewLine =
    previewAuthor && previewAddress ? `${previewAuthor} <${previewAddress}>` : ''

  const dirty = emailDraft.trim() !== (data?.email ?? '').trim()
  const saveDisabled = saveMutation.isPending || isLoading || !dirty

  return (
    <div className="py-6 space-y-8 max-w-2xl">
      <div>
        <h2 className="text-lg font-medium text-foreground">{t('gitIdentity.title')}</h2>
        <p className="text-sm text-muted-foreground mt-1">
          {t('gitIdentity.description')}
        </p>
      </div>

      <div className="space-y-2">
        <label htmlFor="git-identity-email" className="text-sm text-muted-foreground">
          {t('gitIdentity.emailLabel')}
        </label>
        <Input
          id="git-identity-email"
          type="email"
          value={emailDraft}
          onChange={(e) => setEmailDraft(e.target.value)}
          placeholder={t('gitIdentity.emailPlaceholder')}
          disabled={isLoading || saveMutation.isPending}
          className="max-w-md"
        />
        <p className="text-xs text-muted-foreground/70">
          {t('gitIdentity.emailHint')}
        </p>
      </div>

      {previewLine && (
        <div className="space-y-1.5 rounded-md border border-border bg-card px-4 py-3">
          <p className="text-xs uppercase tracking-wide text-muted-foreground">
            {t('gitIdentity.previewLabel')}
          </p>
          <p className="text-xs text-muted-foreground">
            {t('gitIdentity.previewSubLabel')}
          </p>
          <p className="text-sm font-mono text-foreground">{previewLine}</p>
        </div>
      )}

      <div>
        <Button
          size="sm"
          onClick={() => saveMutation.mutate(emailDraft.trim())}
          disabled={saveDisabled}
        >
          {saveMutation.isPending ? t('gitIdentity.saving') : t('gitIdentity.save')}
        </Button>
      </div>

      <div className="rounded-md border border-border bg-muted/40 px-4 py-3">
        <p className="text-xs text-muted-foreground">
          {t('gitIdentity.perRepoNote')}
        </p>
      </div>
    </div>
  )
}
