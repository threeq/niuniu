/**
 * rehype-file-links
 * -----------------
 * Turns workspace file references inside agent chat markdown into clickable
 * "open the file" anchors. Runs at the rehype (hast) layer, mirroring
 * `rehype-linkify.ts` (URLs) — this plugin runs AFTER it in the pipeline, so
 * absolute http(s) URLs have already become `<a>` and are naturally skipped.
 *
 * Anchors are emitted with the custom `niu-file:` href scheme carrying the
 * ENCODED raw reference (exactly as it appeared in the text, possibly with a
 * `:line` suffix, possibly absolute or repo-relative). Resolving that raw ref
 * to a workspace-relative path needs the workspace path, which this plugin
 * (a pure markdown transform) does not know — the consumer component that
 * passes `onOpenFile` owns the resolution. No `niu-file:` anchors are emitted
 * unless the plugin is added to the pipeline, and `MarkdownMessage` only adds
 * it when an `onOpenFile` handler is provided.
 *
 * Skipped contexts:
 *  - `a`    — never nest links (also: URLs already linkified).
 *  - `pre`  — fenced code blocks stay verbatim (copy button handles them).
 *  - `script`/`style` — defensive, same rationale as rehype-linkify.
 *
 * Inline `<code>` spans are INTENTIONALLY linkified: agents overwhelmingly
 * reference files as `` `src/app.ts` `` and users expect the click. The
 * conservative `looksLikeFilePath` predicate keeps command snippets
 * (`pnpm dev`) and option strings unlinked.
 */

import type { Plugin } from 'unified';
import type { Root, RootContent, Text, Element, ElementContent } from 'hast';
import { matchFileRefs } from './chat-file-links';

const FILE_SCHEME = 'niu-file:';

/** Build the href for a raw reference. Encoding keeps the value intact
 *  through hast → React → DOM (slashes, CJK, `C:\` colons all survive). */
export function fileRefHref(raw: string): string {
  return FILE_SCHEME + encodeURIComponent(raw);
}

/** True when an href carries a raw file reference produced by this plugin. */
export function isFileRefHref(href: string | undefined | null): boolean {
  return !!href && href.startsWith(FILE_SCHEME);
}

/** Decode the raw reference back out of an href. */
export function fileRefFromHref(href: string): string {
  return decodeURIComponent(href.slice(FILE_SCHEME.length));
}

export function rehypeFileLinks(): Plugin<[], Root> {
  return function plugin() {
    function walk(node: Root | Element, inSkipped: boolean): void {
      const skip =
        inSkipped ||
        (node.type === 'element' &&
          (node.tagName === 'a' ||
            node.tagName === 'pre' ||
            node.tagName === 'script' ||
            node.tagName === 'style'));

      const children: (Root | Element)['children'] = node.children;
      for (let i = 0; i < children.length; i++) {
        const child = children[i] as RootContent | Element;
        if (child.type === 'text') {
          if (skip) continue;
          const matches = matchFileRefs((child as Text).value);
          if (matches.length === 0) continue;
          const replacement: ElementContent[] = [];
          const value = (child as Text).value;
          let cursor = 0;
          for (const m of matches) {
            if (m.index > cursor) {
              replacement.push({ type: 'text', value: value.slice(cursor, m.index) });
            }
            const anchor: Element = {
              type: 'element',
              tagName: 'a',
              properties: {
                href: fileRefHref(m.raw),
                // Marker read by MarkdownMessage's `a` override (belt and
                // suspenders alongside the scheme check).
                dataNiuniuFile: '1',
              },
              children: [{ type: 'text', value: m.raw }],
            };
            replacement.push(anchor);
            cursor = m.index + m.raw.length;
          }
          if (cursor < value.length) {
            replacement.push({ type: 'text', value: value.slice(cursor) });
          }
          children.splice(i, 1, ...replacement);
          i += replacement.length - 1;
        } else if (child.type === 'element') {
          walk(child, skip);
        }
      }
    }

    return function transformer(tree: Root) {
      walk(tree, false);
    };
  };
}
