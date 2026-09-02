import { useCallback, useEffect, useMemo, useRef } from 'react';
import { useVirtualizer, type Virtualizer } from '@tanstack/react-virtual';
import { Plus } from 'lucide-react';
import { cn } from '@/lib/utils';
import { highlightCode } from '@/lib/syntax-highlight';
import type { CodeLineRenderer, CodeRow, LineCell } from './types';

/**
 * CodeSurface — the one windowed, scrollable code viewport.
 *
 * It owns scroll geometry, row windowing and height measurement; everything
 * line-specific comes from the injected `CodeLineRenderer` (see `types.ts`).
 * Callers hand it a flat `CodeRow[]` and get back a viewport that mounts only
 * the rows on screen.
 *
 * Because only visible rows exist, only visible rows are tokenized — which is
 * what let the old `MAX_HIGHLIGHT_LINES` / `MAX_DIFF_LINES` bail-outs go away.
 * Those constants degraded exactly backwards: past the limit a file lost its
 * highlighting (worst readability) while still mounting every DOM node (worst
 * performance).
 */

/** One line at `leading-5`. Only a first guess — real heights are measured. */
const ROW_HEIGHT = 20;

/**
 * Below this many rows every row is mounted. Windowing costs an absolutely
 * positioned wrapper and a measure pass per row; for a screenful of code that
 * is pure overhead, and it keeps short files byte-identical to the old output.
 */
const VIRTUALIZE_THRESHOLD = 200;

/**
 * Assumed viewport height when the scroll container measures zero — the first
 * paint before layout, and jsdom, which has no layout engine at all. Without
 * it the window would be empty (height 0 fits no rows) and a mounted-but-
 * unlaid-out surface would render nothing at all.
 */
const FALLBACK_VIEWPORT_HEIGHT = 800;

/** Keeps a blank line's row at full height instead of collapsing to zero. */
const ZERO_WIDTH_SPACE = String.fromCharCode(0x200b);

export interface CodeSurfaceProps {
  rows: CodeRow[];
  renderer?: CodeLineRenderer;
  /** Expand a folded run; receives the gap row's key. */
  onExpandGap?: (key: string) => void;
  /** Label for a gap row's button, e.g. "Expand 12 unchanged lines". */
  gapLabel?: (hiddenCount: number) => string;
  /**
   * Row index to scroll into view once mounted. Anchor jumps (search hits,
   * comment permalinks) address rows by index rather than by DOM node, so they
   * work identically whether or not the target row is currently mounted.
   */
  scrollToRow?: number;
  /** Changing this re-runs the scroll even when `scrollToRow` is unchanged. */
  scrollToKey?: string | null;
  /**
   * Show the leading `+`/`−`/space column. Off for plain-file views, where
   * every line is context and a column of spaces is just wasted width.
   */
  showSigns?: boolean;
  className?: string;
}

/**
 * Widest line in the surface, in characters.
 *
 * Needed for two reasons. Split mode must give each half an explicit `ch`
 * flex-basis so the two columns stay aligned while wide code still scrolls
 * instead of wrapping. And in the windowed layout every row is absolutely
 * positioned — absolutely positioned children do not size their parent, so
 * without an explicit width the scroll content would collapse to the viewport
 * and wide lines would be clipped rather than scrollable.
 */
function widestContent(rows: CodeRow[]): number {
  let max = 0;
  for (const row of rows) {
    if (row.kind !== 'line') continue;
    for (const cell of row.cells) {
      const len = cell.line?.content.length ?? 0;
      if (len > max) max = len;
    }
  }
  // Cap it: one pathological minified line shouldn't stretch the scroll width
  // (and every split row's flex-basis) to a hundred thousand columns.
  return Math.min(max, 400);
}

/** Width of one gutter column (`w-11`) plus a code cell's `px-3` padding. */
const GUTTER_PX = 44;
const CODE_PADDING_PX = 24;

function Gutter({
  value,
  tone,
  onAdd,
}: {
  value?: number;
  tone?: 'add' | 'del';
  onAdd?: () => void;
}) {
  return (
    <div
      data-code-gutter=""
      className={cn(
        'relative w-11 shrink-0 select-none border-r border-border px-2 text-right font-mono text-[11px] leading-5 text-muted-foreground/70',
        tone === 'add' && 'bg-diff-add text-diff-add-fg',
        tone === 'del' && 'bg-diff-del text-diff-del-fg',
      )}
    >
      {onAdd ? (
        <>
          <span className="group-hover/line:opacity-0">{value ?? ''}</span>
          <button
            type="button"
            onClick={onAdd}
            className="absolute inset-0 hidden items-center justify-center text-brand hover:bg-brand-soft group-hover/line:flex"
          >
            <Plus className="h-3 w-3" />
          </button>
        </>
      ) : (
        (value ?? '')
      )}
    </div>
  );
}

