/**
 * rehype-linkify
 * --------------
 * Replacement for `micromark-extension-gfm-autolink-literal`, which we
 * removed from the markdown pipeline because it ships an ES2018
 * look-behind regex that crashes macOS 12.x WKWebView at parse time.
 * See `./remark-gfm-safari-safe.ts` for the full context.
 *
 * This plugin runs at the rehype (hast) layer instead of the
 * micromark (markdown tokenizer) layer. It walks every `text` node in
 * the HTML AST and rewrites bare URLs / email addresses into `<a>`
 * elements, using `linkify-it` + `tlds` (no look-behind anywhere).
 *
 * It deliberately skips text inside existing `<a>` (don't nest links)
 * and `<script>`/`<style>` (would corrupt JS/CSS syntax).
 *
 * Code contexts (`<code>`, `<pre>`, `<kbd>`) are INTENTIONALLY linkified:
 * agents frequently emit a bare authorization/login URL inside a fenced
 * code block, and users expect to click it (and the code block's own copy
 * button handles verbatim copying). A stray URL inside a real code snippet
 * merely turns blue — the text is preserved unchanged — which is an
 * acceptable trade for making these links clickable.
 *
 * `<a>` elements are emitted with safe defaults for user-supplied
 * outbound links: `target="_blank"` plus `rel="noopener noreferrer"`.
 */

import type { Plugin } from 'unified'
import type { Root, Text, Element, ElementContent, Parent } from 'hast'
import { visit, SKIP } from 'unist-util-visit'
import LinkifyIt from 'linkify-it'
import tlds from 'tlds'

// Element tag names whose text content should NOT be auto-linkified.
// - a:    don't nest <a> inside <a>
// - script/style: defensive — if a downstream consumer enables rehype-raw
//   and HTML <script>/<style> reach hast as element nodes with text children,
//   we must not wrap URLs inside them (would break the script/CSS syntax).
// NOTE: code/pre/kbd are deliberately NOT skipped — see the module header;
// bare URLs inside code blocks are made clickable on purpose.
const SKIP_PARENT_TAGS = new Set(['a', 'script', 'style'])

export function rehypeLinkify(): Plugin<[], Root> {
  return function plugin() {
    const linkify = new LinkifyIt().tlds(tlds)

    return function transformer(tree: Root) {
      visit(tree, 'text', (node: Text, index, parent) => {
        if (
          parent == null ||
          index == null ||
          (parent.type !== 'element' && parent.type !== 'root')
        ) {
          return
        }

        // If the parent is an element with a tag we skip, bail out.
        if (
          parent.type === 'element' &&
          SKIP_PARENT_TAGS.has((parent as Element).tagName)
        ) {
          return
        }

        const value = node.value
        const matches = linkify.match(value)
        if (!matches || matches.length === 0) return

        const replacement: ElementContent[] = []
        let cursor = 0
        for (const m of matches) {
          if (m.index > cursor) {
            replacement.push({
              type: 'text',
              value: value.slice(cursor, m.index),
            })
          }

          const anchor: Element = {
            type: 'element',
            tagName: 'a',
            properties: {
              href: m.url,
              target: '_blank',
              // String form (not array) so react-markdown's hast → React
              // mapper surfaces it verbatim on the <a> DOM node. Array form
              // would coerce via Array.prototype.toString = comma-join,
              // emitting rel="noopener,noreferrer" which the browser
              // ignores entirely — defeating the security guard.
              rel: 'noopener noreferrer',
            },
            children: [{ type: 'text', value: m.text }],
          }
          replacement.push(anchor)

          cursor = m.lastIndex
        }
        if (cursor < value.length) {
          replacement.push({
            type: 'text',
            value: value.slice(cursor),
          })
        }

        // Splice the new children into the parent, then jump past them
        // so we don't revisit the freshly-created anchor's text.
        const typedParent = parent as Parent
        typedParent.children.splice(index, 1, ...replacement)
        return [SKIP, index + replacement.length]
      })
    }
  }
}
