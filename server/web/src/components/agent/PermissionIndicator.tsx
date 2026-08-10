import { useEffect, useRef, useState, type RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { Bell } from 'lucide-react';
import { usePermissionStore } from '@/stores/permission-store';

interface Props {
  workspaceId: number;
  scrollContainerRef: RefObject<HTMLElement | null>;
  cardSelector?: string;
  onClick?: () => void;
}

export function PermissionIndicator({
  workspaceId,
  scrollContainerRef,
  cardSelector = '[data-permission-card="pending"]',
  onClick,
}: Props) {
  const { t } = useTranslation();
  // Don't `?? []` inside the selector — a fresh `[]` on every miss makes the
  // zustand snapshot churn against Object.is and loops React (#185 in prod).
  const list = usePermissionStore((s) => s.byWorkspace.get(workspaceId));
  const pending = (list ?? []).filter((r) => r.status === 'pending');
  // `null` = not-yet-observed (treated as visible — hides the indicator);
  // observer flips it to true/false once the latest card is mounted.
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
      className="flex items-center gap-1 rounded-full bg-amber-500/10 px-2 py-1 text-xs text-amber-600 hover:bg-amber-500/20"
      onClick={onClick}
    >
      <Bell className="h-3 w-3" />
      {t('workspaces:permission.indicator.pendingCount', { count: pending.length })}
    </button>
  );
}
