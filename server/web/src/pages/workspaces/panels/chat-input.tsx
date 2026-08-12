import { forwardRef, useCallback, useEffect, useImperativeHandle, useRef, useState, type RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Bot, Send, X, Loader2, Paperclip, ListPlus, Activity } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { Workspace, ChatAttachment } from '@/types/api';
import { api } from '@/lib/api';
import { WorkspaceSettingsDialog } from './workspace-settings-dialog';
import { WorkspaceScenesDialog } from './workspace-scenes-dialog';
import { ClaudeSettingsDialog } from './claude-settings-dialog';
import { CodexSettingsDialog } from './codex-settings-dialog';
import { QwenSettingsDialog } from './qwen-settings-dialog';
import { PermissionSelector } from './permission-selector';
import { PermissionSettingsDialog } from './permission-settings-dialog';
import { PermissionIndicator } from '@/components/agent/PermissionIndicator';
import { AskUserIndicator } from '@/components/agent/AskUserIndicator';
import { SlashCommandPopup, type SlashCommandPopupHandle } from './slash-command-popup';
import { QuickActionsPopup } from './quick-actions-popup';
import { QuickActionsDialog } from './quick-actions-dialog';
import { TaskProgressButton } from '../components/task-progress-button';
import { AttachmentTag } from '../components/attachment-tag';
import { FilePickerPopup } from '../components/file-picker-popup';
import { useAttachmentStore } from '@/stores/attachment-store';
import { useAgentSSEStore } from '@/stores/agent-sse-store';
import { useFileUpload } from '@/hooks/use-file-upload';
import { useFilePicker } from '@/hooks/use-file-picker';
import { saveDraft, loadDraft, clearDraft } from '@/lib/chat-draft';
import { isIMEComposing } from '@/lib/ime';
import { computeUsagePillTone } from './chat-input-helpers';

// JSON structure from Claude Code's statusline (written by --statusline-command)
interface ClaudeStatusData {
  model?: { display_name?: string };
  context_window?: { used_percentage?: number; input_tokens?: number; max_tokens?: number };
  cost?: { total_cost_usd?: number; total_lines_added?: number; total_lines_removed?: number };
  rate_limits?: { five_hour?: { status?: string; resets_at?: number; overage_status?: string } };
  agent?: { name?: string };
  // Fallback: may be a raw { status: "no_session" | "unavailable" } response
  status?: string;
}

export interface ChatInputProps {
  workspace: Workspace;
  workDir?: string;
  onSend: (content: string, attachments: ChatAttachment[]) => void;
  sessionState: 'idle' | 'running' | 'waiting_confirm';
  contextPercent?: number;
  totalChanges?: { files: number; additions: number; deletions: number };
  onToggleChanges?: () => void;
  onStopAgent?: () => void;
  onClickTask?: (messageId: string) => void;
  // Scroll container ref (lifted from ChatPanel) — used by PermissionIndicator
  // to detect whether the latest pending permission card is in view.
  messagesScrollRef?: RefObject<HTMLElement | null>;
}

export interface ChatInputHandle {
  setValue: (value: string) => void;
  focus: () => void;
}

