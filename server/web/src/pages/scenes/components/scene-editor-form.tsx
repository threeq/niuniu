import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Plus, Trash2, ExternalLink } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { OwnerPicker } from '@/components/shared/owner-picker';
import { agentFileApi } from '@/lib/team-api';
import { useAuthStore } from '@/stores/auth-store';
import type { SceneDefinition } from '@/types/api';
import type { OwnerRef } from '@/types/org';
import {
  assembleSceneDefinition,
  advancedDraftFromDefinition,
  emptyAdvancedDraft,
  type AdvancedDraft,
  type QuickActionRow,
  type SceneEditorSubmit,
} from './scene-editor-helpers';
import { SceneAdvancedEditor } from './scene-advanced-editor';

interface SceneEditorFormProps {
  mode: 'create' | 'edit';
  initial?: {
    slug: string;
    displayName: string;
    description: string;
    tags: string[];
    definition: SceneDefinition;
    owner?: OwnerRef;
  };
  submitting: boolean;
  error: string | null;
  onSubmit: (data: SceneEditorSubmit) => void;
}

function quickActionsFromDefinition(def: SceneDefinition): QuickActionRow[] {
  return (def.assets?.quick_actions ?? []).map((q) => ({ slug: q.slug, label: q.label, prompt: q.prompt }));
}

function agentNamesFromDefinition(def: SceneDefinition): string[] {
  return (def.assets?.agents ?? []).map((a) => a.name).filter(Boolean);
}

