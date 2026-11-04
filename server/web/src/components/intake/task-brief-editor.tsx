import { useTranslation } from 'react-i18next'
import { Plus, Sparkles, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'
import type { BriefDraftState } from './brief-draft'

interface TaskBriefEditorProps {
  draft: BriefDraftState
  editing: boolean
  onChange: (next: BriefDraftState) => void
  onSuggest: () => void
  suggesting: boolean
  suggestDisabled?: boolean
}

export function TaskBriefEditor({
  draft,
  editing,
  onChange,
  onSuggest,
  suggesting,
  suggestDisabled,
}: TaskBriefEditorProps) {
  const { t } = useTranslation('intake')
  const acceptance = draft.acceptance.length > 0 ? draft.acceptance : ['']

  function setAcceptance(index: number, value: string) {
    const next = [...acceptance]
    next[index] = value
    onChange({ ...draft, acceptance: next })
  }

  function removeAcceptance(index: number) {
    const next = acceptance.filter((_, i) => i !== index)
    onChange({ ...draft, acceptance: next.length > 0 ? next : [''] })
  }

  return (
    <div className="flex flex-col gap-4">
      <section className="flex flex-col gap-1.5">
        <div className="flex items-center justify-between gap-2">
          <label className="text-xs font-medium text-warm-text-muted" htmlFor="intake-brief-goal">
            {t('review.goal')}
          </label>
          {editing && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 gap-1 px-2 text-xs"
              onClick={onSuggest}
              disabled={suggesting || suggestDisabled}
            >
              <Sparkles className={cn('h-3.5 w-3.5', suggesting && 'animate-pulse')} />
              {suggesting ? t('review.suggesting') : t('review.suggest')}
            </Button>
          )}
        </div>
        {editing ? (
          <Textarea
            id="intake-brief-goal"
            value={draft.goal}
            onChange={(e) => onChange({ ...draft, goal: e.target.value })}
            rows={2}
            className="min-h-0 resize-none"
          />
        ) : (
          <p className="rounded-md border border-warm-border bg-warm-muted px-3 py-2 text-sm whitespace-pre-wrap break-words">
            {draft.goal.trim() || t('review.goalEmpty')}
          </p>
        )}
      </section>

      <section className="flex flex-col gap-1.5">
        <span className="text-xs font-medium text-warm-text-muted">{t('review.acceptance')}</span>
        {editing ? (
          <div className="flex flex-col gap-1.5">
            {acceptance.map((item, index) => (
              <div key={index} className="flex items-center gap-1.5">
                <Input
                  value={item}
                  onChange={(e) => setAcceptance(index, e.target.value)}
                  placeholder={t('review.acceptancePlaceholder')}
                  className="h-8"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 shrink-0 text-muted-foreground"
                  onClick={() => removeAcceptance(index)}
                  aria-label={t('review.removeAcceptance')}
                  title={t('review.removeAcceptance')}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 w-fit gap-1 px-2 text-xs"
              onClick={() => onChange({ ...draft, acceptance: [...acceptance, ''] })}
            >
              <Plus className="h-3.5 w-3.5" />
              {t('review.addAcceptance')}
            </Button>
          </div>
        ) : (
          <ul className="flex flex-col gap-1 text-sm">
            {acceptance.filter((item) => item.trim() !== '').length === 0 && (
              <li className="text-warm-text-muted">{t('review.acceptanceEmpty')}</li>
            )}
            {acceptance
              .filter((item) => item.trim() !== '')
              .map((item, index) => (
                <li key={index} className="flex items-start gap-1.5">
                  <span className="mt-0.5 text-warm-text-muted">☐</span>
                  <span className="break-words">{item}</span>
                </li>
              ))}
          </ul>
        )}
      </section>

      <div className="grid gap-3 sm:grid-cols-2">
        <section className="flex flex-col gap-1.5">
          <label className="text-xs font-medium text-warm-text-muted" htmlFor="intake-brief-scope-in">
            {t('review.scopeIn')}
          </label>
          {editing ? (
            <Input
              id="intake-brief-scope-in"
              value={draft.scopeIn}
              onChange={(e) => onChange({ ...draft, scopeIn: e.target.value })}
              placeholder={t('review.scopePlaceholder')}
              className="h-8"
            />
          ) : (
            <span className="text-sm break-words">{draft.scopeIn.trim() || '—'}</span>
          )}
        </section>
        <section className="flex flex-col gap-1.5">
          <label className="text-xs font-medium text-warm-text-muted" htmlFor="intake-brief-scope-out">
            {t('review.scopeOut')}
          </label>
          {editing ? (
            <Input
              id="intake-brief-scope-out"
              value={draft.scopeOut}
              onChange={(e) => onChange({ ...draft, scopeOut: e.target.value })}
              placeholder={t('review.scopePlaceholder')}
              className="h-8"
            />
          ) : (
            <span className="text-sm break-words">{draft.scopeOut.trim() || '—'}</span>
          )}
        </section>
      </div>
    </div>
  )
}
