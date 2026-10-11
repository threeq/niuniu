import { useId, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { FileWarning, ShieldAlert } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import { Label } from '@/components/ui/label';
import type {
  ConfirmCardModel,
  ConfirmDecisionBody,
  ConfirmRisk,
  QuarantinedChunk,
  UntrustedOrigin,
} from '@/types/integration-confirm';

interface Props {
  model: ConfirmCardModel;
  onDecide: (body: ConfirmDecisionBody) => void;
  /** Disable both actions while a decision is in flight. */
  pending?: boolean;
}

/**
 * Renders an integration authorization decision from structured facts only.
 *
 * The decision area (`data-region="decision"`) is built exclusively from the
 * model's structured fields — the operations and scopes the system will
 * enforce. Untrusted document/model prose is confined to the visually separate
 * advisory region (`data-region="advisory"`) and is never a decision input.
 *
 * High-risk items default to unchecked and each must be ticked before the
 * confirm action unlocks, so a risky draft cannot be rubber-stamped with a
 * single click.
 */
export function IntegrationConfirmCard({ model, onDecide, pending = false }: Props) {
  const { t } = useTranslation('integrationConfirm');
  const [acknowledged, setAcknowledged] = useState<Record<string, boolean>>({});

  const ackKeys = useMemo(() => model.risks.map((r) => r.ack_key), [model.risks]);
  const allAcknowledged = ackKeys.every((key) => acknowledged[key]);
  const advisory = model.advisory ?? [];

  const riskLabel = (risk: ConfirmRisk) =>
    t(`risks.${risk.code}`, { target: risk.target, detail: risk.detail ?? '' });

  return (
    <Card className="border-warning/40" data-testid="integration-confirm-card">
      <CardHeader className="py-3">
        <CardTitle className="flex items-center gap-2 text-sm">
          <ShieldAlert className="h-4 w-4 text-warning" />
          <span>{t('title')}</span>
          <span className="ml-auto text-xs font-normal text-warm-text-muted">
            {model.title}
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        {/* Decision area — structured facts only, no untrusted prose. */}
        <div data-region="decision" className="space-y-4">
          <SourceRow origin={model.origin} />

          <section className="space-y-1">
            <h4 className="text-xs font-medium text-warm-text-muted">
              {t('operations.title')}
            </h4>
            {model.operations.length === 0 ? (
              <p className="text-xs text-warm-text-muted">{t('operations.empty')}</p>
            ) : (
              <ul className="divide-y divide-warm-border overflow-hidden rounded-md border border-warm-border">
                {model.operations.map((op, i) => (
                  <li
                    key={`${op.method} ${op.path} ${i}`}
                    className="flex items-center gap-2 px-2 py-1.5"
                  >
                    <code className="rounded bg-warm-muted px-1.5 py-0.5 text-xs font-medium">
                      {op.method}
                    </code>
                    <code className="truncate text-xs">{op.path}</code>
                    <span className="truncate text-xs text-warm-text-muted">
                      {op.name}
                    </span>
                    <span className="ml-auto flex shrink-0 items-center gap-1">
                      {op.high_risk && (
                        <Badge variant="outline" className="border-warning/40 text-warning">
                          {t('operations.highRisk')}
                        </Badge>
                      )}
                      <Badge variant="secondary">
                        {op.read_only ? t('operations.readOnly') : t('operations.write')}
                      </Badge>
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="space-y-1">
            <h4 className="text-xs font-medium text-warm-text-muted">
              {t('scopes.title')}
            </h4>
            {model.scopes.length === 0 ? (
              <p className="text-xs text-warm-text-muted">{t('scopes.empty')}</p>
            ) : (
              <ul className="space-y-1">
                {model.scopes.map((scope) => (
                  <li key={scope.key} className="flex items-center gap-2 text-xs">
                    <code className="font-medium">{scope.key}</code>
                    <span className="truncate text-warm-text-muted">
                      {scope.values.length > 0 ? scope.values.join(', ') : '—'}
                    </span>
                    <Badge variant="outline" className="ml-auto shrink-0">
                      {t(`scopes.change.${scope.change}`)}
                    </Badge>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="space-y-1">
            <h4 className="text-xs font-medium text-warm-text-muted">
              {t('diff.title')}
            </h4>
            {model.diff.length === 0 ? (
              <p className="text-xs text-warm-text-muted">{t('diff.empty')}</p>
            ) : (
              <ul className="space-y-1">
                {model.diff.map((d, i) => (
                  <li key={`${d.field} ${i}`} className="text-xs">
                    <span className="font-medium">{diffFieldLabel(d.field, t)}</span>
                    <span className="ml-2 text-warm-text-muted">
                      {[d.before, d.after].filter(Boolean).join(' → ')}
                    </span>
                    <span className="ml-2 text-warm-text-muted">
                      ({t(`scopes.change.${d.change}`)})
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="space-y-2">
            <h4 className="text-xs font-medium text-warm-text-muted">
              {t('risks.title')}
            </h4>
            {model.risks.length === 0 ? (
              <p className="text-xs text-warm-text-muted">{t('risks.empty')}</p>
            ) : (
              <ul className="space-y-2">
                {model.risks.map((risk) => (
                  <RiskRow
                    key={risk.ack_key}
                    risk={risk}
                    label={riskLabel(risk)}
                    checked={Boolean(acknowledged[risk.ack_key])}
                    disabled={pending}
                    onCheckedChange={(next) =>
                      setAcknowledged((prev) => ({ ...prev, [risk.ack_key]: next }))
                    }
                  />
                ))}
              </ul>
            )}
          </section>
        </div>

        {advisory.length > 0 && <AdvisoryRegion chunks={advisory} />}

        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            disabled={pending || !allAcknowledged}
            onClick={() => onDecide({ decision: 'approve', acknowledged: ackKeys })}
          >
            {t('confirm')}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={pending}
            onClick={() => onDecide({ decision: 'reject', acknowledged: [] })}
          >
            {t('reject')}
          </Button>
          {!allAcknowledged && (
            <span className="text-xs text-warm-text-muted">{t('acknowledgeHint')}</span>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function SourceRow({ origin }: { origin: UntrustedOrigin }) {
  const { t } = useTranslation('integrationConfirm');
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <span className="text-warm-text-muted">{t('source.label')}</span>
      <Badge variant="outline">{t(`source.kind.${origin.kind}`)}</Badge>
      {origin.ref && <code className="truncate text-warm-text-muted">{origin.ref}</code>}
      <span className="text-warning">{t('source.untrusted')}</span>
    </div>
  );
}

function RiskRow({
  risk,
  label,
  checked,
  disabled,
  onCheckedChange,
}: {
  risk: ConfirmRisk;
  label: string;
  checked: boolean;
  disabled: boolean;
  onCheckedChange: (next: boolean) => void;
}) {
  const id = useId();
  return (
    <li className="flex items-start gap-2">
      <Checkbox
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={(state) => onCheckedChange(state === true)}
        className="mt-0.5"
      />
      <Label htmlFor={id} className="text-xs font-normal leading-snug">
        <span className="font-medium text-warning">{label}</span>
        <code className="ml-2 text-warm-text-muted">{risk.ack_key}</code>
      </Label>
    </li>
  );
}

/**
 * Untrusted prose, rendered outside the decision region behind a warning.
 * Nothing here is a decision input — it exists so a human can inspect what the
 * source document actually said.
 */
function AdvisoryRegion({ chunks }: { chunks: QuarantinedChunk[] }) {
  const { t } = useTranslation('integrationConfirm');
  return (
    <section
      data-region="advisory"
      className="space-y-2 rounded-md border border-dashed border-warm-border bg-warm-muted/50 p-2"
    >
      <h4 className="flex items-center gap-1 text-xs font-medium text-warm-text-muted">
        <FileWarning className="h-3.5 w-3.5" />
        {t('advisory.title')}
      </h4>
      <p className="text-xs text-warm-text-muted">{t('advisory.notice')}</p>
      <ul className="space-y-1">
        {chunks.map((chunk, i) => (
          <li key={`${chunk.where} ${i}`} className="text-xs">
            <span className="text-warm-text-muted">
              {t(`advisory.reason.${chunk.reason}`)} · <code>{chunk.where}</code>
            </span>
            <p className="whitespace-pre-wrap break-words">{chunk.text}</p>
          </li>
        ))}
      </ul>
    </section>
  );
}

function diffFieldLabel(field: string, t: TFunction): string {
  if (field.startsWith('scope.')) {
    return `${t('diff.field.scope')}: ${field.slice('scope.'.length)}`;
  }
  return t(`diff.field.${field}`);
}
