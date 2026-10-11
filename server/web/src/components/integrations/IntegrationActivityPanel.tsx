// IntegrationActivityPanel — Settings -> Integrations -> 集成活动.
//
// The read side of the two integration audit tables (Epic #726 / #728). Before
// this view there was no way to answer "which agent called what, and when":
// external_api_audit and data_source_audit were write-only.
//
// Both tables are merged into one newest-first feed by the backend
// (GET /api/me/integration-activity), so paging stays correct across the two
// sources instead of being paginated twice and stitched together client-side.

import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Activity, AlertCircle, ChevronLeft, ChevronRight, RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { EmptyState } from '@/components/shared/empty-state';
import { LoadingSkeleton } from '@/components/shared/loading-skeleton';
import { useExternalProviders } from '@/hooks/useExternalProviders';
import { useIntegrationActivity } from '@/hooks/useIntegrationActivity';
import { listDataSources } from '@/lib/data-sources-api';
import type {
  ActivityKind,
  IntegrationActivityFilters,
  IntegrationActivityItemWithFingerprint,
} from '@/types/integration';

type KindFilter = 'all' | ActivityKind;
type RangeFilter = '24h' | '7d' | '30d' | 'all';

const RANGE_HOURS: Record<Exclude<RangeFilter, 'all'>, number> = {
  '24h': 24,
  '7d': 24 * 7,
  '30d': 24 * 30,
};

const SOURCE_SEPARATOR = ':';

/** Status tint for one feed row. Reads the HTTP status for api rows and the
 *  ok/err word for data rows; both render the value as text, so colour is
 *  never the only signal (design-system §5.10). */
function statusTone(item: IntegrationActivityItemWithFingerprint): {
  key: 'success' | 'warning' | 'destructive' | 'muted';
  text: string;
} {
  if (item.kind === 'data') {
    if (item.status === 'ok') return { key: 'success', text: item.status };
    if (item.status === 'err') return { key: 'destructive', text: item.status };
    return { key: 'muted', text: item.status || '—' };
  }
  const code = item.status_code ?? 0;
  if (code <= 0) return { key: 'muted', text: '—' };
  if (code < 300) return { key: 'success', text: String(code) };
  if (code < 500) return { key: 'warning', text: String(code) };
  return { key: 'destructive', text: String(code) };
}

const TONE_CLASS: Record<string, string> = {
  success: 'bg-success/15 text-success border-success/30',
  warning: 'bg-warning/15 text-warning border-warning/30',
  destructive: 'bg-destructive/15 text-destructive border-destructive/30',
  muted: 'bg-warm-muted text-warm-text-muted border-warm-border',
};

