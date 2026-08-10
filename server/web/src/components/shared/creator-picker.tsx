import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { ChevronDown } from 'lucide-react';
import { listWorkspaceOverviewCreators } from '@/lib/api';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Button } from '@/components/ui/button';

interface Creator {
  id: number;
  username: string;
  display_name: string;
}

interface Props {
  value: number | null;
  onChange: (next: number | null) => void;
  /** Serialized OwnerFilter ('user:42' | 'org:acme' | null) — narrows
   *  the candidate list and cache key. */
  ownerFilter: string | null;
  /** When parent OwnerFilter is "personal", picker is visually hidden. */
  hidden?: boolean;
}

export function CreatorPicker({ value, onChange, ownerFilter, hidden }: Props) {
  const { t } = useTranslation('workspaces');

  // queryKey MUST include ownerFilter — otherwise switching org doesn't
  // refresh the candidate list (and the picker would still show the old
  // org's members). Spec sec 5.3 / architect review A2.
  const { data } = useQuery({
    queryKey: ['workspace-overview-creators', ownerFilter],
    queryFn: () => listWorkspaceOverviewCreators(ownerFilter ?? undefined),
    enabled: !hidden,
    staleTime: 60_000, // 1 min — creator set rarely changes
  });

  if (hidden) return null;

  const creators: Creator[] = data?.data ?? [];
  const selectedValue = value === null ? '_all' : String(value);

  const selectedLabel =
    value === null
      ? t('overview.filter.creator.all')
      : (creators.find((c) => c.id === value)?.display_name ||
          creators.find((c) => c.id === value)?.username ||
          String(value));

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          className="h-7 w-40 justify-between px-2 text-xs font-normal"
        >
          <span className="truncate">{selectedLabel}</span>
          <ChevronDown className="h-3 w-3 opacity-60 shrink-0" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-40">
        <DropdownMenuRadioGroup
          value={selectedValue}
          onValueChange={(v) => onChange(v === '_all' ? null : Number(v))}
        >
          <DropdownMenuRadioItem value="_all">
            {t('overview.filter.creator.all')}
          </DropdownMenuRadioItem>
          {creators.map((c) => (
            <DropdownMenuRadioItem key={c.id} value={String(c.id)}>
              {c.display_name || c.username}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