/** A cell's gutter column(s) plus its code column. */
function Cell({
  cell,
  renderer,
  basisCh,
  showSigns,
}: {
  cell: LineCell;
  renderer?: CodeLineRenderer;
  /** Split-mode flex-basis in `ch`; 0 means "size to content" (unified). */
  basisCh: number;
  showSigns: boolean;
}) {
  const { line } = cell;
  const type = line?.type ?? 'context';
  const tone = type === 'add' ? 'add' : type === 'delete' ? 'del' : undefined;
  const sign = type === 'add' ? '+' : type === 'delete' ? '−' : ' ';
  // Only the last gutter is commentable — it is the new-side number, the one
  // coordinate space anchors live in.
  const onAdd = renderer?.gutterAction?.(cell);

  const nodes = line
    ? renderer?.tokenize
      ? renderer.tokenize(line, cell)
      : line.content.length > 0
        ? highlightCode(line.content)
        : ZERO_WIDTH_SPACE
    : ZERO_WIDTH_SPACE;

  return (
    <>
      {cell.gutters.map((value, i) => (
        <Gutter
          key={i}
          value={value}
          tone={tone}
          onAdd={i === cell.gutters.length - 1 ? onAdd : undefined}
        />
      ))}
      <div
        className={cn(
          'whitespace-pre px-3 font-mono text-[12.5px] leading-5 text-foreground',
          // A pad cell (the empty half of an unbalanced split pair) is a gap in
          // the file, not a line of it — no diff tint, no sign.
          line && type === 'add' && 'bg-diff-add border-l-2 border-diff-add-fg',
          line && type === 'delete' && 'bg-diff-del border-l-2 border-diff-del-fg',
          (!line || type === 'context') && 'border-l-2 border-transparent',
          cell.side === 'old' && 'border-r border-border',
        )}
        style={
          basisCh > 0
            ? { flex: `1 0 ${basisCh}ch`, minWidth: 0 }
            : { flex: '1 0 auto', minWidth: 'max-content' }
        }
      >
        {line && showSigns && (
          <span
            className={cn(
              'mr-2 select-none font-semibold',
              type === 'add' && 'text-diff-add-fg',
              type === 'delete' && 'text-diff-del-fg',
              type === 'context' && 'text-muted-foreground/40',
            )}
          >
            {sign}
          </span>
        )}
        {nodes}
      </div>
    </>
  );
}

