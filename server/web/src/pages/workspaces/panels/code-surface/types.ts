import type { ReactNode } from 'react';

/**
 * The line-renderer plug point shared by every code surface in the workspace
 * (full-file view, unified diff, split diff).
 *
 * WHY THIS EXISTS
 * ---------------
 * Before this, each surface owned its own `<table>`, its own call to
 * `highlightCode`, and its own inline-comment wiring — so anything that wants to
 * change how a *line* renders (a real tokenizer, review annotations, blame
 * gutters) had to be re-implemented per surface and kept in sync by hand.
 *
 * `CodeSurface` owns the windowing, the scroll geometry and the row chrome;
 * everything line-specific arrives through `CodeLineRenderer`. A feature plugs
 * in by supplying one or two of its methods and touches no layout code:
 *
 *   - #687 (shiki highlighting)  → `tokenize`
 *   - #688 (review UI)           → `attachment` + `gutterAction`
 *
 * Every hook is optional; omitting all of them yields plain, un-annotated code
 * with the built-in regex highlighter.
 */

/** How a line relates to the diff it belongs to. Plain files are all `context`. */
export type LineType = 'add' | 'delete' | 'context';

/**
 * One line of code as the renderer sees it. Mirrors the backend's
 * `git.DiffLine` (see `lib/hooks/use-file-diff.ts`) so diff rows pass straight
 * through; full-file rows synthesize `type: 'context'`.
 */
export interface CodeLineData {
  type: LineType;
  content: string;
  /** 1-based; absent when the line does not exist on that side of the diff. */
  old_line?: number;
  new_line?: number;
}

/**
 * Which column a cell occupies. `unified` is the single-column layout (plain
 * files and unified diffs); `old`/`new` are the left/right halves of a split
 * diff. A renderer can use this to style the two sides differently without
 * knowing anything about the surrounding layout.
 */
export type LineSide = 'unified' | 'old' | 'new';

/**
 * One gutter-group + code column inside a row. A unified row has a single cell
 * (with two gutters: old number, then new); a split row has two (old half, new
 * half), each with one gutter.
 */
export interface LineCell {
  side: LineSide;
  /** `null` is the empty half of an unbalanced split pair (a pad cell). */
  line: CodeLineData | null;
  /**
   * Gutter columns rendered before this cell's code, left to right. `undefined`
   * entries render blank (the side the line doesn't exist on). The LAST gutter
   * is the commentable one — it is the new-side number in every layout.
   */
  gutters: Array<number | undefined>;
  /**
   * The coordinate attachments anchor to — always the NEW-side line number, so
   * a given anchor names exactly one thread no matter which surface or view
   * mode produced it. Pure deletions have no anchor and take no attachments.
   */
  anchor?: number;
}

/**
 * A row in the flattened, windowable render list. Everything a surface shows is
 * one of these three, so row index ↔ scroll offset is a pure function of the
 * list — which is what makes `scrollToIndex` anchoring (search hits, comment
 * jumps) exact even with collapsed regions in play.
 */
export type CodeRow =
  | { kind: 'hunk'; key: string; content: string }
  /** A folded run of unchanged lines, replaced by an "expand N lines" button. */
  | { kind: 'gap'; key: string; hiddenCount: number }
  | {
      kind: 'line';
      key: string;
      /** One cell (unified) or two (split, left=old right=new). */
      cells: LineCell[];
      /** New-side anchor for this row's attachment, if it has one. */
      anchor?: number;
    };

/**
 * The plug point itself. All methods are optional and are called during render;
 * they must be cheap and side-effect free.
 */
export interface CodeLineRenderer {
  /**
   * Turn a line's text into renderable nodes — the highlight token stream.
   * Defaults to the built-in regex highlighter (`lib/syntax-highlight`). Only
   * lines currently inside the window are passed here, which is precisely why
   * the old "skip highlighting above N lines" degradation is no longer needed.
   */
  tokenize?(line: CodeLineData, cell: LineCell): ReactNode;
  /**
   * Full-width card rendered directly beneath a line, inside the same row
   * element. Being part of the row (rather than a row of its own) is deliberate:
   * the row is what `measureElement` observes, so a card that grows or collapses
   * re-measures in place instead of shifting every index after it.
   *
   * Return `null` for lines with nothing attached.
   */
  attachment?(anchor: number): ReactNode;
  /**
   * The gutter affordance revealed on row hover (currently "add a comment").
   * Return `undefined` to leave the gutter inert for this cell.
   */
  gutterAction?(cell: LineCell): (() => void) | undefined;
  /** Extra classes for the row element — jump flash, selection, and the like. */
  rowClassName?(row: CodeRow, index: number): string | undefined;
}
