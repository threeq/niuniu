import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  DndContext,
  PointerSensor,
  useSensor,
  useSensors,
  closestCenter,
  type DragEndEvent,
} from '@dnd-kit/core';
import {
  SortableContext,
  useSortable,
  verticalListSortingStrategy,
  arrayMove,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { GripVertical, Layers, Plus, Trash2 } from 'lucide-react';
import { sceneApi, workspaceSceneApi } from '@/lib/api';
import type { SceneLayer, Scene, ApplyResult } from '@/types/api';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { WorkspaceSceneAttachDialog } from './workspace-scene-attach-dialog';
import { WorkspaceMCPPanel } from './workspace-mcp-panel';
import { WorkspacePencilActions } from './workspace-pencil-actions';

/** Slug of the pencil-design (OpenPencil) scene; gates its action card. */
const PENCIL_DESIGN_SLUG = 'pencil-design';

interface WorkspaceScenesPanelProps {
  workspaceId: number;
}

interface LayerRowProps {
  layer: SceneLayer;
  scene?: Scene;
  onDetach: (layer: SceneLayer) => void;
  isDetaching: boolean;
}

function LayerRow({ layer, scene, onDetach, isDetaching }: LayerRowProps) {
  const { t } = useTranslation('scenes');
  const isBase = layer.is_base;
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: layer.id, disabled: isBase });

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.6 : 1,
  };

  return (
    <li
      ref={setNodeRef}
      style={style}
      className="flex items-center gap-2 border border-warm-border rounded-md p-2 bg-warm-surface"
      data-testid="scene-layer-row"
    >
      <button
        type="button"
        aria-label={t('workspace_panel.drag_to_reorder')}
        title={t('workspace_panel.drag_to_reorder')}
        disabled={isBase}
        className="p-0.5 text-warm-text-muted hover:text-warm-text disabled:opacity-30 disabled:cursor-not-allowed cursor-grab active:cursor-grabbing"
        {...attributes}
        {...listeners}
      >
        <GripVertical className="w-4 h-4" aria-hidden />
      </button>
      <Layers className="w-4 h-4 text-warm-text-muted shrink-0" aria-hidden />
      <div className="flex-1 min-w-0">
        <div className="text-sm text-warm-text truncate">
          {isBase
            ? t('workspace_panel.base_layer')
            : scene?.display_name ?? `#${layer.scene_id}`}
        </div>
        {!isBase && scene && (
          <div className="text-xs text-warm-text-muted truncate font-mono">
            {scene.slug}
          </div>
        )}
      </div>
      {!isBase && (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-7 px-2"
          onClick={() => onDetach(layer)}
          disabled={isDetaching}
          aria-label={t('workspace_panel.detach')}
        >
          <Trash2 className="w-3.5 h-3.5" aria-hidden />
        </Button>
      )}
    </li>
  );
}

function provenanceLabel(
  sceneId: number | null | undefined,
  scenes: Map<number, Scene>,
  t: ReturnType<typeof useTranslation>['t'],
): string {
  if (!sceneId) return t('workspace_panel.provenance_base');
  const s = scenes.get(sceneId);
  return t('workspace_panel.provenance_from', { name: s?.display_name ?? `#${sceneId}` });
}

interface ProjectionSummaryProps {
  result: ApplyResult | null;
  layers: SceneLayer[];
  sceneById: Map<number, Scene>;
}

