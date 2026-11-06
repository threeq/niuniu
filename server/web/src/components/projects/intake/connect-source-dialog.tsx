import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Check, KeyRound, Link2 } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { integrationApi } from '@/lib/integration-api'
import {
  intakeApi,
  type IntakeBindingKind,
  type IntakeSource,
  type IntakeSourceInput,
  type IntakeTestResult,
} from '@/lib/intake-api'
import { cn } from '@/lib/utils'
import { ChipsEditor } from './chips-editor'

// Bind-time choices the backend pins at create time (design §11.2): the provider
// decides the CI auth preset, the email transport decides whether the binding is
// a poller or an inbound-parse endpoint.
const CI_PROVIDERS = ['generic', 'gitlab', 'jenkins', 'buildkite', 'circleci'] as const
const CI_EVENTS = ['ci_failed', 'ci_succeeded', 'ci_canceled', 'ci_recovered'] as const
const EMAIL_PROVIDERS = ['generic', 'mailgun', 'sendgrid', 'postmark'] as const

// The inbound webhook body cap (service.maxWebhookBodyBytes).
const MAX_BODY_LIMIT_KB = 25600

// Matches service.validWebhookBindingName: the name is also the source_ref used
// for dedupe, so it stays ASCII-safe.
const NAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/

type EmailMode = 'imap' | 'inbound'

interface Props {
  projectId: number
  /** null closes the dialog. The kind is chosen by the card that opened it. */
  sourceKind: IntakeBindingKind | null
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Fired after a binding is created so the panel can refetch the source list. */
  onConnected: () => void
}

interface ChoiceGroupProps {
  value: string
  options: readonly string[]
  label: (value: string) => string
  ariaLabel: string
  onChange: (value: string) => void
}

function ChoiceGroup({ value, options, label, ariaLabel, onChange }: ChoiceGroupProps) {
  return (
    <div
      className="inline-flex flex-wrap overflow-hidden rounded-md border border-warm-border"
      role="group"
      aria-label={ariaLabel}
    >
      {options.map((option) => (
        <Button
          key={option}
          type="button"
          variant="ghost"
          size="sm"
          aria-pressed={value === option}
          onClick={() => onChange(option)}
          className={cn(
            'rounded-none border-r border-warm-border px-3 last:border-r-0',
            value === option
              ? 'bg-brand-soft text-brand hover:bg-brand-soft'
              : 'bg-warm-muted text-warm-text-muted',
          )}
        >
          {label(option)}
        </Button>
      ))}
    </div>
  )
}

