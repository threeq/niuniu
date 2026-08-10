import { useState } from 'react';
import { PRESET_COLORS, isValidHex } from '../../lib/label-color';
import { useTranslation } from 'react-i18next';

type Props = { value: string; onChange: (hex: string) => void };

export function ColorPicker({ value, onChange }: Props) {
  const { t } = useTranslation('dialogs');
  const [custom, setCustom] = useState(
    value && !(PRESET_COLORS as readonly string[]).includes(value) ? value : ''
  );
  return (
    <div className="space-y-2">
      <div className="text-xs text-muted-foreground">{t('color.preset')}</div>
      <div className="flex gap-1.5 flex-wrap">
        {PRESET_COLORS.map(c => (
          <button key={c} type="button"
            onClick={() => { setCustom(''); onChange(c); }}
            className={`h-6 w-6 rounded ring-offset-1 transition ${value === c ? 'ring-2 ring-foreground' : ''}`}
            style={{ backgroundColor: c }} aria-label={c} />
        ))}
      </div>
      <div className="text-xs text-muted-foreground pt-1">{t('color.custom')}</div>
      <div className="flex items-center gap-2">
        <input type="color"
          value={isValidHex(custom) ? custom : '#000000'}
          onChange={e => { setCustom(e.target.value); onChange(e.target.value); }}
          className="h-7 w-10 rounded border border-border" />
        <input type="text" value={custom}
          onChange={e => { const v = e.target.value.toLowerCase(); setCustom(v); if (isValidHex(v)) onChange(v); }}
          placeholder="#rrggbb"
          className="bg-background border border-border rounded px-2 py-1 text-xs font-mono w-24" />
      </div>
    </div>
  );
}
