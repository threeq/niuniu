import { useTranslation } from 'react-i18next';
import { CredentialList } from '@/components/integrations/CredentialList';
import { ProviderList } from '@/components/integrations/ProviderList';
import { DataSourcesPanel } from '@/components/integrations/data-sources-panel';
import { KnowledgeBasesPanel } from '@/components/knowledge/knowledge-bases-panel';

// Two boards on this page:
//   1. Providers   — "where can I connect, with what auth scheme, and what
//                    operations are allowed". The per-(user, provider)
//                    write gate lives inline on each row.
//   2. Credentials — "with whose token do I connect"
// The legacy standalone WritePermissionsPanel is gone: it pointed at a
// dead /api/me/external-write-prefs endpoint (the legacy handler was
// never wired into the router), so every PATCH 404'd to the SPA shell
// and the toast surfaced "Unexpected token '<'". Write toggle moved to
// the matching ProviderList row (external_api_write_prefs is keyed by
// provider_id, so per-row is the correct shape).
export function IntegrationsPage() {
  const { t } = useTranslation('settings');
  return (
    <div className="flex flex-col gap-4 p-6 max-w-2xl">
      <h1 className="text-xl font-semibold">{t('integrations.pageTitle')}</h1>
      <ProviderList />
      <CredentialList />
      <DataSourcesPanel />
      <KnowledgeBasesPanel />
    </div>
  );
}
