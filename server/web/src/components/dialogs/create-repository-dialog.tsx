import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { apiFetch } from '@/lib/api';
import { toast } from 'sonner';
import { useAuthStore } from '@/stores/auth-store';
import { OwnerPicker } from '@/components/shared/owner-picker';
import type { OwnerRef } from '@/types/org';
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
import { DirectoryBrowser } from './directory-browser';

interface CreateRepositoryDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

type Mode = 'local' | 'remote';

export function CreateRepositoryDialog({ open, onOpenChange }: CreateRepositoryDialogProps) {
  const { t } = useTranslation('repositories');
  const navigate = useNavigate();
  const [mode, setMode] = useState<Mode>('local');
  const [name, setName] = useState('');
  const [path, setPath] = useState('');
  const [remoteURL, setRemoteURL] = useState('');
  const [autoInit, setAutoInit] = useState(true);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [inputError, setInputError] = useState<string | undefined>();
  // false when the chosen local path is inside ~/.niuniu (blocks submit).
  const [pathValid, setPathValid] = useState(true);
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;
  const [owner, setOwner] = useState<OwnerRef>({ type: 'user', id: userId });
  const queryClient = useQueryClient();

  const isRemote = mode === 'remote';

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const value = isRemote ? remoteURL.trim() : path.trim();
    if (!value) return;

    setIsSubmitting(true);
    setInputError(undefined);
    try {
      // Omit `owner` when id is 0 (personal-edition no-currentUser fallback):
      // handler defaults to `{type:'user', id: caller}`. Sending {id: 0} is
      // rejected by EnsureOwnerWritable because 0 != caller's real id.
      // suppressError: true prevents apiFetch from firing a generic "API Error"
      // toast — the catch block below shows targeted toasts for each error code.
      await apiFetch('/repositories', {
        method: 'POST',
        body: JSON.stringify({
          name: name || undefined,
          ...(owner.id > 0 ? { owner } : {}),
          ...(isRemote
            ? { remote_url: remoteURL.trim() }
            : { path: path.trim(), auto_init: autoInit }),
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
    setName('');
    setPath('');
    setRemoteURL('');
    setAutoInit(true);
    setInputError(undefined);
  };

  const handleClose = (open: boolean) => {
    if (!open) resetForm();
    onOpenChange(open);
  };

  const handleModeChange = (m: Mode) => {
    setMode(m);
    setInputError(undefined);
  };

  const canSubmit = !isSubmitting && (isRemote ? remoteURL.trim() !== '' : (path.trim() !== '' && pathValid));

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
            {/* Mode toggle */}
            <div className="flex rounded-md border overflow-hidden text-sm">
              <button
                type="button"
                onClick={() => handleModeChange('local')}
                className={`flex-1 px-3 py-1.5 transition-colors ${
                  !isRemote
                    ? 'bg-primary text-primary-foreground'
                    : 'bg-background hover:bg-muted text-muted-foreground'
                }`}
                disabled={isSubmitting}
              >
                {t('dialogs.create.modeLocal')}
              </button>
              <button
                type="button"
                onClick={() => handleModeChange('remote')}
                className={`flex-1 px-3 py-1.5 transition-colors ${
                  isRemote
                    ? 'bg-primary text-primary-foreground'
                    : 'bg-background hover:bg-muted text-muted-foreground'
                }`}
                disabled={isSubmitting}
              >
                {t('dialogs.create.modeRemote')}
              </button>
            </div>

            {/* Local path input */}
            {!isRemote && (
              <div className="grid gap-2">
                <label htmlFor="path" className="text-sm font-medium">
                  {t('dialogs.create.pathLabel')} <span className="text-destructive">*</span>
                </label>
                <DirectoryBrowser
                  value={path}
                  onChange={setPath}
                  disabled={isSubmitting}
                  error={inputError}
                  onValidityChange={setPathValid}
                />
                <p className="text-xs text-muted-foreground">
                  {t('dialogs.create.pathHint')}
                </p>
              </div>
            )}

            {/* Remote URL input */}
            {isRemote && (
              <div className="grid gap-2">
                <label htmlFor="remoteURL" className="text-sm font-medium">
                  {t('dialogs.create.remoteLabel')} <span className="text-destructive">*</span>
                </label>
                <Input
                  id="remoteURL"
                  value={remoteURL}
                  onChange={(e) => setRemoteURL(e.target.value)}
                  placeholder={t('dialogs.create.remotePlaceholder')}
                  disabled={isSubmitting}
                  className={inputError ? 'border-destructive' : ''}
                />
                {inputError && (
                  <p className="text-xs text-destructive">{inputError}</p>
                )}
                <p className="text-xs text-muted-foreground">
                  {t('dialogs.create.remoteHint')}
                </p>
              </div>
            )}

            {/* Name */}
            <div className="grid gap-2">
              <label htmlFor="name" className="text-sm font-medium">
                {t('dialogs.create.nameLabel')}
              </label>
              <Input
                id="name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={isRemote ? t('dialogs.create.namePlaceholderRemote') : t('dialogs.create.namePlaceholderLocal')}
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
              {isSubmitting ? (isRemote ? t('dialogs.create.cloningInProgress') : t('dialogs.create.creatingInProgress')) : (isRemote ? t('dialogs.create.submitClone') : t('dialogs.create.submitCreate'))}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
