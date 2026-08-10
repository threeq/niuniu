import { useState, useMemo, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Search, X, Tag, Check, CornerDownRight } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/popover';
import type { Issue, Label } from '@/types/api';

export interface FilterState {
  searchQuery: string;
  priority: string;
  assignee: string;
  labelIds: number[];
  /** When false (default) sub-issues (issues with a parent Epic) are hidden so
   * the board only shows regular issues and Epics. The "全部" toggle flips it. */
  showSubIssues: boolean;
}

interface SearchFilterProps {
  issues: Issue[];
  labels?: Label[];
  initialValues?: Partial<FilterState>;
  onFilterChange: (filteredIssues: Issue[], filterState: FilterState) => void;
}

// String to integer priority mapping
const priorityStringToInt: Record<string, number> = {
  low: 0,
  medium: 1,
  high: 2,
  critical: 3,
};

/**
 * Filters issues based on search query, priority, assignee, and labels. All
 * active dimensions are ANDed together; selected labels are ORed among
 * themselves (an issue matches if it carries at least one of the chosen labels).
 */
function filterIssues(
  issues: Issue[],
  searchQuery: string,
  priority: string,
  assignee: string,
  labelIds: number[],
  showSubIssues: boolean
): Issue[] {
  return issues.filter((issue) => {
    // Sub-issue filter — hide issues attached to a parent Epic unless "全部" is on.
    if (!showSubIssues && issue.parent_issue_id != null) {
      return false;
    }

    // Search text filter (title and description)
    if (searchQuery) {
      const query = searchQuery.toLowerCase();
      const matchesTitle = issue.title.toLowerCase().includes(query);
      const matchesDescription = issue.description?.toLowerCase().includes(query);
      if (!matchesTitle && !matchesDescription) {
        return false;
      }
    }

    // Priority filter
    if (priority && issue.priority !== priorityStringToInt[priority]) {
      return false;
    }

    // Assignee filter — match by display_name across the new assignees array
    if (assignee) {
      const target = assignee.toLowerCase();
      const match = (issue.assignees ?? []).some(
        (a) => a.display_name.toLowerCase() === target
      );
      if (!match) return false;
    }

    // Label filter — issue must carry at least one of the selected labels (OR)
    if (labelIds.length > 0) {
      const has = (issue.labels ?? []).some((l) => labelIds.includes(l.id));
      if (!has) return false;
    }

    return true;
  });
}

/**
 * Highlights matching text within a string
 */
export function HighlightText({ text, query }: { text: string; query: string }) {
  if (!query) {
    return <>{text}</>;
  }

  const parts = text.split(new RegExp(`(${escapeRegExp(query)})`, 'gi'));

  return (
    <>
      {parts.map((part, index) =>
        part.toLowerCase() === query.toLowerCase() ? (
          <mark key={index} className="bg-warning/50 px-0.5 rounded">
            {part}
          </mark>
        ) : (
          <span key={index}>{part}</span>
        )
      )}
    </>
  );
}

