// Bans CJK Unified Ideographs (incl. extensions A and compat) AND
// fullwidth Chinese punctuation in source-level string literals,
// template elements, and JSXText. Comments are excluded by AST.

const CJK = /[㐀-䶿一-鿿豈-﫿、。《》「」【】！，：；？]/u

export default {
  meta: {
    type: 'problem',
    docs: { description: 'Disallow Chinese characters in source code; use i18n keys' },
    schema: [],
    messages: {
      noChineseLiteral:
        'Chinese characters are not allowed in source. Move text to src/i18n/locales/<lang>/<namespace>.json and call t(\'<namespace>:<key>\').',
    },
  },
  create(context) {
    return {
      Literal(node) {
        if (typeof node.value === 'string' && CJK.test(node.value)) {
          context.report({ node, messageId: 'noChineseLiteral' })
        }
      },
      TemplateElement(node) {
        if (CJK.test(node.value.raw)) {
          context.report({ node, messageId: 'noChineseLiteral' })
        }
      },
      JSXText(node) {
        if (CJK.test(node.value)) {
          context.report({ node, messageId: 'noChineseLiteral' })
        }
      },
    }
  },
}
