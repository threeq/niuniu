/**
 * remark-gfm-safari-safe
 * ----------------------
 * Behaviour-equivalent drop-in for `remark-gfm`, with ONE deliberate
 * difference: it does NOT load `micromark-extension-gfm-autolink-literal`.
 *
 * Why:
 *   niuniu desktop personal edition targets macOS 12.x (Safari 15.1 /
 *   WKWebView). That JavaScriptCore parses the SPA bundle eagerly, and
 *   `micromark-extension-gfm-autolink-literal` contains an ES2018
 *   look-behind regex of the shape
 *       /(?<=\s|\p{P}|\p{S})([-.\w+]+)@.../gu
 *   which throws SyntaxError at parse time on that engine. A single
 *   throwing module in the dependency graph crashes the whole SPA and
 *   the user sees a blank page right after splash.
 *
 *   See the v1.0.21 vendor report ("mac 版本打不开") and
 *   `docs/superpowers/specs/2026-05-17-safari-12-gfm-fix.md` for the
 *   full diagnosis.
 *
 * What this plugin assembles instead:
 *   - strikethrough  (~~text~~)
 *   - GFM tables
 *   - task list items  (- [x])
 *   - footnotes
 *
 * Autolink-literal behaviour (turning bare URLs / emails into <a>) is
 * handled later in the pipeline by `rehype-linkify.ts`, which walks the
 * HTML AST instead of the markdown tokeniser and uses `linkify-it` —
 * no look-behind anywhere.
 *
 * The structure mirrors the upstream `remark-gfm` source: it pushes
 * micromark extensions, fromMarkdown extensions, and toMarkdown
 * extensions onto the processor's `data` channels. It is a pure
 * configuration plugin with no transformer.
 */

import type { Plugin, Processor } from 'unified'
import type { Root } from 'mdast'

import { gfmStrikethrough } from 'micromark-extension-gfm-strikethrough'
import { gfmTable } from 'micromark-extension-gfm-table'
import { gfmTaskListItem } from 'micromark-extension-gfm-task-list-item'
import { gfmFootnote } from 'micromark-extension-gfm-footnote'

import {
  gfmStrikethroughFromMarkdown,
  gfmStrikethroughToMarkdown,
} from 'mdast-util-gfm-strikethrough'
import {
  gfmTableFromMarkdown,
  gfmTableToMarkdown,
} from 'mdast-util-gfm-table'
import {
  gfmTaskListItemFromMarkdown,
  gfmTaskListItemToMarkdown,
} from 'mdast-util-gfm-task-list-item'
import {
  gfmFootnoteFromMarkdown,
  gfmFootnoteToMarkdown,
} from 'mdast-util-gfm-footnote'

/**
 * Drop-in replacement for `remark-gfm` minus autolink-literal.
 *
 * Usage:
 *   unified()
 *     .use(remarkParse)
 *     .use(remarkGfmSafariSafe())
 *     .use(remarkRehype)
 *     .use(rehypeLinkify)        // <- autolink replacement
 *     .use(rehypeStringify)
 */
export function remarkGfmSafariSafe(): Plugin<[], Root> {
  return function plugin(this: Processor) {
    const data = this.data() as {
      micromarkExtensions?: unknown[]
      fromMarkdownExtensions?: unknown[]
      toMarkdownExtensions?: unknown[]
    }

    const micromarkExtensions =
      data.micromarkExtensions || (data.micromarkExtensions = [])
    const fromMarkdownExtensions =
      data.fromMarkdownExtensions || (data.fromMarkdownExtensions = [])
    const toMarkdownExtensions =
      data.toMarkdownExtensions || (data.toMarkdownExtensions = [])

    // micromark tokenizer extensions (4, not 5: autolink-literal omitted)
    micromarkExtensions.push(gfmStrikethrough())
    micromarkExtensions.push(gfmTable())
    micromarkExtensions.push(gfmTaskListItem())
    micromarkExtensions.push(gfmFootnote())

    // mdast token → tree builders
    fromMarkdownExtensions.push(gfmStrikethroughFromMarkdown())
    fromMarkdownExtensions.push(gfmTableFromMarkdown())
    fromMarkdownExtensions.push(gfmTaskListItemFromMarkdown())
    fromMarkdownExtensions.push(gfmFootnoteFromMarkdown())

    // mdast tree → markdown serializers (wrapped per upstream convention)
    toMarkdownExtensions.push({
      extensions: [
        gfmStrikethroughToMarkdown(),
        gfmTableToMarkdown(),
        gfmTaskListItemToMarkdown(),
        gfmFootnoteToMarkdown(),
      ],
    })
  }
}
