import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Database, Check } from 'lucide-react';
import { sceneApi } from '@/lib/api';
import { integrationApi } from '@/lib/integration-api';
import type { Scene } from '@/types/api';
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
import {
  applyKBConfig,
  buildKBCredentialBody,
  slugify,
  validateKBForm,
  KB_PRESET_TAG,
} from './kb-mcp-config';

interface KBMcpDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/**
 * Guided, low-friction entry to wire a user's own knowledge-base MCP into a
 * scene (issue B). Picks an industry KB preset, points it at the user's endpoint,
 * and stores the API token in credstore — all in one step, no JSON hand-editing.
 */
export function KBMcpDialog({ open, onOpenChange }: KBMcpDialogProps) {
  const { t } = useTranslation('scenes');
  const qc = useQueryClient();

  // Industry KB presets = builtin scenes tagged knowledge-base.
  const { data: presets = [], isLoading } = useQuery({
    queryKey: ['scenes', 'kb-presets'],
    queryFn: () => sceneApi.list({ source: 'builtin', tag: KB_PRESET_TAG }),
    enabled: open,
  });

  const [presetId, setPresetId] = useState<number | null>(null);
  const [name, setName] = useState('');
  const [endpoint, setEndpoint] = useState('');
  const [token, setToken] = useState('');

  const selected = useMemo(
    () => presets.find((p) => p.id === presetId) ?? null,
    [presets, presetId],
  );

  const errors = validateKBForm(name, endpoint, token);
  const canSubmit = !!selected && Object.keys(errors).length === 0;

  const reset = () => {
    setPresetId(null);
    setName('');
    setEndpoint('');
    setToken('');
  };

  const configureMut = useMutation({
    mutationFn: async (): Promise<Scene> => {
      if (!selected) throw new Error('no preset selected');
      const slug = `${slugify(name)}-kb`;
      // 1. Fork the preset into the caller's owner.
      const forked = await sceneApi.fork(selected.id, slug);
      // 2. Point the KB MCP server at the user's endpoint + per-scene alias.
      const definition = applyKBConfig(forked.definition, { slug, endpoint });
      const updated = await sceneApi.update(forked.id, {
        display_name: name.trim(),
        description: forked.description,
        tags: forked.tags,
        definition,
      });
      // 3. Store the API token in credstore under the matching alias.
      await integrationApi.createCredential(buildKBCredentialBody(slug, token));
      return updated;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['scenes'] });
      qc.invalidateQueries({ queryKey: ['external-credentials'] });
      toast.success(t('kb.success'));
      reset();
      onOpenChange(false);
    },
    onError: (e: unknown) => {
      toast.error(e instanceof Error ? e.message : t('kb.error'));
    },
  });

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) reset();
        onOpenChange(o);
      }}
    >
      <DialogContent className="sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>{t('kb.title')}</DialogTitle>
          <DialogDescription>{t('kb.subtitle')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-5 py-1">
          {/* Step 1 — pick an industry preset */}
          <section className="space-y-2">
            <h3 className="text-sm font-medium text-warm-text">{t('kb.step_preset')}</h3>
            {isLoading ? (
              <p className="text-xs text-warm-text-muted py-1">{t('kb.loading')}</p>
            ) : presets.length === 0 ? (
              <p className="text-xs text-warm-text-muted py-1">{t('kb.no_presets')}</p>
            ) : (
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                {presets.map((p) => {
                  const active = p.id === presetId;
                  return (
                    <button
                      key={p.id}
                      type="button"
                      onClick={() => setPresetId(p.id)}
                      className={`flex items-start gap-2 rounded-lg border p-3 text-left transition-colors ${
                        active
                          ? 'border-brand bg-brand/5'
                          : 'border-warm-border hover:bg-accent'
                      }`}
                    >
                      <Database className="h-4 w-4 mt-0.5 shrink-0 text-warm-text-muted" aria-hidden />
                      <span className="min-w-0">
                        <span className="flex items-center gap-1.5 text-sm font-medium text-warm-text">
                          {p.display_name}
                          {active && <Check className="h-3.5 w-3.5 text-brand" aria-hidden />}
                        </span>
                        <span className="block text-xs text-warm-text-muted line-clamp-2 mt-0.5">
                          {p.description}
                        </span>
                      </span>
                    </button>
                  );
                })}
              </div>
            )}
          </section>

          {/* Step 2 — endpoint + credential */}
          <section className="space-y-3">
            <h3 className="text-sm font-medium text-warm-text">{t('kb.step_connect')}</h3>

            <div className="grid gap-1.5">
              <label htmlFor="kb-name" className="text-xs font-medium text-warm-text">
                {t('kb.field_name')}
              </label>
              <Input
                id="kb-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('kb.field_name_ph')}
                disabled={configureMut.isPending}
              />
              {name && errors.name && <p className="text-xs text-destructive">{t(errors.name)}</p>}
            </div>

            <div className="grid gap-1.5">
              <label htmlFor="kb-endpoint" className="text-xs font-medium text-warm-text">
                {t('kb.field_endpoint')}
              </label>
              <Input
                id="kb-endpoint"
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
                placeholder="https://kb.example.com/mcp"
                disabled={configureMut.isPending}
                className="font-mono text-xs"
              />
              {endpoint && errors.endpoint && (
                <p className="text-xs text-destructive">{t(errors.endpoint)}</p>
              )}
            </div>

            <div className="grid gap-1.5">
              <label htmlFor="kb-token" className="text-xs font-medium text-warm-text">
                {t('kb.field_token')}
              </label>
              <Input
                id="kb-token"
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder={t('kb.field_token_ph')}
                disabled={configureMut.isPending}
                className="font-mono text-xs"
              />
              <p className="text-xs text-warm-text-muted">{t('kb.token_hint')}</p>
            </div>
          </section>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={configureMut.isPending}
          >
            {t('kb.cancel')}
          </Button>
          <Button
            type="button"
            onClick={() => configureMut.mutate()}
            disabled={!canSubmit || configureMut.isPending}
          >
            {configureMut.isPending ? t('kb.submitting') : t('kb.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
