import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeftRight, Link2, MessageSquare, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { confirm } from '@/lib/confirm';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { imbotApi, imbotOwnerApi } from '@/lib/imbot-api';
import type { ImBotChat } from '@/types/imbot';
import { useWritableProjects } from '@/pages/settings/imbot-settings/use-writable-projects';
import { ProjectSelect } from '@/pages/settings/imbot-settings/project-select';

interface Props {
  projectId: number;
  chat: ImBotChat;
  canManage: boolean;
}

export function ChatRow({ projectId, chat, canManage }: Props) {
  const { t } = useTranslation('projects');
  const qc = useQueryClient();
  const [bindOpen, setBindOpen] = useState(false);
  const [reassignOpen, setReassignOpen] = useState(false);
  const [reassignTarget, setReassignTarget] = useState<number | null>(null);
  const { projects: writableProjects } = useWritableProjects();

  // Reassign (design §7): move this already-active chat's message stream to a
  // different project. Owner-level endpoint; only offered to other writable
  // projects than the current one.
  const reassignTargets = writableProjects.filter((p) => p.id !== projectId);
  const reassign = useMutation({
    mutationFn: (targetProjectId: number) => imbotOwnerApi.reassignChat(chat.id, targetProjectId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['imbot-chats', projectId] });
      qc.invalidateQueries({ queryKey: ['imbot-owner-pending-chats'] });
      setReassignOpen(false);
      setReassignTarget(null);
      toast.success(t('imbot.reassignOk'));
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  const invalidateChats = () =>
    qc.invalidateQueries({ queryKey: ['imbot-chats', projectId] });

  const remove = useMutation({
    mutationFn: () => imbotApi.deleteChat(projectId, chat.id),
    onSuccess: invalidateChats,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  // Third-layer binding (design §3 layer 3): pin this chat to one task so every
  // message goes straight to it (bind_mode=workspace + pinned_issue_id), or
  // release it back to intent routing (bind_mode=project, pinned cleared).
  const bind = useMutation({
    mutationFn: (body: { bind_mode: string; pinned_issue_id: number | null }) =>
      imbotApi.patchChat(projectId, chat.id, body),
    onSuccess: () => {
      invalidateChats();
      setBindOpen(false);
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  // Issue list is fetched lazily, only while the binding popover is open.
  const issues = useQuery({
    queryKey: ['imbot-project-issues', projectId],
    queryFn: () => imbotApi.listProjectIssues(projectId),
    enabled: bindOpen,
  });

  const label = chat.chat_name || chat.chat_ext_id;
  const active = chat.status === 'active';
  const pinned = chat.bind_mode === 'workspace';
  const pinnedTitle =
    pinned && chat.pinned_issue_id != null
      ? issues.data?.find((i) => i.id === chat.pinned_issue_id)?.title
      : undefined;

  return (
    <div className="flex items-center gap-3 p-2.5">
      <MessageSquare className="h-4 w-4 text-warm-text-muted flex-shrink-0" />
      <div className="flex-1 min-w-0">
        <p className="text-sm text-warm-text truncate">{label}</p>
        <p className="text-xs text-warm-text-muted truncate">{chat.chat_ext_id}</p>
      </div>
      <Badge variant={active ? 'default' : 'outline'}>
        {active ? t('imbot.statusActive') : t('imbot.statusDisabled')}
      </Badge>

      {canManage ? (
        <Popover open={bindOpen} onOpenChange={setBindOpen}>
          <PopoverTrigger asChild>
            <Button type="button" variant="ghost" size="sm" className="gap-1">
              <Link2 className="h-3.5 w-3.5" />
              {pinned ? t('imbot.bindWorkspace') : t('imbot.bindProject')}
            </Button>
          </PopoverTrigger>
          <PopoverContent className="w-72 space-y-2" align="end">
            <p className="text-sm font-medium text-warm-text">{t('imbot.bindModeTitle')}</p>
            <button
              type="button"
              disabled={bind.isPending}
              onClick={() => bind.mutate({ bind_mode: 'project', pinned_issue_id: null })}
              className={
                'w-full rounded-md border px-2.5 py-1.5 text-left text-sm transition-colors ' +
                (!pinned
                  ? 'border-brand bg-brand/5 text-warm-text'
                  : 'border-warm-border text-warm-text-muted hover:bg-warm-muted')
              }
            >
              {t('imbot.bindProject')}
              <span className="block text-xs text-warm-text-muted">{t('imbot.bindProjectHint')}</span>
            </button>

            <div className="space-y-1">
              <p className="text-xs text-warm-text-muted">{t('imbot.bindWorkspaceHint')}</p>
              {issues.isLoading && (
                <p className="text-xs text-warm-text-muted px-1">{t('imbot.loading')}</p>
              )}
              {issues.data && issues.data.length === 0 && (
                <p className="text-xs text-warm-text-muted px-1">{t('imbot.bindNoIssues')}</p>
              )}
              <div className="max-h-48 overflow-y-auto space-y-1">
                {issues.data?.map((iss) => {
                  const selected = pinned && chat.pinned_issue_id === iss.id;
                  return (
                    <button
                      key={iss.id}
                      type="button"
                      disabled={bind.isPending}
                      onClick={() => bind.mutate({ bind_mode: 'workspace', pinned_issue_id: iss.id })}
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
      ) : (
        <Badge variant="secondary">
          {pinned ? t('imbot.bindWorkspace') : t('imbot.bindProject')}
        </Badge>
      )}

      {pinned && pinnedTitle && (
        <span className="text-xs text-warm-text-muted truncate max-w-32" title={pinnedTitle}>
          {pinnedTitle}
        </span>
      )}

      {canManage && active && reassignTargets.length > 0 && (
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

      {canManage && (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="text-destructive hover:text-destructive/80 hover:bg-destructive/10"
          disabled={remove.isPending}
          onClick={async () => {
            if (await confirm(t('imbot.removeChatConfirm', { name: label }))) {
              remove.mutate();
            }
          }}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      )}
    </div>
  );
}
