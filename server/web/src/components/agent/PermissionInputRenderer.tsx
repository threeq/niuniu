import { useTranslation } from 'react-i18next';

interface Props {
  toolName: string;
  input: Record<string, unknown>;
}

export function PermissionInputRenderer({ toolName, input }: Props) {
  const { t } = useTranslation();
  switch (toolName) {
    case 'Bash':
      return (
        <div className="space-y-1">
          <pre className="rounded bg-muted px-2 py-1 text-xs font-mono">{String(input.command ?? '')}</pre>
          {input.description ? (
            <p className="text-xs text-muted-foreground">{String(input.description)}</p>
          ) : null}
        </div>
      );
    case 'Edit':
    case 'Write':
      return (
        <div className="space-y-1">
          <code className="block text-sm">{String(input.file_path ?? '')}</code>
          {toolName === 'Edit' ? (
            <DiffPreview oldStr={String(input.old_string ?? '')} newStr={String(input.new_string ?? '')} />
          ) : (
            <pre className="rounded bg-muted px-2 py-1 text-xs">{truncate(String(input.content ?? ''), 800)}</pre>
          )}
        </div>
      );
    case 'WebFetch':
      return (
        <div className="space-y-1">
          <a className="text-sm underline" href={String(input.url ?? '')} target="_blank" rel="noreferrer">
            {String(input.url ?? '')}
          </a>
          {input.prompt ? <p className="text-xs">{String(input.prompt)}</p> : null}
        </div>
      );
    case 'Task':
      return (
        <div className="space-y-1">
          <p className="text-sm">{String(input.description ?? '')}</p>
          <p className="text-xs text-muted-foreground">
            {t('workspaces:permission.card.subagent', {
              type: String(input.subagent_type ?? t('workspaces:permission.card.fallbackTool')),
            })}
          </p>
        </div>
      );
    default:
      return (
        <details>
          <summary className="text-xs">{t('workspaces:permission.card.toolInputJson')}</summary>
          <pre className="text-xs">{JSON.stringify(input, null, 2)}</pre>
        </details>
      );
  }
}

function truncate(s: string, n: number) {
  return s.length > n ? s.slice(0, n) + '…' : s;
}

function DiffPreview({ oldStr, newStr }: { oldStr: string; newStr: string }) {
  const oldLines = oldStr.split('\n').slice(0, 5);
  const newLines = newStr.split('\n').slice(0, 5);
  return (
    <div className="grid grid-cols-2 gap-2 text-xs">
      <pre className="rounded bg-red-50 p-1 dark:bg-red-950">{oldLines.join('\n')}</pre>
      <pre className="rounded bg-green-50 p-1 dark:bg-green-950">{newLines.join('\n')}</pre>
    </div>
  );
}