function ProjectionSummary({ result, layers, sceneById }: ProjectionSummaryProps) {
  const { t } = useTranslation('scenes');
  // Build a layerId -> sceneId map so we can resolve provenance ids (the
  // backend tags by layer id) back to scene labels. MUST come before the
  // early-return so React's hook order is stable across renders.
  const layerToScene = useMemo(() => {
    const m = new Map<number, number | null>();
    for (const l of layers) m.set(l.id, l.scene_id);
    return m;
  }, [layers]);

  if (!result) {
    return (
      <p className="text-xs text-warm-text-muted">
        {t('workspace_panel.loading')}
      </p>
    );
  }
  // Backend Go nil slices serialize to JSON `null` rather than `[]`, so
  // every array field here needs a defensive fallback before any `.length`
  // / `.map` call. Without this, a workspace with no attached scenes
  // explodes inside the projection summary with
  // "Cannot read properties of null (reading 'length')".
  const proj = result.projection;
  const mcp = proj.mcp ?? [];
  const plugins = proj.plugins ?? [];
  const prompts = proj.prompts ?? [];

  const provenanceFor = (key: string): string[] => {
    const layerIds = proj.provenance?.[key] ?? [];
    return layerIds.map((lid) => {
      const sid = layerToScene.get(lid) ?? null;
      return provenanceLabel(sid, sceneById, t);
    });
  };

  function ProvBadge({ labels }: { labels: string[] }) {
    if (labels.length === 0) return null;
    const text = labels[0];
    const more = labels.length > 1 ? `+${labels.length - 1}` : '';
    return (
      <Badge variant="outline" className="text-[10px] ml-1">
        {text}
        {more && <span className="ml-1 text-warm-text-muted">{more}</span>}
      </Badge>
    );
  }

  return (
    <div className="space-y-3">
      <section>
        <h4 className="text-xs font-semibold text-warm-text-muted uppercase tracking-wide">
          {t('workspace_panel.projection_mcp')}
        </h4>
        {mcp.length === 0 ? (
          <p className="text-xs text-warm-text-muted mt-1">
            {t('workspace_panel.projection_empty_mcp')}
          </p>
        ) : (
          <ul className="mt-1 space-y-1">
            {mcp.map((name) => (
              <li
                key={name}
                className="flex items-center text-xs text-warm-text font-mono"
              >
                <span className="truncate">{name}</span>
                <ProvBadge labels={provenanceFor(`mcp:${name}`)} />
              </li>
            ))}
          </ul>
        )}
      </section>

      <section>
        <h4 className="text-xs font-semibold text-warm-text-muted uppercase tracking-wide">
          {t('workspace_panel.projection_plugins')}
        </h4>
        {plugins.length === 0 ? (
          <p className="text-xs text-warm-text-muted mt-1">
            {t('workspace_panel.projection_empty_plugins')}
          </p>
        ) : (
          <ul className="mt-1 space-y-1">
            {plugins.map((p) => (
              <li
                key={p.source}
                className="flex items-center text-xs text-warm-text font-mono"
              >
                <span className="truncate">
                  {p.source}
                  {p.ref ? `@${p.ref}` : ''}
                </span>
                <ProvBadge labels={provenanceFor(`plugin:${p.source}`)} />
              </li>
            ))}
          </ul>
        )}
      </section>

      <section>
        <h4 className="text-xs font-semibold text-warm-text-muted uppercase tracking-wide">
          {t('workspace_panel.projection_prompts')}
        </h4>
        {prompts.length === 0 ? (
          <p className="text-xs text-warm-text-muted mt-1">
            {t('workspace_panel.projection_empty_prompts')}
          </p>
        ) : (
          <ul className="mt-1 space-y-1">
            {prompts.map((p) => (
              <li
                key={p.id}
                className="flex items-center text-xs text-warm-text"
              >
                <span className="truncate">{p.title}</span>
                <ProvBadge labels={provenanceFor(`prompt:${p.id}`)} />
              </li>
            ))}
          </ul>
        )}
      </section>

      <section>
        <h4 className="text-xs font-semibold text-warm-text-muted uppercase tracking-wide">
          {t('workspace_panel.projection_assets')}
        </h4>
        {(() => {
          const groups: { key: string; items: string[] }[] = [];
          const a = proj.assets ?? {};
          if (a.env_presets?.length)
            groups.push({ key: 'env_presets', items: a.env_presets.map((x) => x.slug) });
          if (a.project_templates?.length)
            groups.push({ key: 'project_templates', items: a.project_templates.map((x) => x.slug) });
          if (a.quick_actions?.length)
            groups.push({ key: 'quick_actions', items: a.quick_actions.map((x) => x.slug) });
          if (a.harness_specs?.length)
            groups.push({ key: 'harness_specs', items: a.harness_specs.map((x) => x.slug) });
          if (a.agents?.length)
            groups.push({ key: 'agents', items: a.agents.map((x) => x.name) });
          if (groups.length === 0) {
            return (
              <p className="text-xs text-warm-text-muted mt-1">
                {t('workspace_panel.projection_empty_assets')}
              </p>
            );
          }
          return (
            <div className="mt-1 space-y-2">
              {groups.map((g) => (
                <div key={g.key}>
                  <div className="text-[11px] text-warm-text-muted font-mono">
                    {g.key}
                  </div>
                  <ul className="space-y-0.5">
                    {g.items.map((s) => (
                      <li key={s} className="text-xs text-warm-text font-mono truncate">
                        {s}
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
            </div>
          );
        })()}
      </section>
    </div>
  );
}

export function WorkspaceScenesPanel({ workspaceId }: WorkspaceScenesPanelProps) {
  const { t } = useTranslation('scenes');
  const qc = useQueryClient();
  const [attachOpen, setAttachOpen] = useState(false);

  const { data: layers = [] } = useQuery({
    queryKey: ['workspace-scene-layers', workspaceId],
    queryFn: () => workspaceSceneApi.listLayers(workspaceId),
  });

  const { data: projection } = useQuery({
    queryKey: ['workspace-projection', workspaceId],
    queryFn: () => workspaceSceneApi.getProjection(workspaceId),
  });

  // Fetch each referenced scene to get display_name / slug.
  const sceneQueries = useQueries({
    queries: layers
      .filter((l) => !l.is_base && l.scene_id != null)
      .map((l) => ({
        queryKey: ['scene', l.scene_id],
        queryFn: () => sceneApi.get(l.scene_id as number),
        staleTime: 60_000,
      })),
  });

  const sceneById = useMemo(() => {
    const m = new Map<number, Scene>();
    for (const q of sceneQueries) {
      if (q.data) m.set(q.data.id, q.data);
    }
    return m;
  }, [sceneQueries]);

  const sensors = useSensors(useSensor(PointerSensor));

  const moveMut = useMutation({
    mutationFn: ({ layerId, position }: { layerId: number; position: number }) =>
      workspaceSceneApi.move(workspaceId, layerId, position),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspace-scene-layers', workspaceId] });
      qc.invalidateQueries({ queryKey: ['workspace-projection', workspaceId] });
    },
  });

  const detachMut = useMutation({
    mutationFn: (layerId: number) => workspaceSceneApi.detach(workspaceId, layerId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspace-scene-layers', workspaceId] });
      qc.invalidateQueries({ queryKey: ['workspace-projection', workspaceId] });
      qc.invalidateQueries({
        queryKey: ['workspace-scene-recommendations', workspaceId],
      });
    },
  });

  function handleDragEnd(event: DragEndEvent) {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const oldIdx = layers.findIndex((l) => l.id === active.id);
    const newIdx = layers.findIndex((l) => l.id === over.id);
    if (oldIdx < 0 || newIdx < 0) return;
    // Base layer at position 0 is pinned; refuse moves that would dislodge it.
    if (layers[oldIdx].is_base || layers[newIdx].is_base) return;
    const reordered = arrayMove(layers, oldIdx, newIdx);
    const moved = reordered[newIdx];
    moveMut.mutate({ layerId: moved.id, position: newIdx });
  }

  const attachedSceneIds = useMemo(
    () => layers.filter((l) => l.scene_id != null).map((l) => l.scene_id as number),
    [layers],
  );

  const nonBaseLayers = layers.filter((l) => !l.is_base);
  const baseLayer = layers.find((l) => l.is_base);

  // The pencil-design action card (open OpenPencil + one-click send) shows only
  // when that scene is one of the attached layers.
  const pencilAttached = useMemo(
    () =>
      layers.some(
        (l) => l.scene_id != null && sceneById.get(l.scene_id)?.slug === PENCIL_DESIGN_SLUG,
      ),
    [layers, sceneById],
  );

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-semibold text-warm-text">
          {t('workspace_panel.title')}
        </h3>
        <Button
          type="button"
          size="sm"
          variant="default"
          onClick={() => setAttachOpen(true)}
        >
          <Plus className="w-4 h-4 mr-1" aria-hidden />
          {t('workspace_panel.attach')}
        </Button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          {baseLayer && (
            <LayerRow
              layer={baseLayer}
              onDetach={() => {}}
              isDetaching={false}
            />
          )}
          {nonBaseLayers.length === 0 ? (
            <p className="text-xs text-warm-text-muted py-3 text-center">
              {t('workspace_panel.empty')}
            </p>
          ) : (
            <DndContext
              sensors={sensors}
              collisionDetection={closestCenter}
              onDragEnd={handleDragEnd}
            >
              <SortableContext
                items={nonBaseLayers.map((l) => l.id)}
                strategy={verticalListSortingStrategy}
              >
                <ul className="space-y-2 mt-2">
                  {nonBaseLayers.map((l) => (
                    <LayerRow
                      key={l.id}
                      layer={l}
                      scene={l.scene_id != null ? sceneById.get(l.scene_id) : undefined}
                      onDetach={(layer) => detachMut.mutate(layer.id)}
                      isDetaching={
                        detachMut.isPending && detachMut.variables === l.id
                      }
                    />
                  ))}
                </ul>
              </SortableContext>
            </DndContext>
          )}
        </div>

        <div className="border border-warm-border rounded-md p-3 bg-warm-surface">
          <h4 className="text-sm font-medium text-warm-text mb-2">
            {t('workspace_panel.projection_title')}
          </h4>
          <ProjectionSummary
            result={projection ?? null}
            layers={layers}
            sceneById={sceneById}
          />
        </div>
      </div>

      {pencilAttached && <WorkspacePencilActions workspaceId={workspaceId} />}

      <div className="border-t border-warm-border pt-4">
        <WorkspaceMCPPanel workspaceId={workspaceId} />
      </div>

      <WorkspaceSceneAttachDialog
        workspaceId={workspaceId}
        open={attachOpen}
        onOpenChange={setAttachOpen}
        attachedSceneIds={attachedSceneIds}
      />
    </div>
  );
}
