import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { sceneApi } from '@/lib/api';
import { useAuthStore } from '@/stores/auth-store';
import type { Scene } from '@/types/api';
import type { OwnerRef } from '@/types/org';
import { OwnerPicker } from '@/components/shared/owner-picker';
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';

interface SceneForkDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  scene: Scene | null;
  onForked?: (forked: Scene) => void;
}

export function SceneForkDialog({ open, onOpenChange, scene, onForked }: SceneForkDialogProps) {
  const { t } = useTranslation('scenes');
  const qc = useQueryClient();
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;
  // Derive the displayed slug from props (default = `${scene.slug}-fork`) and
  // only switch to a user-typed override once the user actually edits. This
  // avoids the setState-in-effect lint failure while preserving the same UX.
  const [customSlug, setCustomSlug] = useState<string | null>(null);
  const [owner, setOwner] = useState<OwnerRef>({ type: 'user', id: userId });
  const defaultSlug = scene ? `${scene.slug}-fork` : '';
  const newSlug = customSlug ?? defaultSlug;

  useEffect(() => {
    if (owner.type === 'user' && owner.id === 0 && userId > 0) {
      setOwner({ type: 'user', id: userId });
    }
  }, [owner.id, owner.type, userId]);

  const forkMut = useMutation({
    mutationFn: async () => {
      if (!scene) throw new Error('no scene');
      return sceneApi.fork(scene.id, newSlug.trim(), owner);
    },
    onSuccess: (forked) => {
      qc.invalidateQueries({ queryKey: ['scenes'] });
      toast.success(t('fork.success'));
      onForked?.(forked);
      onOpenChange(false);
      setCustomSlug(null);
      setOwner({ type: 'user', id: userId });
    },
  });

  const isValid = newSlug.trim().length > 0;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[420px]">
        <DialogHeader>
          <DialogTitle>{t('fork.title')}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-2 py-2">
          <OwnerPicker value={owner} onChange={setOwner} userId={userId} />
          <label htmlFor="scene-fork-slug" className="text-sm font-medium">
            {t('fork.field_new_slug')}
          </label>
          <Input
            id="scene-fork-slug"
            value={newSlug}
            onChange={(e) => setCustomSlug(e.target.value)}
            disabled={forkMut.isPending}
            autoFocus
          />
        </div>
        <DialogFooter>
          <Button
            type="button"
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={forkMut.isPending}
          >
            {t('fork.cancel')}
          </Button>
          <Button
            type="button"
            onClick={() => forkMut.mutate()}
            disabled={!isValid || forkMut.isPending}
          >
            {forkMut.isPending ? t('fork.submitting') : t('fork.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
