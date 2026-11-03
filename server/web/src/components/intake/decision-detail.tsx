import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { AlertTriangle, ArrowUpRight, HelpCircle, Paperclip } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { ApiError } from '@/lib/api'
import { intakeApi, type IntakeDecision, type IntakeRule } from '@/lib/intake-api'
import { cn } from '@/lib/utils'
import { SourceIcon, StrengthBadge } from './decision-list'
import { useRelativeTime } from './use-relative-time'
import { briefFromDecision, briefPayload, type BriefDraftState } from './brief-draft'
import { TaskBriefEditor } from './task-brief-editor'
import { IgnoreReasonDialog } from './ignore-reason-dialog'
import type { IgnoreReason } from './ignore-reason'

interface DecisionDetailProps {
  decision: IntakeDecision
  rules: IntakeRule[]
  rulesFailed: boolean
}

export function DecisionDetail({ decision, rules, rulesFailed }: DecisionDetailProps) {
  const { t } = useTranslation('intake')
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const relative = useRelativeTime()

  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<BriefDraftState>(() => briefFromDecision(decision))
  const [ignoreOpen, setIgnoreOpen] = useState(false)

  const isResume = decision.decision === 'resume'
  const isPendingProposal = decision.decision === 'accept' || isResume
  const message = (decision.payload?.Text ?? '').trim()
  const attachments = decision.payload?.Attachments ?? []
  const ruleById = new Map(rules.map((rule) => [rule.id, rule]))

  function refresh() {
    queryClient.invalidateQueries({ queryKey: ['intake-decisions'] })
    // Accept/ignore changes the calibration sample, so the strip and the
    // enable-auto recommendation must refresh with it.
    queryClient.invalidateQueries({ queryKey: ['intake-calibration'] })
  }

  function openTask(issueId: number | null, workspaceId: number | null) {
    if (workspaceId) {
      navigate({ to: '/workspaces/$id', params: { id: String(workspaceId) } })
      return
    }
    if (issueId) {
      navigate({ to: '/projects/$id', params: { id: String(decision.project_id) } })
    }
  }

  const saveDraft = useMutation({
    mutationFn: () => intakeApi.saveDraft(decision.project_id, decision.id, briefPayload(draft)),
    onSuccess: () => {
      setEditing(false)
      toast.success(t('review.draftSaved'))
      refresh()
    },
    onError: (error) => toast.error(errorMessage(error, t('review.saveFailed'))),
  })

  const accept = useMutation({
    mutationFn: () => intakeApi.accept(decision.project_id, decision.id, briefPayload(draft)),
    onSuccess: (updated) => {
      toast.success(isResume ? t('review.resumed') : t('review.accepted'))
      refresh()
      openTask(updated.issue_id ?? decision.issue_id, updated.workspace_id ?? decision.workspace_id)
    },
    // Edits stay in local state; the user can retry from the same button.
    onError: (error) => toast.error(errorMessage(error, t('review.acceptFailed'))),
  })

  const ignore = useMutation({
    mutationFn: (reason: IgnoreReason) =>
      intakeApi.ignore(decision.project_id, decision.id, reason.code, reason.text),
    onSuccess: () => {
      setIgnoreOpen(false)
      toast.success(t('review.ignored'))
      refresh()
    },
    onError: (error) => toast.error(errorMessage(error, t('review.ignoreFailed'))),
  })

  const suggest = useMutation({
    mutationFn: () => intakeApi.suggestGoal(decision.project_id, decision.id),
    onSuccess: (result) => setDraft((current) => ({ ...current, goal: result.data.suggestion })),
    onError: (error) =>
      toast.error(
        suggestErrorMessage(error, {
          cliUnavailable: t('review.suggestCliUnavailable'),
          rateLimited: t('review.suggestRateLimited'),
          failed: t('review.suggestFailed'),
        }),
      ),
  })

  const busy = accept.isPending || saveDraft.isPending || ignore.isPending

  return (
    <div className="flex h-full flex-col">
      <div className="flex-1 overflow-y-auto p-4 sm:p-6">
        <header className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <span className="flex items-center gap-1.5 rounded-md bg-warm-muted px-2 py-1 text-xs font-medium">
              <SourceIcon source={decision.source_kind} />
              {t(`intake.source.${decision.source_kind}`, {
                defaultValue: decision.source_kind,
              })}
            </span>
            <span className="text-sm text-warm-text-muted">
              {decision.thread_ext_id || t('review.noThread')}
            </span>
            <StrengthBadge strength={decision.strength} />
            {isResume && (
              <span className="rounded bg-warm-muted px-1.5 py-0.5 text-xs font-medium">
                {t('review.decision.resume')}
              </span>
            )}
            <span className="ml-auto text-xs text-warm-text-muted tabular-nums">
              {relative(decision.created_at)}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
            <span className="text-warm-text-muted">{t('review.actor')}</span>
            <span className="font-medium">
              {decision.actor_name || decision.actor_ext_id || t('review.unknownActor')}
            </span>
          </div>
        </header>

        <section className="mt-4 flex flex-wrap items-center gap-2 text-xs">
          <span className="text-warm-text-muted">{t('review.matchedRules')}</span>
          {decision.matched_rule_ids.length === 0 ? (
            <span className="text-warm-text-muted">{t('review.noMatchedRules')}</span>
          ) : (
            decision.matched_rule_ids.map((id) => (
              <span
                key={id}
                className="rounded-md border border-warm-border bg-warm-surface px-2 py-0.5"
                title={rulesFailed ? t('review.rulesLoadFailed') : undefined}
              >
                {ruleById.get(id)?.name ?? `#${id}`}
              </span>
            ))
          )}
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <span className="inline-flex items-center gap-1 rounded-md border border-warm-border px-2 py-0.5 tabular-nums">
                  {t('review.policyVersion', { version: decision.policy_version })}
                  <HelpCircle className="h-3 w-3 text-warm-text-muted" aria-hidden />
                </span>
              </TooltipTrigger>
              <TooltipContent>{t('review.policyVersionHint')}</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        </section>

        {isResume && (decision.issue_id || decision.workspace_id) && (
          <section className="mt-4 flex flex-wrap items-center justify-between gap-2 rounded-md border border-brand/30 bg-brand-soft px-3 py-2">
            <div className="text-sm">
              <span className="text-warm-text-muted">{t('review.resumeTarget')}</span>
              <span className="ml-1 font-medium tabular-nums">#{decision.issue_id}</span>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 gap-1 px-2 text-xs"
              onClick={() => openTask(decision.issue_id, decision.workspace_id)}
            >
              {t('review.openTask')}
              <ArrowUpRight className="h-3.5 w-3.5" />
            </Button>
          </section>
        )}

        <section className="mt-5">
          <h3 className="mb-1.5 text-xs font-medium text-warm-text-muted">
            {t('review.originalMessage')}
          </h3>
          <blockquote className="max-h-52 overflow-y-auto rounded-md border border-warm-border bg-warm-muted px-3 py-2 text-sm whitespace-pre-wrap break-words">
            {message || t('review.noMessage')}
          </blockquote>
          {attachments.length > 0 && (
            <div className="mt-2 flex flex-wrap gap-1.5">
              {attachments.map((attachment, index) => (
                <span
                  key={`${attachment.Name}-${index}`}
                  className="inline-flex items-center gap-1 rounded-md border border-warm-border px-2 py-0.5 text-xs"
                >
                  <Paperclip className="h-3 w-3" aria-hidden />
                  {attachment.Name || attachment.Kind}
                </span>
              ))}
            </div>
          )}
        </section>

        {isPendingProposal ? (
          <section className="mt-5">
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-xs font-medium text-warm-text-muted">{t('review.brief')}</h3>
              {editing && (
                <span className="text-xs text-brand">{t('review.dirtyHint')}</span>
              )}
            </div>
            <TaskBriefEditor
              draft={draft}
              editing={editing}
              onChange={setDraft}
              onSuggest={() => suggest.mutate()}
              suggesting={suggest.isPending}
              suggestDisabled={busy}
            />
          </section>
        ) : (
          <section className="mt-5 rounded-md border border-warm-border bg-warm-muted px-3 py-2 text-sm text-warm-text-muted">
            <AlertTriangle className="mr-1 inline h-3.5 w-3.5" aria-hidden />
            {decision.decision === 'ignore'
              ? t('review.alreadyIgnored', {
                  reason: decision.reason_text || decision.reason_code || t('review.noReason'),
                })
              : t('review.notAcceptable')}
          </section>
        )}
      </div>

      {isPendingProposal && (
        <footer className="flex flex-wrap items-center gap-2 border-t border-warm-border bg-warm-surface px-4 py-3 sm:px-6">
          <Button
            type="button"
            onClick={() => accept.mutate()}
            disabled={busy}
            className="min-w-0 flex-1 sm:flex-none"
          >
            {accept.isPending
              ? t('review.accepting')
              : isResume
                ? t('review.acceptResume')
                : t('review.accept')}
          </Button>
          {editing ? (
            <>
              <Button
                type="button"
                variant="secondary"
                onClick={() => saveDraft.mutate()}
                disabled={busy}
              >
                {saveDraft.isPending ? t('review.saving') : t('review.saveDraft')}
              </Button>
              <Button
                type="button"
                variant="ghost"
                onClick={() => {
                  setDraft(briefFromDecision(decision))
                  setEditing(false)
                }}
                disabled={busy}
              >
                {t('review.cancel')}
              </Button>
            </>
          ) : (
            <Button
              type="button"
              variant="secondary"
              onClick={() => setEditing(true)}
              disabled={busy}
              className={cn(isResume && 'hidden')}
            >
              {t('review.edit')}
            </Button>
          )}
          <Button
            type="button"
            variant="ghost"
            className="ml-auto text-destructive-text hover:bg-destructive/10 hover:text-destructive-text"
            onClick={() => setIgnoreOpen(true)}
            disabled={busy}
          >
            {t('review.ignore')}
          </Button>
        </footer>
      )}

      <IgnoreReasonDialog
        open={ignoreOpen}
        onOpenChange={setIgnoreOpen}
        count={1}
        pending={ignore.isPending}
        onConfirm={(reason) => ignore.mutate(reason)}
      />
    </div>
  )
}

function errorMessage(error: unknown, fallback: string) {
  if (error instanceof Error && error.message) return `${fallback}: ${error.message}`
  return fallback
}

function suggestErrorMessage(
  error: unknown,
  messages: { cliUnavailable: string; rateLimited: string; failed: string },
) {
  if (error instanceof ApiError) {
    if (error.status === 503) return messages.cliUnavailable
    if (error.status === 429) return messages.rateLimited
  }
  return messages.failed
}
