import { useEffect, useRef, useState, type RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { HelpCircle } from 'lucide-react';
import { useAskUserStore } from '@/stores/ask-user-store';

interface Props {
  workspaceId: number;
  scrollContainerRef: RefObject<HTMLElement | null>;
  cardSelector?: string;
  onClick?: () => void;
}

// AskUserIndicator mirrors PermissionIndicator — surfaces a pill in the
// chat toolbar when pending ask-user-question cards exist but the latest
// one isn't currently visible (user scrolled away). Clicking it scrolls
// the first pending card back into view.
export function AskUserIndicator({
  workspaceId,
  scrollContainerRef,
  cardSelector = '[data-ask-user-card="pending"]',
  onClick,
}: Props) {
  const { t } = useTranslation();
  // Same selector pattern as PermissionIndicator — don't `?? []` inside
  // the selector or zustand churns the snapshot and loops React.
  const list = useAskUserStore((s) => s.byWorkspace.get(workspaceId));
  const pending = (list ?? []).filter((r) => r.status === 'pending');
  const [latestVisible, setLatestVisible] = useState<boolean | null>(null);
  const observerRef = useRef<IntersectionObserver | null>(null);

  useEffect(() => {
    const root = scrollContainerRef.current;
    if (!root || pending.length === 0) {
      observerRef.current?.disconnect();
      observerRef.current = null;
      return;
    }
    const cards = Array.from(root.querySelectorAll(cardSelector));
    const last = cards[cards.length - 1] as HTMLElement | undefined;
    if (!last) {
      observerRef.current?.disconnect();
      observerRef.current = null;
      return;
    }
    observerRef.current?.disconnect();
    observerRef.current = new IntersectionObserver(
      ([entry]) => setLatestVisible(entry.isIntersecting),
      { root, threshold: 0.5 },
    );
    observerRef.current.observe(last);
    return () => observerRef.current?.disconnect();
  }, [pending.length, scrollContainerRef, cardSelector]);

  if (pending.length === 0 || latestVisible !== false) return null;

  return (
    <button
      type="button"
      className="flex items-center gap-1 rounded-full bg-warning/10 px-2 py-1 text-xs text-warning hover:bg-warning/20"
      onClick={onClick}
    >
      <HelpCircle className="h-3 w-3" />
      {t('workspaces:askUser.indicator.pendingCount', { count: pending.length })}
    </button>
  );
}
