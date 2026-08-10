import { useEffect, useRef, useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { Group, Panel, Separator } from 'react-resizable-panels';
import {
  Sparkles,
  MessageSquare,
  LayoutGrid,
  Lock,
  Send,
  Loader2,
  FileText,
  CheckCircle2,
  Plus,
  Trash2,
  ChevronDown,
  ChevronRight,
  Paperclip,
  Library,
  CalendarClock,
  X,
  ExternalLink,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { useAssistantStore, type ChatMsg, type Plan } from '@/stores/assistant-store';
import { useConfigStore } from '@/stores/config-store';
import { ArtifactPanelContainer } from '@/pages/workspaces/panels/artifact-panel-container';
import { AssistantContent } from '@/pages/workspaces/panels/chat-message';
import { DirectoryPicker } from '@/components/shared/directory-picker';

type TFn = (k: string, opts?: Record<string, unknown>) => string;

// Per-task composer draft persistence (#req3): unsent input is kept in
// localStorage keyed by the active task (or "new" before a task exists), so
// switching tasks / reloading never loses what you were typing. Cleared on send.
const DRAFT_PREFIX = 'niuniu:assistant:draft:';
const draftKey = (id: number | null) => `${DRAFT_PREFIX}${id ?? 'new'}`;
function loadDraft(id: number | null): string {
  try {
    return localStorage.getItem(draftKey(id)) ?? '';
  } catch {
    return '';
  }
}
function saveDraft(id: number | null, text: string) {
  try {
    if (text) localStorage.setItem(draftKey(id), text);
    else localStorage.removeItem(draftKey(id));
  } catch {
    /* ignore */
  }
}

// AssistantPage is the conversational office assistant: a left rail of plans
// (auto-dispatched tasks), the active plan's conversation in the middle, and
// its deliverables on the right. The kanban remains the source of truth.
export function AssistantPage() {
  const { t } = useTranslation('assistant');
  const navigate = useNavigate();

  const plans = useAssistantStore((s) => s.plans);
  const activePlanId = useAssistantStore((s) => s.activePlanId);
  const busy = useAssistantStore((s) => s.busy);
  const error = useAssistantStore((s) => s.error);
  const restoring = useAssistantStore((s) => s.restoring);
  const submitTurn = useAssistantStore((s) => s.submit);
  const newPlan = useAssistantStore((s) => s.newPlan);
  const selectPlan = useAssistantStore((s) => s.selectPlan);
  const deletePlan = useAssistantStore((s) => s.deletePlan);

  const [input, setInput] = useState('');
  const [files, setFiles] = useState<File[]>([]);
  const [knowledge, setKnowledge] = useState<string[]>([]);
  const [kbOpen, setKbOpen] = useState(false);
  const [kbInput, setKbInput] = useState('');
  const [dragOver, setDragOver] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const personalMode = useConfigStore((s) => s.personalMode);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const fileRef = useRef<HTMLInputElement | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);

  // Seed the composer with a managed-task template and focus it. The assistant
  // agent (office-doc scene) recognizes the recurring request and calls the
  // create_managed_task tool to provision the schedule (GC2 entry point). It
  // runs in the current conversation so the agent can judge, from the user's
  // intent + the task so far, whether to schedule the CURRENT task or spin off
  // a new subtask.
  function startManagedTask() {
    setInput(t('input.managedTaskTemplate'));
    requestAnimationFrame(() => {
      const el = textareaRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(el.value.length, el.value.length);
      }
    });
  }

  const active = plans.find((p) => p.issueId === activePlanId) ?? null;

  useEffect(() => {
    void useAssistantStore.getState().restore();
    // Poll the plan list so the rail stays live (new subtasks, managed tasks,
    // status changes) without needing a manual reload.
    const id = setInterval(() => void useAssistantStore.getState().refresh(), 5000);
    return () => clearInterval(id);
  }, []);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
  }, [active?.messages, active?.steps, activePlanId]);

  // Load this task's saved draft when switching tasks (and on mount). Saving
  // happens in the textarea onChange so it's always keyed to the task being
  // typed in. setInput here is programmatic (doesn't fire onChange), so it can't
  // mis-save the old text under the new task.
  useEffect(() => {
    setInput(loadDraft(activePlanId));
  }, [activePlanId]);

  const hasPayload = input.trim().length > 0 || files.length > 0 || knowledge.length > 0;

  async function submit() {
    if (!hasPayload || busy) return;
    // The task being typed in (active task, or "new" before one exists). Its
    // draft is cleared on a successful send.
    const draftId = activePlanId;
    const ok = await submitTurn(input.trim(), files, knowledge);
    if (ok) {
      saveDraft(draftId, '');
      setInput('');
      setFiles([]);
      setKnowledge([]);
      setKbInput('');
      setKbOpen(false);
    }
  }

  // addFiles appends new files, de-duping by name+size so paste/drop/pick don't
  // double-add the same file.
  function addFiles(incoming: File[]) {
    if (incoming.length === 0) return;
    setFiles((prev) => {
      const seen = new Set(prev.map((f) => `${f.name}:${f.size}`));
      const next = [...prev];
      for (const f of incoming) {
        const key = `${f.name}:${f.size}`;
        if (!seen.has(key)) {
          seen.add(key);
          next.push(f);
        }
      }
      return next;
    });
  }

  function onFilesPicked(e: React.ChangeEvent<HTMLInputElement>) {
    addFiles(Array.from(e.target.files ?? []));
    e.target.value = ''; // allow re-picking the same file
  }

  function onPaste(e: React.ClipboardEvent<HTMLTextAreaElement>) {
    const pasted = Array.from(e.clipboardData.files ?? []);
    if (pasted.length > 0) {
      e.preventDefault(); // pasted files (e.g. a screenshot) become attachments
      addFiles(pasted);
    }
  }

  function onDrop(e: React.DragEvent<HTMLDivElement>) {
    e.preventDefault();
    setDragOver(false);
    addFiles(Array.from(e.dataTransfer.files ?? []));
  }

  function addKnowledgePath(p: string) {
    const v = p.trim();
    if (!v) return;
    setKnowledge((prev) => (prev.includes(v) ? prev : [...prev, v]));
  }

  function addKnowledge() {
    addKnowledgePath(kbInput);
    setKbInput('');
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void submit();
    }
  }

  function gotoKanban() {
    if (active) {
      navigate({ to: '/projects/$id', params: { id: String(active.projectId) } });
    } else {
      navigate({ to: '/projects' });
    }
  }

  return (
    <div className="flex h-full flex-col bg-background">
      <header className="flex h-14 shrink-0 items-center justify-between border-b px-4">
        <div className="flex items-center gap-2.5">
          <Sparkles className="h-5 w-5 text-info" aria-hidden />
          <div className="flex flex-col">
            <span className="text-sm font-semibold leading-tight text-foreground">{t('title')}</span>
            <span className="text-xs leading-tight text-muted-foreground">{t('subtitle')}</span>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <div className="flex items-center gap-1 rounded-md bg-muted p-0.5">
            <button
              type="button"
              className="flex items-center gap-1 rounded-sm bg-background px-2.5 py-1 text-xs font-medium text-foreground shadow-sm"
            >
              <MessageSquare className="h-3.5 w-3.5" />
              {t('tab.chat')}
            </button>
            <button
              type="button"
              onClick={gotoKanban}
              className="flex items-center gap-1 rounded-sm px-2.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
            >
              <LayoutGrid className="h-3.5 w-3.5" />
              {t('tab.kanban')}
            </button>
          </div>
          {/* Local-sandbox assurance is a personal-edition guarantee (data never
              leaves the machine). Team-edition workspaces run on the server, so
              the badge would be misleading there — show it only in personal mode. */}
          {personalMode && (
            <div className="flex items-center gap-1 rounded-md border border-success/40 bg-success/10 px-2 py-1 text-xs text-success">
              <Lock className="h-3 w-3" />
              {t('sandbox.badge')}
            </div>
          )}
        </div>
      </header>

      <div className="flex min-h-0 flex-1">
        <Group orientation="horizontal" className="min-h-0 flex-1">
          {/* Left rail: the user's plans — width adjustable (drag the divider). */}
          <Panel defaultSize={24} minSize={12}>
            <PlanRail
              plans={plans}
              activePlanId={activePlanId}
              onSelect={selectPlan}
              onNew={newPlan}
              onDelete={deletePlan}
              t={t}
            />
          </Panel>

          <Separator className="w-px bg-border" />

          <Panel defaultSize={44} minSize={28}>
            <div className="flex h-full flex-col">
              {/* Progress is pinned (doesn't scroll with messages) and collapses
                  to one line to save space. */}
              {active && (
                <ProgressBar
                  plan={active}
                  t={t}
                  onOpenWorkspace={() =>
                    navigate({ to: '/workspaces/$id', params: { id: String(active.workspaceId) } })
                  }
                />
              )}
              <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto px-4 py-4">
                {!active ? (
                  <div className="flex h-full flex-col items-center justify-center text-center">
                    <Sparkles className="mb-3 h-8 w-8 text-info" aria-hidden />
                    <p className="text-base font-medium text-foreground">{t('empty.title')}</p>
                    <p className="mt-1 text-sm text-muted-foreground">{t('hint')}</p>
                  </div>
                ) : (
                  <div className="mx-auto flex max-w-2xl flex-col gap-3">
                    {active.messages.map((m) => (
                      <MessageBubble key={m.key} msg={m} workspaceId={String(active.workspaceId)} t={t} />
                    ))}
                    {(busy || restoring) && (
                      <div className="flex items-center gap-2 text-sm text-muted-foreground">
                        <Loader2 className="h-4 w-4 animate-spin" />
                        {restoring ? t('restoring') : t('creating')}
                      </div>
                    )}
                  </div>
                )}
              </div>

              {/* Composer — full width on wide screens. */}
              <div className="shrink-0 border-t px-4 py-3">
                <div className="w-full">
                  {error && <p className="mb-2 text-xs text-destructive">{t('error')}</p>}
                  <div
                    onDragOver={(e) => {
                      e.preventDefault();
                      setDragOver(true);
                    }}
                    onDragLeave={() => setDragOver(false)}
                    onDrop={onDrop}
                    className={cn(
                      'rounded-2xl border border-border bg-muted p-2.5 transition-colors focus-within:border-brand',
                      dragOver && 'ring-2 ring-brand',
                    )}
                  >
                    {/* Attachment + knowledge chips */}
                    {(files.length > 0 || knowledge.length > 0) && (
                      <div className="mb-2 flex flex-wrap gap-1.5">
                        {files.map((f, i) => (
                          <Chip
                            key={`f${i}-${f.name}`}
                            icon={<Paperclip className="h-3 w-3" />}
                            label={f.name}
                            onRemove={() => setFiles((prev) => prev.filter((_, j) => j !== i))}
                            removeLabel={t('input.remove')}
                          />
                        ))}
                        {knowledge.map((p, i) => (
                          <Chip
                            key={`k${i}-${p}`}
                            icon={<Library className="h-3 w-3" />}
                            label={p}
                            onRemove={() => setKnowledge((prev) => prev.filter((_, j) => j !== i))}
                            removeLabel={t('input.remove')}
                          />
                        ))}
                      </div>
                    )}

                    {/* Knowledge-folder path input */}
                    {kbOpen && (
                      <div className="mb-2 flex items-center gap-1.5">
                        <input
                          value={kbInput}
                          onChange={(e) => setKbInput(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter') {
                              e.preventDefault();
                              addKnowledge();
                            }
                          }}
                          placeholder={t('input.knowledgePlaceholder')}
                          className="min-w-0 flex-1 rounded-md border border-border bg-background px-2 py-1 text-xs text-foreground outline-none focus:border-brand"
                        />
                        {personalMode && (
                          <Button variant="outline" size="sm" className="h-7 text-xs" onClick={() => setPickerOpen(true)}>
                            {t('input.browse')}
                          </Button>
                        )}
                        <Button variant="outline" size="sm" className="h-7 text-xs" onClick={addKnowledge}>
                          {t('input.add')}
                        </Button>
                      </div>
                    )}

                    <textarea
                      ref={textareaRef}
                      value={input}
                      onChange={(e) => {
                        setInput(e.target.value);
                        saveDraft(activePlanId, e.target.value);
                      }}
                      onKeyDown={onKeyDown}
                      onPaste={onPaste}
                      rows={3}
                      placeholder={active ? t('input.placeholderContinue') : t('input.placeholder')}
                      // Inline style wins over the ring-composed box-shadow on focus
                      // (the lingering "border" was the focus shadow, not a border).
                      style={{ boxShadow: 'none' }}
                      className="block max-h-48 w-full resize-none border-none bg-transparent px-1 py-1 text-sm text-foreground shadow-none outline-none focus:outline-none focus-visible:ring-0 placeholder:text-muted-foreground"
                    />
                    {/* Bottom toolbar: tools on the left, send on the right (conventional). */}
                    <div className="mt-1.5 flex items-center justify-between">
                      <div className="flex items-center gap-0.5">
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8"
                          onClick={() => fileRef.current?.click()}
                          aria-label={t('input.attach')}
                          title={t('input.attach')}
                        >
                          <Paperclip className="h-4 w-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          className={cn('h-8 w-8', kbOpen && 'text-info')}
                          onClick={() => setKbOpen((v) => !v)}
                          aria-label={t('input.knowledge')}
                          title={t('input.knowledge')}
                        >
                          <Library className="h-4 w-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8"
                          onClick={startManagedTask}
                          aria-label={t('input.managedTask')}
                          title={t('input.managedTask')}
                        >
                          <CalendarClock className="h-4 w-4" />
                        </Button>
                        {active && (
                          <ScheduleIndicator
                            plan={active}
                            t={t}
                            onOpen={() => navigate({ to: '/schedules' })}
                          />
                        )}
                      </div>
                      <Button
                        size="icon"
                        className="rounded-full"
                        onClick={() => void submit()}
                        disabled={busy || !hasPayload}
                        aria-label={t('send')}
                      >
                        {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />}
                      </Button>
                    </div>
                  </div>
                  <input ref={fileRef} type="file" multiple className="hidden" onChange={onFilesPicked} />
                  {personalMode && (
                    <DirectoryPicker
                      open={pickerOpen}
                      onOpenChange={setPickerOpen}
                      onSelect={(p) => {
                        addKnowledgePath(p);
                        setPickerOpen(false);
                      }}
                      t={t}
                    />
                  )}
                </div>
              </div>
            </div>
          </Panel>

          <Separator className="w-px bg-border" />

          {/* Right: deliverables preview (#385). */}
          <Panel defaultSize={32} minSize={20}>
            {active ? (
              <ArtifactPanelContainer workspaceId={String(active.workspaceId)} variant="inline" />
            ) : (
              <div className="flex h-full flex-col items-center justify-center px-6 text-center">
                <FileText className="mb-2 h-7 w-7 text-muted-foreground" aria-hidden />
                <p className="text-sm font-medium text-foreground">{t('preview.title')}</p>
                <p className="mt-1 text-xs text-muted-foreground">{t('preview.empty')}</p>
              </div>
            )}
          </Panel>
        </Group>
      </div>
    </div>
  );
}

