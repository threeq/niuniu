import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FlaskConical, X } from 'lucide-react';
import { cn } from '@/lib/utils';

/**
 * StudioDraftBanner — friendly hint shown at the top of a studio workspace
 * (is_studio === 1). Replaces the legacy single-line "草稿在工作舱内迭代，点
 * 【交付】才会写回你的原目录" Alert that:
 *   - referenced a Deliver button that doesn't exist in the SPA yet, so users
 *     hunted for it.
 *   - used Alert default variant (high-contrast background) for a passive
 *     hint, reading like an error / blocker.
 *   - had no dismiss affordance — re-displayed on every navigation.
 *
 * New shape:
 *   - structured (title + body) using design-system info tones (--info)
 *   - dismissible per-workspace, persisted in sessionStorage so the hint
 *     comes back on a fresh session but doesn't nag mid-session.
 *
 * Studio-only — caller already gates on workspace.is_studio === 1.
 */

interface Props {
  workspaceId: string | number;
}

const STORAGE_PREFIX = 'studio-draft-banner-dismissed:';

function readDismissed(key: string): boolean {
  if (typeof window === 'undefined') return false;
  try {
    return window.sessionStorage.getItem(key) === '1';
  } catch {
    return false;
  }
}

function writeDismissed(key: string) {
  if (typeof window === 'undefined') return;
  try {
    window.sessionStorage.setItem(key, '1');
  } catch {
    // sessionStorage may throw in private mode — swallow.
  }
}

export function StudioDraftBanner({ workspaceId }: Props) {
  const { t } = useTranslation('workspaces');
  const storageKey = `${STORAGE_PREFIX}${workspaceId}`;
  // Initialise from storage so we don't flash the banner on remount when
  // the user has already dismissed it earlier in the same session.
  const [dismissed, setDismissed] = useState(() => readDismissed(storageKey));

  // Reset dismissal when the workspace switches — different draft, different
  // hint applicability.
  useEffect(() => {
    setDismissed(readDismissed(storageKey));
  }, [storageKey]);

  if (dismissed) return null;

  return (
    <div
      role="note"
      data-testid="studio-delivery-hint"
      className={cn(
        'relative flex items-start gap-2 rounded-md border px-3 py-2',
        'border-info/30 bg-info/5 text-warm-text',
      )}
    >
      <FlaskConical className="w-4 h-4 mt-0.5 shrink-0 text-info" aria-hidden="true" />
      <div className="flex-1 min-w-0">
        <div className="text-xs font-medium leading-none mb-1">
          {t('studioHint.title')}
        </div>
        <p className="text-xs leading-snug text-warm-text-muted">
          {t('studioHint.body')}
        </p>
      </div>
      <button
        type="button"
        onClick={() => {
          writeDismissed(storageKey);
          setDismissed(true);
        }}
        aria-label={t('studioHint.dismiss')}
        title={t('studioHint.dismiss')}
        className={cn(
          'shrink-0 -mr-1 -mt-1 rounded p-1 text-warm-text-muted',
          'hover:bg-info/10 hover:text-warm-text transition-colors',
        )}
      >
        <X className="w-3.5 h-3.5" aria-hidden="true" />
      </button>
    </div>
  );
}
