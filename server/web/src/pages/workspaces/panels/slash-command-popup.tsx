import { useState, useEffect, useRef, useCallback, useMemo, forwardRef, useImperativeHandle } from 'react';
import { useTranslation } from 'react-i18next';
import { createPortal } from 'react-dom';
import { Loader2 } from 'lucide-react';
import { api } from '@/lib/api';

interface SlashCommandItem {
  name: string;
  description: string;
  source: string; // "builtin" | "command" | "skill"
  plugin: string;
}

interface SlashCommandPopupProps {
  anchorRef: React.RefObject<HTMLElement | null>;
  filter: string;
  workspaceId?: number;
  cliType?: string;
  onSelect: (command: string) => void;
  onClose: () => void;
}

export interface SlashCommandPopupHandle {
  moveUp: () => void;
  moveDown: () => void;
  confirmSelection: () => void;
}

const SOURCE_KEYS: Record<string, string> = {
  builtin: 'sourceBuiltin',
  command: 'sourceCommand',
  skill: 'sourceSkill',
};

// Module-level cache to avoid re-fetching on every mount.
const _commandsCache = new Map<string, SlashCommandItem[]>();

export function clearSlashCommandCache() {
  _commandsCache.clear();
}

export const SlashCommandPopup = forwardRef<SlashCommandPopupHandle, SlashCommandPopupProps>(
  function SlashCommandPopup({ anchorRef, filter, workspaceId, cliType = 'claude', onSelect, onClose }, ref) {
    const { t } = useTranslation('workspaces');
    const [commands, setCommands] = useState<SlashCommandItem[]>([]);
    const [loading, setLoading] = useState(true);
    const [highlightedIndex, setHighlightedIndex] = useState(0);
    const [hoveredItem, setHoveredItem] = useState<SlashCommandItem | null>(null);
    const [hoveredRect, setHoveredRect] = useState<DOMRect | null>(null);
    const popupRef = useRef<HTMLDivElement>(null);
    const highlightedIndexRef = useRef(0);
    const [popupPos, setPopupPos] = useState<{ left: number; bottom: number } | null>(null);

    // Calculate popup position from anchor
    const updatePosition = useCallback(() => {
      if (!anchorRef.current) return;
      const rect = anchorRef.current.getBoundingClientRect();
      setPopupPos({
        left: rect.left,
        bottom: window.innerHeight - rect.top + 4,
      });
    }, [anchorRef]);

    // Sync highlightedIndexRef with highlightedIndex state
    useEffect(() => {
      highlightedIndexRef.current = highlightedIndex;
    }, [highlightedIndex]);

    // Fetch commands on mount (with module-level cache)
    useEffect(() => {
      let mounted = true;
      updatePosition();
      const normalizedCli = cliType === 'codex' ? 'codex' : 'claude';
      const cacheKey = `${normalizedCli}:${workspaceId ?? 'global'}`;
      const cached = _commandsCache.get(cacheKey);
      if (cached) {
        // eslint-disable-next-line react-hooks/set-state-in-effect -- cache-hit fast path: synchronously populate from module-level cache to skip an unnecessary async tick
        setCommands(cached);
        setLoading(false);
        return;
      }
      setLoading(true);
      const params = new URLSearchParams();
      if (normalizedCli === 'codex') params.set('cli_type', 'codex');
      if (workspaceId != null) params.set('workspace_id', String(workspaceId));
      const query = params.size > 0 ? `?${params.toString()}` : '';
      api
        .get<{ commands: SlashCommandItem[] }>(`/slash-commands${query}`)
        .then((res) => {
          if (mounted) {
            _commandsCache.set(cacheKey, res.commands);
            setCommands(res.commands);
          }
        })
        .catch(() => { if (mounted) setCommands([]); })
        .finally(() => { if (mounted) setLoading(false); });
      return () => { mounted = false; };
    }, [updatePosition, cliType, workspaceId]);

    // Filter commands (memoized so useImperativeHandle deps are stable)
    const { builtins, pluginCmds, skills, flatList } = useMemo(() => {
      const lowerFilter = filter.toLowerCase();
      const filtered = commands.filter(
        (c) =>
          c.name.toLowerCase().includes(lowerFilter) ||
          c.description.toLowerCase().includes(lowerFilter) ||
          c.plugin.toLowerCase().includes(lowerFilter),
      );
      const b = filtered.filter((c) => c.source === 'builtin');
      const p = filtered.filter((c) => c.source === 'command');
      const s = filtered.filter((c) => c.source === 'skill');
      return { builtins: b, pluginCmds: p, skills: s, flatList: [...b, ...p, ...s] };
    }, [commands, filter]);

    // Close when filter produces no matches (only after loading completes)
    useEffect(() => {
      if (!loading && commands.length > 0 && flatList.length === 0) {
        onClose();
      }
    }, [loading, commands.length, flatList.length, onClose]);

    // Reset highlight when filter changes (compare-during-render pattern)
    const [prevFilter, setPrevFilter] = useState(filter);
    if (filter !== prevFilter) {
      setPrevFilter(filter);
      setHighlightedIndex(0);
    }

    // Close on outside click (but not on anchor/textarea clicks)
    useEffect(() => {
      const handler = (e: MouseEvent) => {
        const target = e.target as Node;
        if (popupRef.current?.contains(target)) return;
        if (anchorRef.current?.contains(target)) return;
        onClose();
      };
      document.addEventListener('mousedown', handler);
      return () => document.removeEventListener('mousedown', handler);
    }, [onClose, anchorRef]);

    // Expose imperative handle for keyboard nav
    useImperativeHandle(ref, () => ({
      moveUp() {
        setHighlightedIndex((prev) => (prev > 0 ? prev - 1 : flatList.length - 1));
      },
      moveDown() {
        setHighlightedIndex((prev) => (prev < flatList.length - 1 ? prev + 1 : 0));
      },
      confirmSelection() {
        const idx = highlightedIndexRef.current;
        if (flatList.length > 0 && idx < flatList.length) {
          onSelect('/' + flatList[idx].name);
        }
      },
    }), [flatList, onSelect]);

    // Scroll highlighted item into view
    useEffect(() => {
      if (flatList.length === 0) return;
      const el = popupRef.current?.querySelector(`[data-cmd-index="${highlightedIndex}"]`);
      el?.scrollIntoView({ block: 'nearest' });
    }, [highlightedIndex, flatList.length]);

    const handleItemMouseEnter = (item: SlashCommandItem, e: React.MouseEvent) => {
      setHoveredItem(item);
      setHoveredRect((e.currentTarget as HTMLElement).getBoundingClientRect());
    };

    // Compute tooltip position
    const tooltipStyle = hoveredRect && popupPos
      ? {
          position: 'fixed' as const,
          left: popupPos.left + 260 + 8,
          bottom: Math.max(8, window.innerHeight - hoveredRect.bottom),
          zIndex: 99999,
        }
      : undefined;

    const renderGroup = (title: string, items: SlashCommandItem[], startIdx: number) => {
      if (items.length === 0) return null;
      return (
        <div>
          <div className="px-3 py-1 text-[10px] font-semibold text-muted-foreground/70 uppercase tracking-wide bg-muted sticky top-0">
            {title}
          </div>
          {items.map((item, i) => {
            const idx = startIdx + i;
            return (
              <button
                key={item.source + ':' + item.plugin + ':' + item.name}
                data-cmd-index={idx}
                onClick={() => onSelect('/' + item.name)}
                onMouseEnter={(e) => {
                  setHighlightedIndex(idx);
                  handleItemMouseEnter(item, e);
                }}
                onMouseLeave={() => { setHoveredItem(null); setHoveredRect(null); }}
                className={`w-full text-left px-3 py-1.5 text-xs transition-colors flex items-center gap-2 ${
                  idx === highlightedIndex ? 'bg-accent' : 'hover:bg-accent'
                }`}
              >
                <span className="font-mono text-blue-600 shrink-0">/{item.name}</span>
                <span className="text-muted-foreground/70 truncate">{item.description}</span>
              </button>
            );
          })}
        </div>
      );
    };

    if (!popupPos) return null;

    return createPortal(
      <>
        <div
          ref={popupRef}
          className="w-[260px] rounded-md border border-border bg-card shadow-lg"
          style={{
            position: 'fixed',
            left: popupPos.left,
            bottom: popupPos.bottom,
            zIndex: 99999,
          }}
        >
          <div className="max-h-64 overflow-y-auto">
            {loading ? (
              <div className="flex items-center justify-center py-4 text-muted-foreground/70">
                <Loader2 className="h-4 w-4 animate-spin" />
                <span className="ml-2 text-xs">{t('panels.slashCommand.loading')}</span>
              </div>
            ) : flatList.length === 0 ? (
              <div className="px-3 py-3 text-xs text-muted-foreground/70 italic text-center">
                {t('panels.slashCommand.noResults')}
              </div>
            ) : (
              <>
                {renderGroup(t('panels.slashCommand.groupBuiltin'), builtins, 0)}
                {renderGroup(t('panels.slashCommand.groupCommand'), pluginCmds, builtins.length)}
                {renderGroup(t('panels.slashCommand.groupSkill'), skills, builtins.length + pluginCmds.length)}
              </>
            )}
          </div>
        </div>

        {/* Hover detail tooltip */}
        {hoveredItem && tooltipStyle && (
          <div
            className="w-56 rounded-md border border-border bg-card shadow-lg p-3"
            style={tooltipStyle}
          >
            <div className="font-mono text-sm text-blue-600 font-medium mb-1">
              /{hoveredItem.name}
            </div>
            <div className="text-[10px] text-muted-foreground/70 mb-2">
              {SOURCE_KEYS[hoveredItem.source] ? t(`panels.slashCommand.${SOURCE_KEYS[hoveredItem.source]}`) : hoveredItem.source}
              {hoveredItem.plugin ? ` · ${hoveredItem.plugin}` : ''}
            </div>
            <div className="text-xs text-muted-foreground leading-relaxed whitespace-pre-wrap break-words">
              {hoveredItem.description || t('panels.slashCommand.noDescription')}
            </div>
          </div>
        )}
      </>,
      document.body,
    );
  },
);
