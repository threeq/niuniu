import { useTranslation } from 'react-i18next'
import { AgentSettings } from '../settings/agent-settings'

export function AgentsPage() {
  const { t } = useTranslation('nav')
  return (
    <div className="flex flex-col h-full">
      <div className="shrink-0 px-8 pt-8">
        <h1 className="text-2xl font-semibold mb-6">{t('menu.agents')}</h1>
      </div>
      <div className="flex-1 overflow-y-auto px-8 pb-8">
        <div className="max-w-5xl mx-auto">
          <AgentSettings />
        </div>
      </div>
    </div>
  )
}
