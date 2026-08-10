import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { toast } from 'sonner';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Label } from '@/components/ui/label';
import { Button } from '@/components/ui/button';
import { imbotApi } from '@/lib/imbot-api';
import type { Project } from '@/types/api';
import { ProjectSelect } from './project-select';

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projects: Project[];
}

// Owner-level "new bot" entry. A bot's credential intake reuses the existing,
// project-scoped onboarding + secure credential-link flow (design §8: "创建/凭据
// 沿用现有渠道创建 + 安全录入链接流程"). The picked project is only where the
// AI-guided onboarding wizard runs (it needs a project workspace) — the bot is
// owner-level and is NOT bound or default-routed to it. The credential is entered
// on a separate page, never in this dialog.
export function AddBotDialog({ open, onOpenChange, projects }: Props) {
  const { t } = useTranslation('settings');
  const navigate = useNavigate();
  const [projectId, setProjectId] = useState<number | null>(projects[0]?.id ?? null);

  const onboarding = useMutation({
    mutationFn: (pid: number) => imbotApi.startOnboarding(pid),
    onSuccess: (data) => {
      onOpenChange(false);
      void navigate({ to: '/workspaces/$id', params: { id: String(data.workspace_id) } });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : String(e)),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('imbot.addBot')}</DialogTitle>
          <DialogDescription>{t('imbot.addBotDesc')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="grid gap-1.5">
            <Label className="text-sm">{t('imbot.onboardingProject')}</Label>
            <ProjectSelect
              projects={projects}
              value={projectId}
              onChange={setProjectId}
              disabled={projects.length === 0}
            />
            <p className="text-xs text-warm-text-muted">{t('imbot.addBotProjectHint')}</p>
          </div>
          {projects.length === 0 && (
            <p className="text-xs text-destructive">{t('imbot.noWritableProjects')}</p>
          )}
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={onboarding.isPending}
          >
            {t('imbot.cancel')}
          </Button>
          <Button
            type="button"
            disabled={projectId == null || onboarding.isPending}
            onClick={() => {
              if (projectId != null) onboarding.mutate(projectId);
            }}
          >
            {onboarding.isPending ? t('imbot.starting') : t('imbot.startOnboarding')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
