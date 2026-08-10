import { useTranslation } from 'react-i18next';
import { ChevronDown, User, Users } from 'lucide-react';
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';

export type CreatorScope = 'mine' | 'all';

interface Props {
  value: CreatorScope;
  onChange: (next: CreatorScope) => void;
}

export function CreatorScopeChip({ value, onChange }: Props) {
  const { t } = useTranslation('workspaces');
  const Icon = value === 'mine' ? User : Users;
  const label =
    value === 'mine'
      ? t('workspace.scope.mine')
      : t('workspace.scope.all');

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          className="h-6 gap-1 px-2 text-[10px] shrink-0"
        >
          <Icon className="h-3 w-3" />
          {label}
          <ChevronDown className="h-3 w-3 opacity-60" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-48 p-1" align="start">
        <button
          type="button"
          onClick={() => onChange('mine')}
          className={cn(
            'w-full text-left text-xs rounded px-2 py-1.5 hover:bg-accent',
            value === 'mine' && 'bg-accent font-medium'
          )}
        >
          {t('workspace.scope.popover.mine')}
        </button>
        <button
          type="button"
          onClick={() => onChange('all')}
          className={cn(
            'w-full text-left text-xs rounded px-2 py-1.5 hover:bg-accent',
            value === 'all' && 'bg-accent font-medium'
          )}
        >
          {t('workspace.scope.popover.all')}
        </button>
      </PopoverContent>
    </Popover>
  );
}
