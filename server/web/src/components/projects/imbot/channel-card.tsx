import { useTranslation } from 'react-i18next';
import { Copy, Plug, PlugZap, Wifi } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import type { ImBotChannel, ImBotChat } from '@/types/imbot';
import { ChatRow } from './chat-row';

interface Props {
  projectId: number;
  channel: ImBotChannel;
  chats: ImBotChat[];
  canManage: boolean;
}

const TYPE_KEY: Record<string, string> = {
  lark: 'imbot.typeLark',
  dingtalk: 'imbot.typeDingtalk',
  telegram: 'imbot.typeTelegram',
  wework: 'imbot.typeWework',
};

// Project-side view of an IM bot: read-only bot header + the chats routed to THIS
// project. A shared bot has no owning project, so bot-level management (create /
// test / delete) lives only in account settings; every project sees the same
// read-only bot and can act on its own routed chats (via ChatRow). `canManage`
// gates the per-chat actions, which are equal across all routed projects.
export function ChannelCard({ projectId, channel, chats, canManage }: Props) {
  const { t } = useTranslation('projects');
  const connected = channel.status === 'active';

  // Webhook-mode channels need a public callback URL configured on the IM
  // platform; stream-mode channels dial out and need no URL (design §6.2).
  const webhookUrl =
    channel.connection_mode === 'webhook'
      ? `${window.location.origin}/api/imbot/webhook/${channel.id}`
      : null;
  const copyWebhookUrl = () => {
    if (!webhookUrl) return;
    navigator.clipboard.writeText(webhookUrl).then(
      () => toast.success(t('imbot.copied')),
      () => toast.error(t('imbot.copyFailed')),
    );
  };

  return (
    <div className="border border-warm-border rounded-md p-3 space-y-3">
      <div className="flex items-center gap-3">
        {connected ? (
          <PlugZap className="h-4 w-4 text-col-in-progress flex-shrink-0" aria-hidden="true" />
        ) : (
          <Plug className="h-4 w-4 text-warm-text-muted flex-shrink-0" aria-hidden="true" />
        )}
        <div className="flex-1 min-w-0">
          <p className="text-sm font-medium text-warm-text truncate">
            {channel.name}
            <span className="text-warm-text-muted font-normal ml-2">
              {TYPE_KEY[channel.channel_type] ? t(TYPE_KEY[channel.channel_type]) : channel.channel_type}
            </span>
          </p>
          <div className="flex items-center gap-2 mt-0.5">
            <Badge variant="secondary" className="gap-1">
              <Wifi className="h-3 w-3" aria-hidden="true" />
              {channel.connection_mode === 'stream'
                ? t('imbot.modeStream')
                : t('imbot.modeWebhook')}
            </Badge>
            <Badge variant={connected ? 'default' : 'outline'}>
              {connected ? t('imbot.statusActive') : t('imbot.statusDisabled')}
            </Badge>
          </div>
        </div>
      </div>

      {webhookUrl && (
        <div className="flex items-center gap-2 rounded-md bg-warm-muted px-2.5 py-1.5">
          <span className="text-xs text-warm-text-muted flex-shrink-0">{t('imbot.webhookUrl')}</span>
          <code className="text-xs text-warm-text truncate flex-1 min-w-0">{webhookUrl}</code>
          <Button type="button" variant="ghost" size="sm" className="h-6 px-1.5" onClick={copyWebhookUrl}>
            <Copy className="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
        </div>
      )}

      {chats.length > 0 && (
        <div className="border border-warm-border rounded-md divide-y divide-warm-border">
          {chats.map((chat) => (
            <ChatRow key={chat.id} projectId={projectId} chat={chat} canManage={canManage} />
          ))}
        </div>
      )}
    </div>
  );
}
