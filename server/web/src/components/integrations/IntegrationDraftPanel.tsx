// IntegrationDraftPanel — Settings → Integrations → 集成草稿 (Epic #726 / #734).
//
// The human end of the draft pipeline: draft → lint → impact confirmation →
// publish → verify / rollback. An agent may draft and ask; this panel is where
// the user grants. Nothing here composes authorization text of its own — the
// decision area is rendered by IntegrationConfirmCard straight from the
// server's structured model, so "what the user sees" is "what will run".
//
// Two deliberate friction points, both structural rather than cosmetic:
//   * a widening change (scope widened / writes enabled) requires a SEPARATE
//     checkbox before the card's acknowledge-and-confirm even becomes possible;
//   * rolling back to a version whose scope differs from the live one returns a
//     fresh card that must be confirmed before anything is applied.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertCircle, CheckCircle2, FileClock, RotateCcw, Upload } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import { Label } from '@/components/ui/label';
import { EmptyState } from '@/components/shared/empty-state';
import { IntegrationConfirmCard } from './IntegrationConfirmCard';
import { integrationDraftApi } from '@/lib/integration-draft-api';
import type { IntegrationDraftView } from '@/types/integration-draft';
import type { ConfirmCardModel, ConfirmDecisionBody } from '@/types/integration-confirm';

const DRAFTS_KEY = ['integration-drafts'] as const;

function statusTone(status: IntegrationDraftView['status']): string {
  switch (status) {
    case 'published':
      return 'border-success/40 text-success';
    case 'confirmed':
      return 'border-warning/40 text-warning';
    case 'lint_failed':
      return 'border-destructive/40 text-destructive';
    default:
      return '';
  }
}

