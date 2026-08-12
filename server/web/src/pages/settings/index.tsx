import { useState, type ReactNode } from 'react'
import { useSearch, Link, useNavigate } from '@tanstack/react-router'
import {
  BadgeCheck,
  Bot,
  Boxes,
  Building2,
  Cpu,
  GitBranch,
  Info,
  MonitorCog,
  Plug,
  Settings2,
  ShieldCheck,
  SlidersHorizontal,
  UserCog,
  Variable,
  Workflow,
  type LucideIcon,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { AboutSettings } from './about-settings'
import { EnvSettings } from './env-settings'
import { GeneralSettings } from './general-settings'
import { GitCredentialsSettings } from './git-credentials-settings'
import { GitIdentitySettings } from './git-identity-settings'
import { LicenseSettings } from './license-settings'
import { MobileAccessSettings } from './mobile-access'
import { SystemDepsSettings } from './system-deps-settings'
import { UsersSettings } from './users-settings'
import { IntegrationsPage } from './integrations'
import { OrchestrationSettings } from './orchestration-settings'
import { ProjectBlueprintsSettings } from './project-blueprints-settings'
import { ImBotSettings } from './imbot-settings'
import { SecurityTab } from './security/security-tab'
import { useOrgStore } from '@/stores/org-store'
import { useAuthStore } from '@/stores/auth-store'
import { useConfigStore } from '@/stores/config-store'
import { useLicenseStore } from '@/stores/license-store'

type SettingsTab = 'general' | 'users' | 'security' | 'env' | 'git-identity' | 'mobile-access' | 'system-deps' | 'integrations' | 'license' | 'orchestration' | 'blueprints' | 'imbot' | 'about' | 'claude'

interface TabVisibilityCtx {
  authEnabled: boolean
  isAdmin: boolean
  isOrgManagerSomewhere: boolean
}

// `hiddenInTabBar` keeps a tab resolvable via ?tab=<id> (e.g. desktop tray
// shortcut to mobile-access) while hiding the button from the visible tab row.
const tabs: { id: SettingsTab; labelKey: string; icon?: LucideIcon; visible?: (ctx: TabVisibilityCtx) => boolean; hiddenInTabBar?: boolean }[] = [
  { id: 'general', labelKey: 'tabs.general', icon: SlidersHorizontal },
  { id: 'system-deps', labelKey: 'tabs.systemDeps', icon: Cpu },
  { id: 'env', labelKey: 'tabs.env', icon: Variable },
  { id: 'git-identity', labelKey: 'tabs.gitIdentity', icon: GitBranch },
  { id: 'users', labelKey: 'tabs.users', icon: UserCog, visible: ({ authEnabled, isAdmin }) => authEnabled && isAdmin },
  { id: 'security', labelKey: 'tabs.security', icon: ShieldCheck, visible: ({ authEnabled }) => authEnabled },
  { id: 'mobile-access', labelKey: 'tabs.mobileAccess', icon: MonitorCog, hiddenInTabBar: true },
  { id: 'integrations', labelKey: 'tabs.integrations', icon: Plug },
  { id: 'license', labelKey: 'tabs.license', icon: BadgeCheck, visible: ({ authEnabled, isAdmin }) => authEnabled && isAdmin },
  { id: 'orchestration', labelKey: 'tabs.orchestration', icon: Workflow },
  { id: 'blueprints', labelKey: 'tabs.blueprints', icon: Boxes },
  { id: 'imbot', labelKey: 'tabs.imbot', icon: Bot },
  { id: 'about', labelKey: 'tabs.about', icon: Info },
]

const navGroups: { id: string; labelKey: string; tabIds: SettingsTab[] }[] = [
  { id: 'personal', labelKey: 'groups.personal', tabIds: ['general', 'security'] },
  { id: 'team', labelKey: 'groups.team', tabIds: ['users'] },
  { id: 'agents', labelKey: 'groups.agents', tabIds: ['claude', 'integrations', 'orchestration', 'blueprints', 'imbot'] },
  { id: 'system', labelKey: 'groups.system', tabIds: ['system-deps', 'env', 'git-identity', 'license', 'about'] },
]

// Map legacy ?tab values to their current home so old bookmarks/links don't
// silently fall back to "general". Returns undefined when the tab exists
// but is hidden for the current ctx.
const tabAliases: Record<string, string> = {
  // Git 凭证 used to be its own tab; merged into Git 署名 tab (now labeled
  // "Git") as a second section below the personal default email. Keep the
  // alias so old ?tab=git-credentials links still land on the right page.
  'git-credentials': 'git-identity',
  // Claude/Codex account login is now owned by System Dependencies. Keep old
  // query links useful without continuing to expose multi-account settings.
  'claude-accounts': 'system-deps',
  'codex-accounts': 'system-deps',
}

function resolveTab(tab: string | undefined, ctx: TabVisibilityCtx): SettingsTab | undefined {
  if (!tab) return undefined
  const aliased = tabAliases[tab] ?? tab
  const match = tabs.find(t => t.id === aliased)
  if (!match) return undefined
  if (match.visible && !match.visible(ctx)) return undefined
  return match.id as SettingsTab
}

interface SettingsPageProps {
  children?: ReactNode
  orgsActive?: boolean
}

export function SettingsPage({ children, orgsActive = false }: SettingsPageProps) {
  const { t } = useTranslation('settings')
  const search = useSearch({ strict: false }) as { tab?: string }
  const navigate = useNavigate()
  const myOrgs = useOrgStore((s) => s.myOrgs)
  const authUser = useAuthStore((s) => s.user)
  const authEnabled = useConfigStore((s) => s.authEnabled)
  const isAdmin = authUser?.role === 'admin'
  const isOrgManagerSomewhere = myOrgs.some((o) => o.role === 'owner' || o.role === 'admin')
  const ctx: TabVisibilityCtx = { authEnabled, isAdmin, isOrgManagerSomewhere }
  // 多租户组织是功能分级能力：license 未启用 org 时隐藏组织入口（开源个人版）。
  const orgEnabled = useLicenseStore((s) => s.orgEnabled)
  const showOrgsLink = orgEnabled && authEnabled && (myOrgs.length > 0 || isAdmin)

  const [activeTab, setActiveTab] = useState<SettingsTab>(() => resolveTab(search.tab, ctx) ?? 'general')

  // Re-sync when the URL ?tab changes (e.g. tray menu navigates here from
  // another page). Done as a render-time adjustment instead of a useEffect to
  // avoid the cascading render the eslint rule warns about.
  const [prevSearchTab, setPrevSearchTab] = useState(search.tab)
  if (search.tab !== prevSearchTab) {
    setPrevSearchTab(search.tab)
    const next = resolveTab(search.tab, ctx)
    if (next) setActiveTab(next)
  }

  const visibleTabs = tabs.filter(t => (!t.visible || t.visible(ctx)) && !t.hiddenInTabBar)
  const visibleTabById = new Map(visibleTabs.map((tab) => [tab.id, tab]))
  const activeTabMeta = tabs.find((tab) => tab.id === activeTab)
  function handleTabClick(tabId: SettingsTab) {
    setActiveTab(tabId)
    void navigate({ to: '/settings', search: { tab: tabId } })
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-warm-canvas">
      <div className="shrink-0 border-b border-warm-border bg-warm-surface px-6 py-5 lg:px-8">
        <div>
          <h1 className="text-2xl font-semibold text-warm-text">{t('page.title')}</h1>
          <p className="mt-1 text-sm text-warm-text-muted">{t('page.description')}</p>
        </div>
      </div>

      <div className="flex min-h-0 flex-1 flex-col lg:flex-row">
        <aside className="shrink-0 border-b border-warm-border bg-warm-surface px-4 py-4 lg:w-72 lg:border-b-0 lg:border-r lg:px-5 lg:py-6">
          <nav className="flex gap-3 overflow-x-auto lg:flex-col lg:overflow-visible" aria-label={t('page.navLabel')}>
            {navGroups.map((group) => {
              const groupTabs = group.tabIds
                .map((id) => visibleTabById.get(id))
                .filter((tab): tab is (typeof visibleTabs)[number] => Boolean(tab))
              const hasOrgEntry = group.id === 'team' && showOrgsLink
              if (groupTabs.length === 0 && !hasOrgEntry) return null

              return (
                <div key={group.id} className="min-w-48 space-y-1 lg:min-w-0">
                  <div className="px-2 pb-1 text-[11px] font-medium uppercase tracking-normal text-warm-text-muted">
                    {t(group.labelKey)}
                  </div>
                  <div className="space-y-1">
                    {groupTabs.map((tab) => {
                      const Icon = tab.icon ?? Settings2
                      const selected = !children && activeTab === tab.id
                      return (
                        <Button
                          key={tab.id}
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={() => handleTabClick(tab.id)}
                          className={cn(
                            'h-9 w-full justify-start gap-2 px-2 text-left text-warm-text-muted',
                            selected && 'bg-brand-soft text-brand hover:bg-brand-soft hover:text-brand'
                          )}
                        >
                          <Icon className="h-4 w-4 shrink-0" aria-hidden="true" />
                          <span className="truncate">{t(tab.labelKey)}</span>
                        </Button>
                      )
                    })}
                    {hasOrgEntry && (
                      <Button
                        asChild
                        variant="ghost"
                        size="sm"
                        className={cn(
                          'h-9 w-full justify-start gap-2 px-2 text-left text-warm-text-muted',
                          orgsActive && 'bg-brand-soft text-brand hover:bg-brand-soft hover:text-brand'
                        )}
                      >
                        <Link to="/settings/orgs">
                          <Building2 className="h-4 w-4 shrink-0" aria-hidden="true" />
                          <span className="truncate">{t('page.orgsLink')}</span>
                        </Link>
                      </Button>
                    )}
                  </div>
                </div>
              )
            })}
          </nav>
        </aside>

        <div className="min-w-0 flex-1 overflow-y-auto px-5 py-5 lg:px-8 lg:py-6">
          <div className="max-w-5xl">
            {children ? (
              <div className="mb-5 flex items-center gap-2 text-sm text-warm-text-muted">
                <Building2 className="h-4 w-4" aria-hidden="true" />
                <span>{t('page.orgsLink')}</span>
              </div>
            ) : activeTabMeta && (
              <div className="mb-5 flex items-center gap-2 text-sm text-warm-text-muted">
                {(() => {
                  const Icon = activeTabMeta.icon ?? Settings2
                  return <Icon className="h-4 w-4" aria-hidden="true" />
                })()}
                <span>{t(activeTabMeta.labelKey)}</span>
              </div>
            )}
            {children ?? (
              <>
                {activeTab === 'general' && <GeneralSettings />}
                {activeTab === 'system-deps' && <SystemDepsSettings />}
                {activeTab === 'env' && <EnvSettings />}
                {activeTab === 'git-identity' && (
                  <>
                    <GitIdentitySettings />
                    <GitCredentialsSettings />
                  </>
                )}
                {activeTab === 'users' && authEnabled && isAdmin && <UsersSettings />}
                {activeTab === 'security' && authEnabled && <SecurityTab />}
                {activeTab === 'mobile-access' && <MobileAccessSettings />}
                {activeTab === 'integrations' && <IntegrationsPage />}
                {activeTab === 'license' && authEnabled && isAdmin && <LicenseSettings />}
                {activeTab === 'orchestration' && <OrchestrationSettings />}
                {activeTab === 'blueprints' && <ProjectBlueprintsSettings />}
                {activeTab === 'imbot' && <ImBotSettings />}
                {activeTab === 'about' && <AboutSettings />}
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
