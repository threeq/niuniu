import { useRef, useState, type ComponentPropsWithoutRef } from 'react';
import { Copy, Check } from 'lucide-react';
import { toast } from 'sonner';
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown';
import { remarkGfmSafariSafe } from '@/lib/remark-gfm-safari-safe';
import { rehypeLinkify } from '@/lib/rehype-linkify';
import { rehypeFileLinks, isFileRefHref, fileRefFromHref } from '@/lib/rehype-file-links';
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
// File-link variant: appends the workspace file-reference linkifier (which
// must run AFTER rehypeLinkify so absolute http(s) URLs are claimed first).
// Used only when an `onOpenFile` handler is provided.
const REHYPE_PLUGINS_WITH_FILES = [rehypeLinkify(), rehypeFileLinks()];

interface MarkdownMessageProps {
  content: string;
  role: 'user' | 'assistant';
  /**
   * When provided, workspace file references found in the text (plain or
   * inline-code) and relative markdown links become clickable: click invokes
   * `onOpenFile(rawRef)` with the reference exactly as written (possibly
   * absolute, possibly with a `:line` suffix). The caller owns resolution to
   * a workspace-relative path and where to open the file. Absent → text
   * renders exactly as before (other MarkdownMessage consumers).
   */
  onOpenFile?: (rawRef: string) => void;
}

export function MarkdownMessage({ content, role, onOpenFile }: MarkdownMessageProps) {
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

  // react-markdown's defaultUrlTransform strips unknown schemes — without
  // this, every `niu-file:` href the file-links plugin emits would be
  // rewritten to '' before reaching the `a` component.
  const urlTransform = (url: string) => (isFileRefHref(url) ? url : defaultUrlTransform(url));

  const components: Components = {
    a: ({ href, children, ...props }) => {
      if (href && isFileRefHref(href)) {
        if (!onOpenFile) return <span className="break-all">{children}</span>;
        return (
          <a
            href={href}
            title={i18n.t('workspaces:chatMessage.openFile')}
            className="break-all"
            onClick={(e) => {
              e.preventDefault();
              onOpenFile(fileRefFromHref(href));
            }}
            {...props}
          >
            {children}
          </a>
        );
      }
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
      // Relative markdown links ([label](docs/a.md)) point at workspace files
      // when a file-open handler exists — open them the same way. `#anchor`,
      // `mailto:` and any other scheme-carrying href keep their native
      // behavior.
      if (
        onOpenFile &&
        href &&
        !href.startsWith('#') &&
        !/^[A-Za-z][A-Za-z0-9+.-]*:/.test(href)
      ) {
        return (
          <a
            href={href}
            title={i18n.t('workspaces:chatMessage.openFile')}
            className="break-all"
            onClick={(e) => {
              e.preventDefault();
              onOpenFile(href);
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
        rehypePlugins={onOpenFile ? REHYPE_PLUGINS_WITH_FILES : REHYPE_PLUGINS}
        urlTransform={urlTransform}
        components={components}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
}
