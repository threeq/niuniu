import { useCallback, useEffect, useRef, useState } from 'react'
import { UserCircle, LogOut, Loader2, ArrowLeft } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { relayAPI, type RelayStatus } from './relay-api'

const DEFAULT_URL = 'https://niuniu-relay.niu6ai.com'
const RESEND_SECONDS = 60
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

export function RelayAccountPanel({ onStatusChange }: { onStatusChange?: (s: RelayStatus) => void }) {
  const { t } = useTranslation('settings')
  const [status, setStatus] = useState<RelayStatus | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const s = await relayAPI.getStatus()
      setStatus(s)
      onStatusChange?.(s)
    } catch {
      // Non-fatal; keep whatever state we had.
    } finally {
      setLoading(false)
    }
  }, [onStatusChange])

  useEffect(() => {
    refresh()
    // Re-poll every 4 s so the connection dot reflects tunnel transitions.
    const t = window.setInterval(refresh, 4000)
    return () => window.clearInterval(t)
  }, [refresh])

  if (loading || !status) {
    return (
      <div className="flex items-center gap-2 py-4 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        {t('mobileAccess.relayAccount.loadingStatus')}
      </div>
    )
  }

  return status.logged_in ? (
    <LoggedInCard status={status} onLogout={refresh} />
  ) : (
    <AuthForm initialURL={status.url || DEFAULT_URL} onDone={refresh} />
  )
}

