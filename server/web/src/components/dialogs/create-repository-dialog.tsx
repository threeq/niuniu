import { useState, useEffect, useMemo } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { apiFetch, api } from '@/lib/api';
import { toast } from 'sonner';
import { useAuthStore } from '@/stores/auth-store';
import { OwnerPicker } from '@/components/shared/owner-picker';
import type { OwnerRef } from '@/types/org';
import type { DirectoryEntry, DirectoryListResponse } from '@/types/api';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { ScrollArea } from '@/components/ui/scroll-area';
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover';
import { Folder, ChevronLeft, Loader2, HardDrive, ChevronDown } from 'lucide-react';

interface CreateRepositoryDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

type DetectedType = 'local' | 'remote';

/** Auto-detect whether the input is a local path or remote URL. */
function detectRepoType(input: string): DetectedType {
  const trimmed = input.trim();
  if (!trimmed) return 'local';
  // URL schemes
  if (/^(https?|git|ssh|ftp|ftps|file):\/\//i.test(trimmed)) return 'remote';
  // SCP-style: git@host:path
  if (/^git@/.test(trimmed)) return 'remote';
  // Windows drive letter
  if (/^[a-zA-Z]:[\\/]/.test(trimmed)) return 'local';
  // Unix absolute path
  if (/^\//.test(trimmed)) return 'local';
  // Tilde
  if (/^~/.test(trimmed)) return 'local';
  // Default to local
  return 'local';
}

interface SystemInfo {
  os: string;
  disk_drives: string[];
  home_dir?: string;
  data_dir?: string;
}

function defaultBrowseDir(info: SystemInfo | null): string {
  if (info?.home_dir) return info.home_dir;
  if (info?.disk_drives?.length) return `${info.disk_drives[0]}\\`;
  return '/';
}

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

export function CreateRepositoryDialog({ open, onOpenChange }: CreateRepositoryDialogProps) {
  const { t } = useTranslation('repositories');
  const navigate = useNavigate();
  const [address, setAddress] = useState('');
  const [name, setName] = useState('');
  const [autoInit, setAutoInit] = useState(true);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [inputError, setInputError] = useState<string | undefined>();
  const [pathValid, setPathValid] = useState(true);
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;
  const [owner, setOwner] = useState<OwnerRef>({ type: 'user', id: userId });
  const queryClient = useQueryClient();

  // Browse popover state
  const [browseOpen, setBrowseOpen] = useState(false);
  const [browsePath, setBrowsePath] = useState('');
  const [entries, setEntries] = useState<DirectoryEntry[]>([]);
  const [parent, setParent] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [systemInfo, setSystemInfo] = useState<SystemInfo | null>(null);
  const [disks, setDisks] = useState<string[]>([]);
  const [diskOpen, setDiskOpen] = useState(false);

  const detectedType = useMemo(() => detectRepoType(address), [address]);
  const isRemote = detectedType === 'remote';

  // Fetch system info on mount
  useEffect(() => {
    api.get<SystemInfo>('/system-info').then((info) => {
      setSystemInfo(info);
      setDisks(info.disk_drives || []);
    }).catch(() => {
      setSystemInfo({ os: 'unknown', disk_drives: [] });
    });
  }, []);

  // Validate path when it's local
  useEffect(() => {
    if (isRemote) {
      setPathValid(true);
    } else {
      setPathValid(!isUnderNiuniu(address, systemInfo));
    }
  }, [address, isRemote, systemInfo]);

  // Auto-derive name from address
  const derivedName = useMemo(() => {
    if (!address.trim()) return '';
    if (isRemote) {
      // Extract repo name from remote URL
      const m = address.trim().match(/(?:^|\/)([^/]+?)(?:\.git)?$/);
      return m ? m[1] : '';
    }
    // Extract last dir from local path
    const cleaned = address.trim().replace(/[\\/]+$/, '');
    const lastSep = Math.max(cleaned.lastIndexOf('/'), cleaned.lastIndexOf('\\'));
    return lastSep >= 0 ? cleaned.slice(lastSep + 1) : cleaned;
  }, [address, isRemote]);

  const loadDirectory = async (path: string) => {
    setIsLoading(true);
    try {
      const data = await api.get<DirectoryListResponse>('/directories', {
        params: { path },
        suppressError: true,
      });
      setEntries(data.directories || []);
      setParent(data.parent);
      setBrowsePath(data.path);
    } catch {
      setEntries([]);
      setParent(null);
      setBrowsePath(path);
    } finally {
      setIsLoading(false);
    }
  };

  const handleBrowseOpenChange = async (open: boolean) => {
    if (!open) {
      setBrowseOpen(false);
      return;
    }
    setBrowseOpen(true);
    setEntries([]);
    const existing = address.trim();
    if (existing && !isRemote) {
      await loadDirectory(existing);
      return;
    }
    let info = systemInfo;
    if (!info) {
      try {
        info = await api.get<SystemInfo>('/system-info');
        setSystemInfo(info);
        setDisks(info.disk_drives || []);
      } catch {
        info = { os: 'unknown', disk_drives: [] };
      }
    }
    await loadDirectory(defaultBrowseDir(info));
  };

  const handleNavigate = async (entry: DirectoryEntry) => {
    if (isUnderNiuniu(entry.path, systemInfo)) return;
    setAddress(entry.path);
    await loadDirectory(entry.path);
  };

  const handleParent = async () => {
    if (parent) {
      setAddress(parent);
      await loadDirectory(parent);
    }
  };

  const handleDiskSelect = async (disk: string) => {
    const path = systemInfo?.os === 'windows' ? `${disk}\\` : '/';
    setAddress(path);
    await loadDirectory(path);
    setDiskOpen(false);
  };

  const handleAddressChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setAddress(e.target.value);
    setInputError(undefined);
  };

  const handleAddressBlur = () => {
    // If the user typed a local path, validate it
    const trimmed = address.trim();
    if (trimmed && !isRemote) {
      setPathValid(!isUnderNiuniu(trimmed, systemInfo));
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const value = address.trim();
    if (!value) return;

    setIsSubmitting(true);
    setInputError(undefined);
    try {
      await apiFetch('/repositories', {
        method: 'POST',
        body: JSON.stringify({
          name: name || undefined,
          ...(owner.id > 0 ? { owner } : {}),
          ...(isRemote
            ? { remote_url: value }
            : { path: value, auto_init: autoInit }),
        }),
        suppressError: true,
      });
      queryClient.invalidateQueries({ queryKey: ['repositories'] });
      resetForm();
      onOpenChange(false);
      toast.success(t('dialogs.create.successToast'));
    } catch (error) {
      console.error('Failed to create repository:', error);
      const raw = error instanceof Error ? error.message : '';
      const message = raw || t('dialogs.create.errorToast');
      if (raw.startsWith('GIT_IDENTITY_MISSING')) {
        toast.error(t('dialogs.create.errors.gitIdentityMissing'), {
          action: {
            label: t('dialogs.create.errors.goConfigure'),
            onClick: () => navigate({ to: '/settings', search: { tab: 'system-deps' } }),
          },
        })
        return
      }
      if (raw.startsWith('GIT_INITIAL_COMMIT_FAILED')) {
        const detail = raw.replace(/^GIT_INITIAL_COMMIT_FAILED:\s*/, '')
        toast.error(t('dialogs.create.errors.initialCommitFailed', { detail }))
        return
      }
      toast.error(message);
      if (typeof error === 'object' && error && 'message' in error) {
        const err = error as { message?: string };
        if (err.message) {
          if (err.message.includes('CLONE_FAILED')) {
            setInputError(t('dialogs.create.errors.cloneFailed'));
          } else if (err.message.includes('NOT_A_GIT_REPO')) {
            setInputError(t('dialogs.create.errors.notGitRepo'));
          } else if (err.message.includes('PATH_CREATION_FAILED')) {
            setInputError(t('dialogs.create.errors.pathCreationFailed'));
          } else if (err.message.includes('GIT_INIT_FAILED')) {
            setInputError(t('dialogs.create.errors.gitInitFailed'));
          } else if (err.message.includes('REPO_NAME_EXISTS')) {
            setInputError(t('dialogs.create.errors.repoNameExists'));
          } else {
            setInputError(err.message);
          }
        }
      }
    } finally {
      setIsSubmitting(false);
    }
  };

  const resetForm = () => {
    setAddress('');
    setName('');
    setAutoInit(true);
    setInputError(undefined);
    setPathValid(true);
  };

  const handleClose = (open: boolean) => {
    if (!open) resetForm();
    onOpenChange(open);
  };

  const canSubmit = !isSubmitting && address.trim() !== '' && pathValid;
  const isWindows = systemInfo?.os === 'windows';
  const hasDisks = isWindows && disks.length > 0;

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent className="sm:max-w-[525px]">
        <DialogHeader>
          <DialogTitle>{t('dialogs.create.title')}</DialogTitle>
          <DialogDescription>
            {t('dialogs.create.description')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit}>
          <div className="grid gap-4 py-4">
            <OwnerPicker value={owner} onChange={setOwner} userId={userId} />

            {/* Address input with browse button */}
            <div className="grid gap-2">
              <label htmlFor="address" className="text-sm font-medium">
                {t('dialogs.create.inputLabel')} <span className="text-destructive">*</span>
              </label>
              <div className="flex gap-2">
                <div className="relative flex-1">
                  <Input
                    id="address"
                    value={address}
                    onChange={handleAddressChange}
                    onBlur={handleAddressBlur}
                    placeholder={t('dialogs.create.inputPlaceholder')}
                    disabled={isSubmitting}
                    className={inputError || (!pathValid && !isRemote) ? 'border-destructive' : ''}
                  />
                </div>
                <Popover open={browseOpen} onOpenChange={handleBrowseOpenChange}>
                  <PopoverTrigger asChild>
                    <Button
                      type="button"
                      variant="outline"
                      disabled={isSubmitting}
                    >
                      {t('dialogs.create.browse')}
                    </Button>
                  </PopoverTrigger>
                  <PopoverContent className="w-[400px] p-0" align="end">
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
                          {browsePath || '/'}
                        </span>
                      </div>
                      {hasDisks && (
                        <Popover open={diskOpen} onOpenChange={setDiskOpen}>
                          <PopoverTrigger asChild>
                            <Button type="button" variant="ghost" size="sm" className="h-7 px-1">
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
                    <ScrollArea className="h-[200px]">
                      {isLoading ? (
                        <div className="flex items-center justify-center h-full">
                          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
                        </div>
                      ) : entries.length === 0 ? (
                        <div className="flex items-center justify-center h-full text-sm text-muted-foreground p-4">
                          {t('dialogs:directoryBrowser.emptyFolder')}
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
                                title={entryForbidden ? t('dialogs:directoryBrowser.niuniuForbidden') : undefined}
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
                  </PopoverContent>
                </Popover>
              </div>
              {/* Detected type indicator */}
              <div className="flex items-center gap-2">
                <span className="text-xs px-2 py-0.5 rounded-full font-medium border bg-muted text-muted-foreground">
                  {isRemote ? t('dialogs.create.detectedRemote') : t('dialogs.create.detectedLocal')}
                </span>
                <span className="text-xs text-muted-foreground">
                  {isRemote ? t('dialogs.create.detectedRemoteHint') : t('dialogs.create.detectedLocalHint')}
                </span>
              </div>
              {!pathValid && !isRemote && (
                <p className="text-xs text-destructive">{t('dialogs:directoryBrowser.niuniuForbidden')}</p>
              )}
              {inputError && (
                <p className="text-xs text-destructive">{inputError}</p>
              )}
            </div>

            {/* Name */}
            <div className="grid gap-2">
              <label htmlFor="name" className="text-sm font-medium">
                {t('dialogs.create.nameLabel')}
              </label>
              <Input
                id="name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={derivedName || (isRemote ? t('dialogs.create.namePlaceholderRemote') : t('dialogs.create.namePlaceholderLocal'))}
                disabled={isSubmitting}
              />
            </div>

            {/* Auto init (local only) */}
            {!isRemote && (
              <div className="flex items-center gap-2">
                <Checkbox
                  id="autoInit"
                  checked={autoInit}
                  onCheckedChange={(checked: boolean) => setAutoInit(checked === true)}
                  disabled={isSubmitting}
                />
                <label htmlFor="autoInit" className="text-sm font-medium cursor-pointer">
                  {t('dialogs.create.autoInit')}
                </label>
              </div>
            )}
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => handleClose(false)}
              disabled={isSubmitting}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={!canSubmit}>
              {isSubmitting
                ? (isRemote ? t('dialogs.create.cloningInProgress') : t('dialogs.create.creatingInProgress'))
                : (isRemote ? t('dialogs.create.submitClone') : t('dialogs.create.submitCreate'))}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}