export function ConnectSourceDialog({ projectId, sourceKind, open, onOpenChange, onConnected }: Props) {
  const { t } = useTranslation('intake')

  const [step, setStep] = useState<'form' | 'created'>('form')
  const [name, setName] = useState('')
  const [ciProvider, setCiProvider] = useState<string>('generic')
  const [ciEvents, setCiEvents] = useState<string[]>(['ci_failed'])
  const [emailMode, setEmailMode] = useState<EmailMode>('imap')
  const [emailProvider, setEmailProvider] = useState<string>('generic')
  const [credentialId, setCredentialId] = useState<string>('')
  const [mailbox, setMailbox] = useState('INBOX')
  const [pollInterval, setPollInterval] = useState('60')
  const [webhookEvents, setWebhookEvents] = useState<string[]>([])
  const [bodyLimitKb, setBodyLimitKb] = useState('1024')
  const [created, setCreated] = useState<IntakeSource | null>(null)
  const [secretLink, setSecretLink] = useState<string | null>(null)
  const [testResult, setTestResult] = useState<IntakeTestResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<'create' | 'test' | null>(null)
  const [copied, setCopied] = useState<'url' | null>(null)

  // A reopened dialog must not show the previous binding's secret state.
  useEffect(() => {
    if (!open) return
    setStep('form')
    setName('')
    setCiProvider('generic')
    setCiEvents(['ci_failed'])
    setEmailMode('imap')
    setEmailProvider('generic')
    setCredentialId('')
    setMailbox('INBOX')
    setPollInterval('60')
    setWebhookEvents([])
    setBodyLimitKb('1024')
    setCreated(null)
    setSecretLink(null)
    setTestResult(null)
    setError(null)
    setPending(null)
    setCopied(null)
  }, [open, sourceKind])

  const needsCredential = sourceKind === 'email' && emailMode === 'imap'
  const credentialsQuery = useQuery({
    queryKey: ['external-credentials', 'imap'],
    queryFn: () => integrationApi.listCredentials('imap'),
    enabled: open && needsCredential,
    retry: false,
  })
  const credentials = useMemo(() => credentialsQuery.data ?? [], [credentialsQuery.data])

  const nameValid = NAME_PATTERN.test(name.trim())
  const intervalValid = /^\d+$/.test(pollInterval.trim()) && Number(pollInterval) >= 1
  const bodyLimitValid =
    /^\d+$/.test(bodyLimitKb.trim()) &&
    Number(bodyLimitKb) >= 1 &&
    Number(bodyLimitKb) <= MAX_BODY_LIMIT_KB
  const credentialValid = !needsCredential || credentialId !== ''
  const formValid =
    nameValid &&
    credentialValid &&
    (!needsCredential || intervalValid) &&
    (sourceKind !== 'webhook' || bodyLimitValid)

  const buildInput = (kind: IntakeBindingKind): IntakeSourceInput => {
    const input: IntakeSourceInput = {
      source_kind: kind,
      name: name.trim(),
      mode: kind === 'email' ? emailMode : 'push',
    }
    if (kind === 'ci') {
      input.provider = ciProvider
      input.config = { events: ciEvents }
    } else if (kind === 'email' && emailMode === 'imap') {
      input.credential_id = Number(credentialId)
      input.config = {
        mailbox: mailbox.trim() || 'INBOX',
        poll_interval_sec: Number(pollInterval),
      }
    } else if (kind === 'email') {
      input.provider = emailProvider
    } else {
      input.config = {
        events: webhookEvents,
        max_body_bytes: Number(bodyLimitKb) * 1024,
      }
    }
    return input
  }

  const create = async () => {
    if (!sourceKind || !formValid || pending) return
    setPending('create')
    setError(null)
    try {
      const res = await intakeApi.createSource(projectId, buildInput(sourceKind))
      setCreated(res.source)
      setSecretLink(res.secret_link ?? null)
      setStep('created')
      onConnected()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  const runTest = async () => {
    if (!created || pending) return
    setPending('test')
    setError(null)
    try {
      setTestResult(await intakeApi.testSource(projectId, created.id))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(null)
    }
  }

  const inboundUrl = created ? `${window.location.origin}${created.inbound_path}` : ''

  const copy = (value: string) => {
    void navigator.clipboard?.writeText(value).then(() => {
      setCopied('url')
      window.setTimeout(() => setCopied(null), 2000)
    })
  }

  const sourceLabel = sourceKind ? t(`intake.source.${sourceKind}`) : ''

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {step === 'form'
              ? t('intake.connectDialog.title', { source: sourceLabel })
              : t('intake.connectDialog.createdTitle', { name: created?.name ?? name })}
          </DialogTitle>
          <DialogDescription>
            {step === 'form'
              ? t('intake.connectDialog.description')
              : t('intake.connectDialog.createdDescription')}
          </DialogDescription>
        </DialogHeader>

        {step === 'form' ? (
          <div className="space-y-5">
            <div className="space-y-1.5">
              <Label htmlFor="intake-connect-name">{t('intake.connectDialog.name')}</Label>
              <Input
                id="intake-connect-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('intake.connectDialog.namePlaceholder')}
                aria-invalid={name !== '' && !nameValid ? true : undefined}
              />
              {name !== '' && !nameValid ? (
                <p className="text-xs text-destructive-text">{t('intake.connectDialog.nameInvalid')}</p>
              ) : (
                <p className="text-xs text-muted-foreground">{t('intake.connectDialog.nameHint')}</p>
              )}
            </div>

            {sourceKind === 'ci' && (
              <div className="space-y-4">
                <div className="space-y-2">
                  <Label>{t('intake.connectDialog.ciProvider')}</Label>
                  <ChoiceGroup
                    value={ciProvider}
                    options={CI_PROVIDERS}
                    label={(v) => t(`intake.connectDialog.ciProviderName.${v}`)}
                    ariaLabel={t('intake.connectDialog.ciProvider')}
                    onChange={setCiProvider}
                  />
                </div>
                <div className="space-y-2">
                  <Label>{t('intake.connectDialog.ciEvents')}</Label>
                  <div className="flex flex-wrap gap-x-4 gap-y-2">
                    {CI_EVENTS.map((event) => (
                      <label key={event} className="flex select-none items-center gap-2">
                        <Switch
                          checked={ciEvents.includes(event)}
                          // An empty list means "every event" on the inbound path,
                          // so the last selection stays on instead of silently
                          // subscribing to everything.
                          disabled={ciEvents.length === 1 && ciEvents.includes(event)}
                          onCheckedChange={(next) =>
                            setCiEvents((prev) =>
                              next ? [...prev, event] : prev.filter((e) => e !== event),
                            )
                          }
                          aria-label={t(`intake.connectDialog.ciEvent.${event}`)}
                        />
                        <span className="text-sm">{t(`intake.connectDialog.ciEvent.${event}`)}</span>
                      </label>
                    ))}
                  </div>
                  <p className="text-xs text-muted-foreground">{t('intake.connectDialog.ciEventsHint')}</p>
                </div>
                <p className="text-xs text-muted-foreground">{t('intake.connectDialog.ciReachabilityHint')}</p>
              </div>
            )}

            {sourceKind === 'email' && (
              <div className="space-y-4">
                <div className="space-y-2">
                  <Label>{t('intake.connectDialog.transport')}</Label>
                  <ChoiceGroup
                    value={emailMode}
                    options={['imap', 'inbound'] as const}
                    label={(v) => t(`intake.connectDialog.emailMode.${v}`)}
                    ariaLabel={t('intake.connectDialog.transport')}
                    onChange={(v) => setEmailMode(v as EmailMode)}
                  />
                  <p className="text-xs text-muted-foreground">
                    {t(`intake.connectDialog.emailModeHint.${emailMode}`)}
                  </p>
                </div>

                {emailMode === 'imap' ? (
                  <>
                    <div className="space-y-2">
                      <Label htmlFor="intake-connect-credential">
                        {t('intake.connectDialog.imapCredential')}
                      </Label>
                      {credentialsQuery.isLoading ? (
                        <p className="text-xs text-muted-foreground">{t('intake.loading')}</p>
                      ) : credentials.length === 0 ? (
                        <p className="text-xs text-muted-foreground">
                          {t('intake.connectDialog.imapNoCredentials')}
                        </p>
                      ) : (
                        <>
                          <Select value={credentialId} onValueChange={setCredentialId}>
                            <SelectTrigger id="intake-connect-credential">
                              <SelectValue
                                placeholder={t('intake.connectDialog.imapCredentialPlaceholder')}
                              />
                            </SelectTrigger>
                            <SelectContent>
                              {credentials.map((cred) => (
                                <SelectItem key={cred.id} value={String(cred.id)}>
                                  {cred.alias}
                                </SelectItem>
                              ))}
                            </SelectContent>
                          </Select>
                          <p className="text-xs text-muted-foreground">
                            {t('intake.connectDialog.imapCredentialHint')}
                          </p>
                        </>
                      )}
                    </div>

                    <div className="grid gap-4 sm:grid-cols-2">
                      <div className="space-y-1.5">
                        <Label htmlFor="intake-connect-mailbox">{t('intake.connectDialog.mailbox')}</Label>
                        <Input
                          id="intake-connect-mailbox"
                          value={mailbox}
                          onChange={(e) => setMailbox(e.target.value)}
                          placeholder="INBOX"
                        />
                      </div>
                      <div className="space-y-1.5">
                        <Label htmlFor="intake-connect-interval">
                          {t('intake.connectDialog.pollInterval')}
                        </Label>
                        <Input
                          id="intake-connect-interval"
                          inputMode="numeric"
                          value={pollInterval}
                          onChange={(e) => setPollInterval(e.target.value)}
                          className="tabular-nums"
                          aria-invalid={!intervalValid || undefined}
                        />
                        {!intervalValid ? (
                          <p className="text-xs text-destructive-text">
                            {t('intake.connectDialog.pollIntervalInvalid')}
                          </p>
                        ) : null}
                      </div>
                    </div>
                  </>
                ) : (
                  <div className="space-y-2">
                    <Label>{t('intake.connectDialog.emailProvider')}</Label>
                    <ChoiceGroup
                      value={emailProvider}
                      options={EMAIL_PROVIDERS}
                      label={(v) => t(`intake.connectDialog.emailProviderName.${v}`)}
                      ariaLabel={t('intake.connectDialog.emailProvider')}
                      onChange={setEmailProvider}
                    />
                  </div>
                )}
              </div>
            )}

            {sourceKind === 'webhook' && (
              <div className="space-y-4">
                <ChipsEditor
                  label={t('intake.connectDialog.webhookEvents')}
                  hint={t('intake.connectDialog.webhookEventsHint')}
                  values={webhookEvents}
                  onChange={setWebhookEvents}
                  placeholder={t('intake.connectDialog.webhookEventsPlaceholder')}
                  addLabel={t('intake.rules.add')}
                  emptyLabel={t('intake.connectDialog.webhookEventsEmpty')}
                  removeLabel={(v) => t('intake.connectDialog.removeEvent', { event: v })}
                />

                <div className="space-y-1.5">
                  <Label htmlFor="intake-connect-body-limit">{t('intake.connectDialog.bodyLimit')}</Label>
                  <Input
                    id="intake-connect-body-limit"
                    inputMode="numeric"
                    value={bodyLimitKb}
                    onChange={(e) => setBodyLimitKb(e.target.value)}
                    className="tabular-nums"
                    aria-invalid={!bodyLimitValid || undefined}
                  />
                  {!bodyLimitValid ? (
                    <p className="text-xs text-destructive-text">
                      {t('intake.connectDialog.bodyLimitInvalid')}
                    </p>
                  ) : (
                    <p className="text-xs text-muted-foreground">{t('intake.connectDialog.bodyLimitHint')}</p>
                  )}
                </div>

                <div className="rounded-md border border-warm-border bg-warm-muted p-3">
                  <p className="text-sm font-medium">{t('intake.connectDialog.replayWindow')}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t('intake.connectDialog.replayWindowHint')}
                  </p>
                </div>
              </div>
            )}

            {error ? <p className="text-xs text-destructive-text">{error}</p> : null}
          </div>
        ) : (
          <div className="space-y-5">
            <div className="space-y-1">
              <Label htmlFor="intake-connect-inbound-url">{t('intake.connectDialog.inboundUrl')}</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="intake-connect-inbound-url"
                  readOnly
                  value={inboundUrl}
                  className="font-mono text-xs"
                  onFocus={(e) => e.currentTarget.select()}
                />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  aria-label={t('intake.connectDialog.copyInboundUrl')}
                  onClick={() => copy(inboundUrl)}
                >
                  {copied === 'url' ? <Check className="h-4 w-4" /> : <Link2 className="h-4 w-4" />}
                </Button>
              </div>
            </div>

            {secretLink ? (
              <div className="space-y-2 rounded-md border border-warm-border p-3">
                <div className="flex items-center gap-2">
                  <KeyRound className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
                  <p className="text-sm font-medium">{t('intake.connectDialog.secretTitle')}</p>
                </div>
                <p className="text-xs text-muted-foreground">{t('intake.connectDialog.secretOnceHint')}</p>
                <Button type="button" variant="outline" size="sm" asChild>
                  <a href={secretLink} target="_blank" rel="noopener noreferrer">
                    {t('intake.connectDialog.openSecret')}
                  </a>
                </Button>
              </div>
            ) : (
              <p className="text-xs text-muted-foreground">{t('intake.connectDialog.imapNoSecret')}</p>
            )}

            <div className="space-y-2">
              <div className="flex flex-wrap items-center gap-2">
                <Button type="button" variant="outline" size="sm" onClick={runTest} disabled={pending !== null}>
                  {pending === 'test' ? t('intake.connectDialog.testing') : t('intake.connectDialog.test')}
                </Button>
                <span className="text-xs text-muted-foreground">{t('intake.connectDialog.testNotRecorded')}</span>
              </div>

              {testResult ? (
                <div className="space-y-2 rounded-md border border-warm-border p-3">
                  <div className="flex items-center gap-2">
                    <p className="text-sm font-medium">{t('intake.connectDialog.testResultTitle')}</p>
                    <Badge
                      variant="outline"
                      className={cn(
                        'rounded px-1.5 py-0.5 text-xs',
                        testResult.allowed
                          ? 'border-success/30 bg-success/15 text-success'
                          : 'border-warm-border bg-warm-muted text-warm-text-muted',
                      )}
                    >
                      {testResult.allowed
                        ? t('intake.connectDialog.testAllowed')
                        : t('intake.connectDialog.testDenied')}
                    </Badge>
                  </div>
                  <dl className="grid gap-x-4 gap-y-1 text-xs sm:grid-cols-2">
                    <ResultRow label={t('intake.connectDialog.testField.eventKind')} value={testResult.event_kind} />
                    <ResultRow label={t('intake.connectDialog.testField.mode')} value={testResult.mode} />
                    <ResultRow
                      label={t('intake.connectDialog.testField.strength')}
                      value={t(`review.strength.${testResult.strength}`, {
                        defaultValue: testResult.strength,
                      })}
                    />
                    <ResultRow
                      label={t('intake.connectDialog.testField.reasonCode')}
                      value={testResult.reason_code}
                    />
                    {testResult.actor_ext_id ? (
                      <ResultRow
                        label={t('intake.connectDialog.testField.actor')}
                        value={testResult.actor_ext_id}
                      />
                    ) : null}
                    {testResult.thread_ext_id ? (
                      <ResultRow
                        label={t('intake.connectDialog.testField.thread')}
                        value={testResult.thread_ext_id}
                      />
                    ) : null}
                  </dl>
                  {testResult.text ? (
                    <div className="space-y-1">
                      <p className="text-xs font-medium text-muted-foreground">
                        {t('intake.connectDialog.testField.text')}
                      </p>
                      <p className="rounded bg-warm-muted p-2 text-xs">{testResult.text}</p>
                    </div>
                  ) : null}
                  {testResult.warnings?.length ? (
                    <ul className="space-y-1 text-xs text-warm-text-muted">
                      {testResult.warnings.map((w) => (
                        <li key={w}>{w}</li>
                      ))}
                    </ul>
                  ) : null}
                </div>
              ) : null}

              {error ? <p className="text-xs text-destructive-text">{error}</p> : null}
            </div>
          </div>
        )}

        <DialogFooter>
          {step === 'form' ? (
            <>
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                {t('intake.connectDialog.cancel')}
              </Button>
              <Button type="button" onClick={create} disabled={!formValid || pending !== null}>
                {pending === 'create' ? t('intake.connectDialog.creating') : t('intake.connectDialog.create')}
              </Button>
            </>
          ) : (
            <Button type="button" onClick={() => onOpenChange(false)}>
              {t('intake.connectDialog.done')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ResultRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words font-mono">{value}</dd>
    </div>
  )
}
