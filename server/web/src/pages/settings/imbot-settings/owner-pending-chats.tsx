import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { UserCheck } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { imbotOwnerApi } from '@/lib/imbot-api';
import type { ImBotPendingChat } from '@/types/imbot';
import type { Project } from '@/types/api';
import { ProjectSelect } from './project-select';

interface Props {
  pending: ImBotPendingChat[];
  projects: Project[];
}

// Owner-level "awaiting pairing" area: every pending chat (across all bots) gets
// a project picker + Approve button. Approving routes that chat to the chosen
// project (design §7 A1). The pick defaults to the bot's default target project
// when the caller can write to it, else the first writable project.
export function OwnerPendingChats({ pending, projects }: Props) {
  const { t } = useTranslation('settings');
  const qc = useQueryClient();
  const [picks, setPicks] = useState<Record<number, number>>({});

  const defaultProjectId = projects[0]?.id ?? null;
  const pickFor = (chatId: number): number | null => picks[chatId] ?? defaultProjectId;

  const approve = useMutation({
    mutationFn: ({ chatId, projectId }: { chatId: number; projectId: number }) =>
      imbotOwnerApi.approveChat(chatId, projectId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['imbot-owner-pending-chats'] });
      toast.success(t('imbot.approveOk'));
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  return (
    <div className="border border-warning/40 bg-warning/5 rounded-lg p-4 space-y-3">
      <p className="text-sm font-medium text-warm-text flex items-center gap-1.5">
        <UserCheck className="h-4 w-4 text-warning" aria-hidden="true" />
        {t('imbot.pendingTitle', { count: pending.length })}
      </p>
      <p className="text-xs text-warm-text-muted">{t('imbot.pendingHint')}</p>
      <div className="space-y-2">
        {pending.map((chat) => {
          const picked = pickFor(chat.id);
          return (
            <div key={chat.id} className="flex items-center gap-3">
              <div className="flex-1 min-w-0">
                <p className="text-sm text-warm-text truncate">
                  {chat.chat_name || chat.chat_ext_id}
                </p>
                <p className="text-xs text-warm-text-muted truncate">{chat.chat_ext_id}</p>
              </div>
              <ProjectSelect
                projects={projects}
                value={picked}
                onChange={(pid) => setPicks((m) => ({ ...m, [chat.id]: pid }))}
                disabled={projects.length === 0 || approve.isPending}
                className="w-48"
              />
              <Button
                type="button"
                size="sm"
                disabled={picked == null || approve.isPending}
                onClick={() => {
                  if (picked == null) return;
                  approve.mutate({ chatId: chat.id, projectId: picked });
                }}
              >
                {t('imbot.approve')}
              </Button>
            </div>
          );
        })}
      </div>
      {projects.length === 0 && (
        <p className="text-xs text-destructive">{t('imbot.noWritableProjects')}</p>
      )}
    </div>
  );
}
