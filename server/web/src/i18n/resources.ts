import zhCNAuth from './locales/zh-CN/auth.json'
import zhCNCommon from './locales/zh-CN/common.json'
import zhCNNav from './locales/zh-CN/nav.json'
import zhCNSettings from './locales/zh-CN/settings.json'
import zhCNWorkspaces from './locales/zh-CN/workspaces.json'
import zhCNProjects from './locales/zh-CN/projects.json'
import zhCNRepositories from './locales/zh-CN/repositories.json'
import zhCNSchedules from './locales/zh-CN/schedules.json'
import zhCNOrgs from './locales/zh-CN/orgs.json'
import zhCNLogin from './locales/zh-CN/login.json'
import zhCNDialogs from './locales/zh-CN/dialogs.json'
import zhCNPresets from './locales/zh-CN/presets.json'
import zhCNWizard from './locales/zh-CN/wizard.json'
import zhCNIssues from './locales/zh-CN/issues.json'
import zhCNScenes from './locales/zh-CN/scenes.json'
import zhCNData from './locales/zh-CN/data.json'
import zhCNDashboards from './locales/zh-CN/dashboards.json'
import zhCNAssistant from './locales/zh-CN/assistant.json'
import zhCNKnowledge from './locales/zh-CN/knowledge.json'

import enAuth from './locales/en/auth.json'
import enCommon from './locales/en/common.json'
import enNav from './locales/en/nav.json'
import enSettings from './locales/en/settings.json'
import enWorkspaces from './locales/en/workspaces.json'
import enProjects from './locales/en/projects.json'
import enRepositories from './locales/en/repositories.json'
import enSchedules from './locales/en/schedules.json'
import enOrgs from './locales/en/orgs.json'
import enLogin from './locales/en/login.json'
import enDialogs from './locales/en/dialogs.json'
import enPresets from './locales/en/presets.json'
import enWizard from './locales/en/wizard.json'
import enIssues from './locales/en/issues.json'
import enScenes from './locales/en/scenes.json'
import enData from './locales/en/data.json'
import enDashboards from './locales/en/dashboards.json'
import enAssistant from './locales/en/assistant.json'
import enKnowledge from './locales/en/knowledge.json'

import zhTWAuth from './locales/zh-TW/auth.json'
import zhTWCommon from './locales/zh-TW/common.json'
import zhTWNav from './locales/zh-TW/nav.json'
import zhTWSettings from './locales/zh-TW/settings.json'
import zhTWWorkspaces from './locales/zh-TW/workspaces.json'
import zhTWProjects from './locales/zh-TW/projects.json'
import zhTWRepositories from './locales/zh-TW/repositories.json'
import zhTWSchedules from './locales/zh-TW/schedules.json'
import zhTWOrgs from './locales/zh-TW/orgs.json'
import zhTWLogin from './locales/zh-TW/login.json'
import zhTWDialogs from './locales/zh-TW/dialogs.json'
import zhTWPresets from './locales/zh-TW/presets.json'
import zhTWWizard from './locales/zh-TW/wizard.json'
import zhTWIssues from './locales/zh-TW/issues.json'
import zhTWScenes from './locales/zh-TW/scenes.json'
import zhTWData from './locales/zh-TW/data.json'
import zhTWDashboards from './locales/zh-TW/dashboards.json'
import zhTWAssistant from './locales/zh-TW/assistant.json'
import zhTWKnowledge from './locales/zh-TW/knowledge.json'

export const resources = {
  'zh-CN': { auth: zhCNAuth, common: zhCNCommon, nav: zhCNNav, settings: zhCNSettings,
    workspaces: zhCNWorkspaces, projects: zhCNProjects, repositories: zhCNRepositories,
    schedules: zhCNSchedules, orgs: zhCNOrgs, login: zhCNLogin, dialogs: zhCNDialogs,
   
    presets: zhCNPresets, wizard: zhCNWizard, issues: zhCNIssues, scenes: zhCNScenes,
    data: zhCNData, dashboards: zhCNDashboards, assistant: zhCNAssistant,
    knowledge: zhCNKnowledge },
  en: { auth: enAuth, common: enCommon, nav: enNav, settings: enSettings,
    workspaces: enWorkspaces, projects: enProjects, repositories: enRepositories,
    schedules: enSchedules, orgs: enOrgs, login: enLogin, dialogs: enDialogs,
   
    presets: enPresets, wizard: enWizard, issues: enIssues, scenes: enScenes,
    data: enData, dashboards: enDashboards, assistant: enAssistant,
    knowledge: enKnowledge },
  'zh-TW': { auth: zhTWAuth, common: zhTWCommon, nav: zhTWNav, settings: zhTWSettings,
    workspaces: zhTWWorkspaces, projects: zhTWProjects, repositories: zhTWRepositories,
    schedules: zhTWSchedules, orgs: zhTWOrgs, login: zhTWLogin, dialogs: zhTWDialogs,
   
    presets: zhTWPresets, wizard: zhTWWizard, issues: zhTWIssues, scenes: zhTWScenes,
    data: zhTWData, dashboards: zhTWDashboards, assistant: zhTWAssistant,
    knowledge: zhTWKnowledge },
} as const

export const NAMESPACES = [
  'auth', 'common', 'nav', 'settings', 'workspaces', 'projects',
  'repositories', 'schedules', 'orgs', 'login', 'dialogs',
  'presets', 'wizard', 'issues', 'scenes', 'data', 'dashboards', 'assistant', 'knowledge',
] as const
export type Namespace = typeof NAMESPACES[number]
