import { useTranslation } from 'react-i18next';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Folder, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { imbotOwnerApi } from '@/lib/imbot-api';
import type { ImBotPendingChat } from '@/types/imbot';
import type { Project } from '@/types/api';

interface Props {
  chats: ImBotPendingChat[];
  projects: Project[];
}

// The chat->project bindings for a single bot, rendered inline under that bot's
// row (the bot identity is the enclosing card — no per-bot header here, which is
// what used to duplicate the bot list). Each row leads with the project (the
// meaningful routing target) and de-emphasizes the chat id. Deleting a binding
// unpairs the group; it must be re-approved to route again.
export function BotBoundChats({ chats, projects }: Props) {
  const { t } = useTranslation('settings');
  const qc = useQueryClient();

  const del = useMutation({
    mutationFn: (chatId: number) => imbotOwnerApi.deleteChat(chatId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['imbot-owner-chats'] });
      toast.success(t('imbot.unbindOk'));
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  const projectName = (pid: number | null) =>
    pid == null ? t('imbot.boundNoProject') : (projects.find((p) => p.id === pid)?.name ?? `#${pid}`);

  if (chats.length === 0) {
    return <p className="text-xs text-warm-text-muted py-1">{t('imbot.noBoundChatsForBot')}</p>;
  }

  return (
    <div className="space-y-2">
      {chats.map((chat) => (
        <div
          key={chat.id}
          className="flex items-center gap-3 rounded-md border border-warm-border bg-warm-surface px-3 py-2"
        >
          <Folder className="h-4 w-4 text-warm-text-muted flex-shrink-0" aria-hidden="true" />
          <div className="flex-1 min-w-0">
            <p className="text-sm font-medium text-warm-text truncate">
              {projectName(chat.project_id)}
            </p>
            <p className="text-xs text-warm-text-muted truncate">
              {chat.chat_name || chat.chat_ext_id}
            </p>
          </div>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="text-destructive hover:text-destructive"
            disabled={del.isPending}
            onClick={() => del.mutate(chat.id)}
            title={t('imbot.unbind')}
          >
            <Trash2 className="h-4 w-4" aria-hidden="true" />
          </Button>
        </div>
      ))}
    </div>
  );
}
