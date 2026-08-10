import { useEffect, useRef, useState, useCallback } from 'react'
import { CheckCircle, AlertCircle, Loader2, Copy, Check } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { relayAPI, type PairingState } from './relay-api'

interface PairDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onPaired: () => void
}

function extractRelayHost(url?: string): string {
  if (!url) return ''
  try {
    return new URL(url).host
  } catch {
    return url
  }
}

export function PairDialog({ open, onOpenChange, onPaired }: PairDialogProps) {
  const { t } = useTranslation('settings')
  const [state, setState] = useState<PairingState>({ phase: 'idle' })
  const [copied, setCopied] = useState(false)
  const [verifySent, setVerifySent] = useState<'idle' | 'sending' | 'sent' | 'error'>('idle')
  const [verifyError, setVerifyError] = useState<string>('')
  const pollingRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const autoCloseRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const startedRef = useRef(false)
  // Incremented on each send-verification-email attempt. Late responses from
  // an aborted/retried attempt check their captured id against the current ref
  // value and drop their result if they no longer match.
  const verifyReqRef = useRef(0)
  // Tracks dialog mount lifetime so in-flight fetches don't setState after
  // unmount. React 18+ no longer warns, but an unmount-aware guard is still
  // cleaner than relying on tolerance.
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  const stopPolling = useCallback(() => {
    if (pollingRef.current) {
      clearInterval(pollingRef.current)
      pollingRef.current = null
    }
  }, [])

  const stopAutoClose = useCallback(() => {
    if (autoCloseRef.current) {
      clearTimeout(autoCloseRef.current)
      autoCloseRef.current = null
    }
  }, [])

  const handleClose = useCallback(
    (open: boolean) => {
      if (!open) {
        stopPolling()
        stopAutoClose()
        // Reject if still in progress
        if (state.phase !== 'confirmed' && state.phase !== 'idle' && state.phase !== 'failed') {
          relayAPI.rejectPairing().catch(() => {/* ignore */})
        }
        setState({ phase: 'idle' })
        setVerifySent('idle')
        setVerifyError('')
        startedRef.current = false
      }
      onOpenChange(open)
    },
    [onOpenChange, state.phase, stopPolling, stopAutoClose]
  )

  const startPairing = useCallback(async () => {
    setState({ phase: 'starting' })
    try {
      const initial = await relayAPI.startPairing()
      setState(initial)
    } catch (e) {
      setState({ phase: 'failed', error: e instanceof Error ? e.message : String(e) })
      return
    }

    pollingRef.current = setInterval(async () => {
      try {
        const s = await relayAPI.pairingStatus()
        setState(s)
        if (s.phase === 'confirmed') {
          stopPolling()
          onPaired()
          autoCloseRef.current = setTimeout(() => handleClose(false), 2000)
        } else if (s.phase === 'failed') {
          stopPolling()
        }
      } catch {
        // keep polling on transient errors
      }
    }, 1000)
  }, [stopPolling, handleClose, onPaired])

  // Start pairing when dialog opens (deferred to avoid synchronous setState in effect)
  useEffect(() => {
    if (open && !startedRef.current) {
      startedRef.current = true
      const t = setTimeout(() => startPairing(), 0)
      return () => clearTimeout(t)
    }
    if (!open) {
      startedRef.current = false
    }
  }, [open, startPairing])

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      stopPolling()
      stopAutoClose()
    }
  }, [stopPolling, stopAutoClose])

  const handleConfirm = async () => {
    try {
      await relayAPI.confirmPairing()
      setState((s) => ({ ...s, phase: 'confirmed' }))
      stopPolling()
      onPaired()
      autoCloseRef.current = setTimeout(() => handleClose(false), 2000)
    } catch (e) {
      setState({ phase: 'failed', error: e instanceof Error ? e.message : String(e) })
    }
  }

  const handleReject = async () => {
    stopPolling()
    try {
      await relayAPI.rejectPairing()
    } catch {/* ignore */}
    handleClose(false)
  }

  const handleRetry = () => {
    stopPolling()
    startedRef.current = false
    // Invalidate any in-flight verification request so its late response can't
    // clobber the fresh pairing state we're about to enter.
    verifyReqRef.current++
    setVerifySent('idle')
    setVerifyError('')
    startPairing()
    startedRef.current = true
  }

  const handleSendVerification = async () => {
    const reqID = ++verifyReqRef.current
    setVerifySent('sending')
    setVerifyError('')
    try {
      await relayAPI.requestEmailVerification()
      if (!mountedRef.current || verifyReqRef.current !== reqID) return
      setVerifySent('sent')
    } catch (e) {
      if (!mountedRef.current || verifyReqRef.current !== reqID) return
      setVerifySent('error')
      setVerifyError(e instanceof Error ? e.message : String(e))
    }
  }

  const handleCopyUrl = () => {
    if (state.qr_url) {
      navigator.clipboard.writeText(state.qr_url).then(() => {
        setCopied(true)
        setTimeout(() => setCopied(false), 2000)
      })
    }
  }

  const relayHost = extractRelayHost(state.qr_url)

  const renderVerifyEmailCta = () =>
    verifySent === 'sent' ? (
      <div className="flex items-center gap-1.5 text-sm text-success">
        <Check className="h-4 w-4" />
        {t('mobileAccess.pair.dialog.verifyEmailSent')}
      </div>
    ) : (
      <div className="flex flex-col items-center gap-2 w-full">
        <div className="flex gap-2">
          <Button size="sm" onClick={handleSendVerification} disabled={verifySent === 'sending'}>
            {verifySent === 'sending' ? (
              <>
                <Loader2 className="h-3.5 w-3.5 animate-spin mr-1.5" />
                {t('mobileAccess.pair.dialog.sending')}
              </>
            ) : (
              t('mobileAccess.pair.dialog.sendVerifyEmail')
            )}
          </Button>
          <Button size="sm" variant="outline" onClick={handleRetry}>
            {t('mobileAccess.pair.dialog.retryPair')}
          </Button>
        </div>
        {verifySent === 'error' && verifyError && (
          <p className="text-xs text-destructive break-words">{verifyError}</p>
        )}
      </div>
    )

  const renderFailedContent = () => {
    if (state.error_code === 'email_verification_required') {
      return (
        <div className="flex flex-col items-center gap-3 w-full">
          <div className="flex items-start gap-2 w-full rounded-lg border border-amber-500/30 bg-amber-50 dark:bg-amber-950/30 px-4 py-3">
            <AlertCircle className="h-4 w-4 text-amber-600 dark:text-amber-400 mt-0.5 shrink-0" />
            <div className="text-sm text-amber-900 dark:text-amber-200 break-words space-y-1">
              <p className="font-medium">{t('mobileAccess.pair.dialog.emailVerificationRequiredTitle')}</p>
              <p className="text-xs opacity-90">
                {t('mobileAccess.pair.dialog.emailVerificationRequiredDescription')}
              </p>
            </div>
          </div>
          {renderVerifyEmailCta()}
        </div>
      )
    }
    if (state.error_code === 'quota_exceeded') {
      return (
        <div className="flex flex-col items-center gap-3 w-full">
          <div className="flex items-start gap-2 w-full rounded-lg border border-amber-500/30 bg-amber-50 dark:bg-amber-950/30 px-4 py-3">
            <AlertCircle className="h-4 w-4 text-amber-600 dark:text-amber-400 mt-0.5 shrink-0" />
            <div className="text-sm text-amber-900 dark:text-amber-200 break-words space-y-1">
              <p className="font-medium">{t('mobileAccess.pair.dialog.quotaExceededTitle')}</p>
              <p className="text-xs opacity-90">
                {state.error ?? t('mobileAccess.pair.dialog.quotaExceededDefault')}
              </p>
              <p className="text-xs opacity-90">
                {t('mobileAccess.pair.dialog.quotaExceededHint')}
              </p>
            </div>
          </div>
          <Button size="sm" variant="outline" onClick={handleRetry}>
            {t('mobileAccess.pair.dialog.retry')}
          </Button>
        </div>
      )
    }
    return (
      <div className="flex flex-col items-center gap-3 w-full">
        <div className="flex items-start gap-2 w-full rounded-lg border border-destructive/30 bg-destructive/10 dark:bg-red-950/30 px-4 py-3">
          <AlertCircle className="h-4 w-4 text-destructive mt-0.5 shrink-0" />
          <p className="text-sm text-destructive dark:text-red-300 break-words">
            {state.error ?? t('mobileAccess.pair.dialog.pairFailedDefault')}
          </p>
        </div>
        <Button size="sm" variant="outline" onClick={handleRetry}>
          {t('mobileAccess.pair.dialog.retry')}
        </Button>
      </div>
    )
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('mobileAccess.pair.dialog.title')}</DialogTitle>
          <DialogDescription>
            {t('mobileAccess.pair.dialog.description')}
          </DialogDescription>
        </DialogHeader>

        <div className="py-4 flex flex-col items-center gap-4 min-h-[240px] justify-center">
          {/* starting */}
          {(state.phase === 'idle' || state.phase === 'starting') && (
            <div className="flex flex-col items-center gap-3 text-muted-foreground">
              <Loader2 className="h-10 w-10 animate-spin text-info" />
              <p className="text-sm">{t('mobileAccess.pair.dialog.generating')}</p>
            </div>
          )}

          {/* waiting_claim — show QR */}
          {state.phase === 'waiting_claim' && (
            <div className="flex flex-col items-center gap-4 w-full">
              {state.qr_png ? (
                <img
                  src={state.qr_png}
                  alt={t('mobileAccess.pair.dialog.qrAlt')}
                  className="w-48 h-48 border border-border rounded-lg"
                />
              ) : state.qr_url ? (
                <div className="p-4 border border-dashed border-border rounded-lg bg-muted text-center text-xs text-foreground break-all select-all">
                  {state.qr_url}
                </div>
              ) : null}

              {state.qr_url && (
                <div className="flex flex-col items-center gap-1 w-full">
                  {relayHost && (
                    <p className="text-xs text-muted-foreground">
                      {t('mobileAccess.pair.dialog.relayHost')}<span className="font-mono font-semibold text-foreground">{relayHost}</span>
                    </p>
                  )}
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={handleCopyUrl}
                    className="gap-1.5 text-xs"
                  >
                    {copied ? (
                      <Check className="h-3.5 w-3.5 text-success" />
                    ) : (
                      <Copy className="h-3.5 w-3.5" />
                    )}
                    {copied ? t('mobileAccess.pair.dialog.copied') : t('mobileAccess.pair.dialog.copyUrl')}
                  </Button>
                </div>
              )}

              <p className="text-xs text-muted-foreground text-center">
                {t('mobileAccess.pair.dialog.waitingScan')}
              </p>
            </div>
          )}

          {/* ready_to_confirm — show SAS */}
          {state.phase === 'ready_to_confirm' && (
            <div className="flex flex-col items-center gap-4 w-full">
              <p className="text-sm text-foreground">
                {t('mobileAccess.pair.dialog.verifyHeader')}
              </p>
              <div className="text-6xl font-mono font-bold tracking-widest text-info select-all">
                {state.sas}
              </div>
              {state.mobile_name && (
                <p className="text-sm text-muted-foreground">
                  {t('mobileAccess.pair.dialog.deviceLabel')}<span className="font-medium text-foreground">{state.mobile_name}</span>
                </p>
              )}
              <div className="flex gap-3 mt-2">
                <Button variant="outline" onClick={handleReject}>
                  {t('mobileAccess.pair.dialog.cancel')}
                </Button>
                <Button onClick={handleConfirm}>
                  {t('mobileAccess.pair.dialog.confirmPair')}
                </Button>
              </div>
            </div>
          )}

          {/* confirmed */}
          {state.phase === 'confirmed' && (
            <div className="flex flex-col items-center gap-3 text-success">
              <CheckCircle className="h-14 w-14" />
              <p className="text-base font-semibold">{t('mobileAccess.pair.dialog.completed')}</p>
              <p className="text-xs text-muted-foreground">{t('mobileAccess.pair.dialog.autoClose')}</p>
            </div>
          )}

          {/* failed */}
          {state.phase === 'failed' && renderFailedContent()}
        </div>
      </DialogContent>
    </Dialog>
  )
}
