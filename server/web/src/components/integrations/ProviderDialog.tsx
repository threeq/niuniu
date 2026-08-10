// Add / Edit dialog for the AI-Adaptive External API Proxy providers.
//
// Backend: server/internal/server/handler_external_proxy.go
// Spec:    docs/superpowers/specs/2026-05-17-external-api-proxy-redesign.md
//
// `name` is the global identifier credentials and AI use to reference a
// provider; the backend enforces uniqueness, so it is set only on
// Create and locked in Edit mode. All other fields (label, base URL,
// auth wiring, whitelist, OpenAPI URL, enabled flag) round-trip through
// the PUT endpoint.

import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Textarea } from '@/components/ui/textarea';
import { Switch } from '@/components/ui/switch';
import {
  useCreateExternalProvider,
  useUpdateExternalProvider,
  useExternalProviderSchema,
} from '@/hooks/useExternalProviders';
import type {
  ExternalProviderListItem,
  ProxyAuthType,
  UpdateProviderBody,
} from '@/types/integration';

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** When provided, dialog opens in Edit mode. Otherwise it's Create. */
  editing?: ExternalProviderListItem | null;
}

const AUTH_TYPES: { value: ProxyAuthType; labelKey: string }[] = [
  { value: 'bearer', labelKey: 'integrations.providers.authTypeBearer' },
  { value: 'basic', labelKey: 'integrations.providers.authTypeBasic' },
  {
    value: 'custom_header',
    labelKey: 'integrations.providers.authTypeCustomHeader',
  },
  { value: 'oauth', labelKey: 'integrations.providers.authTypeOAuth' },
];

const DEFAULT_WHITELIST = '[{"method":"GET","path":"*"}]';

