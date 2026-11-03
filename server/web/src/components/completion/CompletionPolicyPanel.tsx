import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Zap } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { EmptyState } from '@/components/shared/empty-state'
import { LoadingSkeleton } from '@/components/shared/loading-skeleton'
import { intakeApi, type CompletionProfile } from '@/lib/intake-api'

const tokenFormat = new Intl.NumberFormat()

function ProfileRow({ profile }: { profile: CompletionProfile }) {
  const { t } = useTranslation('intake')
  const summary = [
    t('completion.rounds', { count: profile.max_rounds }),
    profile.token_budget > 0
      ? t('completion.budget', { amount: tokenFormat.format(profile.token_budget) })
      : t('completion.budgetUnlimited'),
    profile.require_review
      ? t('completion.reviewRequired')
      : t('completion.reviewNotRequired'),
  ].join(' · ')

  return (
    <div className="flex items-center justify-between gap-2 p-2">
      <span className="text-sm font-medium truncate">{profile.name}</span>
      <span className="text-xs text-muted-foreground tabular-nums shrink-0">{summary}</span>
    </div>
  )
}

function SwitchRow({ label, hint, checked }: { label: string; hint?: string; checked: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <div className="text-sm font-medium">{label}</div>
        {hint ? <div className="text-xs text-muted-foreground">{hint}</div> : null}
      </div>
      <Switch checked={checked} disabled aria-label={label} />
    </div>
  )
}

interface Props {
  projectId: number
}

export function CompletionPolicyPanel({ projectId }: Props) {
  const { t } = useTranslation('intake')
  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ['completion-settings', projectId],
    queryFn: () => intakeApi.getCompletionSettings(projectId),
    retry: false,
  })

  // The endpoint returns an empty object until a policy exists.
  const settings = data && typeof data.max_rounds === 'number' ? data : null
  const profiles = settings?.profiles ?? []

  return (
    <div className="border rounded-lg p-4 space-y-4">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <Zap className="w-4 h-4 text-muted-foreground" aria-hidden="true" />
          <h3 className="text-sm font-semibold">{t('completion.title')}</h3>
          <Badge variant="outline" className="text-xs font-normal text-muted-foreground">
            {t('completion.comingSoon')}
          </Badge>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">{t('completion.description')}</p>
      </div>

      {isLoading ? (
        <LoadingSkeleton variant="card" count={2} />
      ) : isError ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" aria-hidden="true" />
          <AlertTitle>{t('completion.loadError')}</AlertTitle>
          <AlertDescription>
            <Button variant="outline" size="sm" onClick={() => refetch()}>
              {t('completion.retry')}
            </Button>
          </AlertDescription>
        </Alert>
      ) : !settings ? (
        <EmptyState
          size="compact"
          icon={<Zap className="h-8 w-8 text-muted-foreground" aria-hidden="true" />}
          title={t('completion.emptyTitle')}
          description={t('completion.emptyDescription')}
        />
      ) : (
        <>
          <SwitchRow
            label={t('completion.autohost')}
            hint={t('completion.autohostHint')}
            checked={settings.autohost_enabled}
          />

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <label className="text-sm font-medium">{t('completion.maxRounds')}</label>
              <Input
                value={String(settings.max_rounds)}
                readOnly
                disabled
                aria-label={t('completion.maxRounds')}
                className="w-24 tabular-nums"
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium">{t('completion.tokenBudget')}</label>
              <Input
                value={tokenFormat.format(settings.token_budget)}
                readOnly
                disabled
                aria-label={t('completion.tokenBudget')}
                className="w-32 tabular-nums"
              />
              <p className="text-xs text-muted-foreground">{t('completion.unlimited')}</p>
            </div>
          </div>

          <div className="space-y-3">
            <SwitchRow
              label={t('completion.stopOnDecision')}
              checked={settings.stop_on_decision}
            />
            <SwitchRow
              label={t('completion.requireReview')}
              checked={settings.require_review}
            />
          </div>

          <div className="space-y-2">
            <h4 className="text-sm font-medium">{t('completion.profiles')}</h4>
            {profiles.length === 0 ? (
              <p className="text-xs text-muted-foreground">{t('completion.noProfiles')}</p>
            ) : (
              <div className="border rounded-md divide-y">
                {profiles.map((profile) => (
                  <ProfileRow key={profile.id} profile={profile} />
                ))}
              </div>
            )}
          </div>
        </>
      )}
    </div>
  )
}
