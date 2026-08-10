import { useMemo } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import type { Matcher } from '@/types/permission';

interface Props {
  toolName: string;
  toolInput: Record<string, unknown>;
  onPick: (m: Matcher) => void;
  trigger: ReactNode;
}

export function PermissionMatcherMenu({ toolName, toolInput, onPick, trigger }: Props) {
  const { t } = useTranslation();
  const options = useMemo(() => buildOptions(toolName, toolInput, t), [toolName, toolInput, t]);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>{trigger}</DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {options.map((o, i) => (
          <DropdownMenuItem
            key={`${o.kind}-${o.value}-${i}`}
            onSelect={() => onPick({ kind: o.kind, value: o.value })}
          >
            <span className="font-mono text-xs">{o.kind}</span>
            <span className="ml-2">{o.label}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function buildOptions(
  tool: string,
  input: Record<string, unknown>,
  t: TFunction,
): Array<{ kind: Matcher['kind']; value: string; label: string }> {
  if (tool === 'Bash') {
    const cmd = String(input.command ?? '');
    const tokens = cmd.split(/\s+/);
    const firstToken = tokens[0] ?? '';
    const firstTwo = tokens.slice(0, 2).join(' ');
    return [
      { kind: 'exact', value: cmd, label: t('workspaces:permission.matcher.onlyThisCommand', { cmd }) },
      { kind: 'prefix', value: firstTwo, label: t('workspaces:permission.matcher.prefix', { value: firstTwo }) },
      { kind: 'prefix', value: firstToken, label: t('workspaces:permission.matcher.prefix', { value: firstToken }) },
    ];
  }
  if (tool === 'Edit' || tool === 'Write') {
    const path = String(input.file_path ?? '');
    const dir = path.split('/').slice(0, -1).join('/');
    return [
      { kind: 'exact', value: path, label: t('workspaces:permission.matcher.onlyThisFile') },
      { kind: 'glob', value: `${dir}/**`, label: t('workspaces:permission.matcher.currentDir', { dir }) },
      { kind: 'glob', value: `**`, label: t('workspaces:permission.matcher.wholeWorkspace') },
    ];
  }
  if (tool === 'WebFetch') {
    const url = String(input.url ?? '');
    let host = '';
    try {
      host = new URL(url).host;
    } catch {
      /* ignore */
    }
    return [
      { kind: 'domain', value: host, label: t('workspaces:permission.matcher.domain', { host }) },
      { kind: 'exact', value: url, label: t('workspaces:permission.matcher.onlyThisURL') },
    ];
  }
  return [{ kind: 'any', value: '', label: t('workspaces:permission.matcher.allCalls') }];
}
