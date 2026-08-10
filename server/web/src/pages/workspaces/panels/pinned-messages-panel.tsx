import { useCallback } from 'react';
import { Pin, X, User, Bot, Settings2 } from 'lucide-react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { cn } from '@/lib/utils';
import {
  listPinnedMessages,
  deletePinnedMessage,
  type PinnedMessage,
} from '@/lib/pinned-messages-api';
import { scrollToChatMessage } from '@/lib/scroll-to-chat-message';

interface PinnedMessagesPanelProps {
  workspaceId: string;
}

function RoleIcon({ role }: { role: string }) {
  if (role === 'user') return <User className="size-3.5 shrink-0 text-info" aria-hidden="true" />;
  if (role === 'system') return <Settings2 className="size-3.5 shrink-0 text-warm-text-muted" aria-hidden="true" />;
  return <Bot className="size-3.5 shrink-0 text-success" aria-hidden="true" />;
}

function formatTime(ts: number): string {
  return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

export function PinnedMessagesPanel({ workspaceId }: PinnedMessagesPanelProps) {
  const { t } = useTranslation('workspaces');
  const queryClient = useQueryClient();

  const { data: pins, isLoading } = useQuery({
    queryKey: ['pinned-messages', workspaceId],
    queryFn: () => listPinnedMessages(workspaceId),
  });

  const invalidate = useCallback(() => {
    queryClient.invalidateQueries({ queryKey: ['pinned-messages', workspaceId] });
  }, [queryClient, workspaceId]);

  const handleLocate = useCallback(
    (pin: PinnedMessage) => {
      if (!scrollToChatMessage(pin.message_id)) {
        toast.info(t('pinnedPanel.notLoaded'));
      }
    },
    [t],
  );

  const handleUnpin = useCallback(
    (pin: PinnedMessage) => {
      deletePinnedMessage(workspaceId, pin.id)
        .then(invalidate)
        .catch(() => toast.error(t('pinnedPanel.unpinFailed')));
    },
    [workspaceId, invalidate, t],
  );

  const items = pins ?? [];

  return (
    <div className="flex flex-col h-full bg-warm-surface min-h-0">
      <div className="flex items-center gap-2 px-3 h-9 border-b border-warm-border shrink-0">
        <Pin className="size-3.5 text-warm-text-muted" aria-hidden="true" />
        <span className="text-xs font-medium text-warm-text">{t('pinnedPanel.title')}</span>
        {items.length > 0 && (
          <span className="text-xs text-warm-text-muted tabular-nums">{items.length}</span>
        )}
      </div>

      <div className="flex-1 overflow-y-auto min-h-0">
        {isLoading ? (
          <div className="flex items-center justify-center py-8 text-xs text-warm-text-muted">
            {t('common:actions.loading')}
          </div>
        ) : items.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-1 py-10 px-4 text-center">
            <Pin className="size-5 text-warm-text-muted/50" aria-hidden="true" />
            <span className="text-xs text-warm-text-muted">{t('pinnedPanel.empty')}</span>
          </div>
        ) : (
          <ul className="py-1">
            {items.map((pin) => (
              <li key={pin.id} className="group/pin relative">
                <button
                  type="button"
                  onClick={() => handleLocate(pin)}
                  title={t('pinnedPanel.locate')}
                  className={cn(
                    'w-full text-left px-3 py-2 pr-8 flex items-start gap-2',
                    'hover:bg-accent transition-colors',
                  )}
                >
                  <RoleIcon role={pin.role} />
                  <span className="flex-1 min-w-0">
                    <span className="block text-xs text-warm-text line-clamp-2 break-words">
                      {pin.preview || t('pinnedPanel.noPreview')}
                    </span>
                    <span className="block text-[10px] text-warm-text-muted mt-0.5 tabular-nums">
                      {formatTime(pin.created_at)}
                    </span>
                  </span>
                </button>
                <button
                  type="button"
                  onClick={() => handleUnpin(pin)}
                  aria-label={t('pinnedPanel.unpin')}
                  title={t('pinnedPanel.unpin')}
                  className={cn(
                    'absolute top-1.5 right-1.5 rounded p-1 transition-opacity',
                    'text-warm-text-muted hover:bg-warm-border hover:text-warm-text',
                    'opacity-0 group-hover/pin:opacity-100 focus:opacity-100',
                  )}
                >
                  <X className="size-3.5" aria-hidden="true" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
