import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from '@tanstack/react-router'
import { toast } from 'sonner'
import { ShieldCheck } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useConsentStore } from '@/stores/consent-store'
import { useAuthStore } from '@/stores/auth-store'
import { useConfigStore } from '@/stores/config-store'
import { isAuthEnabled } from '@/lib/edition'
import { openExternalSmart } from '@/lib/shell'
import { TELEMETRY_PRIVACY_URL } from '@/lib/links'

interface ConsentSection {
  h: string
  p: string
}

/**
 * Render a consent string, turning `**...**` markers into bold + highlighted
 * spans so the key risk/liability phrases stand out. Keeps the source text in
 * i18n (no HTML) and uses design-system tokens only.
 */
function renderEmphasis(text: string) {
  return text.split(/\*\*(.+?)\*\*/g).map((part, i) =>
    i % 2 === 1 ? (
      <mark
        key={i}
        className="rounded bg-warning/15 px-0.5 font-semibold text-warm-text"
      >
        {part}
      </mark>
    ) : (
      <span key={i}>{part}</span>
    ),
  )
}

/**
 * Blocking privacy & disclaimer gate. Mounted inside RootLayout (the
 * authenticated shell), so it appears after login and covers the whole app
 * until the user accepts the current agreement version. The overlay is
 * intentionally NOT a dismissable Dialog — there is no close affordance; the
 * only ways out are Agree or Decline.
 *
 * Renders nothing until the consent status has loaded and only when the user
 * still needs to consent, so the common case adds no chrome.
 */
export function ConsentGate() {
  const { t } = useTranslation('common')
  const status = useConsentStore((s) => s.status)
  const loaded = useConsentStore((s) => s.loaded)
  // Personal/self-hosted (Auth.Enabled=false) is the only edition that reports
  // telemetry, so the disclosure section is appended only there. Gate on the
  // loaded flag too, so team edition never flashes the section before config
  // resolves (personalMode defaults to true until /api/health responds).
  const personalMode = useConfigStore((s) => s.personalMode)
  const configLoaded = useConfigStore((s) => s.loaded)
  const [checked, setChecked] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const navigate = useNavigate()

  useEffect(() => {
    void useConsentStore.getState().fetch()
    // Idempotent; populates personalMode for the telemetry section gate below.
    void useConfigStore.getState().load()
  }, [])

  if (!loaded || !status || !status.needs_consent) {
    return null
  }

  const sections = t('consent.sections', { returnObjects: true }) as ConsentSection[]

  const onAgree = async () => {
    setSubmitting(true)
    try {
      await useConsentStore.getState().accept(status.current_version)
    } catch {
      toast.error(t('consent.submitError'))
    } finally {
      setSubmitting(false)
    }
  }

  const onDecline = async () => {
    if (await isAuthEnabled()) {
      useAuthStore.getState().logout()
      void navigate({ to: '/login' })
    } else {
      // Personal edition has no real logout — keep the gate up and remind them.
      toast.info(t('consent.declineHint'))
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4 backdrop-blur-sm">
      <div className="flex max-h-[85vh] w-full max-w-2xl flex-col rounded-lg border border-warm-border bg-warm-surface shadow-lg">
        <div className="flex items-center gap-2 border-b border-warm-border px-6 py-4">
          <ShieldCheck className="size-5 text-brand" />
          <h2 className="text-base font-semibold text-warm-text">{t('consent.title')}</h2>
        </div>

        <div className="px-6 pt-4 text-sm text-warm-text-muted">{renderEmphasis(t('consent.intro'))}</div>

        <ScrollArea className="flex-1 px-6 py-4">
          <div className="space-y-4">
            {sections.map((s, i) => (
              <section key={i} className="space-y-1">
                <h3 className="text-sm font-medium text-warm-text">{s.h}</h3>
                <p className="text-xs leading-relaxed text-warm-text-muted">{renderEmphasis(s.p)}</p>
              </section>
            ))}

            {/* Personal-edition anonymous telemetry disclosure (Epic #329).
                Not part of the legal consent.sections array — appended only for
                personal/self-hosted, since team edition never reports. The
                consent version is intentionally unchanged: existing users who
                already accepted aren't re-prompted; their entry point is the
                Settings -> Privacy switch + the privacy notice. */}
            {configLoaded && personalMode && (
              <section className="space-y-1 border-t border-warm-border pt-4">
                <h3 className="text-sm font-medium text-warm-text">{t('consent.telemetry.h')}</h3>
                <p className="text-xs leading-relaxed text-warm-text-muted">
                  {renderEmphasis(t('consent.telemetry.p'))}
                </p>
                <Button
                  variant="link"
                  className="h-auto p-0 text-xs font-normal text-brand"
                  onClick={() => {
                    void openExternalSmart(TELEMETRY_PRIVACY_URL, personalMode)
                  }}
                >
                  {t('consent.telemetry.learnMore')}
                </Button>
              </section>
            )}
          </div>
        </ScrollArea>

        <div className="space-y-3 border-t border-warm-border px-6 py-4">
          <label className="flex cursor-pointer items-start gap-2">
            <Checkbox
              checked={checked}
              onCheckedChange={(v) => setChecked(v === true)}
              className="mt-0.5"
            />
            <span className="text-sm text-warm-text">{t('consent.checkbox')}</span>
          </label>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={onDecline} disabled={submitting}>
              {t('consent.decline')}
            </Button>
            <Button onClick={onAgree} disabled={!checked || submitting}>
              {t('consent.agree')}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
