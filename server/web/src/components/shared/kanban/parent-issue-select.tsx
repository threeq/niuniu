import { useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Search, ChevronDown, Check } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Input } from '@/components/ui/input'
import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/popover'
import type { Issue } from '@/types/api'

interface ParentIssueSelectProps {
  /** Eligible parent issues to choose from. */
  issues: Issue[]
  /** Currently-selected parent id (null = none/unselected). */
  value: number | null
  onChange: (next: number | null) => void
  disabled?: boolean
  /** Whether to render the "none / top-level" option. Default true. */
  allowNone?: boolean
  /** Label for the "none" option AND the trigger when nothing is selected. */
  noneLabel: string
  /** id forwarded to the trigger button (for a `<label htmlFor>` pairing). */
  id?: string
  /** Accessible label for the trigger (used when there is no visible label). */
  ariaLabel?: string
  /** Extra classes for the trigger button (sizing / width). */
  triggerClassName?: string
  'data-testid'?: string
}

/**
 * Searchable parent-issue picker. Renders a popover with a search box and a
 * filtered list; every option shows the issue's `#id` so the id is both visible
 * and searchable (matching by title OR by id substring).
 *
 * Shared by the quick-create dialog and the Epic hierarchy control so both
 * parent pickers behave identically.
 */
export function ParentIssueSelect({
  issues,
  value,
  onChange,
  disabled,
  allowNone = true,
  noneLabel,
  id,
  ariaLabel,
  triggerClassName,
  'data-testid': dataTestid,
}: ParentIssueSelectProps) {
  const { t } = useTranslation('projects')
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')

  const selected = value == null ? undefined : issues.find((i) => i.id === value)

  // Reset the search each time the popover opens (Radix auto-focuses the search
  // input as the first focusable element in the content).
  const handleOpenChange = (next: boolean) => {
    if (disabled) return
    if (next) setQuery('')
    setOpen(next)
  }

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return issues
    return issues.filter(
      (iss) =>
        iss.title.toLowerCase().includes(q) ||
        String(iss.id).includes(q),
    )
  }, [issues, query])

  const triggerLabel = selected
    ? `#${selected.id} ${selected.title}`
    : value != null
      ? `#${value}`
      : noneLabel

  const select = (next: number | null) => {
    onChange(next)
    setOpen(false)
  }

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <button
          type="button"
          id={id}
          aria-label={ariaLabel}
          data-testid={dataTestid}
          disabled={disabled}
          className={cn(
            'flex h-9 w-full items-center justify-between gap-2 rounded-md border border-input bg-background px-3 py-1 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50',
            triggerClassName,
          )}
        >
          <span className={cn('truncate', value == null && 'text-muted-foreground')}>
            {triggerLabel}
          </span>
          <ChevronDown className="h-4 w-4 shrink-0 opacity-60" aria-hidden="true" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] p-0">
        <div className="relative border-b border-border">
          <Search className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
          <Input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('parentSelect.searchPlaceholder')}
            className="h-9 rounded-none border-0 pl-8 focus-visible:ring-0 focus-visible:ring-offset-0"
          />
        </div>
        <div className="max-h-60 overflow-auto p-1">
          {allowNone && (
            <button
              type="button"
              onClick={() => select(null)}
              className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="flex h-4 w-4 shrink-0 items-center justify-center">
                {value == null && <Check className="h-4 w-4 text-brand" aria-hidden="true" />}
              </span>
              <span className="truncate text-muted-foreground">{noneLabel}</span>
            </button>
          )}
          {filtered.map((iss) => (
            <button
              key={iss.id}
              type="button"
              onClick={() => select(iss.id)}
              className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="flex h-4 w-4 shrink-0 items-center justify-center">
                {value === iss.id && <Check className="h-4 w-4 text-brand" aria-hidden="true" />}
              </span>
              <span className="shrink-0 tabular-nums text-muted-foreground">#{iss.id}</span>
              <span className="truncate">{iss.title}</span>
            </button>
          ))}
          {filtered.length === 0 && (
            <div className="px-2 py-6 text-center text-sm text-muted-foreground">
              {t('parentSelect.noResults')}
            </div>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}
