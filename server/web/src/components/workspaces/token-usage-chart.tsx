import { lazy, Suspense } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useThemeStore } from '@/stores/theme-store';
import type { TokenBucket, TokenUsageSeries } from '@/types/api';
import type { ChartSpec } from '@/types/data';

// echarts lives in its own async chunk behind EChartsRenderer. We reuse that
// renderer's `type: 'echarts'` escape hatch (a full native option) so this
// chart stays out of the main bundle and inherits the shared theme defaults.
const EChartsRenderer = lazy(() => import('@/components/data/echarts-renderer'));

// Read a design-system color token off the document root, wrapping bare HSL
// tuples (e.g. `218 75% 46%`) as comma-separated `hsl(...)`. Same convention as
// echarts-renderer's internal `token()`; duplicated here so building the option
// never pulls the echarts module into this (eagerly-loaded) component.
// Commas are required: zrender's color parser strips spaces then splits on
// commas, so the space-separated hsl syntax silently parses to undefined.
function cssToken(name: string, fallback: string): string {
  if (typeof document === 'undefined') return hslTuple(fallback);
  const raw = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  if (!raw) return hslTuple(fallback);
  if (/^(#|rgb|hsl)/i.test(raw)) return raw;
  return hslTuple(raw);
}

// `218 75% 46%` -> `hsl(218, 75%, 46%)`; `h s% l% / a` -> `hsla(h, s%, l%, a)`.
function hslTuple(tuple: string): string {
  const [color, alpha] = tuple.split('/').map((s) => s.trim());
  const parts = color.split(/\s+/).join(', ');
  return alpha ? `hsla(${parts}, ${alpha})` : `hsl(${parts})`;
}

function compact(n: number): string {
  const abs = Math.abs(n);
  if (abs >= 1e9) return (n / 1e9).toFixed(1).replace(/\.0$/, '') + 'B';
  if (abs >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M';
  if (abs >= 1e3) return (n / 1e3).toFixed(1).replace(/\.0$/, '') + 'K';
  return String(n);
}

const SERIES: {
  key: keyof Pick<
    TokenBucket,
    'input_tokens' | 'output_tokens' | 'cache_creation_tokens' | 'cache_read_tokens'
  >;
  colorVar: string;
  fallback: string;
  labelKey: string;
}[] = [
  { key: 'input_tokens', colorVar: '--info', fallback: '221 83% 53%', labelKey: 'overview.tokenChart.input' },
  { key: 'output_tokens', colorVar: '--brand', fallback: '218 75% 46%', labelKey: 'overview.tokenChart.output' },
  { key: 'cache_creation_tokens', colorVar: '--warning', fallback: '38 92% 50%', labelKey: 'overview.tokenChart.cacheCreation' },
  { key: 'cache_read_tokens', colorVar: '--success', fallback: '142 71% 45%', labelKey: 'overview.tokenChart.cacheRead' },
];

function formatHour(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const mm = d.getMonth() + 1;
  const dd = d.getDate();
  const hh = String(d.getHours()).padStart(2, '0');
  return `${mm}/${dd} ${hh}:00`;
}

/** Stacked-column chart over hourly token buckets, themed via ECharts. */
export function TokenUsageChart({ buckets }: { buckets: TokenBucket[] }) {
  const { t } = useTranslation('workspaces');
  // Re-render on light/dark toggle so cssToken() re-reads the swapped tuples;
  // the option's new identity then makes EChartsRenderer re-init with them.
  useThemeStore((s) => s.resolvedTheme);
  if (buckets.length === 0) {
    return (
      <div className="flex items-center justify-center py-10 text-sm text-muted-foreground">
        {t('overview.tokenChart.empty')}
      </div>
    );
  }

  const axisColor = cssToken('--muted-foreground', '0 0% 45%');
  const splitColor = cssToken('--border', '0 0% 90%');
  const categories = buckets.map((b) => b.hour);

  const option: Record<string, unknown> = {
    color: SERIES.map((s) => cssToken(s.colorVar, s.fallback)),
    grid: { left: 4, right: 8, top: 30, bottom: 2, containLabel: true },
    legend: {
      top: 0,
      left: 0,
      itemWidth: 10,
      itemHeight: 10,
      itemGap: 14,
      icon: 'roundRect',
      textStyle: { color: axisColor, fontSize: 11 },
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      valueFormatter: (v: number | null) => (v == null ? '-' : compact(Number(v))),
    },
    xAxis: {
      type: 'category',
      data: categories,
      boundaryGap: true,
      axisTick: { show: false },
      axisLine: { lineStyle: { color: splitColor } },
      axisLabel: {
        color: axisColor,
        fontSize: 10,
        hideOverlap: true,
        formatter: (v: string) => formatHour(v),
      },
    },
    yAxis: {
      type: 'value',
      axisLabel: { color: axisColor, fontSize: 10, formatter: (v: number) => compact(v) },
      splitLine: { lineStyle: { color: splitColor, opacity: 0.4 } },
    },
    series: SERIES.map((s, i) => ({
      name: t(s.labelKey),
      type: 'bar',
      stack: 'tokens',
      barMaxWidth: 20,
      // Round only the topmost (last) series so the whole stacked column gets a
      // single rounded cap.
      itemStyle: { borderRadius: i === SERIES.length - 1 ? [3, 3, 0, 0] : 0 },
      emphasis: { focus: 'series' },
      data: buckets.map((b) => b[s.key]),
    })),
  };

  const chart: ChartSpec = { type: 'echarts', option };

  return (
    <Suspense fallback={<div className="h-48 w-full animate-pulse rounded bg-muted/40" />}>
      <EChartsRenderer chart={chart} heightClass="h-48" />
    </Suspense>
  );
}

/**
 * OwnerTokenTrend fetches and renders the last-7-day hourly token series for a
 * single owner (ownerParam like "user:1" or "org:slug"). When no single owner
 * is in scope (no filter, or a multi-owner filter), it falls back to the
 * caller's personal owner so the history is always visible — including in
 * personal edition where the owner filter UI is absent.
 */
export function OwnerTokenTrend({
  ownerParam,
  fallbackOwner,
}: {
  ownerParam: string | null;
  fallbackOwner?: string | null;
}) {
  const { t } = useTranslation('workspaces');
  const singleOwner =
    ownerParam && !ownerParam.includes(',') ? ownerParam : fallbackOwner ?? null;

  const { data } = useQuery({
    queryKey: ['token-usage', 'owner', singleOwner],
    enabled: !!singleOwner,
    queryFn: () => api.get<TokenUsageSeries>('/token-usage', { params: { owner: singleOwner! } }),
  });

  if (!singleOwner) return null;

  return (
    <div className="rounded-lg border bg-card p-4">
      <div className="text-sm font-medium mb-3">{t('overview.tokenChart.title')}</div>
      <TokenUsageChart buckets={data?.buckets ?? []} />
    </div>
  );
}
