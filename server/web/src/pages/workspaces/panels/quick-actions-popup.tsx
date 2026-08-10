import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuickActionsStore, type QuickAction } from '@/stores/quick-actions-store';
import { useOrgStore } from '@/stores/org-store';
import { useAuthStore } from '@/stores/auth-store';
import { Zap, Send, Search, Settings2 } from 'lucide-react';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { groupByOwner } from '@/lib/owner-group';

interface QuickActionsPopupProps {
  workspaceId?: number;
  onApply: (content: string) => void;
  onAutoSend: (content: string) => void;
  onManage: () => void;
}

export function QuickActionsPopup({ workspaceId, onApply, onAutoSend, onManage }: QuickActionsPopupProps) {
  const { t } = useTranslation('workspaces');
  const { actions, sceneActions = [], fetchActions } = useQuickActionsStore();
  const myOrgs = useOrgStore((s) => s.myOrgs);
  const currentUser = useAuthStore((s) => s.user);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');

  useEffect(() => {
    if (open) fetchActions(workspaceId);
  }, [open, fetchActions, workspaceId]);

  const handleOpenChange = (next: boolean) => {
    setOpen(next);
    if (!next) setQuery(''); // reset filter when the popup closes
  };

  const handleSelect = (action: QuickAction) => {
    setOpen(false);
    if (action.auto_send) {
      onAutoSend(action.content);
    } else {
      onApply(action.content);
    }
  };

  // Filter by label + content (case-insensitive). Preserves the
  // user-defined position order since `actions` already arrives sorted.
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return actions;
    return actions.filter(
      (a) => a.label.toLowerCase().includes(q) || a.content.toLowerCase().includes(q),
    );
  }, [actions, query]);
  const filteredScene = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return sceneActions;
    return sceneActions.filter(
      (a) => a.label.toLowerCase().includes(q) || a.content.toLowerCase().includes(q),
    );
  }, [sceneActions, query]);

  const groups = groupByOwner(filtered, currentUser?.id ?? 0, myOrgs);
  const showHeaders = myOrgs.length > 0;
  const totalCount = actions.length + sceneActions.length;
  const filteredCount = filtered.length + filteredScene.length;

  const renderRow = (action: QuickAction) => (
    <button
      key={action.id}
      onClick={() => handleSelect(action)}
      className="flex items-center justify-between w-full rounded px-2 py-1.5 text-left text-sm hover:bg-accent transition-colors group"
      title={action.content}
    >
      <span className="truncate font-medium text-foreground">{action.label}</span>
      {action.auto_send && (
        <Send className="h-3 w-3 text-blue-400 shrink-0 ml-1" />
      )}
    </button>
  );

  // Scene-provided quick actions are read-only and never auto-send.
  const renderSceneRow = (action: { slug: string; label: string; content: string }) => (
    <button
      key={`scene:${action.slug}`}
      onClick={() => { setOpen(false); onApply(action.content); }}
      className="flex items-center justify-between w-full rounded px-2 py-1.5 text-left text-sm hover:bg-accent transition-colors group"
      title={action.content}
    >
      <span className="truncate font-medium text-foreground">{action.label}</span>
    </button>
  );

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <button
          className="flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
          title={t('panels.quickActions.triggerTitle')}
        >
          <Zap className="h-3.5 w-3.5" />
          <span>{t('panels.quickActions.trigger')}</span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" side="top" className="w-64 p-1">
        {totalCount === 0 ? (
          <p className="text-xs text-muted-foreground/70 px-2 py-3 text-center">
            {t('panels.quickActions.empty')}
          </p>
        ) : filteredCount === 0 ? (
          <p className="text-xs text-muted-foreground/70 px-2 py-3 text-center">
            {t('panels.quickActions.searchEmpty')}
          </p>
        ) : (
          <div className="max-h-60 overflow-y-auto">
            {showHeaders
              ? groups.map((group) => (
                  <div key={group.key} className="pb-1">
                    <div
                      data-testid="owner-group-header"
                      className="px-2 pt-2 pb-0.5 text-[10px] uppercase tracking-wider text-muted-foreground/70"
                    >
                      {group.label || t('panels.quickActions.groupPersonal')}
                    </div>
                    {group.items.map(renderRow)}
                  </div>
                ))
              : filtered.map(renderRow)}
            {/* Scene-provided group — always headed so it's clearly distinct
                from the user's personal/org actions. */}
            {filteredScene.length > 0 && (
              <div className="pb-1">
                <div
                  data-testid="scene-group-header"
                  className="px-2 pt-2 pb-0.5 text-[10px] uppercase tracking-wider text-muted-foreground/70"
                >
                  {t('panels.quickActions.groupScene')}
                </div>
                {filteredScene.map(renderSceneRow)}
              </div>
            )}
          </div>
        )}
        <div className="border-t mt-1 pt-1 flex items-center gap-1">
          <div className="relative flex-1 min-w-0">
            <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground/60 pointer-events-none" />
            <input
              type="text"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t('panels.quickActions.searchPlaceholder')}
              className="w-full rounded bg-muted/50 pl-7 pr-2 py-1 text-xs text-foreground placeholder:text-muted-foreground/60 focus:outline-none focus:ring-1 focus:ring-ring"
            />
          </div>
          <button
            onClick={() => { setOpen(false); onManage(); }}
            title={t('panels.quickActions.manageTitle')}
            aria-label={t('panels.quickActions.manageTitle')}
            className="shrink-0 rounded p-1.5 text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
          >
            <Settings2 className="h-3.5 w-3.5" />
          </button>
        </div>
      </PopoverContent>
    </Popover>
  );
}
