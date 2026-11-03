import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Pencil, Plus, Trash2 } from 'lucide-react'
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
import { Switch } from '@/components/ui/switch'
import { EmptyState } from '@/components/shared/empty-state'
import { confirm } from '@/lib/confirm'
import {
  intakeApi,
  type GithubRepoConfig,
  type IntakeRule,
  type IntakeSetting,
  type IntakeSourceKind,
} from '@/lib/intake-api'
import { cn } from '@/lib/utils'
import { ChipsEditor } from './chips-editor'
import { dailyCapValue, type DraftSetting, type KnownActor } from './intake-utils'

interface Props {
  projectId: number
  source: IntakeSetting | null
  draft: DraftSetting | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onDraftChange: (patch: Partial<DraftSetting>) => void
  rules: IntakeRule[]
  rulesLoading: boolean
  knownActors: KnownActor[]
  paused: boolean
  pausePending: boolean
  onPausedChange: (paused: boolean) => void
  onEditRule: (rule: IntakeRule) => void
  onCreateRule: () => void
  onOpenHistory: () => void
  isMobile: boolean
}

function conditionSummary(rule: IntakeRule, t: (key: string, opts?: Record<string, unknown>) => string): string {
  const parts: string[] = []
  const actors = rule.conditions?.actors ?? []
  const keywords = rule.conditions?.keywords ?? []
  if (actors.length > 0) parts.push(t('intake.rules.conditionActors', { actors: actors.join(', ') }))
  if (keywords.length > 0) parts.push(t('intake.rules.conditionKeywords', { keywords: keywords.join(', ') }))
  return parts.join(' · ')
}