export function IntegrationActivityPanel() {
  const { t, i18n } = useTranslation('settings');

  const [kind, setKind] = useState<KindFilter>('all');
  const [source, setSource] = useState('all');
  const [range, setRange] = useState<RangeFilter>('7d');
  const [page, setPage] = useState(0);

  const providers = useExternalProviders();
  const dataSources = useQuery({
    queryKey: ['data-sources'],
    queryFn: listDataSources,
  });

  // Any filter change invalidates the current offset.
  const onKindChange = (value: string) => {
    setKind(value as KindFilter);
    setPage(0);
  };
  const onSourceChange = (value: string) => {
    setSource(value);
    setPage(0);
  };
  const onRangeChange = (value: string) => {
    setRange(value as RangeFilter);
    setPage(0);
  };

  const filters = useMemo<IntegrationActivityFilters>(() => {
    const f: IntegrationActivityFilters = {};
    if (kind !== 'all') f.kind = kind;
    const [tag, rawId] = source.split(SOURCE_SEPARATOR);
    const id = Number(rawId);
    if (tag === 'p' && Number.isFinite(id)) f.provider_id = id;
    if (tag === 'd' && Number.isFinite(id)) f.source_id = id;
    if (range !== 'all') {
      const hours = RANGE_HOURS[range];
      f.from = new Date(Date.now() - hours * 3600 * 1000).toISOString();
    }
    return f;
  }, [kind, source, range]);

  const { data, isLoading, isError, error, isFetching, refetch } =
    useIntegrationActivity(filters, page);

  const total = data?.total ?? 0;
  const items = data?.items ?? [];
  const from = total === 0 ? 0 : page * (data?.limit ?? 0) + 1;
  const to = Math.min(total, page * (data?.limit ?? 0) + items.length);
  const hasPrev = page > 0;
  const hasNext = to < total;

  const num = (n: number) => n.toLocaleString(i18n.language);

  const renderTitle = (item: IntegrationActivityItemWithFingerprint) => {
    if (item.kind === 'api') {
      return `${item.method ?? ''} ${item.path ?? ''}`.trim() || '—';
    }
    return item.statement_summary || '—';
  };

  const renderSubtitle = (item: IntegrationActivityItemWithFingerprint) => {
    if (item.kind === 'api') {
      return item.provider_name || `#${item.provider_id ?? '?'}`;
    }
    const who = item.source_name || `#${item.source_id ?? '?'}`;
    const parts = [who, item.access_mode, item.database_name].filter(Boolean);
    return parts.join(' · ');
  };

  // The body fingerprint answers "what shape of data left this machine" — hash
  // prefix + size + top-level field names. Field VALUES are never stored, so
  // this line is safe to display.
  const renderFingerprint = (item: IntegrationActivityItemWithFingerprint) => {
    if (item.kind === 'api') {
      if (!item.body_sha256) return t('integrations.activity.noBody');
      const fields = (item.body_fields ?? []).join(', ');
      return t('integrations.activity.fingerprint', {
        hash: item.body_sha256.slice(0, 12),
        bytes: num(item.body_bytes ?? 0),
        fields: fields || t('integrations.activity.fieldsNone'),
      });
    }
    return t('integrations.activity.dataMeta', {
      rows: num(item.rows ?? 0),
      ms: num(item.duration_ms ?? 0),
    });
  };

  const providerItems = providers.data ?? [];
  const sourceItems = dataSources.data ?? [];

  return (
    <section className="flex flex-col gap-3 rounded border border-warm-border p-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-medium">
            {t('integrations.activity.sectionTitle')}
          </h2>
          <p className="text-xs text-warm-text-muted mt-1">
            {t('integrations.activity.sectionDescription')}
          </p>
        </div>
        <Button
          size="sm"
          variant="ghost"
          aria-label={t('integrations.activity.refresh')}
          title={t('integrations.activity.refresh')}
          onClick={() => refetch()}
          disabled={isFetching}
        >
          <RefreshCw className={isFetching ? 'size-4 animate-spin' : 'size-4'} />
        </Button>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Select value={kind} onValueChange={onKindChange}>
          <SelectTrigger
            className="h-8 w-36"
            aria-label={t('integrations.activity.filterKind')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">
              {t('integrations.activity.kindAll')}
            </SelectItem>
            <SelectItem value="api">
              {t('integrations.activity.kindApi')}
            </SelectItem>
            <SelectItem value="data">
              {t('integrations.activity.kindData')}
            </SelectItem>
          </SelectContent>
        </Select>

        <Select value={source} onValueChange={onSourceChange}>
          <SelectTrigger
            className="h-8 w-48"
            aria-label={t('integrations.activity.filterSource')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">
              {t('integrations.activity.sourceAll')}
            </SelectItem>
            {providerItems.map((p) => (
              <SelectItem key={`p-${p.id}`} value={`p${SOURCE_SEPARATOR}${p.id}`}>
                {p.label || p.name}
              </SelectItem>
            ))}
            {sourceItems.map((s) => (
              <SelectItem key={`d-${s.id}`} value={`d${SOURCE_SEPARATOR}${s.id}`}>
                {s.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select value={range} onValueChange={onRangeChange}>
          <SelectTrigger
            className="h-8 w-32"
            aria-label={t('integrations.activity.filterRange')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="24h">
              {t('integrations.activity.range24h')}
            </SelectItem>
            <SelectItem value="7d">
              {t('integrations.activity.range7d')}
            </SelectItem>
            <SelectItem value="30d">
              {t('integrations.activity.range30d')}
            </SelectItem>
            <SelectItem value="all">
              {t('integrations.activity.rangeAll')}
            </SelectItem>
          </SelectContent>
        </Select>

        <span className="ml-auto text-xs text-warm-text-muted tabular-nums">
          {t('integrations.activity.range', {
            from: num(from),
            to: num(to),
            total: num(total),
          })}
        </span>
      </div>

      {isLoading ? (
        <LoadingSkeleton variant="card" count={3} />
      ) : isError ? (
        <div className="flex items-start gap-2 rounded border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-warm-text">
          <AlertCircle aria-hidden className="size-4 text-destructive shrink-0 mt-0.5" />
          <span>{error instanceof Error ? error.message : String(error)}</span>
        </div>
      ) : items.length === 0 ? (
        <EmptyState
          size="compact"
          icon={<Activity className="h-8 w-8 text-warm-text-muted" />}
          title={t('integrations.activity.empty')}
          description={t('integrations.activity.emptyHint')}
        />
      ) : (
        <ul className="flex flex-col divide-y divide-warm-border/60">
          {items.map((item) => {
            const tone = statusTone(item);
            return (
              <li
                key={`${item.kind}-${item.id}`}
                className="flex items-start gap-3 py-3"
              >
                <Badge variant="outline" className="mt-0.5 shrink-0">
                  {item.kind === 'api'
                    ? t('integrations.activity.kindApi')
                    : t('integrations.activity.kindData')}
                </Badge>
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="text-sm font-medium truncate">
                      {renderTitle(item)}
                    </span>
                    <span
                      className={`inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] tabular-nums shrink-0 ${TONE_CLASS[tone.key]}`}
                    >
                      {tone.text}
                    </span>
                  </div>
                  <div className="text-xs text-warm-text-muted mt-0.5 truncate">
                    {renderSubtitle(item)}
                  </div>
                  <div className="text-[10px] text-warm-text-muted mt-0.5 font-mono truncate">
                    {renderFingerprint(item)}
                  </div>
                </div>
                <time
                  dateTime={item.created_at}
                  className="text-xs text-warm-text-muted tabular-nums shrink-0"
                >
                  {new Date(item.created_at).toLocaleString(i18n.language)}
                </time>
              </li>
            );
          })}
        </ul>
      )}

      {(hasPrev || hasNext) && (
        <div className="flex items-center justify-end gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={!hasPrev || isFetching}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
          >
            <ChevronLeft className="size-4" />
            {t('integrations.activity.prev')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!hasNext || isFetching}
            onClick={() => setPage((p) => p + 1)}
          >
            {t('integrations.activity.next')}
            <ChevronRight className="size-4" />
          </Button>
        </div>
      )}
    </section>
  );
}