export function CodeSurface({
  rows,
  renderer,
  onExpandGap,
  gapLabel,
  scrollToRow,
  scrollToKey,
  showSigns = true,
  className,
}: CodeSurfaceProps) {
  const scrollRef = useRef<HTMLDivElement | null>(null);

  // Split rows carry two cells; unified rows one. Only split needs the explicit
  // column basis — unified sizes to content so wide code scrolls naturally.
  const isSplit = rows.some((r) => r.kind === 'line' && r.cells.length > 1);
  const widestCh = useMemo(() => widestContent(rows), [rows]);
  const basisCh = isSplit ? widestCh : 0;

  const windowed = rows.length > VIRTUALIZE_THRESHOLD;

  // Gutter count is uniform across a surface's line rows, so one row is enough
  // to derive the fixed left-hand width.
  const gutterCount = useMemo(() => {
    const first = rows.find((r) => r.kind === 'line');
    if (first?.kind !== 'line') return 1;
    return first.cells.reduce((n, c) => n + c.gutters.length, 0);
  }, [rows]);

  // Explicit scroll-content width for the windowed layout: absolutely
  // positioned rows contribute nothing to their parent's width, so without this
  // the content box collapses to the viewport and wide lines get clipped
  // instead of scrolling. `ch` is exact here because the sizing element carries
  // the same `font-mono` as the code cells.
  const contentWidth = useMemo(() => {
    const cells = isSplit ? 2 : 1;
    const fixed = gutterCount * GUTTER_PX + cells * CODE_PADDING_PX;
    return `calc(${fixed}px + ${cells * widestCh}ch)`;
  }, [isSplit, gutterCount, widestCh]);

  // A zero measurement is never a real code row / viewport — it means layout
  // hasn't run (first paint) or there is none (jsdom). Substituting a nominal
  // size keeps the window populated; the real value replaces it on the next
  // ResizeObserver tick in a browser.
  const measureElement = useCallback((el: Element) => {
    const h = el.getBoundingClientRect().height;
    return h > 0 ? h : ROW_HEIGHT;
  }, []);

  const observeRect = useCallback(
    (instance: Virtualizer<HTMLDivElement, Element>, cb: (rect: { width: number; height: number }) => void) => {
      const el = instance.scrollElement;
      if (!el) return;
      const report = () => {
        const r = el.getBoundingClientRect();
        cb({
          width: r.width || el.clientWidth || 0,
          height: r.height || el.clientHeight || FALLBACK_VIEWPORT_HEIGHT,
        });
      };
      report();
      if (typeof ResizeObserver === 'undefined') return;
      const ro = new ResizeObserver(report);
      ro.observe(el);
      return () => ro.disconnect();
    },
    [],
  );

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    getItemKey: (i) => rows[i].key,
    measureElement,
    observeElementRect: observeRect,
    overscan: 12,
    enabled: windowed,
  });

  // Anchor jump. Re-runs when the target row OR the request key changes, so
  // hitting the same line twice (or the same index in another file) re-scrolls.
  const { scrollToIndex } = virtualizer;
  useEffect(() => {
    if (scrollToRow == null || scrollToRow < 0 || scrollToRow >= rows.length) return;
    if (windowed) {
      scrollToIndex(scrollToRow, { align: 'center' });
      return;
    }
    // Un-windowed: every row is mounted, so address it directly.
    const el = scrollRef.current?.querySelector<HTMLElement>(`[data-index="${scrollToRow}"]`);
    el?.scrollIntoView({ block: 'center', behavior: 'auto' });
    // rows.length guards a stale index; it is read, not tracked as a trigger.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scrollToRow, scrollToKey, windowed, scrollToIndex]);

  const renderRow = (row: CodeRow, index: number) => {
    const extra = renderer?.rowClassName?.(row, index);

    if (row.kind === 'hunk') {
      return (
        <div className={cn('min-w-full border-y border-border bg-muted/60', extra)}>
          <div className="sticky left-0 w-fit px-3 py-1 font-mono text-[11px] text-muted-foreground">
            {row.content}
          </div>
        </div>
      );
    }

    if (row.kind === 'gap') {
      return (
        <div className={cn('min-w-full border-y border-border bg-muted/40', extra)}>
          <button
            type="button"
            onClick={() => onExpandGap?.(row.key)}
            className="sticky left-0 flex w-fit items-center gap-2 px-3 py-1 text-left text-[11px] text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            {gapLabel?.(row.hiddenCount) ?? `+${row.hiddenCount}`}
          </button>
        </div>
      );
    }

    const attachment = row.anchor != null ? renderer?.attachment?.(row.anchor) : null;
    return (
      <div className={extra}>
        <div data-code-line="" className="group/line flex min-w-full">
          {row.cells.map((cell, i) => (
            <Cell
              key={i}
              cell={cell}
              renderer={renderer}
              basisCh={basisCh}
              showSigns={showSigns}
            />
          ))}
        </div>
        {attachment}
      </div>
    );
  };

  if (!windowed) {
    return (
      <div ref={scrollRef} className={cn('h-full overflow-auto bg-card', className)}>
        <div className="w-max min-w-full">
          {rows.map((row, i) => (
            <div key={row.key} data-index={i}>
              {renderRow(row, i)}
            </div>
          ))}
        </div>
      </div>
    );
  }

  const items = virtualizer.getVirtualItems();
  return (
    <div ref={scrollRef} className={cn('h-full overflow-auto bg-card', className)}>
      <div
        className="relative font-mono text-[12.5px]"
        style={{ height: virtualizer.getTotalSize(), minWidth: contentWidth }}
      >
        {items.map((vi) => (
          <div
            key={vi.key}
            data-index={vi.index}
            ref={virtualizer.measureElement}
            className="absolute left-0 top-0 w-full"
            style={{ transform: `translateY(${vi.start}px)` }}
          >
            {renderRow(rows[vi.index], vi.index)}
          </div>
        ))}
      </div>
    </div>
  );
}