export const ChatInput = forwardRef<ChatInputHandle, ChatInputProps>(function ChatInput({
  workspace,
  workDir,
  onSend,
  sessionState,
  contextPercent,
  totalChanges,
  onToggleChanges,
  onStopAgent,
  onClickTask,
  messagesScrollRef,
}: ChatInputProps, ref) {
  const { t } = useTranslation('workspaces');
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const [usageOpen, setUsageOpen] = useState(false);
  const cliType = workspace.cli_type ?? 'claude';
  // Strictly Claude — gates the Claude-only account picker, statusline and
  // settings. (Was `!== 'codex'`, which wrongly bucketed qwen as Claude.)
  const isClaudeWorkspace = cliType === 'claude';


  useImperativeHandle(ref, () => ({
    setValue(value: string) {
      if (textareaRef.current) {
        textareaRef.current.value = value;
        saveDraft(workspace.id, value);
      }
    },
    focus() {
      textareaRef.current?.focus();
    },
  }), [workspace.id]);
  const usageRef = useRef<HTMLDivElement>(null);
  // Claude statusline data (fetched from file written by --statusline-command).
  // Polled passively so the pill can surface rate-limit alerts without the
  // user opening the popover first.
  const { data: claudeStatus = null } = useQuery({
    queryKey: ['claude-status', workspace.id],
    queryFn: () => api.get<ClaudeStatusData>(`/workspaces/${workspace.id}/claude-status`),
    enabled: isClaudeWorkspace,
    refetchInterval: 60_000,
    staleTime: 55_000,
    refetchOnWindowFocus: false,
  });
  // Live refresh: the 60s poll alone leaves the rate-limit pill stale between
  // ticks. The backend broadcasts `claude_status_changed` whenever a
  // rate_limit_event updates the reset time, so refetch immediately on it.
  const queryClient = useQueryClient();
  useEffect(() => {
    if (!isClaudeWorkspace) return;
    return useAgentSSEStore.getState().addHandler(String(workspace.id), (msg) => {
      if (msg.type === 'claude_status_changed') {
        queryClient.invalidateQueries({ queryKey: ['claude-status', workspace.id] });
      }
    });
  }, [workspace.id, isClaudeWorkspace, queryClient]);
  const [commandMode, setCommandMode] = useState(false);
  const [commandFilter, setCommandFilter] = useState('');
  const commandPopupRef = useRef<SlashCommandPopupHandle>(null);
  const [quickActionsDialogOpen, setQuickActionsDialogOpen] = useState(false);

  const store = useAttachmentStore();
  const { fileInputRef, uploadFiles, openFilePicker, handleFileInputChange, handlePaste } = useFileUpload(String(workspace.id));
  useFilePicker(String(workspace.id));

  const [dragOver, setDragOver] = useState(false);
  const atPositionRef = useRef<number>(-1);

  // Resizable input: drag the top edge to grow/shrink the textarea. `null`
  // height means "use the default" (the rows={3} height). Once dragged, an
  // explicit px height is applied. Clamped so it can't collapse or eat the page.
  const MIN_INPUT_HEIGHT = 56;
  const MAX_INPUT_HEIGHT = 480;
  const [textareaHeight, setTextareaHeight] = useState<number | null>(null);
  const resizeStartRef = useRef<{ startY: number; startHeight: number } | null>(null);

  const handleResizePointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const startHeight = textareaRef.current?.offsetHeight ?? MIN_INPUT_HEIGHT;
    resizeStartRef.current = { startY: e.clientY, startHeight };
    e.currentTarget.setPointerCapture(e.pointerId);
  };

  const handleResizePointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const start = resizeStartRef.current;
    if (!start) return;
    // Dragging up (smaller clientY) grows the input.
    const next = start.startHeight + (start.startY - e.clientY);
    setTextareaHeight(Math.min(MAX_INPUT_HEIGHT, Math.max(MIN_INPUT_HEIGHT, next)));
  };

  const handleResizePointerUp = (e: React.PointerEvent<HTMLDivElement>) => {
    resizeStartRef.current = null;
    e.currentTarget.releasePointerCapture(e.pointerId);
  };

  useEffect(() => {
    if (textareaRef.current) {
      textareaRef.current.value = loadDraft(workspace.id);
    }
  }, [workspace.id]);

  const handleSend = () => {
    const value = textareaRef.current?.value.trim() ?? '';
    if (!value) return;
    if (textareaRef.current) textareaRef.current.value = '';
    clearDraft(workspace.id);
    onSend(value, store.attachments);
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (isIMEComposing(e)) return;
    if (commandMode) {
      if (e.key === 'Escape') {
        e.preventDefault();
        setCommandMode(false);
        setCommandFilter('');
        if (textareaRef.current) {
          textareaRef.current.value = '';
          saveDraft(workspace.id, '');
        }
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        commandPopupRef.current?.moveUp();
        return;
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        commandPopupRef.current?.moveDown();
        return;
      }
      if (e.key === 'Enter') {
        e.preventDefault();
        commandPopupRef.current?.confirmSelection();
        return;
      }
    }
    if (store.filePickerActive) {
      // Cancel: just dismiss the picker. Keep whatever the user typed (the
      // "@..." text) — they usually want to keep editing it, not lose it. Space
      // isn't a cancel key here; it naturally ends the mention token via
      // handleInput (mentions are whitespace-delimited) without deleting.
      if (e.key === 'Escape') {
        e.preventDefault();
        store.closeFilePicker();
        atPositionRef.current = -1;
        return;
      }
      if (e.key === 'ArrowUp') { e.preventDefault(); store.filePickerMoveUp(); return; }
      if (e.key === 'ArrowDown') { e.preventDefault(); store.filePickerMoveDown(); return; }
      if (e.key === 'Enter') {
        e.preventDefault();
        const selected = store.filePickerResults[store.filePickerSelectedIndex];
        if (selected) {
          store.addAttachment({ path: selected.path, type: 'ref', name: selected.name, repo: selected.repo });
          if (textareaRef.current && atPositionRef.current >= 0) {
            const val = textareaRef.current.value;
            const cursor = textareaRef.current.selectionStart ?? val.length;
            textareaRef.current.value = val.slice(0, atPositionRef.current) + val.slice(cursor);
            saveDraft(workspace.id, textareaRef.current.value);
          }
        }
        atPositionRef.current = -1;
        store.closeFilePicker();
        return;
      }
    }
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      handleSend();
    }
  };

  const handleInput = () => {
    const value = textareaRef.current?.value ?? '';
    saveDraft(workspace.id, value);
    const cursorPos = textareaRef.current?.selectionStart ?? value.length;

    // Slash command mode (existing logic)
    if (!commandMode) {
      if (value === '/') {
        setCommandMode(true);
        setCommandFilter('');
      }
    } else {
      if (!value.startsWith('/')) {
        setCommandMode(false);
        setCommandFilter('');
      } else {
        setCommandFilter(value.slice(1));
      }
    }

    // @ file picker — trigger only when @ is at a word boundary AND the token
    // after it has no whitespace. A space therefore ends the mention (closes the
    // picker) without erasing what was typed, and the picker won't re-open over
    // the rest of a normal sentence.
    const textBeforeCursor = value.slice(0, cursorPos);
    const lastAtIdx = textBeforeCursor.lastIndexOf('@');
    const queryAfterAt = lastAtIdx !== -1 ? textBeforeCursor.slice(lastAtIdx + 1) : '';
    const atBoundary = lastAtIdx === 0 || (lastAtIdx > 0 && /[\s\n]/.test(value[lastAtIdx - 1]));
    if (lastAtIdx !== -1 && atBoundary && !/\s/.test(queryAfterAt)) {
      if (!store.filePickerActive) {
        store.openFilePicker();
        atPositionRef.current = lastAtIdx;
      }
      store.setFilePickerQuery(queryAfterAt);
    } else if (store.filePickerActive) {
      store.closeFilePicker();
      atPositionRef.current = -1;
    }
  };

  const handleCommandClose = useCallback(() => {
    setCommandMode(false);
    setCommandFilter('');
  }, []);

  // Selection also exits command mode — popup relies on this to unmount
  const handleCommandSelect = useCallback((cmd: string) => {
    if (textareaRef.current) {
      textareaRef.current.value = cmd + ' ';
      textareaRef.current.focus();
      saveDraft(workspace.id, textareaRef.current.value);
    }
    setCommandMode(false);
    setCommandFilter('');
  }, [workspace.id]);

  const handleDragOver = (e: React.DragEvent) => { e.preventDefault(); setDragOver(true); };
  const handleDragLeave = () => { setDragOver(false); };
  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault(); setDragOver(false);
    if (e.dataTransfer.files.length > 0) uploadFiles(e.dataTransfer.files);
  };

  useEffect(() => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    const handler = (e: Event) => handlePaste(e as ClipboardEvent);
    textarea.addEventListener('paste', handler);
    return () => textarea.removeEventListener('paste', handler);
  }, [handlePaste]);

  // Close usage popover on click outside / Escape
  useEffect(() => {
    if (!usageOpen) return;
    const handleClick = (e: MouseEvent) => {
      if (usageRef.current && !usageRef.current.contains(e.target as Node)) setUsageOpen(false);
    };
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setUsageOpen(false);
    };
    document.addEventListener('mousedown', handleClick);
    document.addEventListener('keydown', handleKey);
    return () => {
      document.removeEventListener('mousedown', handleClick);
      document.removeEventListener('keydown', handleKey);
    };
  }, [usageOpen]);

  return (
    <div className="relative border-t bg-card">
      {/* Layer 1: Status bar */}
      <div className="flex items-center justify-between px-3 py-1.5 border-b border-border text-xs">
        {/* Left: changes summary badge */}
        <div>
          {totalChanges && totalChanges.files > 0 ? (
            <button
              onClick={onToggleChanges}
              className="flex items-center gap-1.5 rounded-full bg-muted hover:bg-accent px-2 py-0.5 text-foreground transition-colors"
            >
              <span className="font-medium">{totalChanges.files !== 1 ? t('panels.chatInput.fileChangesPlural', { count: totalChanges.files }) : t('panels.chatInput.fileChanges', { count: totalChanges.files })}</span>
              <span className="text-success">+{totalChanges.additions}</span>
              <span className="text-destructive">-{totalChanges.deletions}</span>
            </button>
          ) : (
            <span className="text-muted-foreground">{t('panels.chatInput.noChanges')}</span>
          )}
        </div>

        {/* Center: task progress */}
          <TaskProgressButton onClickTask={onClickTask} />

        {/* Right: usage pill, bot icon, agent status, queue count */}
        <div className="flex items-center gap-2">
          {/* Usage pill button with popover.
              Visible only when context is high OR rate limit is non-OK; the
              everyday low-usage case shows no pill. */}
          {(() => {
            const hasRealStatus = claudeStatus && !claudeStatus.status;
            const ctxPct = hasRealStatus ? claudeStatus.context_window?.used_percentage : contextPercent;
            const rateStatus = hasRealStatus ? claudeStatus.rate_limits?.five_hour : undefined;

            const rateAlert = !!rateStatus?.status && rateStatus.status !== 'allowed';
            const tone = computeUsagePillTone(ctxPct, rateStatus);
            if (tone === null) return null;

            const resetTimeText = rateStatus?.resets_at
              ? new Date(rateStatus.resets_at * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
              : null;
            // Reset time is shown inline whenever the backend reports one (not
            // only on rate-limit alert), so the 5h window reset is always visible
            // alongside the always-on context-usage percentage.
            const showResetInline = resetTimeText != null;

            const ctxPctText = ctxPct != null ? `${Math.round(ctxPct)}%` : null;
            // Token detail (e.g. "650k / 1000k") only available from the real
            // backend status; the local SSE fallback carries percent only.
            const fmtK = (n: number) => `${Math.round(n / 1000)}k`;
            const ctxInfo = hasRealStatus ? claudeStatus.context_window : undefined;
            const ctxTokensText =
              ctxInfo?.input_tokens != null && ctxInfo?.max_tokens != null
                ? `${fmtK(ctxInfo.input_tokens)} / ${fmtK(ctxInfo.max_tokens)}`
                : null;

            return (
              <div className="relative" ref={usageRef}>
                <button
                  onClick={() => setUsageOpen(!usageOpen)}
                  className={cn(
                    'flex items-center gap-1 rounded-full px-2 py-0.5 text-xs transition-colors',
                    tone === 'critical'
                      ? 'bg-destructive/10 text-destructive dark:bg-red-950/30 dark:text-red-300'
                      : tone === 'warning'
                      ? 'bg-warning/10 text-warning dark:bg-amber-950/30 dark:text-amber-300'
                      : tone === 'info'
                      ? 'bg-info/10 text-info dark:bg-blue-950/30 dark:text-blue-300'
                      : 'bg-muted text-muted-foreground',
                  )}
                >
                  <Activity className="w-3 h-3" />
                  {ctxPctText && <span className="font-medium tabular-nums">{ctxPctText}</span>}
                  {showResetInline && (
                    <span className="font-medium">
                      {ctxPctText && '· '}
                      {t('panels.chatInput.rateResetsAt', { time: resetTimeText })}
                    </span>
                  )}
                </button>

                {usageOpen && (
                  <div className="absolute bottom-full right-0 mb-2 w-60 rounded-lg bg-popover text-popover-foreground px-4 py-3 space-y-2 text-xs shadow-lg z-50 border border-border">
                    {hasRealStatus && claudeStatus.model?.display_name && (
                      <div className="text-muted-foreground text-center">{claudeStatus.model.display_name}</div>
                    )}
                    {ctxPctText && (
                      <div className="flex items-center justify-between gap-2">
                        <span className="font-semibold text-foreground">{t('panels.chatInput.contextWindow')}</span>
                        <span className="text-muted-foreground tabular-nums">
                          {ctxPctText}
                          {ctxTokensText && ` · ${ctxTokensText}`}
                        </span>
                      </div>
                    )}
                    {rateStatus && (
                      <div className="flex items-center justify-between gap-2">
                        <span className="font-semibold text-foreground">{t('panels.chatInput.rateLimit5h')}</span>
                        <span
                          className={cn(
                            'px-1.5 py-0.5 rounded text-xs',
                            rateStatus.status === 'rejected'
                              ? 'bg-destructive/10 text-destructive dark:bg-red-950/30 dark:text-red-300'
                              : rateStatus.status === 'allowed_warning'
                              ? 'bg-warning/10 text-warning dark:bg-amber-950/30 dark:text-amber-300'
                              : 'bg-success/10 text-success dark:bg-emerald-950/30 dark:text-emerald-300',
                          )}
                        >
                          {rateStatus.status === 'allowed'
                            ? t('panels.chatInput.rateOk')
                            : rateStatus.status === 'allowed_warning'
                            ? t('panels.chatInput.rateWarning')
                            : t('panels.chatInput.rateRejected')}
                        </span>
                      </div>
                    )}
                    {rateAlert && resetTimeText && (
                      <div className="text-muted-foreground text-[10px] text-right">
                        {t('panels.chatInput.rateResetsAt', { time: resetTimeText })}
                      </div>
                    )}
                    <div className="absolute right-4 -bottom-1 w-2 h-2 bg-popover rotate-45 border-r border-b border-border" />
                  </div>
                )}
              </div>
            );
          })()}
          <Bot className="h-3.5 w-3.5 text-muted-foreground" />
          {/* Agent status indicator */}
          <span
            className={cn(
              'flex items-center gap-1 px-1.5 py-0.5 rounded text-xs',
              workspace.agent_status === 'busy'
                ? 'bg-warning/10 text-warning dark:bg-amber-950/30 dark:text-amber-300'
                : workspace.agent_status === 'running'
                ? 'bg-success/10 text-success dark:bg-emerald-950/30 dark:text-emerald-300'
                : 'bg-muted text-muted-foreground'
            )}
          >
            {workspace.agent_status === 'busy' && <Loader2 className="w-2.5 h-2.5 animate-spin" />}
            <span>{
              ({
                busy: t('panels.chatInput.statusBusy'),
                running: t('panels.chatInput.statusRunning'),
                idle: t('panels.chatInput.statusIdle'),
              } as Record<string, string>)[workspace.agent_status ?? ''] ?? workspace.agent_status
            }</span>
            {(workspace.agent_status === 'busy' || workspace.agent_status === 'running') && onStopAgent && (
              <button
                onClick={onStopAgent}
                className="ml-0.5 p-0.5 rounded hover:bg-black/10"
                title={t('panels.chatInput.stopAgent')}
              >
                <X className="w-2.5 h-2.5" />
              </button>
            )}
          </span>
        </div>
      </div>

      {/* Working directory */}
      <div className="px-3 pt-1.5">
        <div className="flex items-center gap-1 text-[11px] text-muted-foreground font-mono">
          <span className="text-muted-foreground/60 shrink-0">$</span>
          <span className="truncate min-w-0 flex-1" title={workDir || workspace.path}>{workDir || workspace.path}</span>
        </div>
      </div>

      {/* Layer 2: Input area with attachments */}
      <div
        className={cn(
          'mx-3 mt-1 rounded-md border bg-muted transition-colors relative focus-within:border-brand',
          dragOver ? 'border-info bg-info/10' : 'border-border',
        )}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        {/* Top-edge resize handle: drag up/down to change the input height */}
        <div
          className="group absolute -top-1.5 inset-x-0 h-3 flex items-center justify-center cursor-ns-resize touch-none z-10"
          onPointerDown={handleResizePointerDown}
          onPointerMove={handleResizePointerMove}
          onPointerUp={handleResizePointerUp}
          title={t('panels.chatInput.resizeHandle')}
        >
          <div className="h-1 w-8 rounded-full bg-border group-hover:bg-muted-foreground/50 transition-colors" />
        </div>
        {store.attachments.length > 0 && (
          <div className="flex flex-wrap gap-1.5 px-3 pt-2">
            {store.attachments.map((a) => (
              <AttachmentTag key={a.path} attachment={a} onRemove={() => store.removeAttachment(a.path)} />
            ))}
          </div>
        )}
        <textarea
          ref={textareaRef}
          className="w-full resize-none px-3 py-2 text-sm placeholder:text-muted-foreground focus:outline-none focus-visible:ring-0 focus-visible:ring-offset-0 bg-transparent text-foreground"
          // Inline style wins over the ring-composed box-shadow on focus; scoped
          // to this textarea only, never affecting other pages. `height` is set
          // only after the user drags the top-edge handle; until then rows={3}
          // defines the default height.
          style={{ boxShadow: 'none', height: textareaHeight != null ? `${textareaHeight}px` : undefined }}
          rows={3}
          placeholder={
            sessionState === 'running' ? t('panels.chatInput.running')
              : sessionState === 'waiting_confirm' ? t('panels.chatInput.waitingConfirm')
              : t(cliType === 'claude' ? 'panels.chatInput.idle' : cliType === 'qwen' ? 'panels.chatInput.idleQwen' : 'panels.chatInput.idleCodex')
          }
          onKeyDown={handleKeyDown}
          onInput={handleInput}
        />
        {dragOver && (
          <div className="absolute inset-0 flex items-center justify-center bg-info/10 rounded-md pointer-events-none">
            <span className="text-info text-sm font-medium">{t('panels.chatInput.dropZone')}</span>
          </div>
        )}
      </div>

      {/* Layer 3: Controls */}
      {/* items-start + the send button's shrink-0 keep Send pinned top-right and
          never compressed; the left group wraps (flex-wrap + min-w-0) so on
          narrow widths — e.g. iPad portrait or a narrow split panel — the
          secondary controls flow to a second line instead of squeezing the
          toolbar or pushing Send off-screen. 附件/快捷 stay first, always visible. */}
      <div className="flex items-start justify-between gap-2 px-3 py-2">
        {/* Left: settings buttons */}
        <div className="flex flex-wrap items-center gap-1 min-w-0">
          <button
            onClick={openFilePicker}
            disabled={store.uploading}
            className="flex items-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-accent transition-colors"
            title={t('panels.chatInput.addAttachment')}
          >
            <Paperclip className="h-3.5 w-3.5" />
            {store.uploading ? t('panels.chatInput.uploading') : t('panels.chatInput.attachment')}
          </button>
          <input ref={fileInputRef} type="file" multiple className="hidden" onChange={handleFileInputChange} />
          <QuickActionsPopup
            workspaceId={Number(workspace.id)}
            onApply={(content) => {
              if (textareaRef.current) {
                textareaRef.current.value = content;
                textareaRef.current.focus();
              }
            }}
            onAutoSend={(content) => {
              onSend(content, []);
            }}
            onManage={() => setQuickActionsDialogOpen(true)}
          />
          <WorkspaceSettingsDialog workspace={workspace} />
          <WorkspaceScenesDialog workspaceId={Number(workspace.id)} />
          {cliType === 'claude' && <ClaudeSettingsDialog workspaceId={workspace.id} />}
          {cliType === 'codex' && <CodexSettingsDialog workspace={workspace} />}
          {cliType === 'qwen' && <QwenSettingsDialog />}
          <PermissionSelector workspaceId={workspace.id} />
          <PermissionSettingsDialog workspaceId={workspace.id} />
          {messagesScrollRef && (
            <PermissionIndicator
              workspaceId={Number(workspace.id)}
              scrollContainerRef={messagesScrollRef}
              onClick={() => {
                const root = messagesScrollRef.current;
                if (!root) return;
                const first = root.querySelector(
                  '[data-permission-card="pending"]',
                ) as HTMLElement | null;
                first?.scrollIntoView({ behavior: 'smooth', block: 'center' });
              }}
            />
          )}
          {messagesScrollRef && (
            <AskUserIndicator
              workspaceId={Number(workspace.id)}
              scrollContainerRef={messagesScrollRef}
              onClick={() => {
                const root = messagesScrollRef.current;
                if (!root) return;
                const first = root.querySelector(
                  '[data-ask-user-card="pending"]',
                ) as HTMLElement | null;
                first?.scrollIntoView({ behavior: 'smooth', block: 'center' });
              }}
            />
          )}
        </div>

        {/* Right: send button — shrink-0 so it never compresses and stays at the
            far right regardless of how the left group wraps. */}
        <button
          onClick={handleSend}
          className={cn(
            'flex shrink-0 items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors',
            sessionState === 'running'
              ? 'bg-warning text-white hover:bg-warning/90'
              : 'bg-info text-white hover:bg-info/90',
          )}
        >
          {sessionState === 'running' ? (
            <>
              <ListPlus className="h-3.5 w-3.5" />
              {t('panels.chatInput.queue')}
            </>
          ) : (
            <>
              <Send className="h-3.5 w-3.5" />
              {t('panels.chatInput.send')}
            </>
          )}
        </button>
      </div>
      {/* Slash command popup */}
      {commandMode && (
        <SlashCommandPopup
          ref={commandPopupRef}
          anchorRef={textareaRef as React.RefObject<HTMLElement>}
          filter={commandFilter}
          workspaceId={Number(workspace.id)}
          cliType={workspace.cli_type}
          onSelect={handleCommandSelect}
          onClose={handleCommandClose}
        />
      )}
      {store.filePickerActive && (
        <div className="absolute bottom-full left-0 right-0 z-50 px-3 pb-1">
          <FilePickerPopup
            results={store.filePickerResults}
            selectedIndex={store.filePickerSelectedIndex}
            loading={store.filePickerLoading}
            query={store.filePickerQuery}
            onSelect={(file) => {
              store.addAttachment({ path: file.path, type: 'ref', name: file.name, repo: file.repo });
              if (textareaRef.current && atPositionRef.current >= 0) {
                const val = textareaRef.current.value;
                const cursor = textareaRef.current.selectionStart ?? val.length;
                textareaRef.current.value = val.slice(0, atPositionRef.current) + val.slice(cursor);
                saveDraft(workspace.id, textareaRef.current.value);
                atPositionRef.current = -1;
              }
              store.closeFilePicker();
            }}
            onClose={store.closeFilePicker}
          />
        </div>
      )}
      {/* Quick actions management dialog */}
      <QuickActionsDialog workspaceId={Number(workspace.id)} open={quickActionsDialogOpen} onOpenChange={setQuickActionsDialogOpen} />
    </div>
  );
});
