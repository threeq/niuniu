import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
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
import { Textarea } from '@/components/ui/textarea'
import { intakeApi, type IntakeRule, type IntakeRuleInput, type IntakeSourceKind } from '@/lib/intake-api'
import { ChipsEditor } from './chips-editor'
import type { KnownActor } from './intake-utils'

interface Props {
  projectId: number
  sourceKind: IntakeSourceKind
  // null = create.
  rule: IntakeRule | null
  open: boolean
  onOpenChange: (open: boolean) => void
  actors: KnownActor[]
}

interface FormState {
  name: string
  instruction: string
  priority: string
  enabled: boolean
  // Preserved verbatim: the CI default rule is scoped by event_kind, and
  // editing its text must not silently drop that condition.
  eventKind: string
  actors: string[]
  keywords: string[]
}

function initialForm(rule: IntakeRule | null): FormState {
  return {
    name: rule?.name ?? '',
    instruction: rule?.instruction ?? '',
    priority: String(rule?.priority ?? 0),
    enabled: rule?.enabled ?? true,
    eventKind: rule?.conditions?.event_kind ?? '',
    actors: [...(rule?.conditions?.actors ?? [])],
    keywords: [...(rule?.conditions?.keywords ?? [])],
  }
}

export function RuleDialog({ projectId, sourceKind, rule, open, onOpenChange, actors }: Props) {
  const { t } = useTranslation('intake')
  const qc = useQueryClient()
  // The parent remounts this dialog per open target (key), so initial state is
  // always the rule being edited — no reset effect needed.
  const [form, setForm] = useState<FormState>(() => initialForm(rule))
  const [submitted, setSubmitted] = useState(false)

  const mutation = useMutation({
    mutationFn: (input: IntakeRuleInput) =>
      rule
        ? intakeApi.updateRule(projectId, rule.id, input)
        : intakeApi.createRule(projectId, input),
    onSuccess: () => {
      toast.success(t('intake.rules.saved'))
      void qc.invalidateQueries({ queryKey: ['intake-rules', projectId] })
      void qc.invalidateQueries({ queryKey: ['intake-policy-versions', projectId] })
      onOpenChange(false)
    },
    onError: (err: unknown) => {
      toast.error(t('intake.rules.saveFailed', { message: err instanceof Error ? err.message : String(err) }))
    },
  })

  const nameError = submitted && !form.name.trim()
  const conditionError =
    submitted && !form.eventKind && form.actors.length === 0 && form.keywords.length === 0
  const priorityValue = form.priority.trim()
  const priorityError =
    submitted && (!/^-?\d+$/.test(priorityValue) || !Number.isSafeInteger(Number(priorityValue)))

  const submit = () => {
    setSubmitted(true)
    if (!form.name.trim() || conditionError || priorityError) return
    mutation.mutate({
      source_kind: sourceKind,
      name: form.name.trim(),
      instruction: form.instruction.trim(),
      priority: Number(priorityValue),
      enabled: form.enabled,
      conditions: {
        ...(form.eventKind ? { event_kind: form.eventKind } : {}),
        actors: form.actors,
        keywords: form.keywords,
      },
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{rule ? t('intake.rules.dialog.editTitle') : t('intake.rules.dialog.createTitle')}</DialogTitle>
          <DialogDescription>{t('intake.rules.dialog.description')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="intake-rule-name">{t('intake.rules.name')}</Label>
            <Input
              id="intake-rule-name"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              placeholder={t('intake.rules.namePlaceholder')}
              aria-invalid={nameError || undefined}
            />
            {nameError ? <p className="text-xs text-destructive-text">{t('intake.rules.requiredName')}</p> : null}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="intake-rule-instruction">{t('intake.rules.instruction')}</Label>
            <Textarea
              id="intake-rule-instruction"
              value={form.instruction}
              onChange={(e) => setForm({ ...form, instruction: e.target.value })}
              placeholder={t('intake.rules.instructionPlaceholder')}
              rows={3}
            />
            <p className="text-xs text-muted-foreground">{t('intake.rules.instructionHint')}</p>
          </div>

          <ChipsEditor
            label={t('intake.rules.actors')}
            hint={t('intake.rules.actorsHint')}
            values={form.actors}
            onChange={(actors) => setForm({ ...form, actors })}
            placeholder={t('intake.rules.actorsPlaceholder')}
            addLabel={t('intake.rules.add')}
            emptyLabel={t('intake.rules.emptyActors')}
            removeLabel={(v) => t('intake.rules.removeActor', { actor: v })}
            suggestions={actors}
            suggestionsLabel={t('intake.rules.suggestions')}
          />

          <ChipsEditor
            label={t('intake.rules.keywords')}
            hint={t('intake.rules.keywordsHint')}
            values={form.keywords}
            onChange={(keywords) => setForm({ ...form, keywords })}
            placeholder={t('intake.rules.keywordsPlaceholder')}
            addLabel={t('intake.rules.add')}
            emptyLabel={t('intake.rules.emptyKeywords')}
            removeLabel={(v) => t('intake.rules.removeKeyword', { keyword: v })}
          />
          {conditionError ? (
            <p className="text-xs text-destructive-text">{t('intake.rules.requiredCondition')}</p>
          ) : null}

          <div className="flex flex-wrap items-end gap-6">
            <div className="w-32 space-y-1.5">
              <Label htmlFor="intake-rule-priority">{t('intake.rules.priority')}</Label>
              <Input
                id="intake-rule-priority"
                inputMode="numeric"
                value={form.priority}
                onChange={(e) => setForm({ ...form, priority: e.target.value })}
                className="tabular-nums"
                aria-invalid={priorityError || undefined}
              />
            </div>
            <p className="text-xs text-muted-foreground">{t('intake.rules.priorityHint')}</p>
            <label className="ml-auto flex items-center gap-2 select-none">
              <span className="text-sm">{t('intake.rules.enabled')}</span>
              <Switch
                checked={form.enabled}
                onCheckedChange={(enabled) => setForm({ ...form, enabled })}
                aria-label={t('intake.rules.enabled')}
              />
            </label>
          </div>
        </div>

        <DialogFooter className="gap-2">
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t('intake.rules.cancel')}
          </Button>
          <Button type="button" onClick={submit} disabled={mutation.isPending}>
            {mutation.isPending ? t('intake.rules.saving') : t('intake.rules.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
