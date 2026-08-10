// Background update poller for personal-mode SPAs.
//
// Fires a check 5s after boot (avoids competing with the cold-start render
// burst), then every 6h while the tab is alive. A toast surfaces only when
// the latest release (from `/api/app-update/latest`, proxied off the official
// website changelog) is newer than the running build AND the user hasn't
// already dismissed the same version this session.
//
// Two layers of throttling:
//
//   - localStorage `niuniu.updateChecker.lastFiredAt`: timestamp of the
//     most recent successful network call. Reloading the tab within the
//     interval reuses the cached result instead of re-checking.
//
//   - localStorage `niuniu.updateChecker.dismissedVersion`: latest tag the
//     user has tapped "稍后" on. We won't re-toast the same version until
//     a newer one is published, so a daily 6h tick doesn't keep nagging.
//
// Standalone-server / team-edition is filtered out at the call site
// (main.tsx checks personalMode); this module assumes it should run.

import { toast } from 'sonner'
import i18n from '@/i18n'
import { checkForUpdates, releasesPageURL } from './version-check'

const LS_LAST_FIRED = 'niuniu.updateChecker.lastFiredAt'
const LS_DISMISSED = 'niuniu.updateChecker.dismissedVersion'
const SIX_HOURS_MS = 6 * 60 * 60 * 1000
const BOOT_DELAY_MS = 5_000

let started = false
let intervalHandle: ReturnType<typeof setInterval> | null = null
let bootTimeoutHandle: ReturnType<typeof setTimeout> | null = null

export function startUpdateChecker(currentVersion: string) {
  if (started) return
  started = true

  const tick = async () => {
    try {
      const result = await checkForUpdates(currentVersion)
      localStorage.setItem(LS_LAST_FIRED, String(Date.now()))
      if (!result.available) return
      const dismissed = localStorage.getItem(LS_DISMISSED)
      if (dismissed === result.latest) return
      surfaceToast(result.latest, result.download_url, result.release_url)
    } catch {
      // Network / upstream-unreachable / GFW / etc. — silent here. The
      // Settings → 关于 panel surfaces a friendlier error inline when the
      // user explicitly clicks 「检查更新」.
    }
  }

  // Stagger first run so we don't compete with /me, /orgs, /system-deps.
  // Skip when localStorage indicates a recent successful check, but still
  // schedule the recurring 6h interval so a long-lived tab eventually
  // catches a new release.
  const last = Number.parseInt(localStorage.getItem(LS_LAST_FIRED) ?? '', 10)
  const fresh = Number.isFinite(last) && Date.now() - last < SIX_HOURS_MS
  if (!fresh) {
    bootTimeoutHandle = setTimeout(() => {
      bootTimeoutHandle = null
      void tick()
    }, BOOT_DELAY_MS)
  }
  intervalHandle = setInterval(() => void tick(), SIX_HOURS_MS)
}

// Vite HMR re-evaluates this module on edit — without explicit cleanup the
// previous module's interval keeps firing in its dead closure, stacking N
// intervals across a long dev session. In production main.tsx imports this
// once at boot so cleanup is a no-op, but the dev-loop guard is cheap.
if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    if (bootTimeoutHandle) clearTimeout(bootTimeoutHandle)
    if (intervalHandle) clearInterval(intervalHandle)
    bootTimeoutHandle = null
    intervalHandle = null
    started = false
  })
}

function surfaceToast(version: string, downloadURL: string, releaseURL: string) {
  const t = i18n.getFixedT(null, 'settings')
  const target = downloadURL || releaseURL || releasesPageURL
  toast(t('about.update.newVersion', { version }), {
    description: t('about.update.toastDescription'),
    duration: 12_000,
    action: {
      label: t('about.update.download'),
      onClick: () => window.open(target, '_blank', 'noopener,noreferrer'),
    },
    cancel: {
      label: t('about.update.dismiss'),
      onClick: () => {
        localStorage.setItem(LS_DISMISSED, version)
      },
    },
  })
}
