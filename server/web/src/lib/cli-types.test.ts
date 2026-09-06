import { describe, it, expect } from 'vitest'
import { CLI_TYPES, DEFAULT_CLI_TYPE, isCliType, cliTypeForArrowKey } from './cli-types'

import enProjects from '@/i18n/locales/en/projects.json'
import enWorkspaces from '@/i18n/locales/en/workspaces.json'
import zhCNProjects from '@/i18n/locales/zh-CN/projects.json'
import zhCNWorkspaces from '@/i18n/locales/zh-CN/workspaces.json'
import zhTWProjects from '@/i18n/locales/zh-TW/projects.json'
import zhTWWorkspaces from '@/i18n/locales/zh-TW/workspaces.json'

// The engine pickers are spread across four screens (new-workspace dialog,
// issue side panel, new-project dialog, project settings). Before CLI_TYPES
// existed each one carried its own hardcoded list, and they drifted: `goose`
// and `cursor` were selectable when creating a project but absent from the
// project settings dropdown, so a project could never switch to them.
//
// These tests pin the two things that made that drift possible — a list that
// can disagree with itself, and labels that can lag behind a new engine.

describe('CLI_TYPES', () => {
  it('has no duplicate entries', () => {
    expect(new Set(CLI_TYPES).size).toBe(CLI_TYPES.length)
  })

  it('contains the default engine', () => {
    expect(CLI_TYPES).toContain(DEFAULT_CLI_TYPE)
  })

  it('narrows only known engines', () => {
    expect(isCliType('cursor')).toBe(true)
    expect(isCliType('not-an-agent')).toBe(false)
  })
})

describe('cliTypeForArrowKey', () => {
  const last = CLI_TYPES[CLI_TYPES.length - 1]

  it('advances and wraps forward', () => {
    expect(cliTypeForArrowKey(CLI_TYPES[0], 'ArrowRight')).toBe(CLI_TYPES[1])
    expect(cliTypeForArrowKey(last, 'ArrowDown')).toBe(CLI_TYPES[0])
  })

  it('retreats and wraps backward', () => {
    expect(cliTypeForArrowKey(CLI_TYPES[1], 'ArrowLeft')).toBe(CLI_TYPES[0])
    expect(cliTypeForArrowKey(CLI_TYPES[0], 'ArrowUp')).toBe(last)
  })

  // Returning null (rather than the current engine) is what lets callers skip
  // preventDefault and leave Tab, Enter and typing to the browser.
  it('ignores non-navigation keys', () => {
    expect(cliTypeForArrowKey(CLI_TYPES[0], 'Enter')).toBeNull()
    expect(cliTypeForArrowKey(CLI_TYPES[0], 'a')).toBeNull()
  })
})

describe('engine label coverage', () => {
  const locales = [
    ['en', enProjects, enWorkspaces],
    ['zh-CN', zhCNProjects, zhCNWorkspaces],
    ['zh-TW', zhTWProjects, zhTWWorkspaces],
  ] as const

  it.each(locales)('%s labels every engine in both picker namespaces', (_locale, projects, workspaces) => {
    const issueLabels = (projects as { issue: { workspace: { cliType: Record<string, string> } } })
      .issue.workspace.cliType
    const dialogLabels = (workspaces as { dialogs: { newWorkspace: { cliType: Record<string, string> } } })
      .dialogs.newWorkspace.cliType

    for (const opt of CLI_TYPES) {
      // Used by the issue panel, the new-project dialog and project settings.
      expect(issueLabels[opt], `issue.workspace.cliType.${opt}`).toBeTruthy()
      // Used by the new-workspace dialog, which also shows a per-engine hint.
      expect(dialogLabels[opt], `dialogs.newWorkspace.cliType.${opt}`).toBeTruthy()
      expect(dialogLabels[`${opt}Hint`], `dialogs.newWorkspace.cliType.${opt}Hint`).toBeTruthy()
    }
  })
})
