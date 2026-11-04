import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Zap } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { intakeApi } from '@/lib/intake-api'
import {
  completionProfileLabel,
  completionSummary,
  resolveCompletionPolicy,
} from '@/lib/completion-policy'
import type { Issue } from '@/types/api'

interface Props {
  issue: Issue
  onUpdate: (data: Partial<Issue>) => Promise<void>
  readOnly?: boolean
}

const INHERIT = 'inherit'

export function IssueCompletionPolicyCard({ issue, onUpdate, readOnly }: Props) {
  const { t } = useTranslation('intake')
  const [saving, setSaving] = useState(false)
  const projectId = issue.project_id

  const settingsQuery = useQuery({
    queryKey: ['completion-settings', projectId],
    queryFn: () => intakeApi.getCompletionSettings(projectId),
    enabled: projectId > 0,
    retry: false,
  })
  const profilesQuery = useQuery({
    queryKey: ['completion-profiles', projectId],
    queryFn: () => intakeApi.listCompletionProfiles(projectId),
    enabled: projectId > 0,
    retry: false,
  })

  const settings = settingsQuery.data
  const profiles = profilesQuery.data ?? []
  const effective = resolveCompletionPolicy(settings, profiles, issue.completion_profile_id)

  // A dangling/mismatched profile id degrades to inheritance, matching
  // completion.Resolver.Resolve — never show an override that is not in force.
  const selectedValue =
    effective.source === 'issue_profile' && effective.profile_id != null
      ? String(effective.profile_id)
      : INHERIT

  const handleChange = async (value: string) => {
    setSaving(true)
    try {
      await onUpdate({
        completion_profile_id: value === INHERIT ? null : Number(value),
      })
    } finally {
      setSaving(false)
    }
  }

  const loadError = settingsQuery.isError || profilesQuery.isError
  const loading = settingsQuery.isLoading || profilesQuery.isLoading

  return (
    <div className="border-b border-border px-5 py-4 space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Zap className="w-4 h-4 text-muted-foreground" aria-hidden="true" />
        <h3 className="text-sm font-semibold">{t('completion.title')}</h3>
        {effective.source === 'issue_profile' ? (
          <Badge className="bg-brand-soft text-brand border-transparent text-xs font-normal">
            {t('completion.sourceIssueProfile', {
              name: completionProfileLabel(effective.profile_name, (key) => t(key)),
            })}
          </Badge>
        ) : (
          <Badge variant="outline" className="text-xs font-normal text-muted-foreground">
            {t('completion.sourceProjectDefault')}
          </Badge>
        )}
      </div>

      {loadError ? (
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <AlertTriangle className="h-4 w-4 text-warning" aria-hidden="true" />
          <span>{t('completion.loadError')}</span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => {
              void settingsQuery.refetch()
              void profilesQuery.refetch()
            }}
          >
            {t('completion.retry')}
          </Button>
        </div>
      ) : (
        <>
          <div className="space-y-1.5">
            <Select
              value={selectedValue}
              onValueChange={(value) => void handleChange(value)}
              disabled={loading || saving || readOnly || projectId <= 0}
            >
              <SelectTrigger className="h-9 w-full sm:w-64" aria-label={t('completion.issueSelectLabel')}>
                <SelectValue placeholder={t('completion.profile.inherit')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={INHERIT}>{t('completion.profile.inherit')}</SelectItem>
                {profiles.map((profile) => (
                  <SelectItem key={profile.id} value={String(profile.id)}>
                    {completionProfileLabel(profile.name, (key) => t(key))}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">{t('completion.issueHint')}</p>
          </div>

          <p className="text-xs text-muted-foreground tabular-nums">
            {completionSummary(effective, t)}
          </p>
        </>
      )}
    </div>
  )
}
