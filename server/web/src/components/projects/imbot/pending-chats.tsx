import { useTranslation } from 'react-i18next';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { UserCheck } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { imbotApi } from '@/lib/imbot-api';
import type { ImBotChat } from '@/types/imbot';

interface Props {
  projectId: number;
  pending: ImBotChat[];
  canManage: boolean;
}

// Highlighted "awaiting pairing" area. Approving a chat admits it into the
// project's routing; rejecting removes the pending record. No project picker —
// the channel already belongs to this project (design §6.2).
export function PendingChats({ projectId, pending, canManage }: Props) {
  const { t } = useTranslation('projects');
  const qc = useQueryClient();

  const invalidate = () => qc.invalidateQueries({ queryKey: ['imbot-chats', projectId] });

  const approve = useMutation({
    mutationFn: (chatId: number) => imbotApi.approveChat(projectId, chatId),
    onSuccess: () => {
      invalidate();
      toast.success(t('imbot.approveOk'));
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });
  const reject = useMutation({
    mutationFn: (chatId: number) => imbotApi.deleteChat(projectId, chatId),
    onSuccess: invalidate,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  return (
    <div className="border border-warning/40 bg-warning/5 rounded-md p-3 space-y-2">
      <p className="text-xs font-medium text-warm-text flex items-center gap-1.5">
        <UserCheck className="h-3.5 w-3.5 text-warning" />
        {t('imbot.pendingTitle', { count: pending.length })}
      </p>
      <div className="space-y-1.5">
        {pending.map((chat) => (
          <div key={chat.id} className="flex items-center gap-3">
            <div className="flex-1 min-w-0">
              <p className="text-sm text-warm-text truncate">{chat.chat_name || chat.chat_ext_id}</p>
              <p className="text-xs text-warm-text-muted truncate">{chat.chat_ext_id}</p>
            </div>
            {canManage && (
              <div className="flex items-center gap-1">
                <Button
                  type="button"
                  size="sm"
                  disabled={approve.isPending}
                  onClick={() => approve.mutate(chat.id)}
                >
                  {t('imbot.approve')}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={reject.isPending}
                  onClick={() => reject.mutate(chat.id)}
                >
                  {t('imbot.reject')}
                </Button>
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
