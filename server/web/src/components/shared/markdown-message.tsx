import { useRef, useState, type ComponentPropsWithoutRef } from 'react';
import { Copy, Check } from 'lucide-react';
import { toast } from 'sonner';
import ReactMarkdown from 'react-markdown';
import { remarkGfmSafariSafe } from '@/lib/remark-gfm-safari-safe';
import { rehypeLinkify } from '@/lib/rehype-linkify';
import { useConfigStore } from '@/stores/config-store';
import { openExternalUrl } from '@/lib/shell';
import { copyTextToClipboard } from '@/lib/copy-to-clipboard';
import { cn } from '@/lib/utils';
import i18n from '@/i18n';
import type { Components } from 'react-markdown';

// CodeBlock renders a fenced code block with a hover-revealed copy button in
// the top-right corner. Long, unwrapped strings (e.g. an OAuth device-login
// URL an agent emits inside ```) are painful to select by hand and the dark
// code surface hides the selection highlight — one-click copy sidesteps both.
// Defined at module scope (stable identity) so react-markdown does not remount
// it on every streamed token, which would drop the transient "copied" state.
function CodeBlock({ children, ...props }: ComponentPropsWithoutRef<'pre'>) {
  const preRef = useRef<HTMLPreElement>(null);
  const [copied, setCopied] = useState(false);
  const label = copied
    ? i18n.t('workspaces:chatMessage.copied')
    : i18n.t('workspaces:chatMessage.copy');

  const handleCopy = () => {
    const text = preRef.current?.textContent ?? '';
    if (!text) return;
    void copyTextToClipboard(text).then((ok) => {
      if (!ok) {
        toast.error(i18n.t('workspaces:chatMessage.copyFailed'));
        return;
      }
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  return (
    <div className="group/code relative">
      <pre ref={preRef} className="overflow-x-auto max-w-full whitespace-pre" {...props}>
        {children}
      </pre>
      <button
        type="button"
        onClick={handleCopy}
        aria-label={label}
        title={label}
        className={cn(
          'absolute right-2 top-2 rounded p-1 transition-opacity',
          'bg-background/70 text-warm-text-muted hover:bg-accent hover:text-warm-text',
          copied
            ? 'opacity-100 text-success'
            : 'opacity-0 group-hover/code:opacity-100 focus:opacity-100',
        )}
      >
        {copied ? (
          <Check className="size-3.5" aria-hidden="true" />
        ) : (
          <Copy className="size-3.5" aria-hidden="true" />
        )}
      </button>
    </div>
  );
}

// Module-level constants — agent message rendering is per-token streaming
// (high frequency); re-allocating plugin arrays + LinkifyIt instance on
// every render is wasteful. Stable identities also let React skip
// react-markdown re-work when other props are unchanged.
const REMARK_PLUGINS = [remarkGfmSafariSafe()];
const REHYPE_PLUGINS = [rehypeLinkify()];

interface MarkdownMessageProps {
  content: string;
  role: 'user' | 'assistant';
}

export function MarkdownMessage({ content, role }: MarkdownMessageProps) {
  const handleLinkClick = (href: string) => {
    if (!href.startsWith('http://') && !href.startsWith('https://')) return;
    // Personal edition: server runs on the user's own machine, so route the
    // URL through /api/shell/open-external which launches the OS default
    // browser. Team/hosted edition: open in a new tab inside the user's own
    // browser — calling the shell endpoint would open it on the server host.
    if (useConfigStore.getState().personalMode) {
      openExternalUrl(href).catch(() => {
        window.open(href, '_blank', 'noopener,noreferrer');
      });
    } else {
      window.open(href, '_blank', 'noopener,noreferrer');
    }
  };

  const components: Components = {
    a: ({ href, children, ...props }) => {
      const isExternal = href?.startsWith('http://') || href?.startsWith('https://');
      if (isExternal && href) {
        return (
          <a
            href={href}
            className="break-all"
            onClick={(e) => {
              e.preventDefault();
              handleLinkClick(href);
            }}
            {...props}
          >
            {children}
          </a>
        );
      }
      return (
        <a href={href} className="break-all" {...props}>
          {children}
        </a>
      );
    },
    pre: CodeBlock,
    code: ({ children, ...props }) => (
      <code className="break-words whitespace-pre-wrap" {...props}>
        {children}
      </code>
    ),
    table: ({ children, ...props }) => (
      <div className="overflow-x-auto max-w-full">
        <table {...props}>{children}</table>
      </div>
    ),
  };

  if (role === 'user') {
    return (
      <div className="whitespace-pre-wrap break-words text-sm text-foreground">{content}</div>
    );
  }

  return (
    <div className="prose prose-sm dark:prose-invert max-w-none min-w-0 text-foreground break-words [&_pre]:overflow-x-auto [&_pre]:max-w-full">
      <ReactMarkdown
        remarkPlugins={REMARK_PLUGINS}
        rehypePlugins={REHYPE_PLUGINS}
        components={components}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
}
