import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Bug, Loader2, Download, ExternalLink, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useConfigStore } from '@/stores/config-store'
import { checkForUpdates, isNetworkError, releasesPageURL, type UpdateCheckResult } from '@/lib/version-check'

const LAST_CHECK_KEY = 'niuniu.about.lastUpdateCheck'

// Map active i18n language to the correct GitHub issue template. Treat
// zh-CN / zh-TW alike — the public repo only ships zh + en templates.
function feedbackURL(lang: string): string {
  const template = lang && lang.toLowerCase().startsWith('zh') ? 'bug_report_zh.yml' : 'bug_report_en.yml'
  return `https://github.com/threeq/niuniu-public/issues/new?template=${template}`
}

// A "release version" is a clean tag like "v1.2.3". Anything else — empty,
// the literal "dev" fallback baked into health.go, or a git-describe
// descriptor such as "v1.0.4-3-gabc1234[-dirty]" — renders as the localized
// "开发版" label so users never see raw build identifiers.
function isReleaseVersion(v: string): boolean {
  if (!v || v === 'dev') return false
  // git describe with commits ahead of tag: "...-N-gHHHHHHH"
  if (/-\d+-g[0-9a-f]{7,}/.test(v)) return false
  // dirty working tree marker
  if (/-dirty$/.test(v)) return false
  // Must start with semver-shaped digits (pre-release/build suffix is fine).
  return /^v?\d+\.\d+\.\d+/.test(v)
}

