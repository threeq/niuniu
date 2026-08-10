import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { X } from 'lucide-react';
import { toast } from 'sonner';
import { Input } from '@/components/ui/input';
import { api } from '@/lib/api';
import type { SelectedUser } from '@/types/org';
import { isIMEComposing } from '@/lib/ime';

export interface UserSearchInputProps {
  orgId: number;
  value: SelectedUser | null;
  onChange: (user: SelectedUser | null) => void;
  placeholder?: string;
  disabled?: boolean;
  autoFocus?: boolean;
}

const DEBOUNCE_MS = 300;
const MAX_QUERY_LEN = 50;

export function UserSearchInput({
  orgId,
  value,
  onChange,
  placeholder,
  disabled,
  autoFocus,
}: UserSearchInputProps) {
  const { t } = useTranslation('orgs');
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<SelectedUser[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [errored, setErrored] = useState(false);
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);

  const tokenRef = useRef(0);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  const displayString = value
    ? t('team.userSearch.displayString', { name: value.display_name, username: value.username })
    : query;

  // Compare-during-render: clear search state when the selected value is set externally
  const [prevValue, setPrevValue] = useState(value);
  if (value !== prevValue) {
    setPrevValue(value);
    if (value) {
      setQuery('');
      setResults([]);
      setTotal(0);
      setErrored(false);
      setOpen(false);
    }
  }

  function fireSearch(q: string) {
    const trimmed = q.trim();
    if (!trimmed || trimmed.length > MAX_QUERY_LEN) {
      setResults([]);
      setTotal(0);
      setLoading(false);
      setErrored(false);
      return;
    }
    const myToken = ++tokenRef.current;
    setLoading(true);
    setErrored(false);
    api
      .searchUsers(orgId, trimmed)
      .then((resp) => {
        if (myToken !== tokenRef.current) return;
        setResults(resp.users);
        setTotal(resp.total);
        setActiveIndex(0);
        setLoading(false);
      })
      .catch(() => {
        if (myToken !== tokenRef.current) return;
        setResults([]);
        setTotal(0);
        setLoading(false);
        setErrored(true);
        toast.error(t('team.userSearch.searchFailed'));
      });
  }

  function handleChange(e: React.ChangeEvent<HTMLInputElement>) {
    const next = e.target.value;
    setQuery(next);
    setOpen(true);
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => fireSearch(next), DEBOUNCE_MS);
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (isIMEComposing(e)) return;
    if (!open) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActiveIndex((i) => Math.min(i + 1, Math.max(results.length - 1, 0)));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActiveIndex((i) => Math.max(i - 1, 0));
    } else if (e.key === 'Enter') {
      // Lone-match shortcut: if exactly one result and its username equals trimmed input, select it.
      if (results.length === 1 && results[0].username === query.trim()) {
        e.preventDefault();
        select(results[0]);
        return;
      }
      if (results[activeIndex]) {
        e.preventDefault();
        select(results[activeIndex]);
      }
    } else if (e.key === 'Escape') {
      setOpen(false);
    }
  }

  function select(u: SelectedUser) {
    onChange(u);
    setQuery('');
    setResults([]);
    setTotal(0);
    setOpen(false);
  }

  function clear() {
    onChange(null);
    setQuery('');
    setResults([]);
    setTotal(0);
    setErrored(false);
    setOpen(false);
    inputRef.current?.focus();
  }

  function handleBlur() {
    setTimeout(() => setOpen(false), 100);
  }

  return (
    <div className="relative flex-1">
      <div className="flex items-center">
        <Input
          ref={inputRef}
          value={value ? displayString : query}
          onChange={handleChange}
          onFocus={() => setOpen(true)}
          onBlur={handleBlur}
          onKeyDown={handleKeyDown}
          placeholder={placeholder}
          disabled={disabled}
          autoFocus={autoFocus}
          readOnly={!!value}
          className="pr-8"
        />
        {value && (
          <button
            type="button"
            aria-label={t('team.userSearch.clearLabel')}
            className="absolute right-2 inline-flex h-6 w-6 items-center justify-center rounded text-muted-foreground hover:bg-muted"
            onClick={clear}
            disabled={disabled}
          >
            <X className="h-4 w-4" />
          </button>
        )}
      </div>
      {open && !value && (
        <div className="absolute z-50 mt-1 w-full rounded-md border bg-popover shadow-md">
          {errored ? (
            <div className="p-3 text-sm text-destructive">
              {t('team.userSearch.searchFailed')}
            </div>
          ) : loading ? (
            <div className="p-3 text-sm text-muted-foreground">
              {t('team.userSearch.searching')}
            </div>
          ) : query.trim() === '' ? (
            <div className="p-3 text-sm text-muted-foreground">
              {t('team.userSearch.promptInput')}
            </div>
          ) : results.length === 0 ? (
            <div className="p-3 text-sm text-muted-foreground">
              {t('team.userSearch.noResults')}
            </div>
          ) : (
            <>
              <ul className="max-h-72 overflow-y-auto py-1">
                {results.map((u, i) => (
                  <li
                    key={u.id}
                    className={`flex cursor-pointer items-center gap-2 px-3 py-2 text-sm hover:bg-accent ${
                      i === activeIndex ? 'bg-accent' : ''
                    }`}
                    onMouseDown={(e) => {
                      e.preventDefault();
                      select(u);
                    }}
                    onMouseEnter={() => setActiveIndex(i)}
                  >
                    <span className="flex h-6 w-6 items-center justify-center rounded-full bg-muted text-xs">
                      {u.display_name.slice(0, 1).toUpperCase()}
                    </span>
                    <span className="font-medium">{u.display_name}</span>
                    <span className="text-muted-foreground">@</span>
                    <span className="text-muted-foreground">{u.username}</span>
                  </li>
                ))}
              </ul>
              {total > results.length && (
                <div className="border-t px-3 py-2 text-xs text-muted-foreground">
                  {t('team.userSearch.showing', { shown: results.length, total })}
                </div>
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
}
