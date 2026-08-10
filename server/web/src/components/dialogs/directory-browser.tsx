import { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Folder, ChevronLeft, Loader2, ChevronDown, HardDrive } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { ScrollArea } from '@/components/ui/scroll-area';
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover';
import { api } from '@/lib/api';
import type { DirectoryEntry, DirectoryListResponse } from '@/types/api';
import { isIMEComposing } from '@/lib/ime';

interface DirectoryBrowserProps {
  value: string;
  onChange: (path: string) => void;
  disabled?: boolean;
  error?: string;
  /** Reports whether the current path is selectable (false when it's inside
   *  the ~/.niuniu data dir). Parents use it to gate submit. */
  onValidityChange?: (valid: boolean) => void;
}

interface SystemInfo {
  os: string;
  disk_drives: string[];
  home_dir?: string;
  /** niuniu data dir (~/.niuniu); off-limits for selection. */
  data_dir?: string;
}

// The directory picker's default location is the user's home dir only (never
// the ~/.niuniu data dir). Falls back to the first disk (Windows) or root when
// the home dir can't be resolved.
function defaultBrowseDir(info: SystemInfo | null, disks: string[]): string {
  if (info?.home_dir) return info.home_dir;
  if (disks.length > 0) return `${disks[0]}\\`;
  return '/';
}

// isUnderNiuniu reports whether path is the niuniu data dir or a descendant.
// Separators are unified and the compare is case-insensitive on Windows, so a
// user-typed `C:\Users\me\.niuniu\...` still matches the Unix-style data_dir.
function isUnderNiuniu(path: string, info: SystemInfo | null): boolean {
  const dataDir = info?.data_dir;
  if (!dataDir || !path) return false;
  const norm = (p: string) => {
    let s = p.replace(/\\/g, '/').replace(/\/+$/, '');
    if (info?.os === 'windows') s = s.toLowerCase();
    return s;
  };
  const p = norm(path);
  const b = norm(dataDir);
  if (!b) return false;
  return p === b || p.startsWith(b + '/');
}

