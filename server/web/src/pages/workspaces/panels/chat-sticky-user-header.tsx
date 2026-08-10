import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ChevronUp } from 'lucide-react';
import type { TimelineEvent } from './chat-message';

interface Props {
  /** Scroll container that holds the chat messages. */
  scrollRef: React.RefObject<HTMLDivElement | null>;
  /** Timeline events; we pick role==='user' && type==='text' as group anchors. */
  events: TimelineEvent[];
}

/**
 * Floating sticky header that mirrors the most recently passed user message
 * as the user scrolls down — same idea as iMessage/WeChat date dividers, but
 * keyed on user prompts so the in-flight agent reply always shows "what was
 * asked". Hidden when no user message has been scrolled past, or when the
 * matching message is still fully visible at the top.
 *
 * Implementation notes:
 * - Listens to the scroll container directly (rAF-throttled) instead of using
 *   IntersectionObserver: the latter loses precision when many user messages
 *   are clustered close to each other and only one fully crosses the
 *   threshold per frame, which gave visible jitter in early prototypes.
 * - Reads element positions via getBoundingClientRect() relative to the
 *   scroll container's own rect — so it stays correct inside a flex/grid
 *   panel that's offset from the viewport.
 */
export function ChatStickyUserHeader({ scrollRef, events }: Props) {
  const { t } = useTranslation('workspaces');
  const [activeId, setActiveId] = useState<string | null>(null);
  const rafRef = useRef<number | null>(null);

  // Memoize so useEffect/useCallback don't churn on every parent render — the
  // events array identity changes on every SSE delta, but the user-only
  // subset is what we actually depend on.
  const userEvents = useMemo(
    () => events.filter((e) => e.role === 'user' && e.type === 'text'),
    [events]
  );

  const recompute = useCallback(() => {
    const container = scrollRef.current;
    if (!container) {
      setActiveId(null);
      return;
    }
    if (userEvents.length === 0) {
      setActiveId(null);
      return;
    }
    const containerTop = container.getBoundingClientRect().top;
    // The threshold is "just below the sticky banner itself" — pick anything
    // whose top edge is at or above containerTop + 4px (small fudge so the
    // banner swaps a moment before the original scrolls fully out of view).
    const threshold = containerTop + 4;
    let candidateId: string | null = null;
    for (const ev of userEvents) {
      const el = document.getElementById(`msg-${ev.messageId}`);
      if (!el) continue;
      const top = el.getBoundingClientRect().top;
      if (top <= threshold) {
        candidateId = ev.id;
      } else {
        break; // events are in chronological order; later ones are even lower
      }
    }
    // Suppress the banner when the candidate is still essentially at the top
    // — i.e. its actual rendered top is within ~24px of the container top.
    // Avoids a duplicate-looking floating bar sitting right on top of the
    // real message.
    if (candidateId) {
      const el = document.getElementById(`msg-${candidateId}`);
      if (el) {
        const top = el.getBoundingClientRect().top;
        if (top >= containerTop - 4) {
          candidateId = null;
        }
      }
    }
    setActiveId(candidateId);
  }, [scrollRef, userEvents]);

  useEffect(() => {
    const container = scrollRef.current;
    if (!container) return;
    const onScroll = () => {
      if (rafRef.current != null) return;
      rafRef.current = requestAnimationFrame(() => {
        rafRef.current = null;
        recompute();
      });
    };
    container.addEventListener('scroll', onScroll, { passive: true });
    // Recompute when the events list changes too (new messages appended, etc.)
    recompute();
    return () => {
      container.removeEventListener('scroll', onScroll);
      if (rafRef.current != null) {
        cancelAnimationFrame(rafRef.current);
        rafRef.current = null;
      }
    };
  }, [scrollRef, recompute]);

  const active = activeId ? userEvents.find((e) => e.id === activeId) : null;
  if (!active) return null;

  const onClick = () => {
    const el = document.getElementById(`msg-${active.messageId}`);
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  };

  // Single-line preview, collapse internal whitespace so multi-line prompts
  // don't blow up the banner height.
  const preview = active.content.replace(/\s+/g, ' ').trim();

  // No negative top margin: a -mt pulls the margin-box above the opaque
  // bg-warm-surface box, leaving a see-through strip at the top edge that
  // scrolling content peeks through. Pin flush so the opaque band masks
  // from the very top; pt-1 is the (opaque) breathing gap above the bar.
  return (
    <div className="sticky top-0 z-10 -mx-4 px-4 pt-1 pb-1 bg-warm-surface pointer-events-none">
      <button
        type="button"
        onClick={onClick}
        className="pointer-events-auto w-full max-w-3xl mx-auto flex items-center gap-2 px-3 py-1.5 rounded-md border border-info/30 border-l-2 border-l-info/60 bg-info/15 shadow-sm hover:bg-info/25 transition-colors text-left"
        title={t('panels.chat.stickyUserBackToMessage')}
        aria-label={t('panels.chat.stickyUserBackToMessage')}
      >
        <span className="text-info font-semibold text-xs shrink-0">You</span>
        <span className="text-sm text-foreground truncate flex-1 min-w-0">{preview}</span>
        <ChevronUp className="w-3.5 h-3.5 text-muted-foreground shrink-0" aria-hidden="true" />
      </button>
    </div>
  );
}
