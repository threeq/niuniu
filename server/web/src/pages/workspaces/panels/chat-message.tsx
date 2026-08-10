import { useState } from 'react';
import { CheckCircle, RefreshCw, Pin, Copy, Check } from 'lucide-react';
import { useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import i18n from '@/i18n';
import { cn } from '@/lib/utils';
import { copyTextToClipboard } from '@/lib/copy-to-clipboard';
import { MarkdownMessage } from '@/components/shared/markdown-message';
import { ThinkingBlock } from '@/components/shared/thinking-block';
import { stripAttachmentPrefix } from '@/lib/strip-attachment-prefix';
import { ToolCallCard } from './tool-call-card';
import { AttachmentPreview } from '../components/attachment-preview';
import { DataResultBlock } from '@/components/data/data-result-block';
import { pinQuery, type PinQueryBody } from '@/lib/dashboards-api';
import { useChatInputBridge } from '@/stores/chat-input-bridge-store';
import type { ChatAttachment } from '@/types/api';
import type { DataBlock, ResultSet } from '@/types/data';

export interface TimelineEvent {
  id: string;
  messageId: string;
  type: string;
  role: string;
  content: string;
  toolName?: string;
  toolInput?: string;
  toolUseId?: string;
  isError?: boolean;
  streaming?: boolean;
  costUsd?: number;
  numTurns?: number;
  durationMs?: number;
  inputTokens?: number;
  outputTokens?: number;
  createdAt?: number;
  attachments?: ChatAttachment[];
}

function formatTime(ts?: number): string | null {
  if (!ts) return null;
  const d = new Date(ts);
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// Completion time on the "Done" line: MM-DD HH:MM:SS (local).
function formatDoneTime(ts?: number): string | null {
  if (!ts) return null;
  const d = new Date(ts);
  const p = (n: number) => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

// Compact token count: 12345 -> "12.3k".
function formatTokens(n?: number): string {
  if (n == null) return '0';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(n);
}

// SystemInfoLine renders a non-persisted observational signal from the backend
// (autohost watchdog pings). Rendered as a muted single line.
function SystemInfoLine({ anchorId, content }: { anchorId: string; content: string }) {
  return (
    <div id={anchorId} className="text-xs text-warning py-0.5">
      {content}
    </div>
  );
}

// A segment of assistant text: either ordinary markdown, or a parsed
// niuniu-data block emitted by the agent as a fenced code block.
type ContentSegment =
  | { kind: 'text'; content: string }
  | { kind: 'data'; block: DataBlock };

// Matches a chart/data fenced code block. Accepted language tags:
//   niuniu-data — canonical: body is a full DataBlock JSON;
//   chart       — alias of niuniu-data;
//   echarts     — body is either a full DataBlock, or a bare native ECharts
//                 option which gets wrapped into {chart:{type:'echarts',option}}.
// The tag may carry trailing whitespace; the body runs until the closing
// fence. Global + multi-line so a single assistant message can hold several
// blocks interleaved with prose.
const NIUNIU_DATA_FENCE = /```(niuniu-data|echarts|chart)[ \t\r]*\n([\s\S]*?)```/g;

// Cheap pre-filter mirror of NIUNIU_DATA_FENCE's tags (substring match only —
// the regex still decides validity).
const DATA_FENCE_TAGS = ['```niuniu-data', '```chart', '```echarts'];

// splitNiuniuDataSegments parses assistant content into ordered text + data
// segments. A niuniu-data fence whose body is valid JSON becomes a data
// segment; on parse failure the raw fence is left in the surrounding text so it
// falls back to a normal code block (never throws).
function splitNiuniuDataSegments(content: string): ContentSegment[] {
  const segments: ContentSegment[] = [];
  let lastIndex = 0;
  let m: RegExpExecArray | null;
  NIUNIU_DATA_FENCE.lastIndex = 0;
  while ((m = NIUNIU_DATA_FENCE.exec(content)) !== null) {
    let parsed: DataBlock | null = null;
    try {
      let obj = JSON.parse(m[2]) as DataBlock;
      // An ```echarts fence may carry a bare native ECharts option (no
      // chart/result envelope) — wrap it into the canonical DataBlock shape.
      if (
        m[1] === 'echarts' &&
        obj && typeof obj === 'object' && !Array.isArray(obj) &&
        !('chart' in obj) && !('result' in obj)
      ) {
        obj = { chart: { type: 'echarts', option: obj as unknown as Record<string, unknown> } } as DataBlock;
      }
      // Minimal shape guard. Two valid shapes:
      //  - a query-derived block: a `result` with a `columns` array;
      //  - a self-contained chart: `chart.type === 'echarts'` with an `option`.
      // Anything else falls back to a plain code block.
      const hasResult =
        !!obj?.result && Array.isArray(obj.result.columns);
      const hasEcharts =
        obj?.chart?.type === 'echarts' &&
        !!obj.chart.option &&
        typeof obj.chart.option === 'object';
      if (obj && typeof obj === 'object' && (hasResult || hasEcharts)) {
        parsed = obj;
      }
    } catch {
      parsed = null;
    }
    if (parsed) {
      if (m.index > lastIndex) {
        segments.push({ kind: 'text', content: content.slice(lastIndex, m.index) });
      }
      segments.push({ kind: 'data', block: parsed });
      lastIndex = m.index + m[0].length;
    }
    // On parse failure leave the fence in place (don't advance lastIndex past
    // it) so it renders as an ordinary code block in the trailing text segment.
  }
  if (lastIndex < content.length) {
    segments.push({ kind: 'text', content: content.slice(lastIndex) });
  }
  if (segments.length === 0) {
    segments.push({ kind: 'text', content });
  }
  return segments;
}

// AssistantContent renders assistant markdown, intercepting any niuniu-data
// fenced blocks into <DataResultBlock>. Pin wires through to the dashboards API
// with the current workspace id so the panel remembers its origin workspace.
export function AssistantContent({
  content,
  workspaceId,
  createdAt,
}: {
  content: string;
  workspaceId: string;
  /**
   * Creation time (ms epoch) of the message this content belongs to. Used as
   * the "data time" for a statically-pinned snapshot — the data was produced
   * when the agent emitted the message. Optional: surfaces (e.g. the standalone
   * assistant) without a per-message timestamp omit it, and the backend falls
   * back to the result's own query time or the pin time.
   */
  createdAt?: number;
}) {
  const queryClient = useQueryClient();

  // Only pay the regex cost when a data fence is actually present — the
  // common case (plain assistant text, streamed token-by-token) stays cheap.
  if (!DATA_FENCE_TAGS.some((tag) => content.includes(tag))) {
    return <MarkdownMessage content={content} role="assistant" />;
  }

  const segments = splitNiuniuDataSegments(content);

  const handlePin = (block: DataBlock) => {
    // Two pin shapes: a re-runnable query (source + statement) or a static
    // self-contained chart (e.g. chart.type='echarts') stored as a snapshot.
    const body: PinQueryBody = {
      name: block.title || i18n.t('data:untitledQuery'),
      chart_spec: block.chart,
      workspace_id: Number(workspaceId) || undefined,
    };
    if (block.source != null && block.statement) {
      body.source = block.source;
      body.operation = { statement: block.statement };
    }
    // Always attach a static snapshot recording the data time. A frontend pin
    // lands as a static panel (a live, source-bound panel is created only via
    // the agent's pin_query MCP tool, which resolves the source by name), so
    // this snapshot — and its message-time queried_at — is what the panel
    // renders and shows as its data time. If a source-bound panel is ever
    // created here, SaveQuery ignores the snapshot, so attaching it is safe.
    const snapshot: { result?: ResultSet; queried_at?: string } = {};
    if (block.result) snapshot.result = block.result;
    if (createdAt) snapshot.queried_at = new Date(createdAt).toISOString();
    body.snapshot = snapshot;
    pinQuery(body)
      .then(() => {
        queryClient.invalidateQueries({ queryKey: ['dashboards'] });
        toast.success(i18n.t('data:pinned'));
      })
      .catch(() => {
        toast.error(i18n.t('data:pinFailed'));
      });
  };

  // "Pin live data" on an echarts block from an integrated data source: instead
  // of snapshotting the inline option, hand the agent a prompt asking it to
  // rebuild the chart as a re-runnable, source-bound panel via the pin_query
  // tool. Auto-sent so the user gets the live chart in one click.
  const handlePinDynamic = (block: DataBlock) => {
    if (block.source == null) {
      toast.error(i18n.t('data:pinMissingSource'));
      return;
    }
    // The re-runnable query: a SQL statement, or (NoSQL) the operation object.
    const query =
      block.statement?.trim() ||
      (block.operation ? JSON.stringify(block.operation) : '') ||
      i18n.t('data:pinDynamicNoStatement');
    const prompt = i18n.t('data:pinDynamicPrompt', {
      source: String(block.source),
      title: block.title || i18n.t('data:untitledQuery'),
      statement: query,
    });
    useChatInputBridge.getState().request(workspaceId, prompt, true);
    toast.success(i18n.t('data:pinDynamicSent'));
  };

  return (
    <>
      {segments.map((seg, i) =>
        seg.kind === 'data' ? (
          <div key={`data-${i}`} className="my-2">
            <DataResultBlock
              data={seg.block}
              onPin={handlePin}
              onPinDynamic={handlePinDynamic}
            />
          </div>
        ) : seg.content.trim() ? (
          <MarkdownMessage key={`text-${i}`} content={seg.content} role="assistant" />
        ) : null,
      )}
    </>
  );
}

// PinButton is the hover-revealed bookmark toggle on a chat message. Filled +
// always-visible when the message is already pinned.
function PinButton({ pinned, onToggle }: { pinned: boolean; onToggle: () => void }) {
  const label = pinned ? i18n.t('workspaces:chatMessage.unpin') : i18n.t('workspaces:chatMessage.pin');
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-label={label}
      aria-pressed={pinned}
      title={label}
      className={cn(
        'rounded p-1 transition-opacity',
        'text-warm-text-muted hover:bg-accent hover:text-warm-text',
        pinned ? 'opacity-100 text-brand' : 'opacity-0 group-hover:opacity-100 focus:opacity-100',
      )}
    >
      <Pin className={cn('size-3.5', pinned && 'fill-current')} aria-hidden="true" />
    </button>
  );
}

// CopyButton copies this message block's markdown source to the clipboard.
// Hover-revealed like PinButton; flips to a check + success tint for 2s after a
// successful copy. Works across the Windows / macOS / Linux desktop webviews via
// the secure-context-aware helper (clipboard API with execCommand fallback).
function CopyButton({ markdown }: { markdown: string }) {
  const [copied, setCopied] = useState(false);
  const label = copied
    ? i18n.t('workspaces:chatMessage.copied')
    : i18n.t('workspaces:chatMessage.copy');
  const handleCopy = () => {
    void copyTextToClipboard(markdown).then((ok) => {
      if (!ok) {
        toast.error(i18n.t('workspaces:chatMessage.copyFailed'));
        return;
      }
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };
  return (
    <button
      type="button"
      onClick={handleCopy}
      aria-label={label}
      title={label}
      className={cn(
        'rounded p-1 transition-opacity',
        'text-warm-text-muted hover:bg-accent hover:text-warm-text',
        copied ? 'opacity-100 text-success' : 'opacity-0 group-hover:opacity-100 focus:opacity-100',
      )}
    >
      {copied ? (
        <Check className="size-3.5" aria-hidden="true" />
      ) : (
        <Copy className="size-3.5" aria-hidden="true" />
      )}
    </button>
  );
}

// MessageActions is the hover-revealed action row anchored to the top-right of a
// chat message block: copy-markdown, plus the pin toggle when pinning is
// supported in this context.
function MessageActions({
  markdown,
  pinned,
  onTogglePin,
}: {
  markdown: string;
  pinned?: boolean;
  onTogglePin?: () => void;
}) {
  return (
    <div className="absolute top-1 right-1 z-10 flex items-center gap-0.5">
      <CopyButton markdown={markdown} />
      {onTogglePin && <PinButton pinned={!!pinned} onToggle={onTogglePin} />}
    </div>
  );
}

interface ChatMessageProps {
  event: TimelineEvent;
  cliType: string;
  showAgentLabel?: boolean;
  toolResults: Map<string, { content: string; isError?: boolean }>;
  workspaceId: string;
  /**
   * Stable per-block DOM key. The first block of an assistant turn keeps the
   * bare messageId; later blocks (text split across tool calls) get
   * `messageId#N`. Used for the `msg-<key>` anchor and pin identity so pinning
   * one block doesn't mark its siblings. Falls back to messageId when absent.
   */
  blockKey?: string;
  /** Whether this message is currently pinned (drives the toggle state). */
  isPinned?: boolean;
  /** Toggle pin for this message. Absent in read-only / unsupported contexts. */
  onTogglePin?: (event: TimelineEvent) => void;
}

export function ChatMessage({ event, cliType, showAgentLabel, toolResults, workspaceId, blockKey, isPinned, onTogglePin }: ChatMessageProps) {
  const anchorId = `msg-${blockKey ?? event.messageId}`;
  switch (event.type) {
    case 'text': {
      if (event.role === 'user') {
        const time = formatTime(event.createdAt);
        return (
          <div id={anchorId} className="group relative py-2 px-3 -mx-1 bg-info/10 rounded-md border-l-2 border-info/40 min-w-0 overflow-hidden">
            <MessageActions
              markdown={stripAttachmentPrefix(event.content, event.attachments)}
              pinned={!!isPinned}
              onTogglePin={onTogglePin ? () => onTogglePin(event) : undefined}
            />
            <div className="flex items-center gap-2 text-xs mb-1">
              <span className="text-info font-semibold">You</span>
              {time && <span className="text-muted-foreground">{time}</span>}
            </div>
            {event.attachments && event.attachments.length > 0 && (
              <div className="flex flex-wrap gap-2 mb-2">
                {event.attachments.map((a) => (
                  <AttachmentPreview key={a.path} attachment={a} workspaceId={workspaceId} />
                ))}
              </div>
            )}
            <div className="text-sm text-foreground whitespace-pre-wrap break-words">{stripAttachmentPrefix(event.content, event.attachments)}</div>
          </div>
        );
      }
      if (event.role === 'system') {
        // Auto-injected autohost continue/recover prompt: delivered to the agent
        // as a user turn, but shown here as a muted system block (RefreshCw icon)
        // so it's clearly an autohost action, not something the user typed.
        const time = formatTime(event.createdAt);
        return (
          <div id={anchorId} className="py-2 px-3 -mx-1 bg-muted/40 rounded-md border-l-2 border-border min-w-0 overflow-hidden">
            <div className="flex items-center gap-2 text-xs mb-1 text-muted-foreground">
              <RefreshCw className="size-3 shrink-0" />
              <span className="font-medium">autohost</span>
              {time && <span>{time}</span>}
            </div>
            <div className="text-xs text-muted-foreground whitespace-pre-wrap break-words">{event.content}</div>
          </div>
        );
      }
      // assistant text
      return (
        <div id={anchorId} className="group relative py-1 min-w-0 overflow-hidden">
          <MessageActions
            markdown={event.content}
            pinned={!!isPinned}
            onTogglePin={onTogglePin ? () => onTogglePin(event) : undefined}
          />
          {showAgentLabel && (
            <div className="flex items-center gap-2 text-xs mb-1">
              <span className="text-success font-semibold">🐂Niuniu({cliType})</span>
              {formatTime(event.createdAt) && <span className="text-muted-foreground">{formatTime(event.createdAt)}</span>}
            </div>
          )}
          <AssistantContent
            content={event.content}
            workspaceId={workspaceId}
            createdAt={event.createdAt}
          />
          {event.streaming && (
            <span className="inline-block w-1.5 h-3.5 bg-muted-foreground animate-pulse ml-0.5 align-text-bottom" />
          )}
        </div>
      );
    }

    case 'tool_use': {
      const result = event.toolUseId ? toolResults.get(event.toolUseId) : undefined;
      return (
        <div id={anchorId}>
          <ToolCallCard
            toolName={event.toolName ?? ''}
            toolInput={event.toolInput}
            toolUseId={event.toolUseId}
            result={result?.content}
            isError={result?.isError ?? event.isError}
          />
        </div>
      );
    }

    case 'tool_result':
      // Rendered inline inside tool_use card
      return null;

    case 'thinking':
      return <div id={anchorId}><ThinkingBlock content={event.content} streaming={event.streaming} /></div>;

    case 'system_info':
      return <SystemInfoLine anchorId={anchorId} content={event.content} />;

    case 'done': {
      const doneTime = formatDoneTime(event.createdAt);
      const hasTokens =
        (event.inputTokens != null && event.inputTokens > 0) ||
        (event.outputTokens != null && event.outputTokens > 0);
      return (
        <div id={anchorId} className="flex items-center gap-1.5 text-xs text-muted-foreground py-1 border-t border-border mt-2">
          <CheckCircle className="h-3.5 w-3.5 text-success shrink-0" />
          <span className="text-success font-medium">Done</span>
          {hasTokens && (
            <span title="input / output tokens">↑{formatTokens(event.inputTokens)} ↓{formatTokens(event.outputTokens)} tokens</span>
          )}
          {event.numTurns != null && (
            <span>· {event.numTurns} turns</span>
          )}
          {event.durationMs != null && (
            <span>· {(event.durationMs / 1000).toFixed(1)}s</span>
          )}
          {doneTime && (
            <span>· {doneTime}</span>
          )}
        </div>
      );
    }

    case 'error':
      return (
        <div
          id={anchorId}
          role="alert"
          className="rounded-md bg-destructive/10 border border-destructive/30 px-3 py-2 text-xs text-destructive"
        >
          <span className="font-semibold">✗ Error: </span>
          {event.content}
        </div>
      );

    default:
      return null;
  }
}
