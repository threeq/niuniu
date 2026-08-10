import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Check, Pencil, Plug, PlugZap, Wifi, X } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { imbotOwnerApi } from '@/lib/imbot-api';
import type { ImBotBot, ImBotPendingChat } from '@/types/imbot';
import type { Project } from '@/types/api';
import { BotBoundChats } from './owner-bound-chats';

interface Props {
  bot: ImBotBot;
  chats: ImBotPendingChat[];
  projects: Project[];
}

const TYPE_KEY: Record<string, string> = {
  lark: 'imbot.typeLark',
  dingtalk: 'imbot.typeDingtalk',
  telegram: 'imbot.typeTelegram',
  wework: 'imbot.typeWework',
};

// One owner-level bot: name / platform / connection mode / status + a rename
// affordance and a connectivity test button, with its chat->project bindings
// listed inline beneath. A bot is a single credential and a single connection,
// owned by the owner; its chats are routed per-chat (see pending list), so there
// is no default/home project here. One platform can hold many bots, so the name
// is editable to keep them apart.
export function BotRow({ bot, chats, projects }: Props) {
  const { t } = useTranslation('settings');
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(bot.name);

  const test = useMutation({
    mutationFn: () => imbotOwnerApi.testBot(bot.id),
    onSuccess: () => toast.success(t('imbot.testOk')),
    onError: (e) =>
      toast.error(t('imbot.testFailed', { message: e instanceof Error ? e.message : String(e) })),
  });

  const rename = useMutation({
    mutationFn: (name: string) => imbotOwnerApi.updateBot(bot.id, name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['imbot-owner-bots'] });
      setEditing(false);
      toast.success(t('imbot.renameOk'));
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  const startEdit = () => {
    setDraft(bot.name);
    setEditing(true);
  };
  const submitRename = () => {
    const name = draft.trim();
    if (!name || name === bot.name) {
      setEditing(false);
      return;
    }
    rename.mutate(name);
  };

  const connected = bot.status === 'active';
  const typeLabel = TYPE_KEY[bot.channel_type] ? t(TYPE_KEY[bot.channel_type]) : bot.channel_type;

  return (
    <div className="border border-warm-border rounded-md p-3 space-y-3">
      <div className="flex items-center gap-3">
        {connected ? (
          <PlugZap className="h-4 w-4 text-success flex-shrink-0" aria-hidden="true" />
        ) : (
          <Plug className="h-4 w-4 text-warm-text-muted flex-shrink-0" aria-hidden="true" />
        )}
        <div className="flex-1 min-w-0">
          {editing ? (
            <div className="flex items-center gap-1.5">
              <Input
                autoFocus
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') submitRename();
                  if (e.key === 'Escape') setEditing(false);
                }}
                className="h-7 max-w-56"
                aria-label={t('imbot.renameLabel')}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                disabled={rename.isPending}
                onClick={submitRename}
                title={t('imbot.renameSave')}
              >
                <Check className="h-4 w-4" aria-hidden="true" />
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                disabled={rename.isPending}
                onClick={() => setEditing(false)}
                title={t('imbot.cancel')}
              >
                <X className="h-4 w-4" aria-hidden="true" />
              </Button>
            </div>
          ) : (
            <p className="text-sm font-medium text-warm-text truncate flex items-center gap-1.5">
              <span className="truncate">{bot.name}</span>
              <span className="text-warm-text-muted font-normal">{typeLabel}</span>
              <button
                type="button"
                onClick={startEdit}
                className="text-warm-text-muted hover:text-warm-text flex-shrink-0"
                title={t('imbot.rename')}
                aria-label={t('imbot.rename')}
              >
                <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </p>
          )}
          <div className="flex items-center gap-2 mt-0.5">
            <Badge variant="secondary" className="gap-1">
              <Wifi className="h-3 w-3" aria-hidden="true" />
              {bot.connection_mode === 'stream' ? t('imbot.modeStream') : t('imbot.modeWebhook')}
            </Badge>
            <Badge variant={connected ? 'default' : 'outline'}>
              {connected ? t('imbot.statusActive') : t('imbot.statusDisabled')}
            </Badge>
          </div>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={test.isPending}
          onClick={() => test.mutate()}
        >
          {t('imbot.test')}
        </Button>
      </div>
      <BotBoundChats chats={chats} projects={projects} />
    </div>
  );
}
