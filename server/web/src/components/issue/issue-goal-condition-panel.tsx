import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, Sparkles } from 'lucide-react'
import { toast } from 'sonner'
import { Textarea } from '@/components/ui/textarea'
import { Button } from '@/components/ui/button'
import { apiFetch, ApiError } from '@/lib/api'

const MAX_LEN = 4000

type Props = {
  issueId: number
  initialCondition: string
  onSave: (next: string) => Promise<void>
}

type SuggestEnvelope = { data: { suggestion: string } }

/**
 * IssueGoalConditionPanel — collapsible panel for editing an issue's autohost
 * completion criterion (`goal_condition`). The LLM judge consumes this string
 * when autohost is preparing to stop. Collapse state is persisted per-issue
 * in localStorage so reopening the issue keeps the user's preference.
 *
 * Save semantics — two paths:
 *
 *  1. **Manual edit** (user types in textarea): saves onBlur, matching the
 *     existing "live edit" pattern of other issue fields. Low-friction for
 *     continuous editing.
 *  2. **AI suggest** (clicks the button): enters DIRTY MODE — value is held
 *     in local state, "未保存" badge + "保存" / "丢弃" buttons appear,
 *     onBlur autosave is DISABLED so the user can review the suggestion
 *     before committing. The user explicitly clicks 保存 to persist (or
 *     丢弃 to revert to the previous saved value). This avoids the
 *     surprise of an autosave the user didn't intend after an AI fill.
 *
 * Dirty mode is exited on save success (新值持久化 → dirty=false) or
 * discard (回退到 initialCondition → dirty=false). Subsequent manual edits
 * keep dirty=true (the user can keep tweaking the AI suggestion before
 * saving) until either button is clicked.
 */
export function IssueGoalConditionPanel({ issueId, initialCondition, onSave }: Props) {
  const { t } = useTranslation('issues')
  const storageKey = `issue-goal-cond-open-${issueId}`
  const [open, setOpen] = useState<boolean>(() => {
    if (typeof window === 'undefined') return false
    // Respect explicit user preference if it exists; otherwise default to
    // expanded for unset conditions (so the AI suggest button + textarea
    // are immediately discoverable for the user who hasn't filled one in
    // yet — that's the target of this feature) and collapsed once a
    // condition is saved (issue is in "view" mode for a returning user).
    const stored = window.localStorage.getItem(storageKey)
    if (stored === '1') return true
    if (stored === '0') return false
    return initialCondition === ''
  })
  const [value, setValue] = useState(initialCondition)
  const [saving, setSaving] = useState(false)
  const [suggesting, setSuggesting] = useState(false)
  // dirty=true while showing an unsaved AI suggestion (or subsequent manual
  // tweaks before the user commits via 保存). Suppresses onBlur autosave.
  const [dirty, setDirty] = useState(false)

  // Sync local value with prop changes (parent refetched after save / page
  // reopen). If we're in dirty mode (user has an unsaved AI suggestion),
  // preserve the in-progress value — otherwise the parent refresh would
  // clobber the user's pending review.
  useEffect(() => {
    if (!dirty) setValue(initialCondition)
  }, [initialCondition, dirty])

  const toggle = () => {
    setOpen((prev) => {
      const next = !prev
      window.localStorage.setItem(storageKey, next ? '1' : '0')
      return next
    })
  }

  const handleBlur = async () => {
    // While in dirty mode (post-AI-suggest), require explicit save click.
    if (dirty) return
    if (value === initialCondition) return
    if (value.length > MAX_LEN) return
    setSaving(true)
    try {
      await onSave(value)
    } finally {
      setSaving(false)
    }
  }

  const handleSaveClick = async () => {
    if (value.length > MAX_LEN) return
    setSaving(true)
    try {
      await onSave(value)
      setDirty(false)
    } finally {
      setSaving(false)
    }
  }

  const handleDiscard = () => {
    setValue(initialCondition)
    setDirty(false)
  }

  const handleSuggest = async () => {
    setSuggesting(true)
    try {
      // suppressError: true so the global "API Error" toast doesn't fire on
      // top of our localized one below.
      const resp = await apiFetch<SuggestEnvelope>(
        `/issues/${issueId}/suggest-goal-condition`,
        { method: 'POST', suppressError: true },
      )
      const next = resp?.data?.suggestion ?? ''
      if (next) {
        setValue(next)
        setDirty(true) // enter unsaved/review mode — user clicks 保存 to commit
      } else {
        toast.error(t('goalCondition.suggestFailed'))
      }
    } catch (e: unknown) {
      let msg: string
      if (e instanceof ApiError && e.status === 503) {
        msg = t('goalCondition.suggestCliUnavailable')
      } else if (e instanceof ApiError && e.status === 429) {
        msg = t('goalCondition.suggestRateLimited')
      } else {
        msg = t('goalCondition.suggestFailed')
      }
      toast.error(msg)
    } finally {
      setSuggesting(false)
    }
  }

  const Icon = open ? ChevronDown : ChevronRight

  return (
    <div className="rounded-lg border bg-card">
      <button
        type="button"
        onClick={toggle}
        className="flex items-center gap-2 w-full px-3 py-2 text-sm hover:bg-accent text-left"
        aria-expanded={open}
      >
        <Icon className="w-4 h-4" />
        <span className="font-medium">{t('goalCondition.title')}</span>
        <span className="text-xs text-muted-foreground ml-auto">
          {initialCondition ? t('goalCondition.set') : t('goalCondition.unset')}
        </span>
      </button>
      {open && (
        <div className="px-3 py-2 border-t flex flex-col gap-2">
          <Textarea
            value={value}
            onChange={(e) => {
              setValue(e.target.value)
              // Manual edits during dirty mode keep dirty=true so the
              // save/discard buttons remain visible until commit.
            }}
            onBlur={handleBlur}
            rows={3}
            className="font-mono text-xs leading-relaxed"
            maxLength={MAX_LEN}
          />
          <div className="flex items-center justify-between text-xs text-muted-foreground gap-2 flex-wrap">
            <div className="flex items-center gap-1.5">
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="gap-1.5 h-7 px-2 text-xs"
                disabled={suggesting || saving}
                onClick={handleSuggest}
              >
                <Sparkles className="w-3.5 h-3.5" />
                {suggesting ? t('goalCondition.suggesting') : t('goalCondition.aiSuggest')}
              </Button>
              {dirty && (
                <>
                  <Button
                    type="button"
                    size="sm"
                    variant="default"
                    className="h-7 px-2 text-xs"
                    disabled={saving || value.length > MAX_LEN}
                    onClick={handleSaveClick}
                  >
                    {saving ? t('common:actions.saving') : t('common:actions.save')}
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    className="h-7 px-2 text-xs"
                    disabled={saving}
                    onClick={handleDiscard}
                  >
                    {t('common:actions.discard')}
                  </Button>
                </>
              )}
            </div>
            <div className="flex items-center gap-3">
              {dirty && (
                <span className="text-warning font-medium">
                  {t('goalCondition.unsaved')}
                </span>
              )}
              <span>
                {t('goalCondition.charCount', { n: value.length, max: MAX_LEN })}
              </span>
              {saving && !dirty && <span>{t('common:actions.saving')}</span>}
            </div>
          </div>
          <p className="text-xs text-muted-foreground">{t('goalCondition.help')}</p>
        </div>
      )}
    </div>
  )
}