function PlanRail({
  plans,
  activePlanId,
  onSelect,
  onNew,
  onDelete,
  t,
}: {
  plans: Plan[];
  activePlanId: number | null;
  onSelect: (id: number) => void;
  onNew: () => void;
  onDelete: (id: number) => void;
  t: TFn;
}) {
  const [confirmId, setConfirmId] = useState<number | null>(null);
  const [collapsed, setCollapsed] = useState<Set<number>>(() => new Set());

  // Two-level grouping: children keyed by their parent conversation's issueId.
  const ids = new Set(plans.map((p) => p.issueId));
  const childrenByParent = new Map<number, Plan[]>();
  for (const p of plans) {
    if (p.parentIssueId > 0 && ids.has(p.parentIssueId)) {
      const arr = childrenByParent.get(p.parentIssueId) ?? [];
      arr.push(p);
      childrenByParent.set(p.parentIssueId, arr);
    }
  }
  // Top-level rows: parentless tasks, plus orphans whose parent isn't listed.
  const topLevel = plans.filter((p) => p.parentIssueId === 0 || !ids.has(p.parentIssueId));

  const toggle = (id: number) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  // row renders one plan; isChild indents it, kids drives the disclosure caret.
  const row = (p: Plan, isChild: boolean, kids: Plan[]) => {
    const selected = p.issueId === activePlanId;
    const confirming = confirmId === p.issueId;
    const hasKids = kids.length > 0;
    const isCollapsed = collapsed.has(p.issueId);
    return (
      <div
        className={cn(
          'group flex items-center gap-1 rounded-md py-1.5 pr-2 text-sm',
          isChild ? 'pl-7' : 'pl-2',
          selected ? 'bg-accent text-foreground' : 'text-muted-foreground hover:bg-accent/60',
        )}
      >
        {hasKids ? (
          <button
            type="button"
            onClick={() => toggle(p.issueId)}
            aria-label={t('plans.toggle')}
            aria-expanded={!isCollapsed}
            className="shrink-0 text-muted-foreground hover:text-foreground"
          >
            <ChevronRight className={cn('h-3.5 w-3.5 transition-transform', !isCollapsed && 'rotate-90')} />
          </button>
        ) : (
          !isChild && <span className="w-3.5 shrink-0" aria-hidden />
        )}
        <button
          type="button"
          onClick={() => onSelect(p.issueId)}
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
          title={p.title}
        >
          <StatusIcon plan={p} />
          <span className="truncate">{p.title}</span>
          {hasKids && <span className="shrink-0 text-xs text-muted-foreground/70">{kids.length}</span>}
        </button>
        {confirming ? (
          <span className="flex items-center gap-1">
            <button
              type="button"
              onClick={() => {
                setConfirmId(null);
                onDelete(p.issueId);
              }}
              className="text-xs font-medium text-destructive hover:underline"
            >
              {t('plans.confirmDelete')}
            </button>
            <button
              type="button"
              onClick={() => setConfirmId(null)}
              className="text-xs text-muted-foreground hover:underline"
            >
              {t('plans.cancel')}
            </button>
          </span>
        ) : (
          <button
            type="button"
            onClick={() => setConfirmId(p.issueId)}
            aria-label={t('plans.delete')}
            className="shrink-0 opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
    );
  };

  return (
    <div className="flex h-full w-full flex-col bg-card">
      <div className="flex items-center justify-between px-3 py-2.5">
        <span className="text-xs font-medium text-muted-foreground">{t('plans.title')}</span>
        <Button variant="ghost" size="sm" className="h-6 gap-1 px-1.5 text-xs" onClick={onNew}>
          <Plus className="h-3.5 w-3.5" />
          {t('newTask')}
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
        {plans.length === 0 ? (
          <p className="px-2 py-6 text-center text-xs text-muted-foreground">{t('plans.empty')}</p>
        ) : (
          <ul className="flex flex-col gap-0.5">
            {topLevel.map((p) => {
              const kids = childrenByParent.get(p.issueId) ?? [];
              const isCollapsed = collapsed.has(p.issueId);
              return (
                <li key={p.issueId}>
                  {row(p, false, kids)}
                  {kids.length > 0 && !isCollapsed && (
                    <ul className="mt-0.5 flex flex-col gap-0.5">
                      {kids.map((k) => (
                        <li key={k.issueId}>{row(k, true, [])}</li>
                      ))}
                    </ul>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}

// A plan is "working" when its agent is actively running — either the live
// stream shows a running step, or the workspace status says so (covers plans
// whose step history hasn't been loaded yet).
function isWorking(plan: Plan): boolean {
  return !plan.completed && (plan.status === 'running' || plan.steps.some((s) => s.status === 'running'));
}

// ScheduleIndicator surfaces, in the composer toolbar, that the active task has
// cron schedule(s). Cases: none → nothing; ≥1 enabled → an info chip with the
// count; all paused → a muted "paused" chip. The title lists each schedule's
// name + cron. Clicking opens the 定时任务 page to manage them.
function ScheduleIndicator({ plan, t, onOpen }: { plan: Plan; t: TFn; onOpen: () => void }) {
  const schedules = plan.schedules ?? [];
  if (schedules.length === 0) return null;
  const enabled = schedules.filter((s) => s.enabled).length;
  const allPaused = enabled === 0;
  const title = schedules
    .map((s) => `${s.name || t('schedule.badge')} · ${s.cron_expr}${s.enabled ? '' : ` (${t('schedule.paused')})`}`)
    .join('\n');
  return (
    <button
      type="button"
      onClick={onOpen}
      title={title}
      aria-label={t('schedule.manage')}
      className={cn(
        'flex items-center gap-1 rounded-full px-2 py-1 text-xs transition-colors',
        allPaused ? 'text-muted-foreground hover:bg-accent' : 'text-info hover:bg-info/10',
      )}
    >
      <CalendarClock className="h-3.5 w-3.5" />
      <span>{allPaused ? t('schedule.paused') : t('schedule.count', { n: schedules.length })}</span>
    </button>
  );
}

// StatusIcon surfaces each plan's run state in the rail: spinner while working,
// check when complete, a quiet dot otherwise.
function StatusIcon({ plan }: { plan: Plan }) {
  if (plan.completed) return <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-success" aria-hidden />;
  if (isWorking(plan)) return <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-info" aria-hidden />;
  return <span className="h-2 w-2 shrink-0 rounded-full bg-muted-foreground/40" aria-hidden />;
}

// Chip is a removable token in the composer for an attached file or a
// knowledge folder.
function Chip({
  icon,
  label,
  onRemove,
  removeLabel,
}: {
  icon: React.ReactNode;
  label: string;
  onRemove: () => void;
  removeLabel: string;
}) {
  return (
    <span className="inline-flex max-w-[220px] items-center gap-1 rounded-md bg-background px-2 py-0.5 text-xs text-foreground">
      {icon}
      <span className="truncate">{label}</span>
      <button type="button" onClick={onRemove} aria-label={removeLabel} className="shrink-0 text-muted-foreground hover:text-destructive">
        <X className="h-3 w-3" />
      </button>
    </span>
  );
}

function MessageBubble({ msg, workspaceId, t }: { msg: ChatMsg; workspaceId: string; t: TFn }) {
  // User turns stay a compact right-aligned bubble of plain text. Assistant
  // turns render full-width markdown — headings/lists/tables/images and
  // echarts/chart fenced blocks — reusing the workspace chat's renderer.
  if (msg.role === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] whitespace-pre-wrap rounded-lg bg-primary px-3 py-2 text-sm text-primary-foreground">
          <div className="mb-0.5 text-xs font-medium opacity-70">{t('you')}</div>
          {msg.text}
        </div>
      </div>
    );
  }
  return (
    <div className="rounded-lg bg-muted px-3 py-2 text-foreground">
      <div className="mb-1 text-xs font-medium opacity-70">{t('assistant')}</div>
      <AssistantContent content={msg.text} workspaceId={workspaceId} />
    </div>
  );
}

// ProgressBar pins a condensed, jargon-free progress line to the top of the
// conversation (it doesn't scroll away). Collapsed by default to one line —
// step count + working/done; click to reveal the full info. Raw tool names are
// never shown; what the assistant is doing comes through its plain replies.
function ProgressBar({ plan, t, onOpenWorkspace }: { plan: Plan; t: TFn; onOpenWorkspace: () => void }) {
  const [open, setOpen] = useState(false);
  const personalMode = useConfigStore((s) => s.personalMode);
  const doneCount = plan.steps.filter((s) => s.status === 'done').length;
  const working = isWorking(plan);

  const summary =
    plan.steps.length === 0 && working
      ? t('plan.planning')
      : working
        ? t('plan.working', { n: doneCount })
        : t('plan.doneCount', { n: doneCount });

  return (
    <div className="shrink-0 border-b bg-card">
      <div className="flex w-full items-center gap-1 px-4 py-2">
        {/* The progress summary toggles the detail panel; the workspace shortcut
            sits beside it (can't nest buttons) so a task can jump to its full
            workspace view for details. */}
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
        >
          {plan.completed ? (
            <CheckCircle2 className="h-4 w-4 shrink-0 text-success" />
          ) : working ? (
            <Loader2 className="h-4 w-4 shrink-0 animate-spin text-info" />
          ) : (
            <CheckCircle2 className="h-4 w-4 shrink-0 text-muted-foreground" />
          )}
          <span className="shrink-0 text-xs font-medium text-foreground">{t('plan.title')}</span>
          <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{summary}</span>
        </button>
        <button
          type="button"
          onClick={onOpenWorkspace}
          title={t('plan.openWorkspaceHint')}
          aria-label={t('plan.openWorkspaceHint')}
          className="flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <ExternalLink className="h-3.5 w-3.5" />
          <span>{t('plan.openWorkspace')}</span>
        </button>
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          aria-label={t('plan.title')}
          className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
        >
          <ChevronDown
            className={cn('h-4 w-4 transition-transform', open && 'rotate-180')}
          />
        </button>
      </div>
      {open && (
        <div className="space-y-1 border-t px-4 pb-2.5 pt-2 text-xs text-muted-foreground">
          <div>{t('plan.title')} {t('plan.autohost')}</div>
          {plan.completed && (
            <div className="flex items-center gap-1 text-success">
              <CheckCircle2 className="h-3 w-3" />
              {t('plan.completed')}
            </div>
          )}
          {personalMode && (
            <div className="flex items-center gap-1">
              <Lock className="h-3 w-3" />
              {t('sandbox.badge')}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
