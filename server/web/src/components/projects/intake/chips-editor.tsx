import { useState } from 'react'
import { X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { addUnique, type KnownActor } from './intake-utils'

interface Props {
  label: string
  hint?: string
  values: string[]
  onChange: (next: string[]) => void
  placeholder: string
  addLabel: string
  emptyLabel: string
  removeLabel: (value: string) => string
  suggestions?: KnownActor[]
  suggestionsLabel?: string
  disabled?: boolean
}

/** Chips + free-text add + one-click suggestions from recently seen actors. */
export function ChipsEditor({
  label,
  hint,
  values,
  onChange,
  placeholder,
  addLabel,
  emptyLabel,
  removeLabel,
  suggestions = [],
  suggestionsLabel,
  disabled = false,
}: Props) {
  const [input, setInput] = useState('')
  const add = (value: string) => {
    const next = addUnique(values, value)
    if (next !== values) onChange(next)
    setInput('')
  }
  const available = suggestions.filter((a) => !values.includes(a.id))

  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      {hint ? <p className="text-xs text-muted-foreground">{hint}</p> : null}

      <div className="flex flex-wrap gap-1.5">
        {values.length === 0 ? (
          <span className="text-xs text-muted-foreground">{emptyLabel}</span>
        ) : (
          values.map((value) => (
            <span
              key={value}
              className="inline-flex items-center gap-1 rounded border border-warm-border bg-warm-muted px-1.5 py-0.5 text-xs text-warm-text"
            >
              {value}
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="h-4 w-4 text-muted-foreground hover:text-foreground"
                aria-label={removeLabel(value)}
                disabled={disabled}
                onClick={() => onChange(values.filter((v) => v !== value))}
              >
                <X className="h-3 w-3" aria-hidden="true" />
              </Button>
            </span>
          ))
        )}
      </div>

      <div className="flex gap-2">
        <Input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              add(input)
            }
          }}
          placeholder={placeholder}
          disabled={disabled}
          className="h-9"
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => add(input)}
          disabled={disabled || !input.trim()}
        >
          {addLabel}
        </Button>
      </div>

      {suggestionsLabel && available.length > 0 ? (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-muted-foreground">{suggestionsLabel}</span>
          {available.slice(0, 8).map((actor) => (
            <Button
              key={actor.id}
              type="button"
              variant="secondary"
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={() => add(actor.id)}
              disabled={disabled}
              title={actor.id}
            >
              {actor.name}
            </Button>
          ))}
        </div>
      ) : null}
    </div>
  )
}
