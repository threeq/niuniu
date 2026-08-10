import { describe, it, expect } from 'vitest'
import { unified } from 'unified'
import remarkParse from 'remark-parse'
import { visit } from 'unist-util-visit'
import type { Root } from 'mdast'
import { remarkGfmSafariSafe } from './remark-gfm-safari-safe'

function parse(md: string): Root {
  const processor = unified().use(remarkParse).use(remarkGfmSafariSafe())
  return processor.runSync(processor.parse(md)) as Root
}

function collectTypes(tree: Root): string[] {
  const types: string[] = []
  visit(tree, (node) => {
    types.push(node.type)
  })
  return types
}

describe('remarkGfmSafariSafe', () => {
  it('parses GFM strikethrough as a delete node', () => {
    const tree = parse('hello ~~world~~')
    expect(collectTypes(tree)).toContain('delete')
  })

  it('parses GFM tables as table nodes', () => {
    const md = [
      '| a | b |',
      '| - | - |',
      '| 1 | 2 |',
    ].join('\n')
    const tree = parse(md)
    const types = collectTypes(tree)
    expect(types).toContain('table')
    expect(types).toContain('tableRow')
    expect(types).toContain('tableCell')
  })

  it('parses task list items with a checked flag', () => {
    const tree = parse('- [x] done\n- [ ] todo')
    let foundChecked = false
    let foundUnchecked = false
    visit(tree, 'listItem', (node) => {
      if (node.checked === true) foundChecked = true
      if (node.checked === false) foundUnchecked = true
    })
    expect(foundChecked).toBe(true)
    expect(foundUnchecked).toBe(true)
  })

  it('parses footnote references and definitions', () => {
    const md = 'See[^1]\n\n[^1]: the note'
    const tree = parse(md)
    const types = collectTypes(tree)
    expect(types).toContain('footnoteReference')
    expect(types).toContain('footnoteDefinition')
  })

  it('does NOT auto-linkify bare URLs (autolink-literal is disabled)', () => {
    const tree = parse('https://example.com bare url')
    let sawLink = false
    visit(tree, 'link', () => {
      sawLink = true
    })
    expect(sawLink).toBe(false)

    // The URL should still be present as plain text content
    let textJoined = ''
    visit(tree, 'text', (node) => {
      textJoined += node.value
    })
    expect(textJoined).toContain('https://example.com')
  })

  it('does NOT auto-linkify bare emails (autolink-literal is disabled)', () => {
    const tree = parse('contact foo@bar.com please')
    let sawLink = false
    visit(tree, 'link', () => {
      sawLink = true
    })
    expect(sawLink).toBe(false)
  })

  it('still honours explicit markdown links (built into core remark)', () => {
    const tree = parse('see [docs](https://example.com)')
    let linkUrl: string | undefined
    visit(tree, 'link', (node) => {
      linkUrl = node.url
    })
    expect(linkUrl).toBe('https://example.com')
  })
})
