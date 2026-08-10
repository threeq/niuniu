import type { ReactNode } from 'react';

const HASH_COMMENT_EXTS = new Set(['py', 'sh', 'bash', 'zsh', 'yaml', 'yml', 'toml', 'rb']);

export function highlightLine(line: string, isHashComment: boolean): ReactNode {
  const trimmed = line.trimStart();
  if (isHashComment && trimmed.startsWith('#')) {
    return <span className="text-gray-400 italic">{line}</span>;
  }
  if (trimmed.startsWith('//')) {
    return <span className="text-gray-400 italic">{line}</span>;
  }

  const tokens: ReactNode[] = [];
  const keywords = /\b(import|export|from|function|const|let|var|return|if|else|for|while|class|interface|type|package|func|def|struct|switch|case|default|break|continue|async|await|try|catch|throw|new|this|null|undefined|true|false|nil)\b/g;
  const strings = /(["'`])(?:(?!\1|\\).|\\.)*?\1/g;
  const numbers = /\b\d+\.?\d*\b/g;

  type Token = { start: number; end: number; className: string };
  const tokenRanges: Token[] = [];

  let m: RegExpExecArray | null;
  while ((m = keywords.exec(line)) !== null) {
    tokenRanges.push({ start: m.index, end: m.index + m[0].length, className: 'text-purple-600' });
  }
  while ((m = strings.exec(line)) !== null) {
    tokenRanges.push({ start: m.index, end: m.index + m[0].length, className: 'text-green-600' });
  }
  while ((m = numbers.exec(line)) !== null) {
    tokenRanges.push({ start: m.index, end: m.index + m[0].length, className: 'text-amber-600' });
  }

  tokenRanges.sort((a, b) => a.start - b.start);

  let pos = 0;
  for (const t of tokenRanges) {
    if (t.start < pos) continue;
    if (t.start > pos) {
      tokens.push(line.slice(pos, t.start));
    }
    tokens.push(<span key={`${t.start}-${t.className}`} className={t.className}>{line.slice(t.start, t.end)}</span>);
    pos = t.end;
  }
  if (pos < line.length) {
    tokens.push(line.slice(pos));
  }

  return tokens.length > 0 ? <>{tokens}</> : line;
}

export function simpleHighlight(code: string, filename: string): ReactNode {
  const ext = filename.split('.').pop()?.toLowerCase() ?? '';
  const isHash = HASH_COMMENT_EXTS.has(ext);

  const lines = code.split('\n');
  return lines.map((line, i) => (
    <div key={i}>{highlightLine(line, isHash)}</div>
  ));
}

export function simpleHighlightWithLineNumbers(code: string, filename: string): ReactNode {
  const ext = filename.split('.').pop()?.toLowerCase() ?? '';
  const isHash = HASH_COMMENT_EXTS.has(ext);

  const lines = code.split('\n');
  const width = String(lines.length).length;

  return lines.map((line, i) => (
    <div key={i} className="flex">
      <span
        className="shrink-0 select-none text-right pr-3 text-gray-400"
        style={{ width: `${width + 1}ch` }}
      >
        {i + 1}
      </span>
      <span className="flex-1 whitespace-pre-wrap break-all">
        {highlightLine(line, isHash) || '\u200B'}
      </span>
    </div>
  ));
}
