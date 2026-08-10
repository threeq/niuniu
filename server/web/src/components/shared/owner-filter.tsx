import { useTranslation } from 'react-i18next';
import { User, Building2, ListFilter, Check, ChevronDown } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu';
import { useOrgStore } from '@/stores/org-store';
import type { OwnerRef } from '@/types/org';

interface Props {
  value: OwnerRef[] | 'all';
  onChange: (v: OwnerRef[] | 'all') => void;
  userId: number;
}

/**
 * Compact owner (person / organization) filter. Renders as a single dropdown
 * button rather than a strip of pills so it stays clean no matter how many orgs
 * the user can access. Suppressed entirely when the user has no orgs.
 */
export function OwnerFilter({ value, onChange, userId }: Props) {
  const { t } = useTranslation('common');
  const myOrgs = useOrgStore((s) => s.myOrgs);
  if (myOrgs.length === 0) return null;

  const isAll = value === 'all';
  const isPersonal = !isAll && containsUser(value, userId);
  const selectedOrg = isAll ? undefined : myOrgs.find((o) => containsOrg(value, o.id));

  const CurrentIcon = isPersonal ? User : selectedOrg ? Building2 : ListFilter;
  const currentLabel = isPersonal
    ? t('ownerFilter.personal')
    : selectedOrg
      ? selectedOrg.name
      : t('ownerFilter.all');

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" className="gap-1.5">
          <CurrentIcon className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="max-w-40 truncate">{currentLabel}</span>
          <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="max-h-80 overflow-y-auto">
        <DropdownMenuItem onClick={() => onChange('all')}>
          <ListFilter className="h-4 w-4 text-muted-foreground" />
          <span className="flex-1">{t('ownerFilter.all')}</span>
          {isAll && <Check className="h-4 w-4" />}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => onChange([{ type: 'user', id: userId }])}>
          <User className="h-4 w-4 text-muted-foreground" />
          <span className="flex-1">{t('ownerFilter.personal')}</span>
          {isPersonal && <Check className="h-4 w-4" />}
        </DropdownMenuItem>
        {myOrgs.length > 0 && <DropdownMenuSeparator />}
        {myOrgs.map((o) => (
          <DropdownMenuItem key={o.id} onClick={() => onChange([{ type: 'org', id: o.id }])}>
            <Building2 className="h-4 w-4 text-muted-foreground" />
            <span className="flex-1 truncate">{o.name}</span>
            {selectedOrg?.id === o.id && <Check className="h-4 w-4" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function containsUser(v: OwnerRef[] | 'all', id: number): boolean {
  return Array.isArray(v) && v.some((o) => o.type === 'user' && o.id === id);
}
function containsOrg(v: OwnerRef[] | 'all', id: number): boolean {
  return Array.isArray(v) && v.some((o) => o.type === 'org' && o.id === id);
}
