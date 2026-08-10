import { useTranslation } from 'react-i18next';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import type { Project } from '@/types/api';

interface Props {
  projects: Project[];
  value: number | null;
  onChange: (projectId: number) => void;
  disabled?: boolean;
  className?: string;
}

// Shared project picker for approving / reassigning an owner-level chat. Only
// lists projects the caller can write to (passed in already filtered). Radix
// Select works on string values, so we bridge to the numeric project id.
export function ProjectSelect({ projects, value, onChange, disabled, className }: Props) {
  const { t } = useTranslation('settings');
  return (
    <Select
      value={value != null ? String(value) : undefined}
      onValueChange={(v) => onChange(Number(v))}
      disabled={disabled}
    >
      <SelectTrigger className={className} aria-label={t('imbot.projectSelectLabel')}>
        <SelectValue placeholder={t('imbot.projectSelectPlaceholder')} />
      </SelectTrigger>
      <SelectContent>
        {projects.map((p) => (
          <SelectItem key={p.id} value={String(p.id)}>
            {p.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
