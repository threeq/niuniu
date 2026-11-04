import { useTranslation } from 'react-i18next'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import type { CompletionPolicyDraft } from './completion-policy-draft'

const NON_NEGATIVE_INT = /^\d+$/

interface SwitchRowProps {
  label: string
  hint?: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}

function SwitchRow({ label, hint, checked, onCheckedChange }: SwitchRowProps) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <div className="text-sm font-medium">{label}</div>
        {hint ? <div className="text-xs text-muted-foreground">{hint}</div> : null}
      </div>
      <Switch checked={checked} onCheckedChange={onCheckedChange} aria-label={label} />
    </div>
  )
}

interface Props {
  draft: CompletionPolicyDraft
  onChange: (next: CompletionPolicyDraft) => void
  idPrefix: string
  disabled?: boolean
}

export function CompletionPolicyFields({ draft, onChange, idPrefix, disabled }: Props) {
  const { t } = useTranslation('intake')
  const roundsInvalid = !NON_NEGATIVE_INT.test(draft.max_rounds.trim())
  const budgetInvalid = !NON_NEGATIVE_INT.test(draft.token_budget.trim())

  return (
    <div className="space-y-4">
      <SwitchRow
        label={t('completion.autohost')}
        hint={t('completion.autohostHint')}
        checked={draft.autohost_enabled}
        onCheckedChange={(checked) => onChange({ ...draft, autohost_enabled: checked })}
      />

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label htmlFor={`${idPrefix}-max-rounds`}>{t('completion.maxRounds')}</Label>
          <Input
            id={`${idPrefix}-max-rounds`}
            type="number"
            min={0}
            step={1}
            inputMode="numeric"
            value={draft.max_rounds}
            onChange={(e) => onChange({ ...draft, max_rounds: e.target.value })}
            disabled={disabled}
            aria-invalid={roundsInvalid}
            className="w-32 tabular-nums"
          />
          {roundsInvalid ? (
            <p className="text-xs text-destructive-text">{t('completion.invalidRounds')}</p>
          ) : null}
        </div>
        <div className="space-y-1.5">
          <Label htmlFor={`${idPrefix}-token-budget`}>{t('completion.tokenBudget')}</Label>
          <Input
            id={`${idPrefix}-token-budget`}
            type="number"
            min={0}
            step={1}
            inputMode="numeric"
            value={draft.token_budget}
            onChange={(e) => onChange({ ...draft, token_budget: e.target.value })}
            disabled={disabled}
            aria-invalid={budgetInvalid}
            className="w-40 tabular-nums"
          />
          {budgetInvalid ? (
            <p className="text-xs text-destructive-text">{t('completion.invalidBudget')}</p>
          ) : (
            <p className="text-xs text-muted-foreground">{t('completion.unlimited')}</p>
          )}
        </div>
      </div>

      <div className="space-y-3">
        <SwitchRow
          label={t('completion.stopOnDecision')}
          checked={draft.stop_on_decision}
          onCheckedChange={(checked) => onChange({ ...draft, stop_on_decision: checked })}
        />
        <SwitchRow
          label={t('completion.requireReview')}
          checked={draft.require_review}
          onCheckedChange={(checked) => onChange({ ...draft, require_review: checked })}
        />
      </div>
    </div>
  )
}
