import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Github,
  Building2,
  Trash2,
  Plus,
  Pencil,
  ChevronDown,
  ChevronRight,
  Link2,
  MailCheck,
} from 'lucide-react';
import { toast } from 'sonner';
import { confirm } from '@/lib/confirm';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { ApiError } from '@/lib/api';
import {
  useExternalCredentials,
  useDeleteCredential,
  useCredentialUsages,
  useDeleteSourceBinding,
  useVerifyImapCredential,
} from '@/hooks/useExternalCredentials';
import { AddCredentialDialog } from './AddCredentialDialog';
import type {
  CredentialInUseError,
  CredentialInUseSource,
  ExternalCredential,
  ProviderName,
} from '@/types/integration';

function ProviderIcon({ provider }: { provider: ProviderName }) {
  if (provider === 'github')
    return <Github className="size-5 text-warm-text" aria-hidden />;
  return <Building2 className="size-5 text-warm-text" aria-hidden />;
}

export function CredentialList() {
  const { t } = useTranslation('settings');
  const { data, isLoading } = useExternalCredentials();
  const [dialogOpen, setDialogOpen] = useState(false);
  // `editing === null` -> "Add new"; otherwise an existing credential.
  const [editing, setEditing] = useState<ExternalCredential | null>(null);

  const handleAdd = () => {
    setEditing(null);
    setDialogOpen(true);
  };
  const handleEdit = (c: ExternalCredential) => {
    setEditing(c);
    setDialogOpen(true);
  };

  return (
    <section className="flex flex-col gap-3 rounded border border-warm-border p-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-medium">
            {t('integrations.credentials.sectionTitle')}
          </h2>
          <p className="text-xs text-warm-text-muted mt-1">
            {t('integrations.credentials.sectionDescription')}
          </p>
        </div>
        <Button size="sm" onClick={handleAdd}>
          <Plus className="size-4 mr-1" />
          {t('integrations.addCredential')}
        </Button>
      </div>

      {isLoading && (
        <div className="text-sm text-warm-text-muted py-2">
          {t('integrations.providers.loading')}
        </div>
      )}

      {!isLoading && (!data || data.length === 0) && (
        <div className="text-sm text-warm-text-muted py-2">
          {t('integrations.noCredentials')}
        </div>
      )}

      {!isLoading && data && data.length > 0 && (
        <div className="flex flex-col divide-y divide-warm-border/60">
          {data.map((c) => (
            <CredentialRow
              key={c.id}
              credential={c}
              onEdit={() => handleEdit(c)}
            />
          ))}
        </div>
      )}

      <AddCredentialDialog
        open={dialogOpen}
        onOpenChange={(next) => {
          setDialogOpen(next);
          if (!next) setEditing(null);
        }}
        editing={editing}
      />
    </section>
  );
}

interface CredentialRowProps {
  credential: ExternalCredential;
  onEdit: () => void;
}

