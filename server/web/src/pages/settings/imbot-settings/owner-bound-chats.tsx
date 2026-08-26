import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeftRight, Folder, Link2, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { confirm } from '@/lib/confirm';
import { Button } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { imbotApi, imbotOwnerApi } from '@/lib/imbot-api';
import type { ImBotPendingChat } from '@/types/imbot';
import type { Project } from '@/types/api';
import { ProjectSelect } from './project-select';

interface Props {
  chats: ImBotPendingChat[];
  projects: Project[];
}

// The chat->project bindings for a single bot, rendered inline under that bot's
// row (the bot identity is the enclosing card — no per-bot header here, which is
// what used to duplicate the bot list). Each row leads with the project (the
// meaningful routing target) and de-emphasizes the chat id.
//
// Per-chat actions mirror the project-scoped ChatRow: reassign to another project,
// switch the routing mode (intent vs pinned task), and unbind. They go through the
// owner-level endpoints, which derive authorization from the chat's own bot rather
// than from a project in the path — that is what makes them usable here, where one
// list can span several owners.
export function BotBoundChats({ chats, projects }: Props) {
  const { t } = useTranslation('settings');
  const qc = useQueryClient();

  const invalidate = () => qc.invalidateQueries({ queryKey: ['imbot-owner-chats'] });

  const del = useMutation({
    mutationFn: (chatId: number) => imbotOwnerApi.deleteChat(chatId),
    onSuccess: () => {
      invalidate();
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
        <ChatBindingRow
          key={chat.id}
          chat={chat}
          projects={projects}
          projectName={projectName(chat.project_id)}
          onInvalidate={invalidate}
          onDelete={() => del.mutate(chat.id)}
          deleting={del.isPending}
        />
      ))}
    </div>
  );
}

interface RowProps {
  chat: ImBotPendingChat;
  projects: Project[];
  projectName: string;
  onInvalidate: () => void;
  onDelete: () => void;
  deleting: boolean;
}

