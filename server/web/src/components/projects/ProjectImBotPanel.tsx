import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useMutation } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { toast } from 'sonner';
import { Bot, ExternalLink, Plus, Sparkles } from 'lucide-react';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { useIsProjectAdmin } from '@/lib/use-is-project-admin';
import { imbotApi } from '@/lib/imbot-api';
import type { Project } from '@/types/api';
import type { OwnerRef } from '@/types/org';
import { ChannelCard } from './imbot/channel-card';
import { AddChannelDialog } from './imbot/add-channel-dialog';
import { PendingChats } from './imbot/pending-chats';

interface Props {
  projectId: number;
  projectOwner?: OwnerRef;
}

export function ProjectImBotPanel({ projectId }: Props) {
  const { t } = useTranslation('projects');
  const navigate = useNavigate();
  const [addOpen, setAddOpen] = useState(false);

  const onboarding = useMutation({
    mutationFn: () => imbotApi.startOnboarding(projectId),
    onSuccess: (data) => {
      void navigate({ to: '/workspaces/$id', params: { id: String(data.workspace_id) } });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  const { data: project } = useQuery({
    queryKey: ['project', String(projectId)],
    queryFn: () => api.get<Project>(`/projects/${projectId}`),
  });
  const isAdmin = useIsProjectAdmin(project ?? null);

  const { data: channels = [], isLoading } = useQuery({
    queryKey: ['imbot-channels', projectId],
    queryFn: () => imbotApi.listChannels(projectId),
  });
  const { data: chats = [] } = useQuery({
    queryKey: ['imbot-chats', projectId],
    queryFn: () => imbotApi.listChats(projectId),
  });

  const pending = chats.filter((c) => c.status === 'pending');

  return (
    <div className="border rounded-lg p-4 space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div className="flex-1 min-w-0">
          <h3 className="text-sm font-semibold text-warm-text flex items-center gap-2">
            <Bot className="h-4 w-4 text-warm-text-muted" />
            {t('imbot.title')}
          </h3>
          <p className="text-xs text-warm-text-muted mt-1">{t('imbot.description')}</p>
          <Link
            to="/settings"
            search={{ tab: 'imbot' }}
            className="mt-1.5 inline-flex items-center gap-1 text-xs text-brand hover:underline"
          >
            <ExternalLink className="h-3 w-3" aria-hidden="true" />
            {t('imbot.manageBotsLink')}
          </Link>
        </div>
        {isAdmin && (
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="default"
              size="sm"
              onClick={() => onboarding.mutate()}
              disabled={onboarding.isPending}
            >
              <Sparkles className="h-4 w-4 mr-1" />
              {t('imbot.onboarding.startButton')}
            </Button>
            <Button type="button" variant="outline" size="sm" onClick={() => setAddOpen(true)}>
              <Plus className="h-4 w-4 mr-1" />
              {t('imbot.addChannel')}
            </Button>
          </div>
        )}
      </div>

      {pending.length > 0 && (
        <PendingChats projectId={projectId} pending={pending} canManage={isAdmin} />
      )}

      {isLoading ? (
        <p className="text-xs text-warm-text-muted text-center py-4">{t('imbot.loading')}</p>
      ) : channels.length === 0 ? (
        <p className="text-xs text-warm-text-muted text-center py-6">{t('imbot.noChannels')}</p>
      ) : (
        <div className="space-y-3">
          {channels.map((ch) => (
            <ChannelCard
              key={ch.id}
              projectId={projectId}
              channel={ch}
              chats={chats.filter((c) => c.channel_id === ch.id && c.status !== 'pending')}
              // Shared bots have no home/default project: every project sees the
              // same read-only bot (management is in account settings) and can act
              // on its own routed chats equally. So chat-level actions are gated
              // only by project-admin, identically across projects.
              canManage={isAdmin}
            />
          ))}
        </div>
      )}

      <AddChannelDialog projectId={projectId} open={addOpen} onOpenChange={setAddOpen} />
    </div>
  );
}
