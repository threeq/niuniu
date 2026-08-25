import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from '@tanstack/react-router'
import { QRCodeSVG } from 'qrcode.react'
import { Check, Copy, Loader2, ShieldAlert } from 'lucide-react'
import { api, ApiError } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useMfaPolicyStore } from '@/stores/mfa-policy-store'
import { useAuthStore } from '@/stores/auth-store'

type Step = 'scan' | 'backup'

/**
 * Blocking two-factor enrollment gate. Mounted inside RootLayout (the
 * authenticated shell), so it appears right after login and covers the whole app
 * until the member finishes enrolling. Like ConsentGate, it is intentionally NOT
 * a dismissable Dialog — there is no close affordance, and the only ways out are
 * completing enrollment or signing out.
 *
 * Renders nothing until the policy has loaded and only when the member actually
 * owes enrollment, so the common case adds no chrome. The real enforcement lives
 * server-side in api.MFAEnrollGuard — this page is the guided path to satisfying
 * it, not the security boundary.
 */
export function MfaEnrollGate() {
  const { t } = useTranslation('auth')
  const policy = useMfaPolicyStore((s) => s.policy)
  const loaded = useMfaPolicyStore((s) => s.loaded)
  const navigate = useNavigate()

  const [step, setStep] = useState<Step>('scan')
  const [setup, setSetup] = useState<{ provisioning_uri: string; secret: string } | null>(null)
  const [setupLoading, setSetupLoading] = useState(false)
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [backupCodes, setBackupCodes] = useState<string[]>([])
  const [copied, setCopied] = useState(false)
  const [acked, setAcked] = useState(false)
  const [secretShown, setSecretShown] = useState(false)

  useEffect(() => {
    void useMfaPolicyStore.getState().fetch()
  }, [])

  const blocking = loaded && policy?.needs_setup === true

  // Provision the secret once the gate becomes visible. Guarded by a ref rather
  // than by `setup`/`setupLoading` state: on failure `setup` stays null and
  // `setupLoading` flips back to false, which would re-trigger this effect and
  // hammer the endpoint in a retry loop. The ref makes it strictly one attempt,
  // with an explicit Retry button for the error case.
  const provisionRequested = useRef(false)
  const provision = useCallback(() => {
    provisionRequested.current = true
    setSetupLoading(true)
    setError('')
    api
      .setupMFA()
      .then((d) => setSetup({ provisioning_uri: d.provisioning_uri, secret: d.secret }))
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)))
      .finally(() => setSetupLoading(false))
  }, [])

  useEffect(() => {
    if (!blocking || provisionRequested.current) return
    provision()
  }, [blocking, provision])

  if (!blocking) return null

  const onEnable = async () => {
    if (code.length !== 6) return
    setError('')
    setSubmitting(true)
    try {
      const res = await api.enableMFA(code)
      setBackupCodes(res.backup_codes)
      setStep('backup')
    } catch (err) {
      setError(
        err instanceof ApiError && err.message ? err.message : t('enrollGate.invalidCode'),
      )
    } finally {
      setSubmitting(false)
    }
  }

  // Only lift the gate once the backup codes have been acknowledged, so they are
  // never silently skipped — they are the sole recovery path if the member loses
  // their authenticator.
  const onFinish = () => {
    useMfaPolicyStore.getState().markEnrolled()
  }

  const onSignOut = () => {
    useAuthStore.getState().logout()
    void navigate({ to: '/login' })
  }

  const onCopy = () => {
    void navigator.clipboard.writeText(backupCodes.join('\n'))
    setCopied(true)
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4 backdrop-blur-sm">
      <div className="flex max-h-[85vh] w-full max-w-lg flex-col rounded-lg border border-warm-border bg-warm-surface shadow-lg">
        <div className="flex items-center gap-2 border-b border-warm-border px-6 py-4">
          <ShieldAlert className="size-5 text-brand" />
          <h2 className="text-base font-semibold text-warm-text">
            {step === 'scan' ? t('enrollGate.title') : t('enrollGate.backupTitle')}
          </h2>
        </div>

        <ScrollArea className="flex-1 px-6 py-4">
          {step === 'scan' ? (
            <div className="space-y-4">
              <p className="text-sm text-warm-text-muted">{t('enrollGate.intro')}</p>

              {setupLoading && (
                <div className="flex justify-center py-8">
                  <Loader2 className="size-6 animate-spin text-muted-foreground" />
                </div>
              )}

              {error && !setup && (
                <Alert variant="destructive">
                  <AlertDescription className="flex items-center justify-between gap-2">
                    <span>{error}</span>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={provision}
                      disabled={setupLoading}
                    >
                      {t('enrollGate.retry')}
                    </Button>
                  </AlertDescription>
                </Alert>
              )}

              {setup && (
                <>
                  {/* Rendered locally — the TOTP secret must never be sent to a
                      third-party QR service. */}
                  <div className="flex justify-center rounded-md border border-warm-border bg-white p-3">
                    <QRCodeSVG value={setup.provisioning_uri} size={180} />
                  </div>

                  <div className="space-y-1 text-center">
                    {secretShown ? (
                      <p className="break-all font-mono text-xs text-warm-text-muted">
                        {setup.secret}
                      </p>
                    ) : (
                      <Button
                        variant="link"
                        className="h-auto p-0 text-xs font-normal text-brand"
                        onClick={() => setSecretShown(true)}
                      >
                        {t('enrollGate.showSecret')}
                      </Button>
                    )}
                  </div>

                  <div className="space-y-2">
                    <label htmlFor="mfa-enroll-code" className="text-sm font-medium text-warm-text">
                      {t('enrollGate.codeLabel')}
                    </label>
                    <Input
                      id="mfa-enroll-code"
                      type="text"
                      inputMode="numeric"
                      autoComplete="one-time-code"
                      maxLength={6}
                      value={code}
                      onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') void onEnable()
                      }}
                      placeholder={t('mfa.codePlaceholder')}
                    />
                  </div>

                  {error && (
                    <Alert variant="destructive">
                      <AlertDescription>{error}</AlertDescription>
                    </Alert>
                  )}
                </>
              )}
            </div>
          ) : (
            <div className="space-y-4">
              <Alert variant="warning">
                <AlertDescription>{t('enrollGate.backupWarning')}</AlertDescription>
              </Alert>

              <div className="grid grid-cols-2 gap-2">
                {backupCodes.map((c) => (
                  <code
                    key={c}
                    className="select-all rounded bg-muted px-3 py-1.5 text-center font-mono text-sm"
                  >
                    {c}
                  </code>
                ))}
              </div>

              <Button variant="outline" size="sm" className="w-full" onClick={onCopy}>
                {copied ? <Check className="mr-1 size-4" /> : <Copy className="mr-1 size-4" />}
                {copied ? t('enrollGate.copied') : t('enrollGate.copy')}
              </Button>

              <label className="flex cursor-pointer items-start gap-2">
                <Checkbox
                  checked={acked}
                  onCheckedChange={(v) => setAcked(v === true)}
                  className="mt-0.5"
                />
                <span className="text-sm text-warm-text">{t('enrollGate.backupAck')}</span>
              </label>
            </div>
          )}
        </ScrollArea>

        <div className="flex justify-end gap-2 border-t border-warm-border px-6 py-4">
          {step === 'scan' ? (
            <>
              <Button variant="outline" onClick={onSignOut} disabled={submitting}>
                {t('enrollGate.signOut')}
              </Button>
              <Button onClick={onEnable} disabled={submitting || code.length !== 6 || !setup}>
                {submitting ? t('enrollGate.verifying') : t('enrollGate.enable')}
              </Button>
            </>
          ) : (
            <Button onClick={onFinish} disabled={!acked}>
              {t('enrollGate.saved')}
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
