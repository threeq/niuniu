import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from '@tanstack/react-router';
import { useQuery } from '@tanstack/react-query';
import { ExternalLink } from 'lucide-react';
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
import type { ExternalCredential, ProviderName } from '@/types/integration';
import type { OwnerRef } from '@/types/org';
import { integrationApi } from '@/lib/integration-api';
import { useExternalCredentials } from '@/hooks/useExternalCredentials';
import { useAddSource } from '@/hooks/useExternalSources';

interface Props {
  projectId: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Owner of the project this dialog binds a source for. When it is an org,
   *  the credential picker additionally surfaces that org's team-shared
   *  credentials alongside the user's personal ones. */
  projectOwner?: OwnerRef;
}

const GITHUB_REPO_RE = /^[^/\s]+\/[^/\s]+$/;
const TAPD_WORKSPACE_RE = /^\d+$/;

export function AddSourceDialog({
  projectId,
  open,
  onOpenChange,
  projectOwner,
}: Props) {
  const { t } = useTranslation('projects');
  const navigate = useNavigate();
  // "My personal" credentials — the legacy current-user list.
  const { data: personalCreds = [] } = useExternalCredentials();
  // "Team shared" credentials — only fetched when the project belongs to an org.
  const orgOwnerId =
    projectOwner?.type === 'org' && projectOwner.id > 0 ? projectOwner.id : 0;
  const { data: orgCreds = [] } = useQuery({
    queryKey: ['external-credentials', 'org', orgOwnerId],
    queryFn: () =>
      integrationApi.listCredentials(undefined, { type: 'org', id: orgOwnerId }),
    enabled: open && orgOwnerId > 0,
  });

  const addSource = useAddSource(projectId);

  const [credentialId, setCredentialId] = useState<string>('');
  const [sourceKey, setSourceKey] = useState('');
  const [error, setError] = useState<string | null>(null);

  // Lookup spans both groups so selection / provider resolution work
  // regardless of which group the chosen credential came from.
  const allCreds = [...orgCreds, ...personalCreds];
  const selectedCred = allCreds.find((c) => String(c.id) === credentialId);
  const provider: ProviderName | undefined = selectedCred?.provider;

  const trimmed = sourceKey.trim();
  // github/tapd have known source-key formats; jira and any user-created
  // custom provider have no fixed format the SPA can validate, so accept any
  // non-empty value. (Previously custom providers fell through to `false`,
  // permanently disabling Save with no label/placeholder.)
  const formatValid =
    provider === 'github'
      ? GITHUB_REPO_RE.test(trimmed)
      : provider === 'tapd'
        ? TAPD_WORKSPACE_RE.test(trimmed)
        : provider
          ? trimmed.length > 0
          : false;

  const formatHint =
    trimmed.length > 0 && !formatValid
      ? provider === 'github'
        ? t('externalSources.invalidGithubRepo')
        : provider === 'tapd'
          ? t('externalSources.invalidTapdWorkspaceId')
          : null
      : null;

  const handleCredentialChange = (nextId: string) => {
    setCredentialId(nextId);
    setSourceKey('');
    setError(null);
  };

  const handleSave = async () => {
    setError(null);
    if (!trimmed || !provider || !selectedCred) return;
    if (!formatValid) {
      setError(
        provider === 'github'
          ? t('externalSources.invalidGithubRepo')
          : t('externalSources.invalidTapdWorkspaceId'),
      );
      return;
    }
    try {
      await addSource.mutateAsync({
        provider,
        sourceKey: trimmed,
        credentialId: selectedCred.id,
      });
      setCredentialId('');
      setSourceKey('');
      onOpenChange(false);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setError(t('externalSources.addFailed', { message: msg }));
    }
  };

  const providerLabel = (p: ProviderName) => {
    if (p === 'github') return 'GitHub';
    if (p === 'jira') return 'Jira';
    return 'TAPD';
  };

  const sourceKeyLabel =
    provider === 'github'
      ? t('externalSources.sourceKeyLabel')
      : provider === 'tapd'
        ? t('externalSources.tapdWorkspaceIdLabel')
        : provider
          ? t('externalSources.genericSourceKeyLabel')
          : '';

  const sourceKeyPlaceholder =
    provider === 'github'
      ? t('externalSources.sourceKeyPlaceholder')
      : provider === 'tapd'
        ? t('externalSources.tapdWorkspaceIdPlaceholder')
        : provider
          ? t('externalSources.genericSourceKeyPlaceholder')
          : '';

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('externalSources.addSource')}</DialogTitle>
          <DialogDescription>{t('externalSources.description')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {allCreds.length === 0 ? (
            <div className="rounded-md border border-warm-border bg-warm-muted/50 p-3 text-sm text-warm-text">
              <p>{t('externalSources.configureAnyCredentialFirst')}</p>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="mt-2"
                onClick={() => {
                  onOpenChange(false);
                  navigate({ to: '/settings/integrations' });
                }}
              >
                <ExternalLink className="h-3.5 w-3.5 mr-1" aria-hidden="true" />
                {t('externalSources.goToCredentials')}
              </Button>
            </div>
          ) : (
            <>
              <div className="grid gap-2">
                <label className="text-sm font-medium text-warm-text">
                  {t('externalSources.credentialLabel')}
                </label>
                <select
                  value={credentialId}
                  onChange={(e) => handleCredentialChange(e.target.value)}
                  className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
                >
                  <option value="" disabled>
                    {t('externalSources.pickCredential')}
                  </option>
                  {orgCreds.length > 0 && (
                    <optgroup label={t('externalSources.teamSharedGroup')}>
                      {orgCreds.map((c) => (
                        <CredentialOption
                          key={`org-${c.id}`}
                          cred={c}
                          providerLabel={providerLabel}
                        />
                      ))}
                    </optgroup>
                  )}
                  {personalCreds.length > 0 && (
                    <optgroup label={t('externalSources.personalGroup')}>
                      {personalCreds.map((c) => (
                        <CredentialOption
                          key={`me-${c.id}`}
                          cred={c}
                          providerLabel={providerLabel}
                        />
                      ))}
                    </optgroup>
                  )}
                </select>
              </div>

              {provider && (
                <div className="grid gap-2">
                  <label className="text-sm font-medium text-warm-text">
                    {sourceKeyLabel}
                  </label>
                  <Input
                    value={sourceKey}
                    onChange={(e) => setSourceKey(e.target.value)}
                    placeholder={sourceKeyPlaceholder}
                    disabled={addSource.isPending}
                  />
                  {formatHint && (
                    <p className="text-xs text-warning">{formatHint}</p>
                  )}
                </div>
              )}
            </>
          )}

          {error && (
            <p className="text-sm text-destructive">{error}</p>
          )}
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={addSource.isPending}
          >
            {t('externalIssues.cancel')}
          </Button>
          <Button
            type="button"
            onClick={handleSave}
            disabled={
              addSource.isPending || !credentialId || !trimmed || !formatValid
            }
          >
            {addSource.isPending
              ? t('externalSources.saving')
              : t('externalSources.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// Single <option> for a credential — shared between the team-shared and
// personal groups so their labels render identically.
function CredentialOption({
  cred,
  providerLabel,
}: {
  cred: ExternalCredential;
  providerLabel: (p: ProviderName) => string;
}) {
  return (
    <option value={String(cred.id)}>
      {cred.alias || providerLabel(cred.provider)} ({providerLabel(cred.provider)})
    </option>
  );
}