export function ProviderDialog({ open, onOpenChange, editing }: Props) {
  const { t } = useTranslation('settings');
  const isEdit = Boolean(editing);

  // In Edit mode, fetch the full schema (label/base-url come from the
  // list but auth wiring + whitelist + profile + openapi_url live on
  // the detail endpoint).
  const schema = useExternalProviderSchema(isEdit ? editing!.name : undefined);

  const [name, setName] = useState('');
  const [label, setLabel] = useState('');
  const [apiBaseUrl, setApiBaseUrl] = useState('');
  const [authType, setAuthType] = useState<ProxyAuthType>('bearer');
  const [authHeader, setAuthHeader] = useState('Authorization');
  const [authPrefix, setAuthPrefix] = useState('Bearer');
  const [whitelist, setWhitelist] = useState(DEFAULT_WHITELIST);
  const [openapiUrl, setOpenapiUrl] = useState('');
  const [enabled, setEnabled] = useState(true);

  const create = useCreateExternalProvider();
  const update = useUpdateExternalProvider();

  // Reset / hydrate when the dialog opens or the editing target changes.
  useEffect(() => {
    if (!open) return;
    if (!editing) {
      setName('');
      setLabel('');
      setApiBaseUrl('');
      setAuthType('bearer');
      setAuthHeader('Authorization');
      setAuthPrefix('Bearer');
      setWhitelist(DEFAULT_WHITELIST);
      setOpenapiUrl('');
      setEnabled(true);
      return;
    }
    setName(editing.name);
    setLabel(editing.label ?? '');
    setApiBaseUrl(editing.api_base_url);
    setAuthType(editing.auth_type);
    setOpenapiUrl(editing.openapi_url ?? '');
    setEnabled(editing.enabled);
  }, [open, editing]);

  // Once the schema loads in Edit mode, hydrate the fields that aren't on
  // the list item.
  useEffect(() => {
    if (!isEdit || !schema.data) return;
    setAuthHeader(schema.data.auth_header || 'Authorization');
    setAuthPrefix(schema.data.auth_prefix || 'Bearer');
    setWhitelist(schema.data.whitelist || DEFAULT_WHITELIST);
  }, [isEdit, schema.data]);

  const validate = (): string | null => {
    if (!name.trim()) return t('integrations.providers.dialog.nameLabel');
    if (!apiBaseUrl.trim())
      return t('integrations.providers.dialog.apiBaseUrlLabel');
    try {
      const parsed = JSON.parse(whitelist);
      if (!Array.isArray(parsed))
        return t('integrations.providers.dialog.whitelistInvalid');
    } catch {
      return t('integrations.providers.dialog.whitelistInvalid');
    }
    return null;
  };

  const handleSave = async () => {
    const validationError = validate();
    if (validationError) {
      toast.error(validationError);
      return;
    }
    try {
      if (isEdit && editing) {
        const body: UpdateProviderBody = {
          label,
          api_base_url: apiBaseUrl,
          auth_type: authType,
          auth_header: authHeader,
          auth_prefix: authPrefix,
          whitelist,
          openapi_url: openapiUrl,
          enabled,
        };
        await update.mutateAsync({ id: editing.id, body });
      } else {
        const created = await create.mutateAsync({
          name: name.trim(),
          label,
          api_base_url: apiBaseUrl,
        });
        // For new providers, immediately PUT the remaining fields so the
        // user's choices in the dialog stick (Create only takes name +
        // label + base URL; auth wiring + whitelist + enabled flag have
        // to come through Update). label/api_base_url ride along so this
        // call is a full snapshot of the dialog, not a partial PATCH.
        await update.mutateAsync({
          id: created.id,
          body: {
            label,
            api_base_url: apiBaseUrl,
            auth_type: authType,
            auth_header: authHeader,
            auth_prefix: authPrefix,
            whitelist,
            openapi_url: openapiUrl,
            enabled,
          },
        });
      }
      toast.success(t('integrations.providers.dialog.saveSuccess'));
      onOpenChange(false);
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e);
      toast.error(
        t('integrations.providers.dialog.saveFailed', { error: msg }),
      );
    }
  };

  const isPending = create.isPending || update.isPending;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {isEdit
              ? t('integrations.providers.dialog.editTitle')
              : t('integrations.providers.dialog.addTitle')}
          </DialogTitle>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="prov-name">
              {t('integrations.providers.dialog.nameLabel')}
            </Label>
            <Input
              id="prov-name"
              value={name}
              disabled={isEdit}
              placeholder={t('integrations.providers.dialog.namePlaceholder')}
              onChange={(e) => setName(e.target.value)}
            />
            <p className="text-xs text-warm-text-muted">
              {t('integrations.providers.dialog.nameHint')}
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="prov-label">
              {t('integrations.providers.dialog.labelLabel')}
            </Label>
            <Input
              id="prov-label"
              value={label}
              placeholder={t('integrations.providers.dialog.labelPlaceholder')}
              onChange={(e) => setLabel(e.target.value)}
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="prov-url">
              {t('integrations.providers.dialog.apiBaseUrlLabel')}
            </Label>
            <Input
              id="prov-url"
              value={apiBaseUrl}
              placeholder={t(
                'integrations.providers.dialog.apiBaseUrlPlaceholder',
              )}
              onChange={(e) => setApiBaseUrl(e.target.value)}
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="prov-auth">
              {t('integrations.providers.dialog.authTypeLabel')}
            </Label>
            <select
              id="prov-auth"
              className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
              value={authType}
              onChange={(e) => setAuthType(e.target.value as ProxyAuthType)}
            >
              {AUTH_TYPES.map((a) => (
                <option key={a.value} value={a.value}>
                  {t(a.labelKey)}
                </option>
              ))}
            </select>
          </div>

          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="prov-header">
                {t('integrations.providers.dialog.authHeaderLabel')}
              </Label>
              <Input
                id="prov-header"
                value={authHeader}
                placeholder={t(
                  'integrations.providers.dialog.authHeaderPlaceholder',
                )}
                onChange={(e) => setAuthHeader(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="prov-prefix">
                {t('integrations.providers.dialog.authPrefixLabel')}
              </Label>
              <Input
                id="prov-prefix"
                value={authPrefix}
                placeholder={t(
                  'integrations.providers.dialog.authPrefixPlaceholder',
                )}
                onChange={(e) => setAuthPrefix(e.target.value)}
              />
            </div>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="prov-whitelist">
              {t('integrations.providers.dialog.whitelistLabel')}
            </Label>
            <Textarea
              id="prov-whitelist"
              rows={4}
              className="font-mono text-xs"
              value={whitelist}
              placeholder={t(
                'integrations.providers.dialog.whitelistPlaceholder',
              )}
              onChange={(e) => setWhitelist(e.target.value)}
            />
            <p className="text-xs text-warm-text-muted">
              {t('integrations.providers.dialog.whitelistHint')}
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="prov-openapi">
              {t('integrations.providers.dialog.openapiUrlLabel')}
            </Label>
            <Input
              id="prov-openapi"
              value={openapiUrl}
              placeholder={t(
                'integrations.providers.dialog.openapiUrlPlaceholder',
              )}
              onChange={(e) => setOpenapiUrl(e.target.value)}
            />
            <p className="text-xs text-warm-text-muted">
              {t('integrations.providers.dialog.openapiUrlHint')}
            </p>
          </div>

          <div className="flex items-center justify-between rounded border border-warm-border px-3 py-2">
            <Label htmlFor="prov-enabled" className="text-sm">
              {t('integrations.providers.dialog.enabledToggleLabel')}
            </Label>
            <Switch
              id="prov-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
            />
          </div>
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={isPending}
          >
            {t('integrations.cancel')}
          </Button>
          <Button onClick={handleSave} disabled={isPending}>
            {isPending ? t('integrations.saving') : t('integrations.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