function CredentialRow({ credential: c, onEdit }: CredentialRowProps) {
  const { t } = useTranslation('settings');
  const [expanded, setExpanded] = useState(false);
  const usages = useCredentialUsages(c.id, expanded);
  const del = useDeleteCredential();
  const unbind = useDeleteSourceBinding();
  const verifyImap = useVerifyImapCredential();
  const usageCount = usages.data?.length ?? 0;

  const handleVerify = () => {
    verifyImap.mutate(c.id, {
      onSuccess: () =>
        toast.success(t('integrations.credentials.imap.testOk')),
      onError: (e) =>
        toast.error(
          t('integrations.credentials.imap.testFailed', {
            error: e instanceof Error ? e.message : String(e),
          }),
        ),
    });
  };

  const handleDelete = async () => {
    if (
      !(await confirm(
        t('integrations.deleteConfirm', {
          provider: c.provider,
        }),
      ))
    )
      return;
    del.mutate(c.id, {
      onError: (e) => {
        if (
          e instanceof ApiError &&
          e.status === 409 &&
          isCredentialInUseError(e.body)
        ) {
          const sources = e.body.in_use_by;
          const projects = sources
            .map(
              (s) =>
                `${s.project_name || `#${s.project_id}`} (${s.source_key})`,
            )
            .join('; ');
          toast.error(
            t('integrations.deleteInUse', {
              count: sources.length,
              projects,
            }),
            { duration: 10_000 },
          );
          // Auto-expand so the user can see + remove the bindings inline.
          setExpanded(true);
          return;
        }
        toast.error(
          t('integrations.deleteFailed', {
            error: e instanceof Error ? e.message : String(e),
          }),
        );
      },
    });
  };

  const handleUnbind = async (s: CredentialInUseSource) => {
    if (
      !(await confirm(
        t('integrations.credentials.unbindConfirm', {
          project: s.project_name || `#${s.project_id}`,
          source_key: s.source_key,
        }),
      ))
    )
      return;
    unbind.mutate(
      { projectId: s.project_id, sourceId: s.id },
      {
        onSuccess: () => usages.refetch(),
        onError: (e) =>
          toast.error(
            t('integrations.credentials.unbindFailed', {
              error: e instanceof Error ? e.message : String(e),
            }),
          ),
      },
    );
  };

  return (
    <div className="flex flex-col py-3">
      <div className="flex items-center gap-3">
        <button
          type="button"
          className="text-warm-text-muted hover:text-warm-text"
          aria-label={
            expanded
              ? t('integrations.credentials.collapseUsages')
              : t('integrations.credentials.expandUsages')
          }
          onClick={() => setExpanded((v) => !v)}
        >
          {expanded ? (
            <ChevronDown className="size-4" />
          ) : (
            <ChevronRight className="size-4" />
          )}
        </button>
        <ProviderIcon provider={c.provider} />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-sm font-medium truncate">
              {c.alias || c.provider}
            </span>
            {expanded && usages.data && usageCount > 0 && (
              <Badge variant="outline" className="gap-1">
                <Link2 className="size-3" aria-hidden />
                {t('integrations.credentials.usageCount', { count: usageCount })}
              </Badge>
            )}
          </div>
          <div className="text-xs text-warm-text-muted">
            {c.provider}
          </div>
        </div>
        {c.provider === 'imap' && (
          <Button
            size="sm"
            variant="outline"
            aria-label={t('integrations.credentials.imap.testButton')}
            disabled={verifyImap.isPending}
            onClick={handleVerify}
          >
            <MailCheck className="size-4" />
          </Button>
        )}
        <Button
          size="sm"
          variant="outline"
          aria-label={t('integrations.credentials.editButton')}
          onClick={onEdit}
        >
          <Pencil className="size-4" />
        </Button>
        <Button
          size="sm"
          variant="ghost"
          aria-label={t('integrations.deleteButton')}
          disabled={del.isPending}
          onClick={handleDelete}
        >
          <Trash2 className="size-4" />
        </Button>
      </div>

      {expanded && (
        <div className="mt-3 ml-8 rounded border border-warm-border/60 bg-warm-surface/40 p-3">
          {usages.isLoading && (
            <div className="text-xs text-warm-text-muted">
              {t('integrations.credentials.usagesLoading')}
            </div>
          )}
          {!usages.isLoading && usages.data && usages.data.length === 0 && (
            <div className="text-xs text-warm-text-muted">
              {t('integrations.credentials.usagesEmpty')}
            </div>
          )}
          {!usages.isLoading && usages.data && usages.data.length > 0 && (
            <ul className="flex flex-col divide-y divide-warm-border/40">
              {usages.data.map((s) => (
                <li
                  key={s.id}
                  className="flex items-center gap-3 py-2 text-xs"
                >
                  <div className="flex-1 min-w-0">
                    <div className="font-medium truncate">
                      {s.project_name || `#${s.project_id}`}
                    </div>
                    <div className="text-warm-text-muted truncate">
                      {s.provider} · {s.source_key}
                    </div>
                  </div>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t('integrations.credentials.unbindButton')}
                    disabled={unbind.isPending}
                    onClick={() => handleUnbind(s)}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

function isCredentialInUseError(body: unknown): body is CredentialInUseError {
  if (!body || typeof body !== 'object') return false;
  const b = body as Record<string, unknown>;
  return b.error_kind === 'in_use' && Array.isArray(b.in_use_by);
}
