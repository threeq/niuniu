import { useTranslation } from 'react-i18next';
import { X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { PROJECT_COLOR_KEYS, getProjectColorStyles } from '@/lib/project-color';

export interface ProjectColorPickerProps {
  value: string | null;
  onChange: (next: string | null) => void;
  disabled?: boolean;
}

export function ProjectColorPicker({ value, onChange, disabled }: ProjectColorPickerProps) {
  const { t } = useTranslation('projects');
  return (
    <div className="space-y-1.5">
      <label className="text-xs font-medium text-muted-foreground">
        {t('settings.color.label')}
      </label>
      <div className="flex flex-wrap gap-1.5 items-center" role="radiogroup" aria-label={t('settings.color.label')}>
        {PROJECT_COLOR_KEYS.map((key) => {
          const sty = getProjectColorStyles(key);
          const selected = value === key;
          return (
            <button
              key={key}
              type="button"
              role="radio"
              aria-checked={selected}
              disabled={disabled}
              onClick={() => onChange(key)}
              aria-label={t(`settings.color.swatch.${key}`)}
              className={cn(
                'w-6 h-6 rounded-full transition-transform',
                sty.swatch,
                selected && 'ring-2 ring-offset-2 ring-offset-background ring-foreground',
                !disabled && !selected && 'hover:scale-110',
                disabled && 'opacity-50 cursor-not-allowed'
              )}
            />
          );
        })}
        <button
          type="button"
          role="radio"
          aria-checked={value === null}
          disabled={disabled}
          onClick={() => onChange(null)}
          aria-label={t('settings.color.clear')}
          className={cn(
            'w-6 h-6 rounded-full bg-muted/40 border border-warm-border flex items-center justify-center text-muted-foreground transition-transform',
            value === null && 'ring-2 ring-offset-2 ring-offset-background ring-foreground',
            !disabled && value !== null && 'hover:scale-110',
            disabled && 'opacity-50 cursor-not-allowed'
          )}
        >
          <X className="w-3 h-3" />
        </button>
      </div>
    </div>
  );
}
