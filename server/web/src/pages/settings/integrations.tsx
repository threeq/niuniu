import { useTranslation } from 'react-i18next';
import { CredentialList } from '@/components/integrations/CredentialList';
import { ProviderList } from '@/components/integrations/ProviderList';
import { DataSourcesPanel } from '@/components/integrations/data-sources-panel';
import { IntegrationActivityPanel } from '@/components/integrations/IntegrationActivityPanel';
import { IntegrationDraftPanel } from '@/components/integrations/IntegrationDraftPanel';
import { OperationFunctionsPanel } from '@/components/integrations/OperationFunctionsPanel';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';

// The page is one workflow with five facets, not a pile of boards:
//
//   providers   — "where can I connect, with whose token" (provider rows carry
//                 the per-owner write gate; credentials belong beside them)
//   dataSources — "what data can the agent query"
//   drafts      — the AI-native write path: an agent drafts, the user confirms
//                 the impact and publishes. Kept as its own facet because a
//                 pending draft is a decision, not a list to browse.
//   activity    — the read side of the two audit tables
//   functions   — the sandbox fallback for calls the declarative contract
//                 cannot express
//
// Facets are tabbed rather than stacked: six stacked cards in one narrow column
// had no hierarchy and no discoverable entry point.
export function IntegrationsPage() {
  const { t } = useTranslation('settings');
  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-6 p-6">
      <header className="flex flex-col gap-1">
        <h1 className="text-xl font-semibold text-warm-text">{t('integrations.pageTitle')}</h1>
        <p className="text-sm text-warm-text-muted">{t('integrations.pageDescription')}</p>
      </header>

      <section
        aria-labelledby="integration-guide-title"
        className="rounded-lg border border-warm-border bg-warm-muted p-4"
      >
        <h2 id="integration-guide-title" className="text-sm font-medium text-warm-text">
          {t('integrations.guide.title')}
        </h2>
        <ol className="mt-2 flex list-decimal flex-col gap-1 pl-5 text-sm text-warm-text-muted">
          <li>{t('integrations.guide.step1')}</li>
          <li>{t('integrations.guide.step2')}</li>
          <li>{t('integrations.guide.step3')}</li>
        </ol>
        <p className="mt-2 text-sm text-warm-text-muted">{t('integrations.guide.manual')}</p>
      </section>

      <Tabs defaultValue="providers" className="flex flex-col gap-4">
        <TabsList>
          <TabsTrigger value="providers">{t('integrations.sections.providers')}</TabsTrigger>
          <TabsTrigger value="dataSources">{t('integrations.sections.dataSources')}</TabsTrigger>
          <TabsTrigger value="drafts">{t('integrations.sections.drafts')}</TabsTrigger>
          <TabsTrigger value="activity">{t('integrations.sections.activity')}</TabsTrigger>
          <TabsTrigger value="functions">{t('integrations.sections.functions')}</TabsTrigger>
        </TabsList>

        <TabsContent value="providers" className="flex flex-col gap-4">
          <ProviderList />
          <CredentialList />
        </TabsContent>

        <TabsContent value="dataSources" className="flex flex-col gap-4">
          <DataSourcesPanel />
        </TabsContent>

        <TabsContent value="drafts" className="flex flex-col gap-4">
          <IntegrationDraftPanel />
        </TabsContent>

        <TabsContent value="activity" className="flex flex-col gap-4">
          <IntegrationActivityPanel />
        </TabsContent>

        <TabsContent value="functions" className="flex flex-col gap-4">
          <OperationFunctionsPanel />
        </TabsContent>
      </Tabs>
    </div>
  );
}
