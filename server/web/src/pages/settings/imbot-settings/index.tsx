import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Bot, Plus } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { imbotOwnerApi } from '@/lib/imbot-api';
import { useWritableProjects } from './use-writable-projects';
import { BotRow } from './bot-row';
import { OwnerPendingChats } from './owner-pending-chats';
import { AddBotDialog } from './add-bot-dialog';

// Owner-level "IM 机器人" settings page. A bot = one credential = one connection,
// owned by the owner and able to route different chats to different projects
// (design 2026-07-08 §10). Lives beside users/orgs in account settings.
export function ImBotSettings() {
  const { t } = useTranslation('settings');
  const [addOpen, setAddOpen] = useState(false);
  const { projects, isLoading: projectsLoading } = useWritableProjects();

  const { data: bots = [], isLoading: botsLoading } = useQuery({
    queryKey: ['imbot-owner-bots'],
    queryFn: () => imbotOwnerApi.listBots(),
  });

  const { data: pending = [] } = useQuery({
    queryKey: ['imbot-owner-pending-chats'],
    queryFn: () => imbotOwnerApi.listPendingChats(),
  });

  const { data: boundChats = [] } = useQuery({
    queryKey: ['imbot-owner-chats'],
    queryFn: () => imbotOwnerApi.listChats(),
  });

  return (
    <div className="space-y-6 py-2">
      <div className="flex items-start justify-between gap-4">
        <div className="flex-1 min-w-0">
          <h2 className="text-lg font-semibold text-warm-text flex items-center gap-2">
            <Bot className="h-5 w-5 text-warm-text-muted" aria-hidden="true" />
            {t('imbot.title')}
          </h2>
          <p className="text-sm text-warm-text-muted mt-1">{t('imbot.description')}</p>
        </div>
        <Button type="button" size="sm" onClick={() => setAddOpen(true)}>
          <Plus className="h-4 w-4 mr-1" aria-hidden="true" />
          {t('imbot.addBot')}
        </Button>
      </div>

      {pending.length > 0 && <OwnerPendingChats pending={pending} projects={projects} />}

      <div className="space-y-3">
        <h3 className="text-sm font-medium text-warm-text">{t('imbot.botsTitle')}</h3>
        {botsLoading || projectsLoading ? (
          <p className="text-sm text-warm-text-muted text-center py-4">{t('imbot.loading')}</p>
        ) : bots.length === 0 ? (
          <p className="text-sm text-warm-text-muted text-center py-6">{t('imbot.noBots')}</p>
        ) : (
          <div className="space-y-3">
            {bots.map((bot) => (
              <BotRow
                key={bot.id}
                bot={bot}
                chats={boundChats.filter((c) => c.channel_id === bot.id)}
                projects={projects}
              />
            ))}
          </div>
        )}
      </div>

      <AddBotDialog open={addOpen} onOpenChange={setAddOpen} projects={projects} />
    </div>
  );
}
