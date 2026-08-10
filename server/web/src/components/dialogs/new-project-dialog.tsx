import { useState, useEffect } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { api, ApiError } from '@/lib/api';
import { projectBlueprintApi } from '@/lib/project-blueprint-api';
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

interface NewProjectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function NewProjectDialog({ open, onOpenChange }: NewProjectDialogProps) {
  const { t } = useTranslation('projects');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [blueprintId, setBlueprintId] = useState<number | ''>('');
  // Project default agent CLI — pre-selected when creating workspaces under this
  // project and used verbatim for issue-auto-created workspaces. Default Claude.
  const [defaultCliType, setDefaultCliType] = useState<'claude' | 'codex' | 'qwen'>('claude');
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;
  const [owner, setOwner] = useState<OwnerRef>({ type: 'user', id: userId });
  const queryClient = useQueryClient();

  // Templates usable by the selected owner (builtins + owner's own).
  const ownerParam = { type: owner.type, id: owner.id };
  const { data: blueprints = [] } = useQuery({
    queryKey: ['project-blueprints', owner.type, owner.id],
    queryFn: () => projectBlueprintApi.list(ownerParam),
    enabled: open,
  });
  const { data: defaultRes } = useQuery({
    queryKey: ['project-blueprints-default', owner.type, owner.id],
    queryFn: () => projectBlueprintApi.getDefault(ownerParam),
    enabled: open,
  });

  // Pre-select the owner's default template once it (and the list) load. Keyed
  // on owner so switching owner re-applies that owner's default.
  useEffect(() => {
    if (!open) return;
    const def = defaultRes?.blueprint_id;
    if (def && blueprints.some((b) => b.id === def)) {
      setBlueprintId(def);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [defaultRes?.blueprint_id, owner.type, owner.id, blueprints.length, open]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || !blueprintId) return;

    setIsSubmitting(true);
    setError(null);
    try {
      // Personal-edition note: when the SPA can't resolve a real caller id
      // (no token + no /api/auth/me bootstrap), `userId` falls back to 0.
      // Sending `owner: {type:'user', id:0}` makes EnsureOwnerWritable reject
      // because 0 != the IdentityResolver-supplied caller id. Instead, omit
      // `owner` and let the handler default to `{type:'user', id: caller}`
      // (project.go ~244-246).
      const body: { name: string; description: string; owner?: OwnerRef; blueprint_id?: number; default_cli_type?: string } =
        { name, description };
      if (owner.id > 0) body.owner = owner;
      if (blueprintId) body.blueprint_id = blueprintId as number;
      if (defaultCliType !== 'claude') body.default_cli_type = defaultCliType;
      await api.post('/projects', body);
      queryClient.invalidateQueries({ queryKey: ['projects'] });
      setName('');
      setDescription('');
      setBlueprintId('');
      setDefaultCliType('claude');
      onOpenChange(false);
    } catch (err) {
      console.error('Failed to create project:', err);
      // Surface a clear, localized message. A duplicate name carries the stable
      // PROJECT_NAME_TAKEN code — name the conflicting project so the user knows
      // exactly which name clashed (the user is the owner, so it's one of theirs).
      if (err instanceof ApiError && err.code === 'PROJECT_NAME_TAKEN') {
        setError(t('newProject.nameExists', { name: name.trim() }));
      } else {
        setError(t('newProject.createFailed'));
      }
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleOpenChange = (newOpen: boolean) => {
    if (!newOpen) {
      setName('');
      setDescription('');
      setBlueprintId('');
      setDefaultCliType('claude');
      setOwner({ type: 'user', id: userId });
      setError(null);
    }
    onOpenChange(newOpen);
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-[425px]">
        <DialogHeader>
          <DialogTitle>{t('newProject.title')}</DialogTitle>
          <DialogDescription>
            {t('newProject.description')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit}>
          <div className="grid gap-4 py-4">
            <OwnerPicker value={owner} onChange={setOwner} userId={userId} />
            <div className="grid gap-2">
              <label htmlFor="new-project-name" className="text-sm font-medium">
                {t('newProject.nameLabel')} <span className="text-destructive">*</span>
              </label>
              <Input
                id="new-project-name"
                value={name}
                onChange={(e) => {
                  setName(e.target.value);
                  if (error) setError(null);
                }}
                placeholder={t('newProject.namePlaceholder')}
                disabled={isSubmitting}
              />
            </div>
            <div className="grid gap-2">
              <label htmlFor="new-project-description" className="text-sm font-medium">
                {t('newProject.descriptionLabel')}
              </label>
              <Input
                id="new-project-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder={t('newProject.descriptionPlaceholder')}
                disabled={isSubmitting}
              />
            </div>
            <div className="grid gap-2">
              <label htmlFor="new-project-template" className="text-sm font-medium">
                {t('newProject.templateLabel')} <span className="text-destructive">*</span>
              </label>
              <select
                id="new-project-template"
                value={blueprintId}
                onChange={(e) => setBlueprintId(e.target.value ? Number(e.target.value) : '')}
                disabled={isSubmitting || blueprints.length === 0}
                className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
              >
                <option value="" disabled>{t('newProject.templatePlaceholder')}</option>
                {blueprints.map((bp) => (
                  <option key={bp.id} value={bp.id}>
                    {bp.name}{bp.is_builtin ? `（${t('newProject.templateBuiltin')}）` : ''}
                  </option>
                ))}
              </select>
            </div>
            <div className="grid gap-2">
              <label htmlFor="new-project-default-agent" className="text-sm font-medium">
                {t('newProject.defaultAgentLabel')}
              </label>
              <select
                id="new-project-default-agent"
                value={defaultCliType}
                onChange={(e) => setDefaultCliType(e.target.value as 'claude' | 'codex' | 'qwen')}
                disabled={isSubmitting}
                className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
              >
                <option value="claude">{t('issue.workspace.cliType.claude')}</option>
                <option value="codex">{t('issue.workspace.cliType.codex')}</option>
                <option value="qwen">{t('issue.workspace.cliType.qwen')}</option>
              </select>
            </div>
          </div>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => handleOpenChange(false)}
              disabled={isSubmitting}
            >
              {t('newProject.cancel')}
            </Button>
            <Button type="submit" disabled={isSubmitting || !name.trim() || !blueprintId}>
              {isSubmitting ? t('newProject.creating') : t('newProject.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