function escapeRegExp(string: string): string {
  return string.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

export function SearchFilter({
  issues,
  labels = [],
  initialValues = {},
  onFilterChange,
}: SearchFilterProps) {
  const { t } = useTranslation('projects');
  const [searchQuery, setSearchQuery] = useState(initialValues.searchQuery || '');
  const [debouncedQuery, setDebouncedQuery] = useState(initialValues.searchQuery || '');
  const [priority, setPriority] = useState(initialValues.priority || '');
  const [assignee, setAssignee] = useState(initialValues.assignee || '');
  const [labelIds, setLabelIds] = useState<number[]>(initialValues.labelIds || []);
  // Sub-issues are hidden by default; the "全部" toggle reveals them.
  const [showSubIssues, setShowSubIssues] = useState(initialValues.showSubIssues ?? false);

  // Debounce search query - 300ms delay to avoid excessive re-renders
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedQuery(searchQuery);
    }, 300);

    return () => clearTimeout(timer);
  }, [searchQuery]);

  // Get unique assignees (by display_name) from issues
  const uniqueAssignees = useMemo(() => {
    const assignees = new Set<string>();
    issues.forEach((issue) => {
      (issue.assignees ?? []).forEach((a) => assignees.add(a.display_name));
    });
    return Array.from(assignees).sort();
  }, [issues]);

  // Apply filters whenever filter criteria change
  useEffect(() => {
    const filtered = filterIssues(issues, debouncedQuery, priority, assignee, labelIds, showSubIssues);
    const filterState: FilterState = {
      searchQuery: debouncedQuery,
      priority,
      assignee,
      labelIds,
      showSubIssues,
    };
    onFilterChange(filtered, filterState);
  }, [issues, debouncedQuery, priority, assignee, labelIds, showSubIssues, onFilterChange]);

  const toggleLabel = useCallback((id: number) => {
    setLabelIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]
    );
  }, []);

  return (
    <div className="flex items-center gap-2 flex-nowrap min-w-0">
      {/* Search Input */}
      <div className="relative flex-1 min-w-[120px] max-w-md">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
        <Input
          type="text"
          placeholder={t('kanban.search.placeholder')}
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          className="h-8 pl-9 pr-4 text-sm"
        />
        {searchQuery && (
          <button
            onClick={() => setSearchQuery('')}
            className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground/70 hover:text-muted-foreground"
          >
            <X className="w-4 h-4" />
          </button>
        )}
      </div>

      {/* Priority Filter */}
      <select
        value={priority}
        onChange={(e) => setPriority(e.target.value)}
        aria-label={t('kanban.search.priorityLabel')}
        className="h-8 px-2 text-sm rounded-md border border-input bg-background focus:outline-none focus:ring-2 focus:ring-ring"
      >
        <option value="">{t('kanban.search.priorityAll')}</option>
        <option value="critical">{t('kanban.priority.critical')}</option>
        <option value="high">{t('kanban.priority.high')}</option>
        <option value="medium">{t('kanban.priority.medium')}</option>
        <option value="low">{t('kanban.priority.low')}</option>
      </select>

      {/* Assignee Filter */}
      <select
        value={assignee}
        onChange={(e) => setAssignee(e.target.value)}
        aria-label={t('kanban.search.assigneeLabel')}
        className="h-8 px-2 text-sm rounded-md border border-input bg-background focus:outline-none focus:ring-2 focus:ring-ring"
      >
        <option value="">{t('kanban.search.assigneeAll')}</option>
        {uniqueAssignees.map((a) => (
          <option key={a} value={a}>
            {a}
          </option>
        ))}
      </select>

      {/* Sub-issue visibility toggle — "全部" reveals child issues, hidden by default. */}
      <Button
        type="button"
        variant={showSubIssues ? 'default' : 'outline'}
        size="sm"
        className="h-8 gap-1.5"
        aria-pressed={showSubIssues}
        title={t('kanban.search.showSubIssuesHint')}
        onClick={() => setShowSubIssues((v) => !v)}
      >
        <CornerDownRight className={cn('w-4 h-4', !showSubIssues && 'text-muted-foreground')} aria-hidden="true" />
        <span className="text-sm">{t('kanban.search.showAll')}</span>
      </Button>

      {/* Label Filter — multi-select popover */}
      {labels.length > 0 && (
        <Popover>
          <PopoverTrigger asChild>
            <Button
              variant="outline"
              size="sm"
              className="h-8 gap-1.5"
              aria-label={t('kanban.search.labelLabel')}
            >
              <Tag className="w-4 h-4" aria-hidden="true" />
              <span className="text-sm">
                {labelIds.length > 0
                  ? t('kanban.search.labelSelected', { count: labelIds.length })
                  : t('kanban.search.labelAll')}
              </span>
            </Button>
          </PopoverTrigger>
          <PopoverContent align="start" className="w-56 p-1">
            <div className="max-h-64 overflow-auto">
              {labels.map((label) => {
                const checked = labelIds.includes(label.id);
                return (
                  <button
                    key={label.id}
                    type="button"
                    onClick={() => toggleLabel(label.id)}
                    className="w-full flex items-center gap-2 px-2 py-1.5 text-sm rounded-sm text-left
                               hover:bg-accent hover:text-accent-foreground transition-colors
                               focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <span className="flex items-center justify-center w-4 h-4 shrink-0">
                      {checked && <Check className="w-4 h-4 text-brand" aria-hidden="true" />}
                    </span>
                    <span
                      className="w-2.5 h-2.5 rounded-full shrink-0 border border-warm-border"
                      style={{ backgroundColor: label.color }}
                      aria-hidden="true"
                    />
                    <span className="truncate">{label.name}</span>
                  </button>
                );
              })}
            </div>
          </PopoverContent>
        </Popover>
      )}
    </div>
  );
}
