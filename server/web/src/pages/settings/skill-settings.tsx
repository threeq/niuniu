import { useMemo, useState } from 'react';
import { toast } from 'sonner';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  Globe,
  HardDrive,
  Layers,
  Loader2,
  Package,
  RefreshCw,
  Search,
  Sparkles,
  Trash2,
} from 'lucide-react';
import { skillApi, getWorkspacesOverview } from '@/lib/api';
import type { SkillInfo, SkillTarget } from '@/types/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Badge } from '@/components/ui/badge';
import { Switch } from '@/components/ui/switch';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { cn } from '@/lib/utils';

// The agent columns of the enable matrix. Mirrors service.SkillAgents.
const SKILL_AGENTS: Array<'claude' | 'codex' | 'qwen' | 'omp' | 'goose'> = [
  'claude',
  'codex',
  'qwen',
  'omp',
  'goose',
];

type Scope = 'global' | 'workspace';

/**
 * Skill 管理 (issue #666, SkillsGate-style): manage Agent Skills across the
 * CLI agents niuniu drives. Install != enable - a global install lands in the
 * niuniu store disabled by default; the per-agent switches turn it on globally
 * or scoped to the selected workspace (what a scene does implicitly).
 */
export function SkillSettings() {
  const { t } = useTranslation('settings');
  const qc = useQueryClient();
  const [scope, setScope] = useState<Scope>('global');
  const [workspaceId, setWorkspaceId] = useState<number | null>(null);
  const [search, setSearch] = useState('');

  const effectiveWorkspaceId = scope === 'workspace' && workspaceId ? workspaceId : undefined;

  const { data: skills, isLoading } = useQuery({
    queryKey: ['skills', effectiveWorkspaceId ?? 0],
    queryFn: () => skillApi.list(effectiveWorkspaceId),
  });

  const { data: overview } = useQuery({
    queryKey: ['workspaces-overview-skills'],
    queryFn: () => getWorkspacesOverview(),
    enabled: scope === 'workspace',
  });

  const invalidate = () =>
    qc.invalidateQueries({ queryKey: ['skills', effectiveWorkspaceId ?? 0] });

  const installMut = useMutation({
    mutationFn: (name: string) => skillApi.install(name),
    onSuccess: () => {
      toast.success(t('skills.toasts.installed'));
      void invalidate();
    },
    onError: (e: Error) => toast.error(t('skills.toasts.actionFailed', { error: e.message })),
  });
  const updateMut = useMutation({
    mutationFn: (name: string) => skillApi.update(name),
    onSuccess: () => {
      toast.success(t('skills.toasts.updated'));
      void invalidate();
    },
    onError: (e: Error) => toast.error(t('skills.toasts.actionFailed', { error: e.message })),
  });
  const uninstallMut = useMutation({
    mutationFn: (name: string) => skillApi.uninstall(name),
    onSuccess: () => {
      toast.success(t('skills.toasts.uninstalled'));
      void invalidate();
    },
    onError: (e: Error) => toast.error(t('skills.toasts.actionFailed', { error: e.message })),
  });

  // Per-tile enable/disable: flipping the (agent x scope) switch. Failure
  // surfaces per-target errors from the result list.
  const toggleMut = useMutation({
    mutationFn: ({ skill, agent, enable }: { skill: SkillInfo; agent: string; enable: boolean }) => {
      const target: SkillTarget = {
        agent: agent as SkillTarget['agent'],
        scope,
      };
      const body = {
        name: skill.name,
        workspace_id: effectiveWorkspaceId,
        targets: [target],
      };
      return enable ? skillApi.enable(body) : skillApi.disable(body);
    },
    onSuccess: (res) => {
      const failed = res.results.filter((r) => !r.ok);
      if (failed.length > 0) {
        toast.error(t('skills.toasts.toggleFailed', { error: failed[0].error ?? '' }));
      }
      void invalidate();
    },
    onError: (e: Error) => toast.error(t('skills.toasts.actionFailed', { error: e.message })),
  });

  const rows = useMemo(() => {
    const list = skills ?? [];
    const q = search.trim().toLowerCase();
    if (!q) return list;
    return list.filter(
      (s) =>
        s.name.toLowerCase().includes(q) ||
        (s.description ?? '').toLowerCase().includes(q),
    );
  }, [skills, search]);

  const isEnabledAt = (skill: SkillInfo, agent: string): boolean =>
    (skill.installed ?? []).some(
      (st) => st.agent === agent && st.scope === scope,
    );

  const hasUpdate = (skill: SkillInfo): boolean =>
    (skill.installed ?? []).some((st) => st.update);

  const sourceBadge = (skill: SkillInfo) => {
    switch (skill.source) {
      case 'builtin':
        return <Badge variant="outline" className="text-[10px]">{t('skills.sourceBuiltin')}</Badge>;
      case 'marketplace':
        return <Badge variant="outline" className="text-[10px]">{t('skills.sourceMarketplace')}</Badge>;
      default:
        return <Badge variant="secondary" className="text-[10px]">{t('skills.sourceUser')}</Badge>;
    }
  };

  return (
    <div className="space-y-4">
      <section>
        <h2 className="text-base font-semibold text-warm-text flex items-center gap-2">
          <Sparkles className="h-4 w-4" aria-hidden />
          {t('skills.title')}
        </h2>
        <p className="mt-1 text-sm text-warm-text-muted">{t('skills.subtitle')}</p>
      </section>

      {/* Scope selector: 全局（按 agent） vs 工作空间 - the reference managers'
          Global Workspace / Project Workspaces split. */}
      <section className="flex flex-wrap items-center gap-3">
        <div className="inline-flex rounded-md border border-warm-border overflow-hidden">
          <button
            type="button"
            onClick={() => setScope('global')}
            className={cn(
              'flex items-center gap-1.5 px-3 py-1.5 text-xs',
              scope === 'global'
                ? 'bg-brand-soft text-brand'
                : 'text-warm-text-muted hover:text-warm-text',
            )}
          >
            <Globe className="h-3.5 w-3.5" aria-hidden />
            {t('skills.scopeGlobal')}
          </button>
          <button
            type="button"
            onClick={() => setScope('workspace')}
            className={cn(
              'flex items-center gap-1.5 px-3 py-1.5 text-xs',
              scope === 'workspace'
                ? 'bg-brand-soft text-brand'
                : 'text-warm-text-muted hover:text-warm-text',
            )}
          >
            <Layers className="h-3.5 w-3.5" aria-hidden />
            {t('skills.scopeWorkspace')}
          </button>
        </div>
        {scope === 'workspace' && (
          <Select
            value={workspaceId != null ? String(workspaceId) : undefined}
            onValueChange={(v) => setWorkspaceId(Number(v))}
          >
            <SelectTrigger className="h-8 w-56">
              <SelectValue placeholder={t('skills.workspacePlaceholder')} />
            </SelectTrigger>
            <SelectContent>
              {(overview?.workspaces ?? []).map((w) => (
                <SelectItem key={w.workspace_id} value={String(w.workspace_id)}>
                  {w.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        <div className="relative ml-auto">
          <Search className="absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-warm-text-muted" aria-hidden />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('skills.searchPlaceholder')}
            className="h-8 w-56 pl-7 text-xs"
          />
        </div>
      </section>

      {scope === 'workspace' && workspaceId == null && (
        <p className="text-xs text-warm-text-muted">{t('skills.pickWorkspaceHint')}</p>
      )}

      {isLoading ? (
        <div className="flex items-center gap-2 text-sm text-warm-text-muted">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
          {t('skills.loading')}
        </div>
      ) : (
        <div className="rounded-lg border border-warm-border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-64">{t('skills.colSkill')}</TableHead>
                <TableHead>{t('skills.colDescription')}</TableHead>
                {SKILL_AGENTS.map((agent) => (
                  <TableHead key={agent} className="text-center">
                    {agent}
                  </TableHead>
                ))}
                <TableHead className="w-40 text-right">{t('skills.colActions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.length === 0 && (
                <TableRow>
                  <TableCell colSpan={7 + 2} className="py-6 text-center text-xs text-warm-text-muted">
                    {t('skills.empty')}
                  </TableCell>
                </TableRow>
              )}
              {rows.map((skill) => {
                const isUser = skill.source === 'user';
                const canToggle = !isUser && (scope === 'global' || workspaceId != null);
                return (
                  <TableRow key={`${skill.source}:${skill.name}`}>
                    <TableCell>
                      <div className="flex flex-col gap-1">
                        <span className="font-mono text-xs text-warm-text">
                          {skill.name}
                          {skill.version ? (
                            <span className="ml-1 text-warm-text-muted">v{skill.version}</span>
                          ) : null}
                        </span>
                        <div className="flex flex-wrap items-center gap-1">
                          {sourceBadge(skill)}
                          {skill.global_installed && !isUser && (
                            <Badge variant="secondary" className="text-[10px] gap-0.5">
                              <HardDrive className="mr-0.5 h-2.5 w-2.5" aria-hidden />
                              {t('skills.inStore')}
                            </Badge>
                          )}
                          {hasUpdate(skill) && (
                            <Badge className="text-[10px]">{t('skills.updatable')}</Badge>
                          )}
                        </div>
                      </div>
                    </TableCell>
                    <TableCell className="max-w-md">
                      <p className="line-clamp-2 text-xs text-warm-text-muted">
                        {skill.description ?? '-'}
                      </p>
                    </TableCell>
                    {SKILL_AGENTS.map((agent) => {
                      // Marketplace skills ride the claude plugin system only.
                      const agentAvailable =
                        skill.source !== 'marketplace' || agent === 'claude';
                      const busy = toggleMut.isPending;
                      return (
                        <TableCell key={agent} className="text-center">
                          <Switch
                            checked={isEnabledAt(skill, agent)}
                            disabled={!canToggle || !agentAvailable || busy}
                            onCheckedChange={(checked) =>
                              toggleMut.mutate({ skill, agent, enable: checked })
                            }
                            aria-label={`${skill.name} ${agent}`}
                          />
                        </TableCell>
                      );
                    })}
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1">
                        {!isUser && !skill.global_installed && (
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-7 px-2 text-xs"
                            disabled={installMut.isPending}
                            onClick={() => installMut.mutate(skill.name)}
                          >
                            <Package className="mr-1 h-3 w-3" aria-hidden />
                            {t('skills.install')}
                          </Button>
                        )}
                        {skill.source === 'builtin' && (
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-7 px-2 text-xs"
                            disabled={updateMut.isPending}
                            onClick={() => updateMut.mutate(skill.name)}
                          >
                            <RefreshCw className="mr-1 h-3 w-3" aria-hidden />
                            {t('skills.update')}
                          </Button>
                        )}
                        {!isUser && (
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-7 px-2 text-xs text-destructive hover:text-destructive"
                            disabled={uninstallMut.isPending}
                            onClick={() => uninstallMut.mutate(skill.name)}
                          >
                            <Trash2 className="mr-1 h-3 w-3" aria-hidden />
                            {t('skills.uninstall')}
                          </Button>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}

      <p className="text-xs text-warm-text-muted">{t('skills.modelHint')}</p>
    </div>
  );
}