// AboutSettings — surfaces build version + (in personal mode) a manual
// check-for-updates control. Background polling lives in main.tsx so it
// also fires when the user never opens this tab.
//
// Why no update check in team / standalone-server modes: the user manages
// the binary themselves there; auto-checking GitHub Releases for an upgrade
// would be misleading, since their deployment cadence is independent of
// niuniu's release cadence.
export function AboutSettings() {
  const { t, i18n } = useTranslation('settings')
  const serverVersion = useConfigStore((s) => s.serverVersion)
  const personalMode = useConfigStore((s) => s.personalMode)
  const loaded = useConfigStore((s) => s.loaded)

  const [checking, setChecking] = useState(false)
  const [result, setResult] = useState<UpdateCheckResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [networkBlocked, setNetworkBlocked] = useState(false)
  const [lastCheckedAt, setLastCheckedAt] = useState<number | null>(() => {
    const raw = localStorage.getItem(LAST_CHECK_KEY)
    return raw ? Number.parseInt(raw, 10) : null
  })
  // StrictMode runs effects twice in dev. autoFiredRef ensures the
  // first-mount auto-check fires exactly once even under double-mount.
  const autoFiredRef = useRef(false)

  const handleCheck = useCallback(async () => {
    setChecking(true)
    setError(null)
    setNetworkBlocked(false)
    try {
      const r = await checkForUpdates(serverVersion || 'dev')
      setResult(r)
      const now = Date.now()
      setLastCheckedAt(now)
      localStorage.setItem(LAST_CHECK_KEY, String(now))
    } catch (e) {
      // GFW / offline / DNS / CORS errors all surface as `Failed to fetch`
      // or similar — opaque message to anyone non-technical. Render a
      // dedicated state with a fallback "open releases page" CTA, so the
      // user still has a path even when api.github.com is unreachable.
      if (isNetworkError(e)) {
        setNetworkBlocked(true)
      } else {
        setError(e instanceof Error ? e.message : String(e))
      }
    } finally {
      setChecking(false)
    }
  }, [serverVersion])

  // Auto-trigger one check on first mount in personal mode so the
  // "check for updates" button immediately reflects the latest state when
  // the user opens this tab. The background poller in main.tsx covers
  // app-lifetime checks; this just primes the per-page UX.
  useEffect(() => {
    if (!personalMode || !loaded) return
    if (autoFiredRef.current) return
    // Skip if a recent check is already cached (within 1h) — main.tsx's
    // boot poll typically populates this.
    const FRESH_MS = 60 * 60 * 1000
    const cachedRaw = localStorage.getItem(LAST_CHECK_KEY)
    const cached = cachedRaw ? Number.parseInt(cachedRaw, 10) : NaN
    if (Number.isFinite(cached) && Date.now() - cached < FRESH_MS) return
    autoFiredRef.current = true
    void handleCheck()
  }, [personalMode, loaded, handleCheck])

  return (
    <div className="py-6 space-y-6 max-w-2xl">
      <div>
        <h2 className="text-lg font-semibold mb-1">{t('about.title')}</h2>
        <p className="text-sm text-muted-foreground">{t('about.description')}</p>
      </div>

      <div className="rounded-lg border bg-card p-5 space-y-3">
        <div className="flex items-center justify-between gap-3">
          <span className="text-sm text-muted-foreground">{t('about.serverVersion')}</span>
          <span className="font-mono text-sm">
            {isReleaseVersion(serverVersion) ? serverVersion : t('about.versionDev')}
          </span>
        </div>
        <div className="flex items-center justify-between gap-3">
          <span className="text-sm text-muted-foreground">{t('about.edition')}</span>
          <span className="text-sm">
            {personalMode ? t('about.editionPersonal') : t('about.editionTeam')}
          </span>
        </div>
      </div>

      <div className="rounded-lg border bg-card p-5">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h3 className="font-medium text-sm">{t('about.feedback.title')}</h3>
            <p className="text-xs text-muted-foreground mt-0.5">
              {t('about.feedback.description')}
            </p>
          </div>
          <Button
            size="sm"
            variant="outline"
            onClick={() => window.open(feedbackURL(i18n.language), '_blank', 'noopener,noreferrer')}
            className="gap-1.5 shrink-0"
          >
            <Bug className="h-3.5 w-3.5" />
            {t('about.feedback.openButton')}
          </Button>
        </div>
      </div>

      {personalMode && (
        <div className="rounded-lg border bg-card p-5 space-y-3">
          <div className="flex items-center justify-between gap-3">
            <div>
              <h3 className="font-medium text-sm">{t('about.update.title')}</h3>
              <p className="text-xs text-muted-foreground mt-0.5">
                {t('about.update.description')}
              </p>
            </div>
            <Button size="sm" variant="outline" disabled={checking} onClick={handleCheck} className="gap-1.5 shrink-0">
              {checking ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
              {t('about.update.checkButton')}
            </Button>
          </div>

          {error && (
            <div className="rounded border border-destructive/30 bg-destructive/10 dark:bg-red-950/30 px-3 py-2 text-sm text-destructive dark:text-red-300">
              {error}
            </div>
          )}

          {networkBlocked && (
            <div className="rounded border border-amber-500/30 bg-amber-500/10 dark:bg-amber-950/30 px-3 py-3 space-y-2">
              <p className="text-sm text-amber-700 dark:text-amber-300">
                {t('about.update.networkUnavailable')}
              </p>
              <Button
                size="sm"
                variant="outline"
                onClick={() => window.open(releasesPageURL, '_blank', 'noopener,noreferrer')}
                className="gap-1.5"
              >
                <ExternalLink className="h-3.5 w-3.5" />
                {t('about.update.viewRelease')}
              </Button>
            </div>
          )}

          {result && !result.available && !error && !networkBlocked && (
            <div className="text-sm text-muted-foreground">
              {t('about.update.upToDate', { version: result.current })}
            </div>
          )}

          {result?.available && (
            <div className="rounded border border-info/30 bg-info/5 dark:bg-blue-950/30 px-3 py-3 space-y-2">
              <div className="text-sm font-medium">
                {t('about.update.newVersion', { version: result.latest })}
              </div>
              <div className="flex items-center gap-2 flex-wrap">
                {result.download_url && (
                  <Button
                    size="sm"
                    onClick={() => window.open(result.download_url, '_blank', 'noopener,noreferrer')}
                    className="gap-1.5"
                  >
                    <Download className="h-3.5 w-3.5" />
                    {t('about.update.download')}
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => window.open(result.release_url || releasesPageURL, '_blank', 'noopener,noreferrer')}
                  className="gap-1.5"
                >
                  <ExternalLink className="h-3.5 w-3.5" />
                  {t('about.update.viewRelease')}
                </Button>
              </div>
            </div>
          )}

          {lastCheckedAt && (
            <div className="text-xs text-muted-foreground">
              {t('about.update.lastChecked', {
                time: new Date(lastCheckedAt).toLocaleString(),
              })}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