export function SceneEditorForm({ mode, initial, submitting, error, onSubmit }: SceneEditorFormProps) {
  const { t } = useTranslation('scenes');
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;

  const [slug, setSlug] = useState(initial?.slug ?? '');
  const [displayName, setDisplayName] = useState(initial?.displayName ?? '');
  const [description, setDescription] = useState(initial?.description ?? '');
  const [tagsRaw, setTagsRaw] = useState((initial?.tags ?? []).join(', '));
  const [quickActions, setQuickActions] = useState<QuickActionRow[]>(
    initial ? quickActionsFromDefinition(initial.definition) : [],
  );
  const [agentNames, setAgentNames] = useState<string[]>(
    initial ? agentNamesFromDefinition(initial.definition) : [],
  );
  const [advanced, setAdvanced] = useState<AdvancedDraft>(
    initial ? advancedDraftFromDefinition(initial.definition) : emptyAdvancedDraft(),
  );
  const [owner, setOwner] = useState<OwnerRef>(initial?.owner ?? { type: 'user', id: userId });
  const [localError, setLocalError] = useState<string | null>(null);

  useEffect(() => {
    if (mode === 'create' && owner.type === 'user' && owner.id === 0 && userId > 0) {
      setOwner({ type: 'user', id: userId });
    }
  }, [mode, owner.id, owner.type, userId]);

  // Existing system agents (managed on the Agents page) the scene can reference.
  const { data: availableAgents = [] } = useQuery({
    queryKey: ['agents'],
    queryFn: () => agentFileApi.list(),
  });

  const addQuickAction = () => setQuickActions((rows) => [...rows, { slug: '', label: '', prompt: '' }]);
  const updateQuickAction = (i: number, patch: Partial<QuickActionRow>) =>
    setQuickActions((rows) => rows.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  const removeQuickAction = (i: number) => setQuickActions((rows) => rows.filter((_, idx) => idx !== i));

  const toggleAgent = (name: string) =>
    setAgentNames((names) => (names.includes(name) ? names.filter((n) => n !== name) : [...names, name]));
  // Referenced agents that no longer exist for this owner (e.g. forked scene).
  const missingAgents = agentNames.filter((n) => !availableAgents.some((a) => a.name === n));

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setLocalError(null);

    if (!slug.trim() || !displayName.trim()) {
      setLocalError(t('new.error_required'));
      return;
    }

    let definition: SceneDefinition;
    try {
      definition = assembleSceneDefinition(advanced, quickActions, agentNames);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setLocalError(`${t('new.error_definition_parse')}: ${msg}`);
      return;
    }

    const tags = tagsRaw
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);

    onSubmit({ slug: slug.trim(), displayName: displayName.trim(), description: description.trim(), tags, definition, owner });
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-5">
      <OwnerPicker
        value={owner}
        onChange={setOwner}
        userId={userId}
        autoSelectDefault={mode === 'create'}
        disabled={submitting}
      />

      <div className="grid gap-2">
        <label htmlFor="scene-slug" className="text-sm font-medium">
          {t('new.field_slug')} <span className="text-destructive">*</span>
        </label>
        <Input
          id="scene-slug"
          value={slug}
          onChange={(e) => setSlug(e.target.value)}
          placeholder="customer-support"
          disabled={submitting || mode === 'edit'}
        />
        <p className="text-xs text-warm-text-muted">{t('new.field_slug_hint')}</p>
      </div>

      <div className="grid gap-2">
        <label htmlFor="scene-name" className="text-sm font-medium">
          {t('new.field_display_name')} <span className="text-destructive">*</span>
        </label>
        <Input id="scene-name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} disabled={submitting} />
      </div>

      <div className="grid gap-2">
        <label htmlFor="scene-desc" className="text-sm font-medium">
          {t('new.field_description')}
        </label>
        <Input id="scene-desc" value={description} onChange={(e) => setDescription(e.target.value)} disabled={submitting} />
      </div>

      <div className="grid gap-2">
        <label htmlFor="scene-tags" className="text-sm font-medium">
          {t('new.field_tags')}
        </label>
        <Input id="scene-tags" value={tagsRaw} onChange={(e) => setTagsRaw(e.target.value)} disabled={submitting} />
      </div>

      {/* Quick actions — structured editor */}
      <section className="space-y-2 rounded-lg border border-warm-border p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold text-warm-text">{t('editor.quick_actions_title')}</h2>
          <Button type="button" variant="outline" size="sm" onClick={addQuickAction} disabled={submitting}>
            <Plus className="h-4 w-4 mr-1" /> {t('editor.add_quick_action')}
          </Button>
        </div>
        <p className="text-xs text-warm-text-muted">{t('editor.quick_actions_hint')}</p>
        {quickActions.length === 0 ? (
          <p className="text-xs text-warm-text-muted py-1">{t('editor.quick_actions_empty')}</p>
        ) : (
          <div className="space-y-3">
            {quickActions.map((row, i) => (
              <div key={i} className="rounded-md border border-warm-border p-3 space-y-2">
                <div className="flex gap-2">
                  <Input
                    value={row.slug}
                    onChange={(e) => updateQuickAction(i, { slug: e.target.value })}
                    placeholder={t('editor.field_slug')}
                    disabled={submitting}
                    className="font-mono text-xs"
                  />
                  <Input
                    value={row.label}
                    onChange={(e) => updateQuickAction(i, { label: e.target.value })}
                    placeholder={t('editor.field_label')}
                    disabled={submitting}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => removeQuickAction(i)}
                    disabled={submitting}
                    className="text-destructive hover:text-destructive/80 hover:bg-destructive/10 shrink-0"
                    aria-label={t('editor.remove')}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
                <Textarea
                  value={row.prompt}
                  onChange={(e) => updateQuickAction(i, { prompt: e.target.value })}
                  placeholder={t('editor.field_prompt')}
                  disabled={submitting}
                  rows={2}
                  className="text-xs"
                />
              </div>
            ))}
          </div>
        )}
      </section>

      {/* Agents — select from existing system agents (managed on /agents). */}
      <section className="space-y-2 rounded-lg border border-warm-border p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold text-warm-text">{t('editor.agents_title')}</h2>
          <Button asChild variant="outline" size="sm">
            <Link to="/settings/agents">
              <ExternalLink className="h-4 w-4 mr-1" aria-hidden /> {t('editor.manage_agents')}
            </Link>
          </Button>
        </div>
        <p className="text-xs text-warm-text-muted">{t('editor.agents_hint')}</p>
        {availableAgents.length === 0 ? (
          <p className="text-xs text-warm-text-muted py-1">{t('editor.agents_none')}</p>
        ) : (
          <div className="space-y-1">
            {availableAgents.map((a) => (
              <label
                key={a.id}
                className="flex items-start gap-2 rounded-md border border-warm-border p-2 cursor-pointer hover:bg-accent"
              >
                <input
                  type="checkbox"
                  checked={agentNames.includes(a.name)}
                  onChange={() => toggleAgent(a.name)}
                  disabled={submitting}
                  className="mt-0.5 rounded border-border"
                />
                <div className="min-w-0">
                  <div className="text-sm font-medium text-warm-text">{a.name}</div>
                  {a.description && (
                    <div className="text-xs text-warm-text-muted truncate">{a.description}</div>
                  )}
                </div>
              </label>
            ))}
          </div>
        )}
        {missingAgents.length > 0 && (
          <p className="text-xs text-warning">
            {t('editor.agents_missing', { names: missingAgents.join(', ') })}
          </p>
        )}
      </section>

      {/* Everything else — structured advanced editor */}
      <div className="grid gap-2">
        <h2 className="text-sm font-medium">{t('editor.advanced_title')}</h2>
        <p className="text-xs text-warm-text-muted">{t('editor.advanced_hint')}</p>
        <SceneAdvancedEditor value={advanced} onChange={setAdvanced} disabled={submitting} />
      </div>

      {(localError || error) && <p className="text-xs text-destructive">{localError || error}</p>}

      <div className="flex justify-end">
        <Button type="submit" disabled={submitting || !slug.trim() || !displayName.trim()}>
          {submitting
            ? mode === 'create'
              ? t('new.submitting')
              : t('editor.saving')
            : mode === 'create'
              ? t('new.submit')
              : t('editor.save')}
        </Button>
      </div>
    </form>
  );
}
