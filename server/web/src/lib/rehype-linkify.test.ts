import { describe, it, expect } from 'vitest'
import { unified } from 'unified'
import type { Root, Element, Text } from 'hast'
import { rehypeLinkify } from './rehype-linkify'

/**
 * Build a minimal hast tree wrapping a single child inside <p>.
 */
function pTree(child: Element | Text): Root {
  return {
    type: 'root',
    children: [
      {
        type: 'element',
        tagName: 'p',
        properties: {},
        children: [child],
      },
    ],
  }
}

/**
 * Apply rehype-linkify synchronously via a real unified processor.
 * This exercises the actual plugin contract (transformer wiring,
 * `this: Processor` semantics) rather than poking the inner function.
 */
function runLinkify(tree: Root): Root {
  return unified().use(rehypeLinkify()).runSync(tree) as Root
}

function firstParagraphChildren(tree: Root): Array<Element | Text> {
  const p = tree.children[0] as Element
  return p.children as Array<Element | Text>
}

describe('rehypeLinkify', () => {
  it('wraps a bare URL in a <p> into an <a> with safe rel/target', () => {
    const tree = pTree({
      type: 'text',
      value: 'visit https://example.com please',
    })
    runLinkify(tree)
    const children = firstParagraphChildren(tree)
    expect(children).toHaveLength(3)
    expect(children[0]).toMatchObject({ type: 'text', value: 'visit ' })
    expect(children[2]).toMatchObject({ type: 'text', value: ' please' })

    const a = children[1] as Element
    expect(a.type).toBe('element')
    expect(a.tagName).toBe('a')
    expect(a.properties?.href).toBe('https://example.com')
    expect(a.properties?.target).toBe('_blank')
    // rel is emitted as a single space-separated string (not an array) so
    // react-markdown's hast → React DOM mapper surfaces it verbatim. Array
    // form would coerce via Array.toString = comma-join, emitting
    // rel="noopener,noreferrer" which browsers ignore entirely.
    expect(a.properties?.rel).toBe('noopener noreferrer')
    expect((a.children[0] as Text).value).toBe('https://example.com')
  })

  it('wraps a bare email into <a href="mailto:...">', () => {
    const tree = pTree({
      type: 'text',
      value: 'mail foo@bar.com today',
    })
    runLinkify(tree)
    const children = firstParagraphChildren(tree)
    const a = children.find(
      (c): c is Element => c.type === 'element' && c.tagName === 'a',
    )
    expect(a).toBeDefined()
    expect(a!.properties?.href).toBe('mailto:foo@bar.com')
    expect((a!.children[0] as Text).value).toBe('foo@bar.com')
  })

  it('linkifies URLs inside <code> (agents emit login URLs in code)', () => {
    const tree: Root = {
      type: 'root',
      children: [
        {
          type: 'element',
          tagName: 'code',
          properties: {},
          children: [{ type: 'text', value: 'see https://example.com' }],
        },
      ],
    }
    runLinkify(tree)
    const code = tree.children[0] as Element
    const a = code.children.find(
      (c): c is Element => c.type === 'element' && c.tagName === 'a',
    )
    expect(a).toBeDefined()
    expect(a!.properties?.href).toBe('https://example.com')
  })

  it('linkifies URLs inside <pre> (fenced code blocks are clickable)', () => {
    const tree: Root = {
      type: 'root',
      children: [
        {
          type: 'element',
          tagName: 'pre',
          properties: {},
          children: [{ type: 'text', value: 'curl https://example.com' }],
        },
      ],
    }
    runLinkify(tree)
    const pre = tree.children[0] as Element
    const a = pre.children.find(
      (c): c is Element => c.type === 'element' && c.tagName === 'a',
    )
    expect(a).toBeDefined()
    expect(a!.properties?.href).toBe('https://example.com')
  })

  it('does not nest links inside an existing <a>', () => {
    const tree: Root = {
      type: 'root',
      children: [
        {
          type: 'element',
          tagName: 'a',
          properties: { href: '/other' },
          children: [{ type: 'text', value: 'click https://example.com' }],
        },
      ],
    }
    runLinkify(tree)
    const a = tree.children[0] as Element
    expect(a.children).toHaveLength(1)
    expect((a.children[0] as Text).value).toBe('click https://example.com')
  })

  it('linkifies URLs inside <kbd>', () => {
    const tree: Root = {
      type: 'root',
      children: [
        {
          type: 'element',
          tagName: 'kbd',
          properties: {},
          children: [{ type: 'text', value: 'press https://x.com' }],
        },
      ],
    }
    runLinkify(tree)
    const kbd = tree.children[0] as Element
    const a = kbd.children.find(
      (c): c is Element => c.type === 'element' && c.tagName === 'a',
    )
    expect(a).toBeDefined()
    expect(a!.properties?.href).toBe('https://x.com')
  })

  it('linkifies URLs surrounded by CJK punctuation', () => {
    // Note: linkify-it (matching the original GFM autolink-literal spec)
    // requires the URL to be preceded by whitespace/punctuation/symbol —
    // NOT a letter. CJK letters (\p{L}) directly adjacent to a URL are
    // treated the same way as Latin letters: no autolink. CJK punctuation
    // (中文逗号/句号/etc — \p{P}) does trigger the boundary, so realistic
    // mixed-language prose written with proper punctuation still works.
    const tree = pTree({
      type: 'text',
      value: '访问，https://example.com，看看',
    })
    runLinkify(tree)
    const children = firstParagraphChildren(tree)
    const a = children.find(
      (c): c is Element => c.type === 'element' && c.tagName === 'a',
    )
    expect(a).toBeDefined()
    expect(a!.properties?.href).toBe('https://example.com')
  })

  it('linkifies schema-less domains like example.org', () => {
    const tree = pTree({
      type: 'text',
      value: 'go to example.org for info',
    })
    runLinkify(tree)
    const children = firstParagraphChildren(tree)
    const a = children.find(
      (c): c is Element => c.type === 'element' && c.tagName === 'a',
    )
    expect(a).toBeDefined()
    // linkify-it normalizes schema-less to http://
    expect(a!.properties?.href).toBe('http://example.org')
    expect((a!.children[0] as Text).value).toBe('example.org')
  })

  it('handles multiple URLs in a single text node', () => {
    const tree = pTree({
      type: 'text',
      value: 'see https://a.com and https://b.com okay',
    })
    runLinkify(tree)
    const children = firstParagraphChildren(tree)
    const anchors = children.filter(
      (c): c is Element => c.type === 'element' && c.tagName === 'a',
    )
    expect(anchors).toHaveLength(2)
    expect(anchors[0].properties?.href).toBe('https://a.com')
    expect(anchors[1].properties?.href).toBe('https://b.com')
  })

  it('leaves text without URLs untouched', () => {
    const tree = pTree({ type: 'text', value: 'plain text only' })
    runLinkify(tree)
    const children = firstParagraphChildren(tree)
    expect(children).toHaveLength(1)
    expect((children[0] as Text).value).toBe('plain text only')
  })
})