export function IntegrationDraftPanel() {
  const { t } = useTranslation('integrationConfirm');
  const queryClient = useQueryClient();
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [wideningAcknowledged, setWideningAcknowledged] = useState(false);
  const [feedback, setFeedback] = useState<{ tone: 'ok' | 'error'; text: string } | null>(null);
  // A rollback that changes scope answers with the card to confirm before it is
  // applied. Holding it here keeps the "nothing happened yet" state visible.
  const [pendingRollback, setPendingRollback] = useState<{
    versionId: number;
    version: number;
    card: ConfirmCardModel;
  } | null>(null);

  const drafts = useQuery({
    queryKey: DRAFTS_KEY,
    queryFn: () => integrationDraftApi.list(),
  });

  const selected = useMemo(
    () => drafts.data?.find((d) => d.id === selectedId) ?? null,
    [drafts.data, selectedId],
  );

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: DRAFTS_KEY });
  };

  const confirm = useMutation({
    mutationFn: (args: { id: number; body: ConfirmDecisionBody; second: boolean }) =>
      integrationDraftApi.confirm(args.id, {
        decision: args.body.decision,
        acknowledged: args.body.acknowledged,
        second_confirmation: args.second,
      }),
    onSuccess: (view) => {
      setFeedback(null);
      if (view.requires_second_confirmation) {
        // The server refused to treat one click as both the widening and its
        // approval. Say so plainly instead of silently doing nothing.
        setFeedback({ tone: 'error', text: t('draft.second.stillRequired') });
      }
      refresh();
    },
    onError: (err: unknown) => setFeedback({ tone: 'error', text: String(err) }),
  });

  const publish = useMutation({
    mutationFn: (id: number) => integrationDraftApi.publish(id),
    onSuccess: (res) => {
      setFeedback(
        res.published
          ? { tone: 'ok', text: t('draft.publishDone') }
          : { tone: 'error', text: res.verify?.detail || res.detail || t('draft.publishRolledBack') },
      );
      refresh();
    },
    onError: (err: unknown) => setFeedback({ tone: 'error', text: String(err) }),
  });

  const versions = useQuery({
    queryKey: ['integration-versions', selected?.kind, selected?.target_name],
    queryFn: () =>
      selected
        ? integrationDraftApi.versions(selected.kind, selected.target_name)
        : Promise.resolve([]),
    enabled: Boolean(selected),
  });

  const rollback = useMutation({
    mutationFn: (args: { versionId: number; version: number; confirmed: boolean; acknowledged: string[] }) =>
      integrationDraftApi
        .rollback(args.versionId, { confirmed: args.confirmed, acknowledged: args.acknowledged })
        .then((res) => ({ res, args })),
    onSuccess: ({ res, args }) => {
      if (res.requires_confirmation && res.card) {
        // Restoring this version would change scope, so nothing was applied:
        // show the system-rendered card and require a fresh decision.
        setPendingRollback({ versionId: args.versionId, version: args.version, card: res.card });
        setFeedback(null);
        return;
      }
      setPendingRollback(null);
      setFeedback({
        tone: res.rolled_back ? 'ok' : 'error',
        text: res.detail || (res.rolled_back ? t('draft.rollbackDone') : t('draft.rollbackFailed')),
      });
      refresh();
    },
    onError: (err: unknown) => setFeedback({ tone: 'error', text: String(err) }),
  });

  const busy = confirm.isPending || publish.isPending || rollback.isPending;

  if (drafts.isLoading) {
    return (
      <Card>
        <CardHeader className="py-3">
          <CardTitle className="text-sm">{t('draft.title')}</CardTitle>
        </CardHeader>
        <CardContent className="text-xs text-warm-text-muted">{t('draft.loading')}</CardContent>
      </Card>
    );
  }

  const list = drafts.data ?? [];

  return (
    <Card>
      <CardHeader className="py-3">
        <CardTitle className="flex items-center gap-2 text-sm">
          <FileClock className="h-4 w-4" />
          {t('draft.title')}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-xs text-warm-text-muted">{t('draft.notice')}</p>

        {list.length === 0 ? (
          <EmptyState
            size="compact"
            title={t('draft.empty')}
            description={t('draft.emptyHint')}
          />
        ) : (
          <ul className="divide-y divide-warm-border overflow-hidden rounded-md border border-warm-border">
            {list.map((d) => (
              <li key={d.id}>
                <button
                  type="button"
                  onClick={() => {
                    setSelectedId(d.id);
                    setWideningAcknowledged(false);
                    setFeedback(null);
                  }}
                  className="flex w-full items-center gap-2 px-2 py-2 text-left text-xs hover:bg-warm-muted"
                >
                  <Badge variant="outline">{t(`draft.kind.${d.kind}`)}</Badge>
                  <span className="truncate font-medium">{d.target_name}</span>
                  {d.agent_authored && (
                    <span className="truncate text-warm-text-muted">{t('draft.agentAuthored')}</span>
                  )}
                  <Badge variant="outline" className={`ml-auto shrink-0 ${statusTone(d.status)}`}>
                    {t(`draft.status.${d.status}`)}
                  </Badge>
                </button>
              </li>
            ))}
          </ul>
        )}

        {feedback && (
          <p
            role="status"
            className={
              feedback.tone === 'ok'
                ? 'flex items-start gap-1.5 text-xs text-success'
                : 'flex items-start gap-1.5 text-xs text-destructive'
            }
          >
            {feedback.tone === 'ok' ? (
              <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            ) : (
              <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            )}
            <span className="break-words">{feedback.text}</span>
          </p>
        )}

        {selected && (
          <div className="space-y-3" data-testid="draft-detail">
            {selected.issues.length > 0 && (
              <ul className="space-y-1 rounded-md border border-warm-border p-2">
                {selected.issues.map((iss, i) => (
                  <li
                    key={`${iss.path} ${i}`}
                    className={
                      iss.severity === 'error'
                        ? 'text-xs text-destructive'
                        : 'text-xs text-warm-text-muted'
                    }
                  >
                    <code>{iss.path}</code> — {iss.message}
                  </li>
                ))}
              </ul>
            )}

            {selected.requires_second_confirmation && (
              <div className="space-y-2 rounded-md border border-warning/40 p-2">
                <p className="text-xs font-medium text-warning">{t('draft.second.title')}</p>
                <p className="text-xs text-warm-text-muted">{t('draft.second.notice')}</p>
                <div className="flex items-center gap-2">
                  <Checkbox
                    id={`draft-second-${selected.id}`}
                    checked={wideningAcknowledged}
                    disabled={busy}
                    onCheckedChange={(state) => setWideningAcknowledged(state === true)}
                  />
                  <Label htmlFor={`draft-second-${selected.id}`} className="text-xs font-normal">
                    {t('draft.second.checkbox')}
                  </Label>
                </div>
              </div>
            )}

            {selected.card ? (
              <IntegrationConfirmCard
                model={selected.card}
                pending={busy || (selected.requires_second_confirmation && !wideningAcknowledged)}
                confirmLabelKey="draft.confirmImpact"
                onDecide={(body) =>
                  confirm.mutate({
                    id: selected.id,
                    body,
                    second: wideningAcknowledged,
                  })
                }
              />
            ) : (
              <p className="text-xs text-warm-text-muted">{t('draft.noCard')}</p>
            )}

            {selected.status === 'confirmed' && (
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  size="sm"
                  disabled={busy}
                  onClick={() => publish.mutate(selected.id)}
                >
                  <Upload className="mr-1.5 h-3.5 w-3.5" />
                  {t('draft.publish')}
                </Button>
                <span className="text-xs text-warm-text-muted">{t('draft.publishHint')}</span>
              </div>
            )}

            {versions.data && versions.data.length > 0 && (
              <section className="space-y-1">
                <h4 className="text-xs font-medium text-warm-text-muted">
                  {t('draft.versions.title')}
                </h4>
                <ul className="space-y-1">
                  {versions.data.map((v) => (
                    <li key={v.id} className="flex items-center gap-2 text-xs">
                      <span className="font-medium">
                        {t('draft.versions.version', { version: v.version })}
                      </span>
                      {v.active === 1 && (
                        <Badge variant="secondary">{t('draft.versions.active')}</Badge>
                      )}
                      <Button
                        size="sm"
                        variant="ghost"
                        className="ml-auto"
                        disabled={busy}
                        onClick={() =>
                          rollback.mutate({
                            versionId: v.id,
                            version: v.version,
                            confirmed: false,
                            acknowledged: [],
                          })
                        }
                      >
                        <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
                        {t('draft.versions.rollback')}
                      </Button>
                    </li>
                  ))}
                </ul>
              </section>
            )}

            {pendingRollback && (
              <section className="space-y-2">
                <p className="text-xs text-warm-text-muted">
                  {t('draft.rollbackNeedsConfirmation', { version: pendingRollback.version })}
                </p>
                <IntegrationConfirmCard
                  model={pendingRollback.card}
                  pending={busy}
                  confirmLabelKey="draft.versions.confirmRollback"
                  onDecide={(body) => {
                    if (body.decision === 'reject') {
                      setPendingRollback(null);
                      return;
                    }
                    rollback.mutate({
                      versionId: pendingRollback.versionId,
                      version: pendingRollback.version,
                      confirmed: true,
                      acknowledged: body.acknowledged,
                    });
                  }}
                />
              </section>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