export function SourceConfigDialog({
  projectId,
  source,
  draft,
  open,
  onOpenChange,
  onDraftChange,
  rules,
  rulesLoading,
  knownActors,
  paused,
  pausePending,
  onPausedChange,
  onEditRule,
  onCreateRule,
  onOpenHistory,
  isMobile,
}: Props) {
  const { t } = useTranslation('intake')
  const qc = useQueryClient()

  const { data: versions = [] } = useQuery({
    queryKey: ['intake-policy-versions', projectId, source?.source_kind, 'latest'],
    queryFn: () => intakeApi.listPolicyVersions(projectId, source!.source_kind as IntakeSourceKind, 1),
    enabled: open && !!source,
    retry: false,
  })
  const latestVersion = versions[0]?.version

  const toggleRule = useMutation({
    mutationFn: (rule: IntakeRule) =>
      intakeApi.updateRule(projectId, rule.id, {
        source_kind: rule.source_kind,
        name: rule.name,
        instruction: rule.instruction,
        conditions: rule.conditions,
        priority: rule.priority,
        enabled: !rule.enabled,
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['intake-rules', projectId] })
      void qc.invalidateQueries({ queryKey: ['intake-policy-versions', projectId] })
    },
    onError: (err: unknown) => {
      toast.error(t('intake.rules.saveFailed', { message: err instanceof Error ? err.message : String(err) }))
    },
  })

  const deleteRule = useMutation({
    mutationFn: (rule: IntakeRule) => intakeApi.deleteRule(projectId, rule.id),
    onSuccess: () => {
      toast.success(t('intake.rules.deleted'))
      void qc.invalidateQueries({ queryKey: ['intake-rules', projectId] })
      void qc.invalidateQueries({ queryKey: ['intake-policy-versions', projectId] })
    },
    onError: (err: unknown) => {
      toast.error(t('intake.rules.saveFailed', { message: err instanceof Error ? err.message : String(err) }))
    },
  })

  const githubQuery = useQuery({
    queryKey: ['intake-github-config', projectId],
    queryFn: () => intakeApi.getGithubConfig(projectId),
    enabled: open && source?.source_kind === 'github',
    retry: false,
  })

  const githubMutation = useMutation({
    mutationFn: (repos: { source_key: string; events: string[] }[]) =>
      intakeApi.updateGithubConfig(projectId, repos),
    onSuccess: (data) => {
      qc.setQueryData(['intake-github-config', projectId], data)
    },
    onError: (err: unknown) => {
      toast.error(
        t('intake.config.githubSaveFailed', {
          message: err instanceof Error ? err.message : String(err),
        }),
      )
    },
  })

  // Saving immediately (like a rule toggle) keeps the repo/event config
  // independent of the settings draft's save button.
  const toggleGithubEvent = (repo: GithubRepoConfig, event: string) => {
    const next = repo.events.includes(event)
      ? repo.events.filter((e) => e !== event)
      : [...repo.events, event]
    // The server rejects an empty selection; keep the last event on.
    if (next.length === 0) return
    githubMutation.mutate([{ source_key: repo.source_key, events: next }])
  }

  if (!source || !draft) return null

  const capValue = dailyCapValue(draft.daily_cap)
  const capError = capValue === null

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('intake.config.title', { source: t(`intake.source.${source.source_kind}`) })}</DialogTitle>
          <DialogDescription>{t('intake.config.description')}</DialogDescription>
        </DialogHeader>

        <div className={cn('space-y-6', isMobile ? '' : 'min-w-0')}>
          {source.source_kind === 'github' && (
            <div className="space-y-3">
              <div>
                <h4 className="text-sm font-medium">{t('intake.config.githubEventsTitle')}</h4>
                <p className="text-xs text-muted-foreground">{t('intake.config.githubEventsHint')}</p>
              </div>
              {githubQuery.isLoading ? (
                <p className="py-2 text-center text-xs text-muted-foreground">{t('intake.loading')}</p>
              ) : (githubQuery.data?.repos.length ?? 0) === 0 ? (
                <p className="text-xs text-muted-foreground">{t('intake.config.githubNoRepos')}</p>
              ) : (
                githubQuery.data!.repos.map((repo) => (
                  <div key={repo.source_key} className="space-y-2 rounded-md border border-warm-border p-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="text-sm font-medium">{repo.source_key}</span>
                      {repo.last_polled_at && (
                        <span className="text-xs text-muted-foreground">
                          {t('intake.config.githubLastPolled', {
                            time: new Date(repo.last_polled_at).toLocaleString(),
                          })}
                        </span>
                      )}
                    </div>
                    <div className="flex flex-wrap gap-x-4 gap-y-2">
                      {(githubQuery.data?.known_events ?? []).map((event) => (
                        <div key={event} className="flex items-center gap-2">
                          <Switch
                            checked={repo.events.includes(event)}
                            onCheckedChange={() => toggleGithubEvent(repo, event)}
                            disabled={
                              githubMutation.isPending &&
                              githubMutation.variables?.[0]?.source_key === repo.source_key
                            }
                            aria-label={t(`intake.githubEvent.${event}`)}
                          />
                          <span className="text-sm">{t(`intake.githubEvent.${event}`)}</span>
                        </div>
                      ))}
                    </div>
                    {repo.last_error && (
                      <p className="text-xs text-destructive">
                        {t('intake.config.githubError', { message: repo.last_error })}
                      </p>
                    )}
                  </div>
                ))
              )}
            </div>
          )}

          <ChipsEditor
            label={t('intake.config.allowList')}
            hint={t('intake.config.allowListHint')}
            values={draft.allow_list}
            onChange={(allow_list) => onDraftChange({ allow_list })}
            placeholder={t('intake.config.allowListPlaceholder')}
            addLabel={t('intake.rules.add')}
            emptyLabel={t('intake.config.allowListEmpty')}
            removeLabel={(v) => t('intake.config.removeMember', { member: v })}
            suggestions={knownActors}
            suggestionsLabel={t('intake.rules.suggestions')}
          />

          <ChipsEditor
            label={t('intake.config.botList')}
            hint={t('intake.config.botListHint')}
            values={draft.bot_list}
            onChange={(bot_list) => onDraftChange({ bot_list })}
            placeholder={t('intake.config.botListPlaceholder')}
            addLabel={t('intake.rules.add')}
            emptyLabel={t('intake.config.botListEmpty')}
            removeLabel={(v) => t('intake.config.removeMember', { member: v })}
            suggestions={knownActors}
            suggestionsLabel={t('intake.rules.suggestions')}
          />

          <div className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <h4 className="text-sm font-medium">{t('intake.config.rulesTitle')}</h4>
              <Button type="button" variant="outline" size="sm" onClick={onCreateRule}>
                <Plus className="mr-1 h-4 w-4" aria-hidden="true" />
                {t('intake.config.addRule')}
              </Button>
            </div>

            {rulesLoading ? (
              <p className="py-3 text-center text-xs text-muted-foreground">{t('intake.loading')}</p>
            ) : rules.length === 0 ? (
              <EmptyState
                size="compact"
                title={t('intake.config.rulesEmptyTitle')}
                description={t('intake.config.rulesEmpty')}
              />
            ) : isMobile ? (
              <div className="space-y-2">
                {rules.map((rule) => (
                  <div key={rule.id} className="space-y-2 rounded-md border border-warm-border p-3">
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <p className="truncate text-sm font-medium">{rule.name}</p>
                        <p className="text-xs text-muted-foreground">{conditionSummary(rule, t)}</p>
                      </div>
                      <Switch
                        checked={rule.enabled}
                        onCheckedChange={() => toggleRule.mutate(rule)}
                        aria-label={t('intake.rules.enabledToggle', { name: rule.name })}
                      />
                    </div>
                    <div className="flex gap-2">
                      <Button type="button" variant="outline" size="sm" onClick={() => onEditRule(rule)}>
                        <Pencil className="mr-1 h-4 w-4" aria-hidden="true" />
                        {t('intake.rules.edit')}
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                        onClick={async () => {
                          if (await confirm({ description: t('intake.rules.deleteConfirm', { name: rule.name }), destructive: true })) {
                            deleteRule.mutate(rule)
                          }
                        }}
                      >
                        <Trash2 className="mr-1 h-4 w-4" aria-hidden="true" />
                        {t('intake.rules.delete')}
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            ) : (
              <div className="overflow-hidden rounded-md border border-warm-border">
                <table className="w-full text-sm">
                  <thead className="bg-warm-muted text-xs font-medium text-warm-text-muted">
                    <tr>
                      <th className="px-3 py-2 text-left">{t('intake.rules.table.name')}</th>
                      <th className="px-3 py-2 text-left">{t('intake.rules.table.conditions')}</th>
                      <th className="px-3 py-2 text-left">{t('intake.rules.table.enabled')}</th>
                      <th className="px-3 py-2 text-right">{t('intake.rules.table.actions')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rules.map((rule) => (
                      <tr key={rule.id} className="h-10 border-t border-warm-border">
                        <td className="max-w-40 truncate px-3 py-2 font-medium">{rule.name}</td>
                        <td className="px-3 py-2 text-xs text-muted-foreground">
                          {conditionSummary(rule, t)}
                        </td>
                        <td className="px-3 py-2">
                          <Switch
                            checked={rule.enabled}
                            onCheckedChange={() => toggleRule.mutate(rule)}
                            aria-label={t('intake.rules.enabledToggle', { name: rule.name })}
                          />
                        </td>
                        <td className="px-3 py-2">
                          <div className="flex justify-end gap-1">
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              onClick={() => onEditRule(rule)}
                              aria-label={t('intake.rules.editNamed', { name: rule.name })}
                            >
                              <Pencil className="h-4 w-4" aria-hidden="true" />
                            </Button>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                              onClick={async () => {
                                if (await confirm({ description: t('intake.rules.deleteConfirm', { name: rule.name }), destructive: true })) {
                                  deleteRule.mutate(rule)
                                }
                              }}
                              aria-label={t('intake.rules.deleteNamed', { name: rule.name })}
                            >
                              <Trash2 className="h-4 w-4" aria-hidden="true" />
                            </Button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="intake-daily-cap">{t('intake.config.dailyCap')}</Label>
              <Input
                id="intake-daily-cap"
                inputMode="numeric"
                value={draft.daily_cap}
                onChange={(e) => onDraftChange({ daily_cap: e.target.value })}
                className="tabular-nums"
                aria-invalid={capError || undefined}
              />
              {capError ? (
                <p className="text-xs text-destructive">{t('intake.config.dailyCapError')}</p>
              ) : (
                <p className="text-xs text-muted-foreground">{t('intake.config.dailyCapHint')}</p>
              )}
            </div>

            <div className="space-y-1.5">
              <Label>{t('intake.config.policyVersion')}</Label>
              <div className="flex items-center justify-between gap-2">
                <span className="text-sm tabular-nums text-muted-foreground">
                  {latestVersion ? t('intake.history.version', { version: latestVersion }) : t('intake.config.noVersion')}
                </span>
                <Button type="button" variant="outline" size="sm" onClick={onOpenHistory}>
                  {t('intake.config.viewHistory')}
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">{t('intake.config.policyVersionHint')}</p>
            </div>
          </div>

          <div className="space-y-2 rounded-md border border-destructive/30 bg-destructive/5 p-3">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                <p className="text-sm font-medium text-destructive">{t('intake.config.pause')}</p>
                <p className="mt-1 text-xs text-muted-foreground">{t('intake.pauseHint')}</p>
              </div>
              <Switch
                checked={paused}
                onCheckedChange={onPausedChange}
                disabled={pausePending}
                aria-label={t('intake.pause')}
              />
            </div>
          </div>
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t('intake.config.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
