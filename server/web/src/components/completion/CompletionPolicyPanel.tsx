import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Pencil, Plus, Trash2, Zap } from 'lucide-react'
import { toast } from 'sonner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ApiError } from '@/lib/api'
import { confirm } from '@/lib/confirm'
import { intakeApi, type CompletionProfile } from '@/lib/intake-api'
import { completionProfileLabel, completionSummary } from '@/lib/completion-policy'
import { LoadingSkeleton } from '@/components/shared/loading-skeleton'
import { CompletionPolicyFields } from './completion-policy-fields'
import {
  draftFromPolicy,
  isPolicyDraftValid,
  policyValuesFromDraft,
  type CompletionPolicyDraft,
} from './completion-policy-draft'
import { CompletionProfileDialog } from './completion-profile-dialog'

interface Props {
  projectId: number
}

export function CompletionPolicyPanel({ projectId }: Props) {
  const { t } = useTranslation('intake')
  const qc = useQueryClient()
  // null = no local edits; the form then mirrors the server state. Keeping the
  // draft across refetches means an unrelated refetch never clobbers typing.
  const [editedDraft, setDraft] = useState<CompletionPolicyDraft | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingProfile, setEditingProfile] = useState<CompletionProfile | null>(null)

  const settingsQuery = useQuery({
    queryKey: ['completion-settings', projectId],
    queryFn: () => intakeApi.getCompletionSettings(projectId),
    retry: false,
  })
  const profilesQuery = useQuery({
    queryKey: ['completion-profiles', projectId],
    queryFn: () => intakeApi.listCompletionProfiles(projectId),
    retry: false,
  })

  const settings = settingsQuery.data
  const draft = editedDraft ?? (settings ? draftFromPolicy(settings) : null)

  const saveMut = useMutation({
    mutationFn: (values: CompletionPolicyDraft) =>
      intakeApi.updateCompletionSettings(projectId, policyValuesFromDraft(values)),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['completion-settings', projectId] })
      qc.invalidateQueries({ queryKey: ['completion-profiles', projectId] })
      toast.success(t('completion.saved'))
    },
    onError: (err: unknown) => {
      const message = err instanceof Error ? err.message : String(err)
      toast.error(t('completion.saveFailed', { message }))
    },
  })

  const deleteMut = useMutation({
    mutationFn: (profileId: number) => intakeApi.deleteCompletionProfile(projectId, profileId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['completion-profiles', projectId] })
      toast.success(t('completion.profileDeleted'))
    },
    onError: (err: unknown) => {
      if (err instanceof ApiError && err.status === 409) {
        toast.error(t('completion.deleteInUseBlocked'))
      } else {
        const message = err instanceof Error ? err.message : String(err)
        toast.error(t('completion.profileDeleteFailed', { message }))
      }
    },
  })

  const loading = settingsQuery.isLoading || profilesQuery.isLoading
  const loadError = settingsQuery.isError || profilesQuery.isError
  const profiles = profilesQuery.data ?? []

  const dirty =
    !!settings &&
    !!draft &&
    (draft.autohost_enabled !== settings.autohost_enabled ||
      draft.max_rounds !== String(settings.max_rounds) ||
      draft.token_budget !== String(settings.token_budget) ||
      draft.stop_on_decision !== settings.stop_on_decision ||
      draft.require_review !== settings.require_review)
  const canSave = !!draft && dirty && isPolicyDraftValid(draft) && !saveMut.isPending
  const budgetWarning =
    !!draft &&
    isPolicyDraftValid(draft) &&
    draft.token_budget.trim() === '0' &&
    Number(draft.max_rounds.trim()) >= 20

  const openCreate = () => {
    setEditingProfile(null)
    setDialogOpen(true)
  }
  const openEdit = (profile: CompletionProfile) => {
    setEditingProfile(profile)
    setDialogOpen(true)
  }

  const handleDelete = async (profile: CompletionProfile) => {
    const label = completionProfileLabel(profile.name, (key) => t(key))
    if ((profile.issue_count ?? 0) > 0) {
      toast.error(t('completion.deleteInUse', { count: profile.issue_count }))
      return
    }
    const ok = await confirm({
      title: t('completion.deleteConfirmTitle'),
      description: t('completion.deleteConfirmDescription', { name: label }),
      confirmText: t('completion.delete'),
      cancelText: t('completion.cancel'),
      destructive: true,
    })
    if (ok) deleteMut.mutate(profile.id)
  }

  return (
    <div className="border rounded-lg p-4 space-y-4">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <Zap className="w-4 h-4 text-muted-foreground" aria-hidden="true" />
          <h3 className="text-sm font-semibold">{t('completion.title')}</h3>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">{t('completion.description')}</p>
      </div>

      {loading ? (
        <LoadingSkeleton variant="card" count={2} />
      ) : loadError || !settings || !draft ? (
        <Alert variant="destructive">
          <AlertTriangle className="h-4 w-4" aria-hidden="true" />
          <AlertTitle>{t('completion.loadError')}</AlertTitle>
          <AlertDescription>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                void settingsQuery.refetch()
                void profilesQuery.refetch()
              }}
            >
              {t('completion.retry')}
            </Button>
          </AlertDescription>
        </Alert>
      ) : (
        <>
          <CompletionPolicyFields
            draft={draft}
            onChange={setDraft}
            idPrefix="completion-default"
            disabled={saveMut.isPending}
          />

          {budgetWarning ? (
            <div className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3">
              <AlertTriangle className="h-4 w-4 text-warning shrink-0 mt-0.5" aria-hidden="true" />
              <p className="text-xs text-warm-text">{t('completion.budgetWarning')}</p>
            </div>
          ) : null}

          <div className="flex justify-end">
            <Button
              type="button"
              onClick={() => saveMut.mutate(draft)}
              disabled={!canSave}
            >
              {saveMut.isPending ? t('completion.saving') : t('completion.save')}
            </Button>
          </div>

          <div className="space-y-2 border-t border-warm-border pt-4">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <h4 className="text-sm font-medium">{t('completion.profiles')}</h4>
                <p className="mt-0.5 text-xs text-muted-foreground">{t('completion.profilesHint')}</p>
              </div>
              <Button type="button" variant="outline" size="sm" onClick={openCreate}>
                <Plus className="h-4 w-4 mr-1" aria-hidden="true" />
                {t('completion.newProfile')}
              </Button>
            </div>

            {profiles.length === 0 ? (
              <p className="text-xs text-muted-foreground py-2">{t('completion.noProfiles')}</p>
            ) : (
              <div className="border rounded-md divide-y">
                {profiles.map((profile) => {
                  const count = profile.issue_count ?? 0
                  const label = completionProfileLabel(profile.name, (key) => t(key))
                  return (
                    <div key={profile.id} className="flex flex-wrap items-center gap-2 p-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="text-sm font-medium">{label}</span>
                          {count > 0 ? (
                            <Badge variant="outline" className="text-xs font-normal">
                              {t('completion.issueCount', { count })}
                            </Badge>
                          ) : (
                            <span className="text-xs text-muted-foreground">
                              {t('completion.issueCountZero')}
                            </span>
                          )}
                        </div>
                        <p className="mt-0.5 text-xs text-muted-foreground tabular-nums">
                          {completionSummary(profile, t)}
                        </p>
                      </div>
                      <div className="flex items-center gap-1 shrink-0">
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          aria-label={t('completion.editNamedProfile', { name: label })}
                          onClick={() => openEdit(profile)}
                        >
                          <Pencil className="h-4 w-4" aria-hidden="true" />
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          aria-label={t('completion.deleteNamedProfile', { name: label })}
                          className="text-destructive hover:text-destructive/80 hover:bg-destructive/10"
                          disabled={deleteMut.isPending}
                          onClick={() => void handleDelete(profile)}
                        >
                          <Trash2 className="h-4 w-4" aria-hidden="true" />
                        </Button>
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </>
      )}

      <CompletionProfileDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        projectId={projectId}
        profile={editingProfile}
      />
    </div>
  )
}
