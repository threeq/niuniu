import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from '@tanstack/react-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeftRight, Eye, Link2, MessageSquare, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { confirm } from '@/lib/confirm';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { imbotApi, imbotOwnerApi } from '@/lib/imbot-api';
import { intakeApi } from '@/lib/intake-api';
import type { ImBotAgentMode, ImBotChat, ImBotIntakeMode } from '@/types/imbot';
import { useWritableProjects } from '@/pages/settings/imbot-settings/use-writable-projects';
import { ProjectSelect } from '@/pages/settings/imbot-settings/project-select';

interface Props {
  projectId: number;
  chat: ImBotChat;
  canManage: boolean;
}

const INTAKE_MODES: ImBotIntakeMode[] = ['respond', 'auto'];

export function ChatRow({ projectId, chat, canManage }: Props) {
  const { t } = useTranslation('projects');
  const qc = useQueryClient();
  const [bindOpen, setBindOpen] = useState(false);
  const [modeOpen, setModeOpen] = useState(false);
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

  // Agent-employee mode (issue #664): 'command' answers only when addressed,
  // 'observe' also watches the conversation and may report or start work on its
  // own. A distinct mutation from `bind` because the two are orthogonal — a chat
  // can be pinned to one task AND observe, or neither.
  const setMode = useMutation({
    mutationFn: (agent_mode: ImBotAgentMode) => imbotApi.patchChat(projectId, chat.id, { agent_mode }),
    onSuccess: () => {
      invalidateChats();
      setModeOpen(false);
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  // Per-chat intake mode (design §6.7.7). 'inherit' follows the project's im_bot
  // setting; the rest override it for this chat only. Shares the settings cache
  // key with the intake policy panel, so this adds no extra request there.
  const setIntakeMode = useMutation({
    mutationFn: (intake_mode: ImBotIntakeMode) =>
      imbotApi.patchChat(projectId, chat.id, { intake_mode }),
    onSuccess: invalidateChats,
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  // Pending calibration decisions for this project; the key is shared across
  // rows so the whole channel card costs one request. Counted per chat below.
  const pendingDecisions = useQuery({
    queryKey: ['imbot-chat-pending-decisions', projectId],
    queryFn: () => intakeApi.listDecisions(projectId, { executed: false, limit: 200 }),
    retry: false,
  });

  const label = chat.chat_name || chat.chat_ext_id;
  const active = chat.status === 'active';
  const pinned = chat.bind_mode === 'workspace';
  // Rows fetched before agent_mode existed carry undefined; treat that as the
  // conservative default, matching the backend's normalization.
  const observing = chat.agent_mode === 'observe';
  const pinnedTitle =
    pinned && chat.pinned_issue_id != null
      ? issues.data?.find((i) => i.id === chat.pinned_issue_id)?.title
      : undefined;

  // A row written before intake_mode existed carries no value; treat it as the
  // backend's dry-run default. The project's own im_bot mode is what "inherit"
  // resolves to, so the override badge compares values rather than provenance.
  // No project default any more: every chat carries its own mode. A legacy row
  // that still says "inherit" is shown (and eventually written back) as observe.
  const rawIntakeMode: ImBotIntakeMode = chat.intake_mode ?? 'respond';
  const chatIntakeMode: ImBotIntakeMode =
    rawIntakeMode === 'auto' || rawIntakeMode === 'observe' ? 'auto' : 'respond';
  const pendingCount = (pendingDecisions.data ?? []).filter(
    (d) =>
      !d.executed &&
      (d.decision === 'accept' || d.decision === 'resume') &&
      d.source_kind === 'im_bot' &&
      d.payload?.ChatExtID === chat.chat_ext_id,
  ).length;

  return (
    <div className="space-y-1 p-2.5">
      <div className="flex flex-wrap items-center gap-2 sm:gap-3">
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
              <Button
                type="button"
                variant="ghost"
                aria-pressed={!pinned}
                disabled={bind.isPending}
                onClick={() => bind.mutate({ bind_mode: 'project', pinned_issue_id: null })}
                className={
                  'h-auto w-full flex-col items-start justify-start gap-0 whitespace-normal rounded-md border px-2.5 py-1.5 text-left text-sm font-normal ' +
                  (!pinned
                    ? 'border-brand bg-brand/5 text-warm-text'
                    : 'border-warm-border text-warm-text-muted hover:bg-warm-muted')
                }
              >
                {t('imbot.bindProject')}
                <span className="block text-xs text-warm-text-muted">{t('imbot.bindProjectHint')}</span>
              </Button>

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
                      <Button
                        key={iss.id}
                        type="button"
                        variant="ghost"
                        aria-pressed={selected}
                        disabled={bind.isPending}
                        onClick={() => bind.mutate({ bind_mode: 'workspace', pinned_issue_id: iss.id })}
                        className={
                          'h-auto w-full justify-start gap-0 whitespace-normal rounded-md border px-2.5 py-1.5 text-left text-sm font-normal ' +
                          (selected
                            ? 'border-brand bg-brand/5 text-warm-text'
                            : 'border-warm-border text-warm-text-muted hover:bg-warm-muted')
                        }
                      >
                        <span className="truncate">{iss.title}</span>
                      </Button>
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

        {canManage ? (
          <Popover open={modeOpen} onOpenChange={setModeOpen}>
            <PopoverTrigger asChild>
              <Button type="button" variant="ghost" size="sm" className="gap-1">
                <Eye className="h-3.5 w-3.5" aria-hidden="true" />
                {observing ? t('imbot.agentModeObserve') : t('imbot.agentModeCommand')}
              </Button>
            </PopoverTrigger>
            <PopoverContent className="w-72 space-y-2" align="end">
              <p className="text-sm font-medium text-warm-text">{t('imbot.agentModeTitle')}</p>
              <Button
                type="button"
                variant="ghost"
                aria-pressed={!observing}
                disabled={setMode.isPending}
                onClick={() => setMode.mutate('command')}
                className={
                  'h-auto w-full flex-col items-start justify-start gap-0 whitespace-normal rounded-md border px-2.5 py-1.5 text-left text-sm font-normal ' +
                  (!observing
                    ? 'border-brand bg-brand/5 text-warm-text'
                    : 'border-warm-border text-warm-text-muted hover:bg-warm-muted')
                }
              >
                {t('imbot.agentModeCommand')}
                <span className="block text-xs text-warm-text-muted">
                  {t('imbot.agentModeCommandHint')}
                </span>
              </Button>
              <Button
                type="button"
                variant="ghost"
                aria-pressed={observing}
                disabled={setMode.isPending}
                onClick={() => setMode.mutate('observe')}
                className={
                  'h-auto w-full flex-col items-start justify-start gap-0 whitespace-normal rounded-md border px-2.5 py-1.5 text-left text-sm font-normal ' +
                  (observing
                    ? 'border-brand bg-brand/5 text-warm-text'
                    : 'border-warm-border text-warm-text-muted hover:bg-warm-muted')
                }
              >
                {t('imbot.agentModeObserve')}
                <span className="block text-xs text-warm-text-muted">
                  {t('imbot.agentModeObserveHint')}
                </span>
              </Button>
            </PopoverContent>
          </Popover>
        ) : (
          observing && <Badge variant="secondary">{t('imbot.agentModeObserve')}</Badge>
        )}

        {canManage ? (
          <div className="flex items-center gap-1.5">
            <Select
              value={chatIntakeMode}
              onValueChange={(value) => setIntakeMode.mutate(value as ImBotIntakeMode)}
              disabled={setIntakeMode.isPending}
            >
              <SelectTrigger
                className="h-8 w-32"
                aria-label={t('imbot.intakeModeTitle')}
                title={t('imbot.intakeModeTitle')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {INTAKE_MODES.map((mode) => (
                  <SelectItem key={mode} value={mode}>
                    {t(`imbot.intakeMode.${mode}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        ) : (
          <Badge variant="secondary">{t(`imbot.intakeMode.${chatIntakeMode}`)}</Badge>
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
            aria-label={t('imbot.removeChat')}
            className="text-destructive hover:text-destructive/80 hover:bg-destructive/10"
            disabled={remove.isPending}
            onClick={async () => {
              if (await confirm(t('imbot.removeChatConfirm', { name: label }))) {
                remove.mutate();
              }
            }}
          >
            <Trash2 className="h-4 w-4" aria-hidden="true" />
          </Button>
        )}
      </div>

      {chatIntakeMode === 'auto' && (
        <p className="pl-7 text-xs text-warm-text-muted">
          {t('imbot.intakeModeAutoHint')}
        </p>
      )}
      {pendingCount > 0 && (
        <p className="pl-7 text-xs">
          <Link
            to="/intake"
            search={{ project: projectId, chat: chat.chat_ext_id }}
            className="text-brand hover:underline"
          >
            {t('imbot.intakePendingLink', { count: pendingCount })}
          </Link>
        </p>
      )}
    </div>
  );
}
