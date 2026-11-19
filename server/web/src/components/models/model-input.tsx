import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

/**
 * A provider model field backed by the shared model library: the input still
 * accepts any name (the library never gates what a provider can save), the
 * datalist offers the owner's known names for search/pick, and a name that is
 * not in the library yet surfaces a one-click "add to library" action.
 */
export function ModelInput({
  value,
  onChange,
  models,
  onAdd,
  placeholder,
  id,
}: {
  value: string
  onChange: (value: string) => void
  models: string[]
  onAdd?: (name: string) => void
  placeholder?: string
  id?: string
}) {
  const { t } = useTranslation('settings')
  const generatedId = useId()
  const listId = `${generatedId}-model-list`
  const trimmed = value.trim()
  const missing = trimmed !== '' && !models.includes(trimmed)

  return (
    <div className="flex gap-1">
      <Input
        id={id}
        list={listId}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
      <datalist id={listId}>
        {models.map((model) => (
          <option key={model} value={model} />
        ))}
      </datalist>
      {missing && onAdd ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="shrink-0"
          onClick={() => onAdd(trimmed)}
        >
          {t('models.addToLibrary')}
        </Button>
      ) : null}
    </div>
  )
}
