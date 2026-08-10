import { useState, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { formatAcceleratorFromEvent } from '@/lib/accelerator'
import { cn } from '@/lib/utils'

interface KeybindingInputProps {
  /** Current accelerator, e.g. "Cmd+Shift+Z" (empty = unset). */
  value: string
  /** Called with the new accelerator once the user presses a valid combo. */
  onChange: (accelerator: string) => void
  disabled?: boolean
  className?: string
}

/**
 * A focusable capture field for a global-hotkey accelerator. While focused it
 * listens for a keydown, formats it via lib/accelerator, and reports the result.
 * Esc cancels capture without changing the value. Requires at least one modifier
 * (bare keys are refused by the formatter).
 */
export function KeybindingInput({ value, onChange, disabled, className }: KeybindingInputProps) {
  const { t } = useTranslation('settings')
  const [capturing, setCapturing] = useState(false)

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLButtonElement>) => {
      if (disabled) return
      e.preventDefault()
      e.stopPropagation()
      if (e.key === 'Escape') {
        e.currentTarget.blur()
        return
      }
      const accel = formatAcceleratorFromEvent(e)
      if (accel) {
        onChange(accel)
        e.currentTarget.blur()
      }
    },
    [disabled, onChange]
  )

  return (
    <button
      type="button"
      disabled={disabled}
      onKeyDown={handleKeyDown}
      onFocus={() => setCapturing(true)}
      onBlur={() => setCapturing(false)}
      aria-label={t('general.globalShortcuts.captureLabel')}
      className={cn(
        'inline-flex h-9 min-w-40 items-center justify-center rounded-lg border px-3 text-sm font-medium tabular-nums transition-colors',
        capturing ? 'border-info text-info' : 'border-border bg-card text-foreground hover:bg-accent/50',
        disabled && 'cursor-not-allowed opacity-50',
        className
      )}
    >
      {capturing
        ? t('general.globalShortcuts.capturing')
        : value || t('general.globalShortcuts.unset')}
    </button>
  )
}
