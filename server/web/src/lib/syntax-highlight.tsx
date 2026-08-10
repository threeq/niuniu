import type { ReactNode } from 'react';

/**
 * Basic, language-agnostic syntax highlighting for the line-level diff viewer.
 *
 * This is intentionally lightweight (no per-language grammar, no external
 * dependency): it recognizes the lexical categories that read well across most
 * languages — comments, string literals, numbers and a broad keyword set — and
 * wraps each in a token-classed <span>. All colors come from design-system
 * `--syntax-*` tokens (see docs/design-system.md §2.5.1); never hardcode colors.
 *
 * "Basic" means a few deliberate trade-offs: block comments are only colored
 * when they open and close on the same line, and `#` is treated as a comment
 * only when it is the first non-blank character (covers Python/YAML/shell
 * whole-line comments without breaking CSS hex colors or `i--`).
 */

const KEYWORDS = new Set([
  // declarations / structure
  'const', 'let', 'var', 'function', 'func', 'fn', 'def', 'lambda', 'class',
  'struct', 'interface', 'type', 'enum', 'trait', 'impl', 'package', 'module',
  'mod', 'namespace', 'import', 'export', 'from', 'use', 'using', 'require',
  // control flow
  'return', 'if', 'else', 'elif', 'for', 'while', 'do', 'switch', 'case',
  'default', 'break', 'continue', 'goto', 'match', 'when', 'then', 'end',
  'try', 'catch', 'finally', 'throw', 'throws', 'raise', 'except', 'with',
  'defer', 'select', 'fallthrough', 'yield', 'async', 'await', 'go',
  // modifiers / visibility
  'public', 'private', 'protected', 'static', 'final', 'abstract', 'virtual',
  'override', 'readonly', 'mut', 'pub', 'extern', 'inline', 'const',
  // types / values
  'void', 'int', 'long', 'short', 'byte', 'char', 'float', 'double', 'bool',
  'boolean', 'string', 'str', 'true', 'false', 'nil', 'null', 'none', 'undefined',
  'new', 'delete', 'this', 'self', 'super', 'extends', 'implements', 'in', 'of',
  'is', 'as', 'and', 'or', 'not', 'range', 'chan', 'map', 'pass', 'where',
]);

// Order matters: comments and strings first so their inner contents are not
// re-tokenized; identifiers last (post-classified into keyword vs plain).
const TOKEN_RE =
  /(\/\/[^\n]*|\/\*[\s\S]*?\*\/)|("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`)|(\b\d[\w.]*\b)|([A-Za-z_$][\w$]*)/g;

export function highlightCode(code: string): ReactNode[] {
  if (!code) return [code];

  // Whole-line `#` comment (Python / YAML / shell / shebang).
  if (code.trimStart().startsWith('#')) {
    return [
      <span key="c" className="text-syntax-comment italic">
        {code}
      </span>,
    ];
  }

  const nodes: ReactNode[] = [];
  let last = 0;
  let key = 0;
  TOKEN_RE.lastIndex = 0;
  let m: RegExpExecArray | null;

  while ((m = TOKEN_RE.exec(code)) !== null) {
    if (m.index > last) nodes.push(code.slice(last, m.index));

    const [full, comment, str, num, ident] = m;
    if (comment != null) {
      nodes.push(
        <span key={key++} className="text-syntax-comment italic">
          {full}
        </span>,
      );
    } else if (str != null) {
      nodes.push(
        <span key={key++} className="text-syntax-string">
          {full}
        </span>,
      );
    } else if (num != null) {
      nodes.push(
        <span key={key++} className="text-syntax-number">
          {full}
        </span>,
      );
    } else if (ident != null) {
      if (KEYWORDS.has(ident)) {
        nodes.push(
          <span key={key++} className="text-syntax-keyword">
            {full}
          </span>,
        );
      } else {
        nodes.push(full);
      }
    }
    last = m.index + full.length;
  }

  if (last < code.length) nodes.push(code.slice(last));
  return nodes;
}
