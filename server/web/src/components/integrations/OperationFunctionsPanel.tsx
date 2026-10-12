import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { AlertTriangle, Play, RefreshCw, ShieldCheck, Trash2 } from 'lucide-react';

import { operationFunctionsApi } from '@/lib/operation-functions-api';
import type {
  FenceAvailability,
  FunctionCallRecord,
  OperationFunction,
} from '@/types/operation-function';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';

// Operation functions (Epic #726 / #739).
//
// Two things this panel exists to make visible, neither of which has anywhere
// else to live:
//
//  1. Whether the OS fence is actually enforcing anything on THIS host. A
//     platform where the sandbox could not be established refuses to run
//     functions, and the refusal must be seen rather than experienced as a
//     mysterious per-call failure.
//  2. What a function does. Each row shows its declared reach and its first-run
//     observation trail — the request log the observation window exists to
//     produce.

function opsLabel(fn: OperationFunction): string[] {
  return fn.egress.map(o =>
    o.operation ? o.operation : `${o.method ?? ''} ${o.path ?? ''}`.trim(),
  );
}

function statusVariant(status: OperationFunction['status']): 'default' | 'secondary' | 'destructive' {
  if (status === 'active') return 'default';
  if (status === 'observing') return 'secondary';
  return 'destructive';
}