export function DirectoryBrowser({ value, onChange, disabled, error, onValidityChange }: DirectoryBrowserProps) {
  const { t } = useTranslation('dialogs');
  const [isOpen, setIsOpen] = useState(false);
  const [currentPath, setCurrentPath] = useState('');
  const [entries, setEntries] = useState<DirectoryEntry[]>([]);
  const [parent, setParent] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [diskOpen, setDiskOpen] = useState(false);
  const [disks, setDisks] = useState<string[]>([]);
  const [systemInfo, setSystemInfo] = useState<SystemInfo | null>(null);
  const isWindows = systemInfo?.os === 'windows';
  const hasDisks = isWindows && disks.length > 0;
  const inputRef = useRef<HTMLInputElement>(null);

  // The currently-entered path is forbidden when it lands inside ~/.niuniu.
  const forbidden = isUnderNiuniu(currentPath, systemInfo);

  // Fetch system info once on mount so home_dir/data_dir are available for the
  // default location AND for validating the path before the picker is opened.
  useEffect(() => {
    api.get<SystemInfo>('/system-info').then((info) => {
      setSystemInfo(info);
      setDisks(info.disk_drives || []);
    }).catch(() => {
      setSystemInfo({ os: navigator.platform.startsWith('Win') ? 'windows' : 'linux', disk_drives: [] });
    });
  }, []);

  // Report validity to the parent so it can disable submit on a forbidden path.
  useEffect(() => {
    onValidityChange?.(!forbidden);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- onValidityChange is a stable callback from the parent; depending on `forbidden` is sufficient
  }, [forbidden]);

  // Sync with external value changes
  useEffect(() => {
    if (value !== currentPath) {
      setCurrentPath(value);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- currentPath intentionally omitted: this effect only syncs when the external prop changes; adding currentPath would re-fire on internal navigation
  }, [value]);

  const loadDirectory = async (path: string) => {
    setIsLoading(true);
    try {
      const data = await api.get<DirectoryListResponse>('/directories', {
        params: { path },
        suppressError: true,
      });
      setEntries(data.directories || []);
      setParent(data.parent);
      setCurrentPath(data.path);
    } catch {
      // Directory doesn't exist or not accessible - show as empty
      setEntries([]);
      setParent(null);
      setCurrentPath(path);
    } finally {
      setIsLoading(false);
    }
  };

  const handleNavigate = async (entry: DirectoryEntry) => {
    // The ~/.niuniu data dir and its descendants are not selectable.
    if (isUnderNiuniu(entry.path, systemInfo)) return;
    onChange(entry.path);
    await loadDirectory(entry.path);
  };

  const handleParent = async () => {
    if (parent) {
      onChange(parent);
      await loadDirectory(parent);
    }
  };

  const handleDiskSelect = async (disk: string) => {
    const path = isWindows ? `${disk}\\` : '/';
    onChange(path);
    await loadDirectory(path);
    setDiskOpen(false);
  };

  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const newPath = e.target.value;
    setCurrentPath(newPath);
  };

  const handleInputBlur = () => {
    onChange(currentPath);
  };

  const handleInputKeyDown = (e: React.KeyboardEvent) => {
    if (isIMEComposing(e)) return;
    if (e.key === 'Enter') {
      e.preventDefault();
      loadDirectory(currentPath);
    }
  };

  const handleToggle = async () => {
    if (isOpen) {
      setIsOpen(false);
      return;
    }
    setIsOpen(true);
    setEntries([]);

    // If the field already holds a path, browse from there. Otherwise open on
    // the user's home dir — never the ~/.niuniu data dir.
    const existing = value.trim();
    if (existing) {
      await loadDirectory(existing);
      return;
    }

    // Resolve system info (home/disks) — already fetched on mount, but fall
    // back to a fetch so the default doesn't depend on effect timing.
    let info = systemInfo;
    if (!info) {
      try {
        info = await api.get<SystemInfo>('/system-info');
        setSystemInfo(info);
        setDisks(info.disk_drives || []);
      } catch {
        info = {
          os: navigator.platform.startsWith('Win') ? 'windows' : 'linux',
          disk_drives: [],
        };
      }
    }
    await loadDirectory(defaultBrowseDir(info, info.disk_drives || disks));
  };

  return (
    <>
      <div className="flex gap-2">
        <div className="relative flex-1">
          <Input
            ref={inputRef}
            value={currentPath}
            onChange={handleInputChange}
            onBlur={handleInputBlur}
            onKeyDown={handleInputKeyDown}
            placeholder="/path/to/directory"
            disabled={disabled}
            className={`pr-8 ${error || forbidden ? 'border-destructive' : ''}`}
          />
          <div className="absolute right-1 top-1/2 -translate-y-1/2 flex items-center">
            {hasDisks && (
              <Popover open={diskOpen} onOpenChange={setDiskOpen}>
                <PopoverTrigger asChild>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-7 px-1"
                  >
                    <ChevronDown className="h-3 w-3" />
                  </Button>
                </PopoverTrigger>
                <PopoverContent className="w-auto p-1" align="end">
                  <div className="grid grid-cols-3 gap-1">
                    {disks.map((disk) => (
                      <Button
                        key={disk}
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => handleDiskSelect(disk)}
                        className="justify-start"
                      >
                        <HardDrive className="h-3 w-3 mr-1" />
                        {disk}
                      </Button>
                    ))}
                  </div>
                </PopoverContent>
              </Popover>
            )}
          </div>
          {forbidden ? (
            <p className="text-xs text-destructive mt-1">{t('directoryBrowser.niuniuForbidden')}</p>
          ) : error ? (
            <p className="text-xs text-destructive mt-1">{error}</p>
          ) : null}
        </div>
        <Button
          type="button"
          variant="outline"
          onClick={handleToggle}
          disabled={disabled}
        >
          {isOpen ? t('directoryBrowser.close') : t('directoryBrowser.open')}
        </Button>
      </div>

      {isOpen && (
        <div className="mt-2 border rounded-md bg-background">
          <div className="flex items-center gap-2 p-2 border-b bg-muted/50">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={handleParent}
              disabled={!parent || isLoading}
            >
              <ChevronLeft className="h-4 w-4" />
            </Button>
            <div className="flex-1 overflow-x-auto" style={{ scrollbarWidth: 'thin' }}>
              <span className="text-sm text-muted-foreground whitespace-nowrap">
                {currentPath || '/'}
              </span>
            </div>
          </div>

          <ScrollArea className="h-[200px]">
            {isLoading ? (
              <div className="flex items-center justify-center h-full">
                <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
              </div>
            ) : entries.length === 0 ? (
              <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
                {t('directoryBrowser.emptyFolder')}
              </div>
            ) : (
              <div className="p-1">
                {entries.map((entry) => {
                  const entryForbidden = isUnderNiuniu(entry.path, systemInfo);
                  return (
                    <button
                      key={entry.path}
                      type="button"
                      onClick={() => handleNavigate(entry)}
                      disabled={entryForbidden}
                      title={entryForbidden ? t('directoryBrowser.niuniuForbidden') : undefined}
                      className={`flex items-center gap-2 w-full px-2 py-1.5 text-sm rounded-sm text-left ${
                        entryForbidden
                          ? 'opacity-50 cursor-not-allowed'
                          : 'hover:bg-muted'
                      }`}
                    >
                      <Folder className="h-4 w-4 text-muted-foreground" />
                      <span className="truncate">{entry.name}</span>
                    </button>
                  );
                })}
              </div>
            )}
          </ScrollArea>
        </div>
      )}
    </>
  );
}