function LoggedInCard({ status, onLogout }: { status: RelayStatus; onLogout: () => void }) {
  const { t } = useTranslation('settings')
  const [busy, setBusy] = useState(false)
  const dot = status.connected ? 'bg-success' : 'bg-muted-foreground/40'
  const dotLabel = status.connected ? t('mobileAccess.relayAccount.connected') : t('mobileAccess.relayAccount.disconnected')

  const handleLogout = async () => {
    setBusy(true)
    try {
      await relayAPI.logout()
      onLogout()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex items-center justify-between gap-4 rounded-lg border border-border bg-card px-5 py-4">
      <div className="flex items-center gap-3 min-w-0">
        <UserCircle className="h-9 w-9 text-muted-foreground shrink-0" />
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium text-foreground truncate">{status.email}</span>
            <span className="flex items-center gap-1 text-xs text-muted-foreground">
              <span className={`h-1.5 w-1.5 rounded-full ${dot}`} />
              {dotLabel}
            </span>
          </div>
          <p className="text-xs text-muted-foreground mt-0.5 truncate">{status.url}</p>
        </div>
      </div>
      <Button variant="outline" size="sm" onClick={handleLogout} disabled={busy} className="gap-1.5">
        <LogOut className="h-3.5 w-3.5" />
        {t('mobileAccess.relayAccount.logout')}
      </Button>
    </div>
  )
}

// Two-stage passwordless auth form mirroring the mobile email-code flow.
//
// Stage 'email': user types address + (optional) self-host URL, taps
// 「获取验证码」, we POST /api/relay/email-code/request (which the server
// proxies to the relay's anti-enumeration 200-on-unknown-email endpoint)
// and advance to 'code'.
//
// Stage 'code': 6-digit input, auto-submits on the 6th digit; verify
// failures (`invalid_code`, `expired_code`, `too_many_attempts`) render
// inline as Chinese text translated server-side. A 60s resend countdown
// runs after each request; 「修改邮箱」 returns to the email stage with
// the typed-in address preserved.
function AuthForm({ initialURL, onDone }: { initialURL: string; onDone: () => void }) {
  const { t } = useTranslation('settings')
  const [stage, setStage] = useState<'email' | 'code'>('email')
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [url, setUrl] = useState(initialURL)
  const [showSelfHost, setShowSelfHost] = useState(initialURL !== DEFAULT_URL)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [resendIn, setResendIn] = useState(0)
  const codeInputRef = useRef<HTMLInputElement>(null)

  const emailValid = EMAIL_RE.test(email.trim())
  const effectiveURL = showSelfHost ? (url.trim() || DEFAULT_URL) : DEFAULT_URL

  // Resend countdown — ticks after every successful code request.
  useEffect(() => {
    if (resendIn <= 0) return
    const id = window.setInterval(() => setResendIn(s => Math.max(0, s - 1)), 1000)
    return () => window.clearInterval(id)
  }, [resendIn])

  // Autofocus the code field whenever we enter the code stage.
  useEffect(() => {
    if (stage === 'code') codeInputRef.current?.focus()
  }, [stage])

  const requestCode = async () => {
    if (!emailValid) {
      setError(t('mobileAccess.relayAccount.invalidEmail'))
      return
    }
    setError(null)
    setBusy(true)
    try {
      await relayAPI.requestEmailCode(email.trim(), effectiveURL)
      setStage('code')
      setCode('')
      setResendIn(RESEND_SECONDS)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const verify = async (codeToVerify: string) => {
    setBusy(true)
    setError(null)
    try {
      await relayAPI.verifyEmailCode(email.trim(), codeToVerify, effectiveURL)
      onDone()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setCode('')
      codeInputRef.current?.focus()
    } finally {
      setBusy(false)
    }
  }

  const onCodeChange = (v: string) => {
    const cleaned = v.replace(/\D/g, '').slice(0, 6)
    setCode(cleaned)
    if (cleaned.length === 6 && !busy) {
      void verify(cleaned)
    }
  }

  if (stage === 'code') {
    return (
      <div className="rounded-lg border border-border bg-card p-5 space-y-3">
        <div className="flex items-center justify-between gap-3">
          <button
            type="button"
            onClick={() => {
              setStage('email')
              setError(null)
              setCode('')
            }}
            className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            {t('mobileAccess.relayAccount.changeEmail')}
          </button>
          <span className="text-sm text-muted-foreground truncate">{email.trim()}</span>
        </div>
        <p className="text-sm text-foreground">{t('mobileAccess.relayAccount.codeSent')}</p>
        <Input
          ref={codeInputRef}
          inputMode="numeric"
          pattern="[0-9]*"
          autoComplete="one-time-code"
          maxLength={6}
          placeholder={t('mobileAccess.relayAccount.codePlaceholder')}
          value={code}
          onChange={e => onCodeChange(e.target.value)}
          className="font-mono tracking-[0.4em] text-center text-base"
        />
        {error && (
          <div className="rounded border border-destructive/30 bg-destructive/10 dark:bg-red-950/30 px-3 py-2 text-sm text-destructive dark:text-red-300">{error}</div>
        )}
        <div className="flex items-center justify-between">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            disabled={resendIn > 0 || busy}
            onClick={requestCode}
          >
            {resendIn > 0
              ? t('mobileAccess.relayAccount.resendIn', { seconds: resendIn })
              : t('mobileAccess.relayAccount.resendNow')}
          </Button>
          <Button
            type="button"
            disabled={code.length !== 6 || busy}
            onClick={() => verify(code)}
            className="gap-1.5"
          >
            {busy && <Loader2 className="h-4 w-4 animate-spin" />}
            {t('mobileAccess.relayAccount.submitVerify')}
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="rounded-lg border border-border bg-card p-5 space-y-3">
      <Input
        type="email"
        placeholder={t('mobileAccess.relayAccount.emailPlaceholder')}
        value={email}
        onChange={e => setEmail(e.target.value)}
        autoComplete="email"
        autoFocus
      />
      <button
        type="button"
        onClick={() => setShowSelfHost(v => !v)}
        className="text-xs text-muted-foreground hover:text-foreground"
      >
        {showSelfHost ? t('mobileAccess.relayAccount.useDefaultRelay') : t('mobileAccess.relayAccount.useSelfHosted')}
      </button>
      {showSelfHost && (
        <div className="space-y-1">
          <Input
            type="url"
            placeholder={t('mobileAccess.relayAccount.urlPlaceholder')}
            value={url}
            onChange={e => setUrl(e.target.value)}
          />
          <p className="text-xs text-muted-foreground">{t('mobileAccess.relayAccount.urlHint')}</p>
        </div>
      )}
      {error && (
        <div className="rounded border border-destructive/30 bg-destructive/10 dark:bg-red-950/30 px-3 py-2 text-sm text-destructive dark:text-red-300">{error}</div>
      )}
      <Button
        type="button"
        onClick={requestCode}
        disabled={!emailValid || busy}
        className="w-full gap-1.5"
      >
        {busy && <Loader2 className="h-4 w-4 animate-spin" />}
        {t('mobileAccess.relayAccount.submitRequestCode')}
      </Button>
    </div>
  )
}
