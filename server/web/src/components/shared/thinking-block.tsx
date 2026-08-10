import { useState } from 'react';

interface ThinkingBlockProps {
  content: string;
  streaming?: boolean;
}

/** Renders a collapsible thinking block similar to Claude CLI's thinking display */
export function ThinkingBlock({ content, streaming }: ThinkingBlockProps) {
  const [expanded, setExpanded] = useState(false);

  return (
    <div className="my-1">
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-1.5 text-xs text-purple-500 hover:text-purple-700 dark:hover:text-purple-400 font-mono"
      >
        <span className={`transition-transform ${expanded ? 'rotate-90' : ''}`}>▸</span>
        <span className="font-semibold">
          {streaming ? 'Thinking...' : 'Thinking'}
        </span>
        {!expanded && content && (
          <span className="text-purple-300 truncate max-w-[300px]">
            {content.slice(0, 60)}{content.length > 60 ? '...' : ''}
          </span>
        )}
      </button>
      {expanded && (
        <pre className="mt-1 ml-4 p-2 bg-purple-50 dark:bg-purple-950/30 rounded text-xs text-purple-800 dark:text-purple-300 overflow-x-auto max-h-40 whitespace-pre-wrap">
          {content || '...'}
        </pre>
      )}
    </div>
  );
}
