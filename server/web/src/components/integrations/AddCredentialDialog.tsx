import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Github, Building2, Eye, EyeOff, Info, Plug } from 'lucide-react';
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
import { OwnerPicker } from '@/components/shared/owner-picker';
import { useAuthStore } from '@/stores/auth-store';
import type { OwnerRef } from '@/types/org';
import {
  useCreateCredential,
  usePatchCredential,
  useVerifyImapCredential,
} from '@/hooks/useExternalCredentials';
import { useExternalProviders } from '@/hooks/useExternalProviders';
import type {
  ExternalCredential,
  ExternalProviderListItem,
  ProxyAuthMode,
} from '@/types/integration';

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** When provided, dialog opens in Edit mode: alias is pre-filled,
   *  provider is locked, secret fields stay empty (the encrypted
   *  config is never returned to the SPA — leaving the secret fields
   *  blank keeps the current value; filling them rewrites the config). */
  editing?: ExternalCredential | null;
}

// Dialog driven entirely by provider.auth_modes: one mode renders the
// matching field set directly; multiple modes render a mode toggle. The
// resulting credential config carries the chosen mode explicitly under
// `auth_mode` so buildAuthHeader on the backend doesn't have to guess.
//
// Provider-specific niceties (help URLs, special copy) are surfaced via
// optional i18n keys keyed by (provider name, mode) — see helpUrlKeyFor /
// helpLabelKeyFor below. Falling back to a generic empty link is fine.
export function AddCredentialDialog({
  open,
  onOpenChange,
  editing,
}: Props) {
  const { t } = useTranslation('settings');
  const isEdit = Boolean(editing);
  const currentUser = useAuthStore((s) => s.user);
  const userId = currentUser?.id ?? 0;
  // Owner the new credential is created under — personal by default; org
  // selection (when the user belongs to orgs) creates a team-shared credential.
  // Only meaningful in Create mode; owner is immutable on existing credentials.
  const [owner, setOwner] = useState<OwnerRef>({ type: 'user', id: userId });
  const providers = useExternalProviders();
  const allProviders = providers.data ?? [];
  const enabledProviders = useMemo(
    () => allProviders.filter((p) => p.enabled),
    [allProviders],
  );

  const [providerName, setProviderName] = useState<string>('');
  // Hydrate provider selection from the editing target (locked) or — for
  // create — pick a sensible default once the provider list loads.
  useEffect(() => {
    if (!open) return;
    if (editing) {
      setProviderName(editing.provider);
      return;
    }
    if (providerName) return;
    if (enabledProviders.length === 0) return;
    const preferred =
      enabledProviders.find((p) => p.name === 'github') ?? enabledProviders[0];
    setProviderName(preferred.name);
  }, [open, providerName, enabledProviders, editing]);

  // In Edit mode the credential's provider may have been disabled since
  // — fall back to the full provider list so the dialog still renders
  // the right form for an existing credential.
  const selectedProvider: ExternalProviderListItem | undefined = useMemo(() => {
    const fromEnabled = enabledProviders.find((p) => p.name === providerName);
    if (fromEnabled) return fromEnabled;
    if (isEdit) return allProviders.find((p) => p.name === providerName);
    return undefined;
  }, [enabledProviders, allProviders, providerName, isEdit]);

  // Mode state — defaults to the provider's first listed mode whenever
  // the provider changes.
  const [authMode, setAuthMode] = useState<ProxyAuthMode>('bearer');
  useEffect(() => {
    if (!selectedProvider) return;
    const modes = selectedProvider.auth_modes ?? [selectedProvider.auth_type];
    if (modes.length === 0) return;
    if (modes.includes(authMode)) return;
    setAuthMode(modes[0] as ProxyAuthMode);
  }, [selectedProvider, authMode]);

  const [alias, setAlias] = useState('');
  const [token, setToken] = useState('');
  const [basicUser, setBasicUser] = useState('');
  const [basicPassword, setBasicPassword] = useState('');
  // imap-mode fields (office-mail scene). Collected here, encrypted, and later
  // injected into the scene MCP env via ${cred:mailbox.<field>} placeholders.
  const [imapHost, setImapHost] = useState('');
  const [imapPort, setImapPort] = useState('993');
  const [imapUser, setImapUser] = useState('');
  const [imapPassword, setImapPassword] = useState('');
  const [imapSecurity, setImapSecurity] = useState('ssl');
  // Optional SMTP override (sending). Left blank → derived imap.->smtp. host.
  const [smtpHost, setSmtpHost] = useState('');
  const [smtpPort, setSmtpPort] = useState('');
  const [smtpSecurity, setSmtpSecurity] = useState('ssl');
  const [showSecret, setShowSecret] = useState(false);
  const create = useCreateCredential();
  const patch = usePatchCredential();
  const verifyImap = useVerifyImapCredential();
  const mutating = create.isPending || patch.isPending || verifyImap.isPending;

  const isImap = authMode === 'imap';

  const resetFields = () => {
    setAlias('');
    setToken('');
    setBasicUser('');
    setBasicPassword('');
    setImapHost('');
    setImapPort('993');
    setImapUser('');
    setImapPassword('');
    setImapSecurity('ssl');
    setSmtpHost('');
    setSmtpPort('');
    setSmtpSecurity('ssl');
    setShowSecret(false);
  };

  // Hydrate the alias from the editing target whenever the dialog opens.
  // Secret fields stay empty by design (see Props.editing doc).
  useEffect(() => {
    if (!open) return;
    if (editing) {
      setAlias(editing.alias);
      setToken('');
      setBasicUser('');
      setBasicPassword('');
    }
  }, [open, editing]);

  const handleProviderChange = (next: string) => {
    setProviderName(next);
    resetFields();
  };

  const handleModeChange = (next: ProxyAuthMode) => {
    setAuthMode(next);
    // Clear cross-mode fields to avoid stale state leaking into config.
    setToken('');
    setBasicUser('');
    setBasicPassword('');
    setImapHost('');
    setImapUser('');
    setImapPassword('');
  };

  // In Edit mode we accept an alias-only change (no fresh secret); in
  // Create mode the secret fields are required.
  const hasFreshSecret = useMemo(() => {
    switch (authMode) {
      case 'imap':
        return (
          imapHost.length > 0 ||
          imapUser.length > 0 ||
          imapPassword.length > 0
        );
      case 'basic':
        return basicUser.length > 0 || basicPassword.length > 0;
      case 'bearer':
      case 'custom_header':
      default:
        return token.length > 0;
    }
  }, [authMode, token, basicUser, basicPassword, imapHost, imapUser, imapPassword]);

  const canSave = useMemo(() => {
    if (!selectedProvider || alias.trim().length === 0) return false;
    const imapComplete =
      imapHost.trim().length > 0 &&
      imapPort.trim().length > 0 &&
      imapUser.trim().length > 0 &&
      imapPassword.length > 0;
    if (isEdit) {
      // Edit: accept alias-only change OR a complete new secret.
      const aliasChanged = editing ? alias.trim() !== editing.alias : false;
      if (!aliasChanged && !hasFreshSecret) return false;
      if (hasFreshSecret) {
        if (authMode === 'imap') return imapComplete;
        if (authMode === 'basic')
          return basicUser.length > 0 && basicPassword.length > 0;
        return token.length > 0;
      }
      return true;
    }
    // Create: require a full secret.
    switch (authMode) {
      case 'imap':
        return imapComplete;
      case 'basic':
        return basicUser.length > 0 && basicPassword.length > 0;
      case 'bearer':
      case 'custom_header':
      default:
        return token.length > 0;
    }
  }, [
    selectedProvider,
    alias,
    authMode,
    token,
    basicUser,
    basicPassword,
    imapHost,
    imapPort,
    imapUser,
    imapPassword,
    isEdit,
    editing,
    hasFreshSecret,
  ]);

  const buildConfig = (): Record<string, unknown> => {
    switch (authMode) {
      case 'imap': {
        // Field names match what the office-mail projector reads from the
        // decrypted credential (imap_host / imap_port / username / password /
        // security, + optional smtp_* for sending).
        const cfg: Record<string, unknown> = {
          auth_mode: 'imap',
          imap_host: imapHost.trim(),
          imap_port: imapPort.trim(),
          username: imapUser.trim(),
          password: imapPassword,
          security: imapSecurity,
        };
        if (smtpHost.trim()) {
          cfg.smtp_host = smtpHost.trim();
          if (smtpPort.trim()) cfg.smtp_port = smtpPort.trim();
          cfg.smtp_security = smtpSecurity;
        }
        return cfg;
      }
      case 'basic':
        return {
          auth_mode: 'basic',
          user: basicUser,
          password: basicPassword,
        };
      case 'custom_header':
        return { auth_mode: 'custom_header', token };
      case 'query_param':
        // Single api_key the proxy injects into the URL query (e.g. SerpAPI).
        // Stored under `api_key` so credToken picks it up on the backend.
        return { auth_mode: 'query_param', api_key: token };
      case 'bearer':
      default:
        return { auth_mode: 'bearer', token };
    }
  };

  const handleSave = async () => {
    if (!selectedProvider) return;
    try {
      if (isEdit && editing) {
        const body: { alias?: string; config?: Record<string, unknown> } = {};
        const trimmedAlias = alias.trim();
        if (trimmedAlias && trimmedAlias !== editing.alias) {
          body.alias = trimmedAlias;
        }
        if (hasFreshSecret) {
          body.config = buildConfig();
        }
        if (!body.alias && !body.config) {
          // Nothing to do — close the dialog without a network call.
          resetFields();
          onOpenChange(false);
          return;
        }
        await patch.mutateAsync({ id: editing.id, body });
        toast.success(t('integrations.credentials.editSaved'));
      } else {
        await create.mutateAsync({
          alias: alias.trim(),
          provider: selectedProvider.name,
          config: buildConfig(),
          owner,
        });
        toast.success(t('integrations.savedDeferredVerify'));
      }
      resetFields();
      onOpenChange(false);
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : 'unknown';
      toast.error(t('integrations.saveFailed', { error: msg }));
    }
  };

  // Save (creating or updating with a fresh secret), then run the bind-time
  // IMAP login probe so the user gets immediate "works / wrong password"
  // feedback. On the create path the credential is persisted regardless of the
  // probe result (deferred-verify), so the dialog closes either way.
  const handleTest = async () => {
    if (!selectedProvider) return;
    try {
      let id: number | undefined = isEdit ? editing?.id : undefined;
      if (!id) {
        const created = await create.mutateAsync({
          alias: alias.trim(),
          provider: selectedProvider.name,
          config: buildConfig(),
          owner,
        });
        id = created.id;
      } else if (hasFreshSecret) {
        await patch.mutateAsync({ id, body: { config: buildConfig() } });
      }
      if (!id) return;
      await verifyImap.mutateAsync(id);
      toast.success(t('integrations.credentials.imap.testOk'));
      if (!isEdit) {
        resetFields();
        onOpenChange(false);
      }
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : 'unknown';
      toast.error(t('integrations.credentials.imap.testFailed', { error: msg }));
      // On the create path the credential was still saved — close so the user
      // can fix it from the list rather than risk a duplicate-alias re-create.
      if (!isEdit && create.isSuccess) {
        resetFields();
        onOpenChange(false);
      }
    }
  };

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      resetFields();
      setOwner({ type: 'user', id: userId });
    }
    onOpenChange(next);
  };

  const TitleIcon = pickIcon(selectedProvider?.name);
  const providerLabel =
    selectedProvider?.label || selectedProvider?.name || '';
  const titleLabel = isEdit
    ? t('integrations.credentials.editTitle', {
        provider: providerLabel,
        alias: editing?.alias ?? '',
      })
    : providerLabel || t('integrations.addCredential');
  const modes: ProxyAuthMode[] = (selectedProvider?.auth_modes as ProxyAuthMode[]) ?? [];

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <TitleIcon className="size-4" /> {titleLabel}
          </DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div
            role="note"
            className="flex items-start gap-2 rounded border border-info/40 bg-info/10 px-3 py-2 text-xs text-warm-text"
          >
            <Info aria-hidden className="size-4 text-info shrink-0 mt-0.5" />
            <span>{t('integrations.deferredVerifyNotice')}</span>
          </div>

          {!isEdit && (
            <div className="flex flex-col gap-2">
              <Label>{t('integrations.ownerLabel')}</Label>
              <OwnerPicker value={owner} onChange={setOwner} userId={userId} />
              <p className="text-xs text-warm-text-muted">
                {t('integrations.ownerHint')}
              </p>
            </div>
          )}

          <div className="flex flex-col gap-2">
            <Label htmlFor="provider">{t('integrations.providerLabel')}</Label>
            <select
              id="provider"
              value={providerName}
              onChange={(e) => handleProviderChange(e.target.value)}
              disabled={isEdit || enabledProviders.length === 0}
              className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
            >
              {enabledProviders.length === 0 && !isEdit && (
                <option value="">
                  {t('integrations.credentials.noEnabledProvider')}
                </option>
              )}
              {isEdit && selectedProvider && (
                <option key={selectedProvider.id} value={selectedProvider.name}>
                  {providerLabel}
                  {selectedProvider.created_by !== 'system'
                    ? ` · ${selectedProvider.name}`
                    : ''}
                </option>
              )}
              {!isEdit &&
                enabledProviders.map((p) => (
                  <option key={p.id} value={p.name}>
                    {p.label || p.name}
                    {p.created_by !== 'system' ? ` · ${p.name}` : ''}
                  </option>
                ))}
            </select>
            {enabledProviders.length === 0 && !isEdit && (
              <p className="text-xs text-warm-text-muted">
                {t('integrations.credentials.noEnabledProviderHint')}
              </p>
            )}
            {isEdit && (
              <p className="text-xs text-warm-text-muted">
                {t('integrations.credentials.editProviderLocked')}
              </p>
            )}
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="cred-alias">{t('integrations.aliasLabel')}</Label>
            <Input
              id="cred-alias"
              type="text"
              placeholder={t('integrations.aliasPlaceholder')}
              value={alias}
              onChange={(e) => setAlias(e.target.value)}
            />
          </div>

          {modes.length > 1 && (
            <div className="flex flex-col gap-2">
              <Label>{t('integrations.credentials.authModeLabel')}</Label>
              <div className="flex flex-wrap gap-2">
                {modes.map((m) => (
                  <Button
                    key={m}
                    type="button"
                    variant={authMode === m ? 'default' : 'outline'}
                    size="sm"
                    onClick={() => handleModeChange(m)}
                  >
                    {t(authModeLabelKey(m))}
                  </Button>
                ))}
              </div>
            </div>
          )}

          {isEdit && (
            <p className="text-xs text-warm-text-muted">
              {t('integrations.credentials.editKeepCurrent')}
            </p>
          )}

          {selectedProvider && isImap && (
            <ImapFields
              host={imapHost}
              setHost={setImapHost}
              port={imapPort}
              setPort={setImapPort}
              user={imapUser}
              setUser={setImapUser}
              password={imapPassword}
              setPassword={setImapPassword}
              security={imapSecurity}
              setSecurity={setImapSecurity}
              smtpHost={smtpHost}
              setSmtpHost={setSmtpHost}
              smtpPort={smtpPort}
              setSmtpPort={setSmtpPort}
              smtpSecurity={smtpSecurity}
              setSmtpSecurity={setSmtpSecurity}
              showSecret={showSecret}
              setShowSecret={setShowSecret}
            />
          )}

          {selectedProvider && !isImap && (
            <AuthFields
              providerName={selectedProvider.name}
              mode={authMode}
              token={token}
              setToken={setToken}
              user={basicUser}
              setUser={setBasicUser}
              password={basicPassword}
              setPassword={setBasicPassword}
              showSecret={showSecret}
              setShowSecret={setShowSecret}
            />
          )}
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => handleOpenChange(false)}
            disabled={mutating}
          >
            {t('integrations.cancel')}
          </Button>
          {isImap && (
            <Button
              variant="outline"
              onClick={handleTest}
              disabled={mutating || (!isEdit && !canSave)}
            >
              {verifyImap.isPending
                ? t('integrations.credentials.imap.testing')
                : t('integrations.credentials.imap.testButton')}
            </Button>
          )}
          <Button onClick={handleSave} disabled={!canSave || mutating}>
            {mutating ? t('integrations.saving') : t('integrations.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function pickIcon(name?: string) {
  if (name === 'github') return Github;
  if (name === 'tapd' || name === 'jira') return Building2;
  return Plug;
}

function authModeLabelKey(mode: ProxyAuthMode): string {
  switch (mode) {
    case 'basic':
      return 'integrations.credentials.authModeBasic';
    case 'custom_header':
      return 'integrations.credentials.authModeCustomHeader';
    case 'query_param':
      return 'integrations.credentials.authModeQueryParam';
    case 'bearer':
    default:
      return 'integrations.credentials.authModeBearer';
  }
}

interface AuthFieldsProps {
  providerName: string;
  mode: ProxyAuthMode;
  token: string;
  setToken: (v: string) => void;
  user: string;
  setUser: (v: string) => void;
  password: string;
  setPassword: (v: string) => void;
  showSecret: boolean;
  setShowSecret: (next: boolean | ((prev: boolean) => boolean)) => void;
}

function AuthFields({
  providerName,
  mode,
  token,
  setToken,
  user,
  setUser,
  password,
  setPassword,
  showSecret,
  setShowSecret,
}: AuthFieldsProps) {
  const { t } = useTranslation('settings');
  const help = providerHelp(providerName, mode);

  if (mode === 'basic') {
    return (
      <>
        <Label htmlFor="auth-user">
          {t(perProviderKey(providerName, mode, 'userLabel'), {
            defaultValue: t('integrations.credentials.userLabel'),
          })}
        </Label>
        <Input
          id="auth-user"
          type="text"
          placeholder={t(perProviderKey(providerName, mode, 'userPlaceholder'), {
            defaultValue: t('integrations.credentials.userPlaceholder'),
          })}
          value={user}
          onChange={(e) => setUser(e.target.value)}
        />
        <Label htmlFor="auth-pass">
          {t(perProviderKey(providerName, mode, 'passwordLabel'), {
            defaultValue: t('integrations.credentials.passwordLabel'),
          })}
        </Label>
        <SecretInput
          id="auth-pass"
          placeholder={t(
            perProviderKey(providerName, mode, 'passwordPlaceholder'),
            { defaultValue: t('integrations.credentials.passwordPlaceholder') },
          )}
          value={password}
          onChange={setPassword}
          showSecret={showSecret}
          setShowSecret={setShowSecret}
        />
        {help && <HelpLink href={help.url} label={help.label} />}
      </>
    );
  }
  // bearer + custom_header — single token field.
  return (
    <>
      <Label htmlFor="auth-token">
        {t(perProviderKey(providerName, mode, 'tokenLabel'), {
          defaultValue: t('integrations.credentials.tokenLabel'),
        })}
      </Label>
      <SecretInput
        id="auth-token"
        placeholder={t(perProviderKey(providerName, mode, 'tokenPlaceholder'), {
          defaultValue: t('integrations.credentials.tokenPlaceholder'),
        })}
        value={token}
        onChange={setToken}
        showSecret={showSecret}
        setShowSecret={setShowSecret}
      />
      {help && <HelpLink href={help.url} label={help.label} />}
    </>
  );
}

interface ImapFieldsProps {
  host: string;
  setHost: (v: string) => void;
  port: string;
  setPort: (v: string) => void;
  user: string;
  setUser: (v: string) => void;
  password: string;
  setPassword: (v: string) => void;
  security: string;
  setSecurity: (v: string) => void;
  smtpHost: string;
  setSmtpHost: (v: string) => void;
  smtpPort: string;
  setSmtpPort: (v: string) => void;
  smtpSecurity: string;
  setSmtpSecurity: (v: string) => void;
  showSecret: boolean;
  setShowSecret: (next: boolean | ((prev: boolean) => boolean)) => void;
}

// ImapFields collects the mailbox connection details for the office-mail scene.
// IMAP fields are required; the SMTP block is optional (sending) — left blank,
// the projector derives smtp host from the imap host (imap.->smtp.).
function ImapFields({
  host,
  setHost,
  port,
  setPort,
  user,
  setUser,
  password,
  setPassword,
  security,
  setSecurity,
  smtpHost,
  setSmtpHost,
  smtpPort,
  setSmtpPort,
  smtpSecurity,
  setSmtpSecurity,
  showSecret,
  setShowSecret,
}: ImapFieldsProps) {
  const { t } = useTranslation('settings');
  return (
    <>
      <Label htmlFor="imap-host">
        {t('integrations.credentials.imap.hostLabel')}
      </Label>
      <Input
        id="imap-host"
        type="text"
        placeholder={t('integrations.credentials.imap.hostPlaceholder')}
        value={host}
        onChange={(e) => setHost(e.target.value)}
      />
      <Label htmlFor="imap-port">
        {t('integrations.credentials.imap.portLabel')}
      </Label>
      <Input
        id="imap-port"
        type="text"
        inputMode="numeric"
        placeholder="993"
        value={port}
        onChange={(e) => setPort(e.target.value)}
      />
      <Label htmlFor="imap-user">
        {t('integrations.credentials.imap.userLabel')}
      </Label>
      <Input
        id="imap-user"
        type="text"
        placeholder={t('integrations.credentials.imap.userPlaceholder')}
        value={user}
        onChange={(e) => setUser(e.target.value)}
      />
      <Label htmlFor="imap-pass">
        {t('integrations.credentials.imap.passwordLabel')}
      </Label>
      <SecretInput
        id="imap-pass"
        placeholder={t('integrations.credentials.imap.passwordPlaceholder')}
        value={password}
        onChange={setPassword}
        showSecret={showSecret}
        setShowSecret={setShowSecret}
      />
      <Label htmlFor="imap-security">
        {t('integrations.credentials.imap.securityLabel')}
      </Label>
      <select
        id="imap-security"
        value={security}
        onChange={(e) => setSecurity(e.target.value)}
        className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
      >
        <option value="ssl">
          {t('integrations.credentials.imap.securitySsl')}
        </option>
        <option value="starttls">
          {t('integrations.credentials.imap.securityStarttls')}
        </option>
        <option value="none">
          {t('integrations.credentials.imap.securityNone')}
        </option>
      </select>

      <p className="text-xs text-warm-text-muted mt-1">
        {t('integrations.credentials.imap.smtpHint')}
      </p>
      <Label htmlFor="smtp-host">
        {t('integrations.credentials.imap.smtpHostLabel')}
      </Label>
      <Input
        id="smtp-host"
        type="text"
        placeholder={t('integrations.credentials.imap.smtpHostPlaceholder')}
        value={smtpHost}
        onChange={(e) => setSmtpHost(e.target.value)}
      />
      {smtpHost.trim() && (
        <>
          <Label htmlFor="smtp-port">
            {t('integrations.credentials.imap.smtpPortLabel')}
          </Label>
          <Input
            id="smtp-port"
            type="text"
            inputMode="numeric"
            placeholder="465"
            value={smtpPort}
            onChange={(e) => setSmtpPort(e.target.value)}
          />
          <Label htmlFor="smtp-security">
            {t('integrations.credentials.imap.securityLabel')}
          </Label>
          <select
            id="smtp-security"
            value={smtpSecurity}
            onChange={(e) => setSmtpSecurity(e.target.value)}
            className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
          >
            <option value="ssl">
              {t('integrations.credentials.imap.securitySsl')}
            </option>
            <option value="starttls">
              {t('integrations.credentials.imap.securityStarttls')}
            </option>
          </select>
        </>
      )}

      <HelpLink
        href="integrations.credentials.imap.helpUrl"
        label="integrations.credentials.imap.helpLabel"
      />
    </>
  );
}

// perProviderKey returns an i18n key that lets us override labels +
// placeholders for known providers (github / jira / tapd) without
// hardcoding their forms in JSX. Falls back to the generic key in the
// AuthFields renderer via i18next's defaultValue.
function perProviderKey(
  providerName: string,
  mode: ProxyAuthMode,
  field: 'userLabel' | 'userPlaceholder' | 'passwordLabel' | 'passwordPlaceholder' | 'tokenLabel' | 'tokenPlaceholder',
): string {
  return `integrations.credentials.providers.${providerName}.${mode}.${field}`;
}

// Provider-specific help link returned only when the matching i18n keys
// are defined; otherwise we omit the link.
function providerHelp(
  providerName: string,
  mode: ProxyAuthMode,
): { url: string; label: string } | null {
  // We rely on i18n at render time so callers in TSX still go through t().
  // The function itself just declares which providers have help: lookup
  // happens in i18n with the keys integrations.credentials.providers.
  // <name>.<mode>.helpUrl + helpLabel; if either is missing the consumer
  // hides the link by passing falsy text through HelpLink.
  if (providerName === 'github' && mode === 'bearer') {
    return {
      url: 'integrations.credentials.providers.github.bearer.helpUrl',
      label: 'integrations.credentials.providers.github.bearer.helpLabel',
    };
  }
  if (providerName === 'tapd' && mode === 'basic') {
    return {
      url: 'integrations.credentials.providers.tapd.basic.helpUrl',
      label: 'integrations.credentials.providers.tapd.basic.helpLabel',
    };
  }
  if (providerName === 'tapd' && mode === 'bearer') {
    return {
      url: 'integrations.credentials.providers.tapd.bearer.helpUrl',
      label: 'integrations.credentials.providers.tapd.bearer.helpLabel',
    };
  }
  if (providerName === 'jira' && mode === 'basic') {
    return {
      url: 'integrations.credentials.providers.jira.basic.helpUrl',
      label: 'integrations.credentials.providers.jira.basic.helpLabel',
    };
  }
  if (providerName === 'serp-api' && mode === 'query_param') {
    return {
      url: 'integrations.credentials.providers.serp-api.query_param.helpUrl',
      label: 'integrations.credentials.providers.serp-api.query_param.helpLabel',
    };
  }
  if (providerName === 'gsc' && mode === 'bearer') {
    return {
      url: 'integrations.credentials.providers.gsc.bearer.helpUrl',
      label: 'integrations.credentials.providers.gsc.bearer.helpLabel',
    };
  }
  return null;
}

interface SecretInputProps {
  id: string;
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
  showSecret: boolean;
  setShowSecret: (next: boolean | ((prev: boolean) => boolean)) => void;
}

function SecretInput({
  id,
  value,
  onChange,
  placeholder,
  showSecret,
  setShowSecret,
}: SecretInputProps) {
  const { t } = useTranslation('settings');
  return (
    <div className="relative">
      <Input
        id={id}
        type={showSecret ? 'text' : 'password'}
        placeholder={placeholder}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      <button
        type="button"
        aria-label={
          showSecret
            ? t('integrations.credentials.secretHide')
            : t('integrations.credentials.secretShow')
        }
        className="absolute right-2 top-1/2 -translate-y-1/2 text-warm-text-muted"
        onClick={() => setShowSecret((v) => !v)}
      >
        {showSecret ? (
          <EyeOff className="size-4" />
        ) : (
          <Eye className="size-4" />
        )}
      </button>
    </div>
  );
}

function HelpLink({ href, label }: { href: string; label: string }) {
  const { t } = useTranslation('settings');
  // href + label are i18n keys; resolve via t(). When the keys are not
  // defined the resolved value equals the key — we treat that as missing
  // and hide the link.
  const resolvedHref = t(href);
  const resolvedLabel = t(label);
  if (resolvedHref === href || resolvedLabel === label) return null;
  if (!resolvedHref.startsWith('http')) return null;
  return (
    <a
      href={resolvedHref}
      target="_blank"
      rel="noreferrer"
      className="text-xs text-warm-text-muted underline"
    >
      {resolvedLabel}
    </a>
  );
}
