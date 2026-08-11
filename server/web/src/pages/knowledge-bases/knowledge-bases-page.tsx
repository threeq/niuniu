import { KnowledgeBasesPanel } from '@/components/knowledge/knowledge-bases-panel';

// Top-level knowledge-base management page (KB as a first-class citizen,
// mirroring the /repositories entry). The panel is self-contained: it owns its
// title, description, "new knowledge base" action, and the create/edit/browse/
// search flows, so this page is just a padded container hosting it.
export function KnowledgeBasesPage() {
  return (
    <div className="p-6 max-w-3xl">
      <KnowledgeBasesPanel />
    </div>
  );
}