export function OperationFunctionsPanel() {
  const { t } = useTranslation('settings');
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<OperationFunction | null>(null);
  const [runInput, setRunInput] = useState('{}');
  const [runError, setRunError] = useState<string | null>(null);
  const [runOutput, setRunOutput] = useState<string | null>(null);
  const [rollbackScopes, setRollbackScopes] = useState<string[] | null>(null);
  // The version the re-confirmation is about. Guessing it from the list (e.g.
  // "the first inactive one") rolls back something the user did not click.
  const [pendingRollback, setPendingRollback] = useState<number | null>(null);
  const [selfTestError, setSelfTestError] = useState<string | null>(null);

  const fence = useQuery<FenceAvailability>({
    queryKey: ['operation-functions', 'sandbox'],
    queryFn: () => operationFunctionsApi.sandbox(),
  });
  const functions = useQuery<OperationFunction[]>({
    queryKey: ['operation-functions', 'list'],
    queryFn: () => operationFunctionsApi.list(),
  });
  const audit = useQuery<FunctionCallRecord[]>({
    queryKey: ['operation-functions', 'audit'],
    queryFn: () => operationFunctionsApi.audit(50),
  });
  const versions = useQuery({
    queryKey: ['operation-functions', selected?.id, 'versions'],
    queryFn: () => operationFunctionsApi.versions(selected!.id),
    enabled: !!selected,
  });
  const observation = useQuery({
    queryKey: ['operation-functions', selected?.id, 'observation'],
    queryFn: () => operationFunctionsApi.observation(selected!.id),
    enabled: !!selected,
  });

  const refreshAll = () => {
    void queryClient.invalidateQueries({ queryKey: ['operation-functions'] });
  };

  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) =>
      operationFunctionsApi.setEnabled(id, enabled),
    onSuccess: refreshAll,
  });
  const remove = useMutation({
    mutationFn: (id: number) => operationFunctionsApi.remove(id),
    onSuccess: () => {
      setSelected(null);
      refreshAll();
    },
  });
  const run = useMutation({
    mutationFn: ({ id, input }: { id: number; input: unknown }) =>
      operationFunctionsApi.invoke(id, { input }),
    onSuccess: result => {
      setRunError(null);
      setRunOutput(JSON.stringify(result, null, 2));
      refreshAll();
    },
    onError: (err: unknown) => {
      setRunOutput(null);
      setRunError(err instanceof Error ? err.message : String(err));
    },
  });
  const rollback = useMutation({
    mutationFn: ({ id, versionId, confirmed }: { id: number; versionId: number; confirmed: boolean }) =>
      operationFunctionsApi.rollback(id, { version_id: versionId, confirmed }),
    onSuccess: (result, variables) => {
      if (result.requires_confirmation && result.changed_scopes) {
        setPendingRollback(variables.versionId);
        setRollbackScopes(result.changed_scopes.map(sc => `${sc.key}: ${sc.before} → ${sc.after}`));
        return;
      }
      setPendingRollback(null);
      setRollbackScopes(null);
      refreshAll();
    },
  });
  const selfTest = useMutation({
    mutationFn: () => operationFunctionsApi.sandboxSelfTest(),
    onSuccess: () => {
      setSelfTestError(null);
      void queryClient.invalidateQueries({ queryKey: ['operation-functions', 'sandbox'] });
    },
    // A host whose fence is missing answers 503 here. Swallowing that would
    // leave the user staring at a button that appears to do nothing.
    onError: (err: unknown) => {
      setSelfTestError(err instanceof Error ? err.message : String(err));
    },
  });

  const list = functions.data ?? [];
  const auditRows = audit.data ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('integrations.functions.title')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {fence.data && <FenceBanner fence={fence.data} />}

        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => selfTest.mutate()}
            disabled={selfTest.isPending}
          >
            <ShieldCheck className="mr-2 h-4 w-4" />
            {t('integrations.functions.runSelfTest')}
          </Button>
          <Button variant="ghost" size="sm" onClick={refreshAll}>
            <RefreshCw className="mr-2 h-4 w-4" />
            {t('integrations.functions.refresh')}
          </Button>
        </div>

        {selfTestError && <p className="text-sm text-destructive">{selfTestError}</p>}

        {selfTest.data && (
          <ul className="flex flex-col gap-1 text-sm">
            {selfTest.data.probes.map(p => (
              <li key={p.name} className="flex items-center gap-2">
                <Badge variant={p.denied ? 'default' : 'destructive'}>
                  {p.denied ? t('integrations.functions.denied') : t('integrations.functions.allowed')}
                </Badge>
                <span className="font-medium">{p.name}</span>
                <span className="text-muted-foreground truncate">{p.error}</span>
              </li>
            ))}
          </ul>
        )}

        {list.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('integrations.functions.empty')}</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {list.map(fn => (
              <li key={fn.id} className="flex flex-col gap-2 rounded-md border p-3">
                <div className="flex items-center justify-between gap-2">
                  <div className="flex min-w-0 flex-col">
                    <span className="truncate font-medium">{fn.name}</span>
                    <span className="truncate text-xs text-muted-foreground">{fn.tool_name}</span>
                  </div>
                  <div className="flex items-center gap-2">
                    <Badge variant={statusVariant(fn.status)}>
                      {t(`integrations.functions.status.${fn.status}`)}
                    </Badge>
                    {fn.idempotent && (
                      <Badge variant="outline">{t('integrations.functions.idempotent')}</Badge>
                    )}
                    <Switch
                      checked={fn.enabled}
                      onCheckedChange={enabled => toggle.mutate({ id: fn.id, enabled })}
                      aria-label={t('integrations.functions.toggleEnabled')}
                    />
                    <Button variant="ghost" size="sm" onClick={() => setSelected(fn)}>
                      {t('integrations.functions.details')}
                    </Button>
                  </div>
                </div>
                <div className="flex flex-wrap gap-1">
                  {opsLabel(fn).map(op => (
                    <Badge key={op} variant="secondary">
                      {op}
                    </Badge>
                  ))}
                </div>
                {fn.observing && (
                  <p className="text-xs text-muted-foreground">
                    {t('integrations.functions.observing', {
                      calls: fn.observation_calls,
                      limit: fn.observation_limit,
                    })}
                  </p>
                )}
              </li>
            ))}
          </ul>
        )}

        {selected && (
          <div className="flex flex-col gap-3 rounded-md border p-3">
            <div className="flex items-center justify-between">
              <h3 className="font-medium">{selected.name}</h3>
              <div className="flex gap-2">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setSelected(null);
                    setRunOutput(null);
                    setRunError(null);
                    setRollbackScopes(null);
                    setPendingRollback(null);
                  }}
                >
                  {t('integrations.functions.close')}
                </Button>
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => remove.mutate(selected.id)}
                  disabled={remove.isPending}
                >
                  <Trash2 className="mr-2 h-4 w-4" />
                  {t('integrations.functions.delete')}
                </Button>
              </div>
            </div>

            <pre className="max-h-64 overflow-auto rounded bg-muted p-2 text-xs">
              <code>{selected.code}</code>
            </pre>
            <p className="text-xs text-muted-foreground">{selected.code_hash}</p>

            <div className="flex flex-col gap-2">
              <Textarea
                value={runInput}
                onChange={e => setRunInput(e.target.value)}
                rows={3}
                aria-label={t('integrations.functions.runInputLabel')}
              />
              <Button
                size="sm"
                variant="secondary"
                disabled={run.isPending}
                onClick={() => {
                  try {
                    const parsed = JSON.parse(runInput || '{}');
                    run.mutate({ id: selected.id, input: parsed });
                  } catch (err) {
                    setRunOutput(null);
                    setRunError(err instanceof Error ? err.message : String(err));
                  }
                }}
              >
                <Play className="mr-2 h-4 w-4" />
                {t('integrations.functions.run')}
              </Button>
              {runError && <p className="text-sm text-destructive">{runError}</p>}
              {runOutput && (
                <pre className="max-h-64 overflow-auto rounded bg-muted p-2 text-xs">
                  <code>{runOutput}</code>
                </pre>
              )}
            </div>

            <div className="flex flex-col gap-1">
              <h4 className="text-sm font-medium">{t('integrations.functions.versions')}</h4>
              {(versions.data ?? []).map(v => (
                <div key={v.id} className="flex items-center justify-between text-xs">
                  <span>
                    v{v.version} · {v.code_hash.slice(0, 19)}
                    {v.active ? ` · ${t('integrations.functions.activeVersion')}` : ''}
                  </span>
                  {!v.active && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() =>
                        rollback.mutate({ id: selected.id, versionId: v.id, confirmed: false })
                      }
                    >
                      {t('integrations.functions.rollback')}
                    </Button>
                  )}
                </div>
              ))}
              {rollbackScopes && (
                <div className="rounded-md border border-destructive p-2 text-xs">
                  <p className="flex items-center gap-1 font-medium">
                    <AlertTriangle className="h-3 w-3" />
                    {t('integrations.functions.rollbackWidens')}
                  </p>
                  <ul className="mt-1 flex flex-col gap-1">
                    {rollbackScopes.map(s => (
                      <li key={s}>{s}</li>
                    ))}
                  </ul>
                  <Button
                    variant="destructive"
                    size="sm"
                    className="mt-2"
                    disabled={pendingRollback === null}
                    onClick={() => {
                      if (pendingRollback === null) return;
                      rollback.mutate({
                        id: selected.id,
                        versionId: pendingRollback,
                        confirmed: true,
                      });
                    }}
                  >
                    {t('integrations.functions.confirmRollback')}
                  </Button>
                </div>
              )}
            </div>

            <div className="flex flex-col gap-1">
              <h4 className="text-sm font-medium">{t('integrations.functions.observationTrail')}</h4>
              {(observation.data ?? []).length === 0 ? (
                <p className="text-xs text-muted-foreground">
                  {t('integrations.functions.noObservation')}
                </p>
              ) : (
                <ul className="flex flex-col gap-1 text-xs">
                  {(observation.data ?? []).map(row => (
                    <li key={row.id} className="flex items-center gap-2">
                      <Badge variant={row.status === 'ok' ? 'default' : 'destructive'}>
                        {row.status}
                      </Badge>
                      <span>{row.caller}</span>
                      <span>{row.duration_ms}ms</span>
                      <span className="truncate">{(row.egress_ops ?? []).join(', ')}</span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        )}

        <div className="flex flex-col gap-1">
          <h4 className="text-sm font-medium">{t('integrations.functions.audit')}</h4>
          {auditRows.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t('integrations.functions.noAudit')}</p>
          ) : (
            <ul className="flex flex-col gap-1 text-xs">
              {auditRows.map(row => (
                <li key={row.id} className="flex items-center gap-2">
                  <Badge variant={row.status === 'ok' ? 'default' : 'secondary'}>{row.status}</Badge>
                  <span className="truncate font-medium">{row.function_name}</span>
                  <span>{row.caller}</span>
                  <span>{row.duration_ms}ms</span>
                  <span className="truncate">{row.error_message}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

// FenceBanner is the "no silent degradation" surface: when the OS sandbox could
// not be established, functions are refused, and this says so plainly instead
// of letting the user discover it one failed call at a time.
//
// Degraded gets its own headline rather than the healthy one. A fence running
// without a hardening layer must not read as "in force" — colour alone is not a
// disclosure, and the whole point of this component is that a weaker fence is
// never something the user has to infer.
function FenceBanner({ fence }: { fence: FenceAvailability }) {
  const { t } = useTranslation('settings');
  const ready = fence.available && !fence.degraded;
  const tone = !fence.available
    ? 'border-destructive text-destructive'
    : fence.degraded
      ? 'border-warning text-warning'
      : 'border-success text-success';
  return (
    <div className={`rounded-md border p-3 text-sm ${tone}`}>
      <p className="flex items-center gap-2 font-medium">
        {ready ? <ShieldCheck className="h-4 w-4" /> : <AlertTriangle className="h-4 w-4" />}
        {!fence.available
          ? t('integrations.functions.fenceUnavailable')
          : fence.degraded
            ? t('integrations.functions.fenceDegraded', { backend: fence.backend })
            : t('integrations.functions.fenceOk', { backend: fence.backend })}
      </p>
      {fence.reason && <p className="mt-1 text-xs">{fence.reason}</p>}
      <ul className="mt-2 flex flex-col gap-1 text-xs">
        {fence.components.map(c => (
          <li key={c.name}>
            {c.name}: {c.ok ? t('integrations.functions.fenceEnforced') : c.detail}
            {!c.required && ` (${t('integrations.functions.fenceHardeningOnly')})`}
          </li>
        ))}
      </ul>
    </div>
  );
}