function ChatBindingRow({
  chat,
  projects,
  projectName,
  onInvalidate,
  onDelete,
  deleting,
}: RowProps) {
  const { t } = useTranslation('settings');
  const [reassignOpen, setReassignOpen] = useState(false);
  const [reassignTarget, setReassignTarget] = useState<number | null>(null);
  const [routeOpen, setRouteOpen] = useState(false);

  const label = chat.chat_name || chat.chat_ext_id;
  const pinned = chat.bind_mode === 'workspace';
  // Reassigning to the project it already routes to would be a no-op.
  const reassignTargets = projects.filter((p) => p.id !== chat.project_id);

  const reassign = useMutation({
    mutationFn: (targetProjectId: number) => imbotOwnerApi.reassignChat(chat.id, targetProjectId),
    onSuccess: () => {
      onInvalidate();
      setReassignOpen(false);
      setReassignTarget(null);
      toast.success(t('imbot.reassignOk'));
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  const route = useMutation({
    mutationFn: (body: { bind_mode: string; pinned_issue_id: number | null }) =>
      imbotOwnerApi.patchChatOwner(chat.id, body),
    onSuccess: () => {
      onInvalidate();
      setRouteOpen(false);
      toast.success(t('imbot.routeOk'));
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  // Only fetched while the routing popover is open, and only when the chat is
  // actually routed somewhere (pinning needs that project's issues).
  const issues = useQuery({
    queryKey: ['imbot-project-issues', chat.project_id],
    queryFn: () => imbotApi.listProjectIssues(chat.project_id as number),
    enabled: routeOpen && chat.project_id != null,
  });

  return (
    <div className="flex items-center gap-3 rounded-md border border-warm-border bg-warm-surface px-3 py-2">
      <Folder className="h-4 w-4 text-warm-text-muted flex-shrink-0" aria-hidden="true" />
      <div className="flex-1 min-w-0">
        <p className="text-sm font-medium text-warm-text truncate">{projectName}</p>
        <p className="text-xs text-warm-text-muted truncate">{label}</p>
      </div>

      {chat.project_id != null && (
        <Popover open={routeOpen} onOpenChange={setRouteOpen}>
          <PopoverTrigger asChild>
            <Button type="button" variant="ghost" size="sm" className="gap-1">
              <Link2 className="h-3.5 w-3.5" aria-hidden="true" />
              {pinned ? t('imbot.routePinned') : t('imbot.routeIntent')}
            </Button>
          </PopoverTrigger>
          <PopoverContent className="w-72 space-y-2" align="end">
            <p className="text-sm font-medium text-warm-text">{t('imbot.routeMode')}</p>
            <button
              type="button"
              disabled={route.isPending}
              onClick={() => route.mutate({ bind_mode: 'project', pinned_issue_id: null })}
              className={
                'w-full rounded-md border px-2.5 py-1.5 text-left text-sm transition-colors ' +
                (!pinned
                  ? 'border-brand bg-brand/5 text-warm-text'
                  : 'border-warm-border text-warm-text-muted hover:bg-warm-muted')
              }
            >
              {t('imbot.routeIntent')}
              <span className="block text-xs text-warm-text-muted">
                {t('imbot.routeIntentHint')}
              </span>
            </button>

            <div className="space-y-1">
              <p className="text-xs text-warm-text-muted">{t('imbot.routePinnedHint')}</p>
              {issues.isLoading && (
                <p className="text-xs text-warm-text-muted px-1">{t('imbot.loading')}</p>
              )}
              {issues.data && issues.data.length === 0 && (
                <p className="text-xs text-warm-text-muted px-1">{t('imbot.routeNoIssues')}</p>
              )}
              <div className="max-h-48 overflow-y-auto space-y-1">
                {issues.data?.map((iss) => {
                  const selected = pinned && chat.pinned_issue_id === iss.id;
                  return (
                    <button
                      key={iss.id}
                      type="button"
                      disabled={route.isPending}
                      onClick={() =>
                        route.mutate({ bind_mode: 'workspace', pinned_issue_id: iss.id })
                      }
                      className={
                        'w-full rounded-md border px-2.5 py-1.5 text-left text-sm truncate transition-colors ' +
                        (selected
                          ? 'border-brand bg-brand/5 text-warm-text'
                          : 'border-warm-border text-warm-text-muted hover:bg-warm-muted')
                      }
                    >
                      {iss.title}
                    </button>
                  );
                })}
              </div>
            </div>
          </PopoverContent>
        </Popover>
      )}

      {reassignTargets.length > 0 && (
        <Popover open={reassignOpen} onOpenChange={setReassignOpen}>
          <PopoverTrigger asChild>
            <Button type="button" variant="ghost" size="sm" className="gap-1">
              <ArrowLeftRight className="h-3.5 w-3.5" aria-hidden="true" />
              {t('imbot.reassign')}
            </Button>
          </PopoverTrigger>
          <PopoverContent className="w-72 space-y-2" align="end">
            <p className="text-sm font-medium text-warm-text">{t('imbot.reassignTitle')}</p>
            <p className="text-xs text-warm-text-muted">{t('imbot.reassignHint')}</p>
            <ProjectSelect
              projects={reassignTargets}
              value={reassignTarget}
              onChange={setReassignTarget}
              disabled={reassign.isPending}
            />
            <Button
              type="button"
              size="sm"
              className="w-full"
              disabled={reassignTarget == null || reassign.isPending}
              onClick={() => {
                if (reassignTarget != null) reassign.mutate(reassignTarget);
              }}
            >
              {t('imbot.reassignConfirm')}
            </Button>
          </PopoverContent>
        </Popover>
      )}

      <Button
        type="button"
        variant="ghost"
        size="sm"
        className="text-destructive hover:text-destructive"
        disabled={deleting}
        onClick={async () => {
          if (await confirm(t('imbot.unbindConfirm', { name: label }))) onDelete();
        }}
        title={t('imbot.unbind')}
      >
        <Trash2 className="h-4 w-4" aria-hidden="true" />
      </Button>
    </div>
  );
}
