// CapabilityDialog — "what the agent sees" for an external API provider or a
// data source (Epic #726 / #738).
//
// Shows the two-layer operable capability description (structured operations /
// scope + limits + worked examples) generated from the confirmed contract, plus
// the EXACT block injected into the workspace instruction file. Read-only: the
// description is generated server-side and cannot be edited here.

import { useTranslation } from 'react-i18next';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Badge } from '@/components/ui/badge';
import { useCapability } from '@/hooks/useCapability';
import type { CapabilityTargetKind } from '@/types/capability';

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  kind: CapabilityTargetKind;
  /** The provider name or data source id/name to describe. */
  target?: string | number;
  title: string;
}

export function CapabilityDialog({ open, onOpenChange, kind, target, title }: Props) {
  const { t } = useTranslation('settings');
  const { data, isLoading, isError } = useCapability(kind, target, open);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {t('integrations.capability.title', { name: title })}
          </DialogTitle>
          <DialogDescription>
            {t('integrations.capability.description')}
          </DialogDescription>
        </DialogHeader>

        <div className="max-h-96 space-y-4 overflow-y-auto pr-1 text-sm">
          {isLoading && (
            <p className="text-warm-text-muted">
              {t('integrations.capability.loading')}
            </p>
          )}
          {isError && (
            <p className="text-destructive">
              {t('integrations.capability.loadFailed')}
            </p>
          )}

          {data && (
            <>
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="secondary">
                  {data.doc.target_kind === 'provider'
                    ? t('integrations.capability.kindProvider')
                    : t('integrations.capability.kindDataSource')}
                </Badge>
                <code className="text-xs text-warm-text-muted">
                  {data.doc.tool}
                </code>
              </div>

              <p>{data.doc.usage}</p>

              {data.doc.operations && data.doc.operations.length > 0 && (
                <section className="space-y-1">
                  <h3 className="text-xs font-medium">
                    {t('integrations.capability.operations')}
                  </h3>
                  <ul className="space-y-1">
                    {data.doc.operations.map((op) => (
                      <li key={op.name} className="flex flex-wrap items-baseline gap-2">
                        <code className="text-xs">{op.name}</code>
                        <span className="text-xs text-warm-text-muted">
                          {op.method} {op.path}
                        </span>
                        <Badge variant={op.read_only ? 'secondary' : 'outline'}>
                          {op.read_only
                            ? t('integrations.capability.readOnly')
                            : t('integrations.capability.write')}
                        </Badge>
                        {op.summary && (
                          <span className="text-xs text-warm-text-muted">
                            {op.summary}
                          </span>
                        )}
                        {op.notes && (
                          <span className="text-xs text-warm-text-muted">
                            {op.notes}
                          </span>
                        )}
                      </li>
                    ))}
                  </ul>
                </section>
              )}

              {data.doc.scope && (
                <section className="space-y-1">
                  <h3 className="text-xs font-medium">
                    {t('integrations.capability.scope')}
                  </h3>
                  <ul className="space-y-0.5 text-xs text-warm-text-muted">
                    <li>
                      {t('integrations.capability.scopeKind')}: {data.doc.scope.kind}
                    </li>
                    {data.doc.scope.databases &&
                      data.doc.scope.databases.length > 0 && (
                        <li>
                          {t('integrations.capability.scopeDatabases')}:{' '}
                          {data.doc.scope.databases.join(', ')}
                        </li>
                      )}
                    {data.doc.scope.allow.length > 0 && (
                      <li>
                        {t('integrations.capability.scopeAllow')}:{' '}
                        {data.doc.scope.allow.join(', ')}
                      </li>
                    )}
                    {data.doc.scope.deny && data.doc.scope.deny.length > 0 && (
                      <li>
                        {t('integrations.capability.scopeDeny')}:{' '}
                        {data.doc.scope.deny.join(', ')}
                      </li>
                    )}
                    <li>
                      {t('integrations.capability.scopeAccessMode')}:{' '}
                      {data.doc.scope.default_access_mode}
                    </li>
                  </ul>
                </section>
              )}

              {data.doc.examples && data.doc.examples.length > 0 && (
                <section className="space-y-1">
                  <h3 className="text-xs font-medium">
                    {t('integrations.capability.examples')}
                  </h3>
                  <ul className="space-y-1">
                    {data.doc.examples.map((ex) => (
                      <li key={ex.call} className="space-y-1">
                        <div className="text-xs text-warm-text-muted">{ex.task}</div>
                        <pre className="overflow-x-auto whitespace-pre-wrap rounded-md bg-muted p-2 text-xs">
                          {ex.call}
                        </pre>
                      </li>
                    ))}
                  </ul>
                </section>
              )}

              {data.doc.limits && data.doc.limits.length > 0 && (
                <section className="space-y-1">
                  <h3 className="text-xs font-medium">
                    {t('integrations.capability.limits')}
                  </h3>
                  <ul className="list-inside list-disc space-y-0.5 text-xs text-warm-text-muted">
                    {data.doc.limits.map((l) => (
                      <li key={l}>{l}</li>
                    ))}
                  </ul>
                </section>
              )}

              <section className="space-y-1">
                <h3 className="text-xs font-medium">
                  {t('integrations.capability.agentBlock')}
                </h3>
                <p className="text-xs text-warm-text-muted">
                  {t('integrations.capability.agentBlockHint')}
                </p>
                <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-2 text-xs">
                  {data.agent_block}
                </pre>
              </section>
            </>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
