import { useState } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, GitFork, Pencil, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { sceneApi } from '@/lib/api';
import { confirm } from '@/lib/confirm';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { SceneForkDialog } from './components/scene-fork-dialog';
import type { Scene } from '@/types/api';

interface SceneDetailPageProps {
  sceneId: string;
}

function Section({
  title,
  isEmpty,
  emptyText,
  children,
}: {
  title: string;
  isEmpty: boolean;
  emptyText: string;
  children: React.ReactNode;
}) {
  return (
    <section className="bg-warm-surface border border-warm-border rounded-lg p-4 space-y-2">
      <h2 className="text-sm font-semibold text-warm-text">{title}</h2>
      {isEmpty ? (
        <p className="text-xs text-warm-text-muted">{emptyText}</p>
      ) : (
        children
      )}
    </section>
  );
}

function ListBlock({ items }: { items: string[] }) {
  return (
    <ul className="text-sm text-warm-text space-y-1">
      {items.map((item, i) => (
        <li key={i} className="font-mono text-xs">
          {item}
        </li>
      ))}
    </ul>
  );
}

export function SceneDetailPage({ sceneId: sceneIdProp }: SceneDetailPageProps) {
  const { t } = useTranslation('scenes');
  const navigate = useNavigate();
  const qc = useQueryClient();
  const sceneId = Number(sceneIdProp);
  const [forkOpen, setForkOpen] = useState(false);

  const { data: scene, isLoading } = useQuery({
    queryKey: ['scene', sceneId],
    queryFn: () => sceneApi.get(sceneId),
    enabled: Number.isFinite(sceneId),
  });

  const deleteMut = useMutation({
    mutationFn: () => sceneApi.delete(sceneId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['scenes'] });
      navigate({ to: '/scenes' });
    },
  });

  if (isLoading) {
    return (
      <div className="p-6 text-sm text-warm-text-muted">{t('list.loading')}</div>
    );
  }
  if (!scene) {
    return (
      <div className="p-6 text-sm text-warm-text-muted">{t('list.empty')}</div>
    );
  }

  const isBuiltin = scene.source === 'builtin';
  const def = scene.definition;
  const mcpNames = (def.mcp ?? []).map((m) => m.name);
  const pluginNames = (def.plugins ?? []).map(
    (p) => `${p.source}${p.ref ? `@${p.ref}` : ''}${p.optional ? ' (optional)' : ''}`,
  );
  const prompts = def.prompts ?? [];
  const credentials = def.required_credentials ?? [];
  const rules = def.match?.rules ?? [];
  const assetGroups: { key: string; items: string[] }[] = [];
  const assets = def.assets ?? {};
  if (assets.env_presets?.length) {
    assetGroups.push({ key: 'env_presets', items: assets.env_presets.map((a) => a.slug) });
  }
  if (assets.project_templates?.length) {
    assetGroups.push({ key: 'project_templates', items: assets.project_templates.map((a) => a.slug) });
  }
  if (assets.quick_actions?.length) {
    assetGroups.push({ key: 'quick_actions', items: assets.quick_actions.map((a) => `${a.slug} — ${a.label}`) });
  }
  if (assets.harness_specs?.length) {
    assetGroups.push({ key: 'harness_specs', items: assets.harness_specs.map((a) => a.slug) });
  }
  if (assets.agents?.length) {
    assetGroups.push({ key: 'agents', items: assets.agents.map((a) => a.name) });
  }

  async function handleDelete() {
    if (!(await confirm(t('detail.confirm_delete')))) return;
    deleteMut.mutate();
  }

  return (
    <div className="flex flex-col h-full overflow-y-auto">
      <div className="p-6 max-w-4xl mx-auto w-full space-y-4">
        <div>
          <Button asChild variant="ghost" size="sm">
            <Link to="/scenes">
              <ArrowLeft className="w-4 h-4 mr-1" aria-hidden />
              {t('detail.back')}
            </Link>
          </Button>
        </div>

        <header className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h1 className="text-2xl font-semibold text-warm-text truncate">
                {scene.display_name}
              </h1>
              <Badge variant={isBuiltin ? 'secondary' : 'default'}>
                {t(`source.${scene.source}`)}
              </Badge>
            </div>
            <p className="text-xs text-warm-text-muted mt-1 font-mono">
              {scene.slug}
            </p>
            {scene.description && (
              <p className="text-sm text-warm-text-muted mt-2">
                {scene.description}
              </p>
            )}
            {scene.tags?.length > 0 && (
              <div className="flex flex-wrap gap-1 mt-2">
                {scene.tags.map((tag) => (
                  <Badge key={tag} variant="outline" className="text-[11px]">
                    {tag}
                  </Badge>
                ))}
              </div>
            )}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            {!isBuiltin && (
              <Button asChild variant="outline" size="sm">
                <Link to="/scenes/$id/edit" params={{ id: String(sceneId) }}>
                  <Pencil className="w-4 h-4 mr-1" aria-hidden />
                  {t('detail.edit')}
                </Link>
              </Button>
            )}
            <Button
              type="button"
              variant="default"
              size="sm"
              onClick={() => setForkOpen(true)}
            >
              <GitFork className="w-4 h-4 mr-1" aria-hidden />
              {t('detail.fork')}
            </Button>
            {!isBuiltin && (
              <Button
                type="button"
                variant="destructive"
                size="sm"
                onClick={handleDelete}
                disabled={deleteMut.isPending}
              >
                <Trash2 className="w-4 h-4 mr-1" aria-hidden />
                {t('detail.delete')}
              </Button>
            )}
          </div>
        </header>

        {isBuiltin && (
          <p className="text-xs text-warm-text-muted bg-warm-muted border border-warm-border rounded-md px-3 py-2">
            {t('detail.builtin_readonly')}
          </p>
        )}

        <Section
          title={t('detail.section_mcp')}
          isEmpty={mcpNames.length === 0}
          emptyText={t('detail.no_items')}
        >
          <ListBlock items={mcpNames} />
        </Section>

        <Section
          title={t('detail.section_plugins')}
          isEmpty={pluginNames.length === 0}
          emptyText={t('detail.no_items')}
        >
          <ListBlock items={pluginNames} />
        </Section>

        <Section
          title={t('detail.section_prompts')}
          isEmpty={prompts.length === 0}
          emptyText={t('detail.no_items')}
        >
          <ul className="space-y-3">
            {prompts.map((p) => (
              <li key={p.id} className="space-y-1">
                <div className="text-sm font-medium text-warm-text">{p.title}</div>
                <pre className="text-xs text-warm-text-muted whitespace-pre-wrap font-mono bg-warm-muted px-2 py-1 rounded">
                  {p.body}
                </pre>
              </li>
            ))}
          </ul>
        </Section>

        <Section
          title={t('detail.section_assets')}
          isEmpty={assetGroups.length === 0}
          emptyText={t('detail.no_items')}
        >
          <div className="space-y-3">
            {assetGroups.map((g) => (
              <div key={g.key} className="space-y-1">
                <div className="text-xs font-semibold text-warm-text-muted">
                  {g.key}
                </div>
                <ListBlock items={g.items} />
              </div>
            ))}
          </div>
        </Section>

        <Section
          title={t('detail.section_credentials')}
          isEmpty={credentials.length === 0}
          emptyText={t('detail.no_items')}
        >
          <ul className="text-sm text-warm-text space-y-1">
            {credentials.map((c) => (
              <li key={c.alias} className="flex flex-wrap items-center gap-2 text-xs">
                <span className="font-mono">{c.alias}</span>
                <Badge variant="outline" className="text-[11px]">
                  {c.provider}
                </Badge>
                {c.optional && (
                  <span className="text-warm-text-muted">(optional)</span>
                )}
                {c.purpose && (
                  <span className="text-warm-text-muted">— {c.purpose}</span>
                )}
              </li>
            ))}
          </ul>
        </Section>

        <Section
          title={t('detail.section_match')}
          isEmpty={rules.length === 0}
          emptyText={t('detail.no_items')}
        >
          <ul className="text-xs text-warm-text space-y-1">
            {rules.map((r, i) => (
              <li key={i} className="font-mono">
                {r.signal}{' '}
                <span className="text-warm-text-muted">
                  ({r.args ? JSON.stringify(r.args) : '{}'})
                </span>{' '}
                <Badge variant="outline" className="text-[11px]">
                  {r.weight > 0 ? `+${r.weight}` : r.weight}
                </Badge>
              </li>
            ))}
          </ul>
        </Section>
      </div>

      <SceneForkDialog
        open={forkOpen}
        onOpenChange={setForkOpen}
        scene={scene}
        onForked={(forked: Scene) => {
          toast.success(t('fork.success'));
          navigate({ to: '/scenes/$id', params: { id: String(forked.id) } });
        }}
      />
    </div>
  );
}
