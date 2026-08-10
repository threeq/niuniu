import { useEffect, useId } from 'react';
import { useTranslation } from 'react-i18next';
import { User, Building2 } from 'lucide-react';
import { useOrgStore } from '@/stores/org-store';
import type { OwnerRef } from '@/types/org';

interface Props {
  value: OwnerRef;
  onChange: (o: OwnerRef) => void;
  userId: number;
  autoSelectDefault?: boolean;
  disabled?: boolean;
}

export function OwnerPicker({ value, onChange, userId, autoSelectDefault = true, disabled = false }: Props) {
  const { t } = useTranslation('common');
  const selectId = useId();
  const myOrgs = useOrgStore((s) => s.myOrgs);

  // Load last-used owner from localStorage, validating it against current orgs.
  useEffect(() => {
    if (!autoSelectDefault) return;
    if (value.id) return;  // already set
    const key = `lastOwner:${userId}`;
    const saved = localStorage.getItem(key);
    if (saved) {
      try {
        const parsed = JSON.parse(saved) as OwnerRef;
        // If the saved owner is an org, verify the user is still a member.
        if (parsed.type === 'org') {
          const stillMember = myOrgs.some((o) => o.id === parsed.id);
          if (!stillMember) {
            // Org no longer accessible — fall back to personal owner.
            localStorage.removeItem(key);
            onChange({ type: 'user', id: userId });
            return;
          }
        }
        onChange(parsed);
      } catch {
        // ignore malformed saved value
      }
    }
  }, [autoSelectDefault, userId, value.id, onChange, myOrgs]);

  // Suppress when user has no orgs — always personal.
  if (myOrgs.length === 0) return null;

  const handleChange = (o: OwnerRef) => {
    localStorage.setItem(`lastOwner:${userId}`, JSON.stringify(o));
    onChange(o);
  };

  const currentKey = `${value.type}:${value.id}`;

  const CurrentIcon = value.type === 'org' ? Building2 : User;

  return (
    <div className="space-y-1">
      <label htmlFor={selectId} className="flex items-center gap-1 text-sm font-medium">
        <CurrentIcon className="h-3.5 w-3.5 text-muted-foreground" />
        {t('ownerFilter.label')}
      </label>
      <select
        id={selectId}
        aria-label={t('ownerFilter.label')}
        value={currentKey}
        disabled={disabled}
        onChange={(e) => {
          const [type, idStr] = e.target.value.split(':');
          handleChange({ type: type as 'user' | 'org', id: Number(idStr) });
        }}
        className="w-full px-3 py-2 border rounded-md bg-background disabled:cursor-not-allowed disabled:opacity-50"
      >
        <option value={`user:${userId}`}>{t('ownerFilter.personal')}</option>
        {myOrgs.map((o) => (
          <option key={o.id} value={`org:${o.id}`}>{o.name}</option>
        ))}
      </select>
    </div>
  );
}
