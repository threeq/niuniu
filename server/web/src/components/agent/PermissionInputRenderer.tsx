import { useTranslation } from 'react-i18next';

interface Props {
  toolName: string;
  input: Record<string, unknown>;
}

export function PermissionInputRenderer({ toolName, input }: Props) {
  const { t } = useTranslation();
  switch (toolName) {
    // A data-proxy write (run_data_query) is confirmed through this card. Show
    // the target AND the value-free summary of the data about to be sent —
    // field names + size + sensitive-pattern hits — so the user approves the
    // DATA, not just the endpoint (Epic #726 / #747).
    case 'DataQuery':
      return <DataQueryPreview input={input} />;
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

interface EgressSummaryShape {
  bytes?: number;
  fields?: string[];
  sensitive_hits?: { pattern: string; count: number }[];
}

/** The data-proxy write preview: the target objects plus the value-free data
 *  summary of the payload this write will send. */
function DataQueryPreview({ input }: { input: Record<string, unknown> }) {
  const { t } = useTranslation();
  const objects = Array.isArray(input.objects) ? (input.objects as string[]) : [];
  const summary = input.data_summary as EgressSummaryShape | undefined;
  const target = [input.source, input.kind, input.mode]
    .map((v) => (v == null ? '' : String(v)))
    .filter(Boolean)
    .join(' · ');

  return (
    <div className="space-y-1">
      {target ? <p className="text-sm">{target}</p> : null}
      {objects.length > 0 ? (
        <p className="text-xs text-muted-foreground">
          {t('workspaces:permission.card.dataQueryObjects', { objects: objects.join(', ') })}
        </p>
      ) : null}
      {input.statement_summary ? (
        <pre className="rounded bg-muted px-2 py-1 text-xs font-mono">
          {String(input.statement_summary)}
        </pre>
      ) : null}
      {summary ? (
        <div className="rounded border border-border px-2 py-1 text-xs space-y-0.5">
          <p className="font-medium">{t('workspaces:permission.card.dataSummaryTitle')}</p>
          <p className="text-muted-foreground">
            {t('workspaces:permission.card.dataSummarySize', { bytes: summary.bytes ?? 0 })}
            {' · '}
            {t('workspaces:permission.card.dataSummaryFields', {
              fields:
                (summary.fields ?? []).join(', ') ||
                t('workspaces:permission.card.dataSummaryFieldsNone'),
            })}
          </p>
          {summary.sensitive_hits && summary.sensitive_hits.length > 0 ? (
            <p className="text-destructive">
              {t('workspaces:permission.card.dataSummarySensitive', {
                patterns: summary.sensitive_hits
                  .map((h) => `${h.pattern}×${h.count}`)
                  .join(', '),
              })}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
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
