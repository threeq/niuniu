import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Upload } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { apiFetch, ApiError } from '@/lib/api'
import { useLicenseStore } from '@/stores/license-store'
import type { LicenseStatus } from '@/types/api'

const QUERY_KEY = ['admin-license']

export function LicenseSettings() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const [uploading, setUploading] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const { data: detail, isLoading } = useQuery({
    queryKey: QUERY_KEY,
    queryFn: () => apiFetch<LicenseStatus>('/admin/license', { suppressError: true }),
  })

  async function handleFileChange(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return
    // Reset so the same file can be re-selected after an error
    e.target.value = ''

    setUploading(true)
    try {
      const licenseText = await file.text()
      const updated = await apiFetch<LicenseStatus>('/admin/license', {
        method: 'POST',
        body: JSON.stringify({ license: licenseText }),
        suppressError: true,
      })
      queryClient.setQueryData(QUERY_KEY, updated)
      void useLicenseStore.getState().fetch()
      toast.success(t('license.uploadSuccess'))
    } catch (err) {
      if (err instanceof ApiError && err.code === 'LICENSE_INVALID') {
        toast.error(t('license.uploadFailed'))
      } else {
        const msg = err instanceof ApiError ? err.message : t('license.uploadFailed')
        toast.error(msg)
      }
    } finally {
      setUploading(false)
    }
  }

  const expiresDisplay =
    detail && detail.expires_at > 0
      ? new Date(detail.expires_at * 1000).toLocaleDateString()
      : '—'

  const seatsDisplay = detail
    ? `${detail.seats_used} / ${detail.max_seats || '∞'}`
    : '—'

  return (
    <div className="py-6 space-y-8 max-w-2xl">
      <div>
        <h2 className="text-lg font-medium text-foreground">{t('license.title')}</h2>
      </div>

      {isLoading ? (
        <p className="text-sm text-muted-foreground">{t('systemDeps.detecting')}</p>
      ) : (
        <div className="space-y-4 rounded-md border border-border bg-card px-4 py-4">
          {detail?.state === 'unlicensed' && (
            <p className="text-sm font-medium text-destructive">{t('license.unlicensed')}</p>
          )}
          <div className="grid grid-cols-2 gap-x-8 gap-y-3 text-sm">
            <span className="text-muted-foreground">{t('license.customer')}</span>
            <span className="text-foreground font-medium">
              {detail?.customer || '—'}
              {detail?.is_trial && (
                <span className="ml-2 inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium bg-warning/10 text-warning border border-warning/30">
                  {t('license.trial')}
                </span>
              )}
            </span>

            <span className="text-muted-foreground">{t('license.seats')}</span>
            <span className="text-foreground font-medium tabular-nums">{seatsDisplay}</span>

            <span className="text-muted-foreground">{t('license.expires')}</span>
            <span className="text-foreground font-medium tabular-nums">{expiresDisplay}</span>
          </div>
        </div>
      )}

      <div>
        <input
          ref={fileInputRef}
          type="file"
          accept=".lic"
          className="hidden"
          onChange={handleFileChange}
        />
        <Button
          size="sm"
          variant="outline"
          disabled={uploading}
          onClick={() => fileInputRef.current?.click()}
        >
          <Upload className="mr-1.5 h-3.5 w-3.5" />
          {uploading ? t('systemDeps.tool.installing') : t('license.upload')}
        </Button>
      </div>
    </div>
  )
}
