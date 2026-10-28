import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { adminSettingsApi } from '@/lib/api'

const SHARE_BASE_URL_KEY = 'share.base_url'

// 分享访问域名（团队版 admin 设置）：生成的分享链接形如
// <域名>/share/ws/<token>。留空 = 用当前访问地址。
export function ShareSettings() {
  const { t } = useTranslation('settings')
  const [value, setValue] = useState<string | null>(null)
  const [savedTick, setSavedTick] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['admin-setting', SHARE_BASE_URL_KEY],
    queryFn: () => adminSettingsApi.get(SHARE_BASE_URL_KEY),
  })
  if (value === null && query.data) {
    setValue(query.data.value)
  }

  const mutation = useMutation({
    mutationFn: async (v: string) => {
      await adminSettingsApi.put(SHARE_BASE_URL_KEY, v.trim())
    },
    onSuccess: () => {
      setError(null)
      setSavedTick(true)
      setTimeout(() => setSavedTick(false), 2000)
      void query.refetch()
    },
    onError: () => {
      setError(t('share.saveFailed'))
    },
  })

  if (query.isLoading || value === null) {
    return <div className="py-6 text-sm text-muted-foreground">…</div>
  }

  return (
    <div className="py-6 space-y-6 max-w-xl">
      <div>
        <h2 className="text-lg font-medium text-foreground">{t('share.title')}</h2>
        <p className="text-sm text-muted-foreground mt-1">{t('share.description')}</p>
      </div>
      <div className="space-y-2">
        <label className="text-sm font-medium text-foreground" htmlFor="share-base-url">
          {t('share.baseUrlLabel')}
        </label>
        <Input
          id="share-base-url"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder={t('share.baseUrlPlaceholder')}
          className="font-mono text-sm"
        />
        <p className="text-xs text-muted-foreground/70">{t('share.baseUrlHint')}</p>
      </div>
      {error && <p className="text-sm text-destructive">{error}</p>}
      <div className="flex items-center gap-3">
        <Button size="sm" disabled={mutation.isPending} onClick={() => mutation.mutate(value)}>
          {mutation.isPending ? t('common:actions.saving') : t('common:actions.save')}
        </Button>
        {savedTick && <span className="text-sm text-success">{t('share.saved')}</span>}
      </div>
    </div>
  )
}
