import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import type { SystemDepsInfo, ToolStatus } from '@/types/api'

// Mock the api module so the gate's probe is controllable per-test.
vi.mock('./api', () => ({
  api: { getSystemDeps: vi.fn() },
}))

import { api } from './api'
import { useConfigStore } from '../stores/config-store'
import {
  MINIMUM_TOOLS,
  minimumDepsMet,
  shouldDivertToSystemDeps,
  resetDepsGateCache,
} from './system-deps-gate'

const getSystemDeps = api.getSystemDeps as unknown as ReturnType<typeof vi.fn>

function tool(name: ToolStatus['name'], found: boolean): ToolStatus {
  return { name, found, version: found ? '1.0' : '', path: found ? `/usr/bin/${name}` : '', installable: false }
}

function info(found: Record<string, boolean>): SystemDepsInfo {
  return {
    platform: 'linux',
    package_manager: 'apt-get',
    can_install: true,
    personal_mode: true,
    tools: (['node', 'python3', 'git', 'claude', 'codex'] as ToolStatus['name'][]).map(
      (n) => tool(n, found[n] ?? false),
    ),
  }
}

const allRequired = { node: true, git: true, claude: true }

describe('minimumDepsMet', () => {
  it('is true when node+git+claude are found (python3/codex absent is OK)', () => {
    expect(minimumDepsMet(info(allRequired))).toBe(true)
  })

  it('only gates on node/git/claude', () => {
    expect(MINIMUM_TOOLS).toEqual(['node', 'git', 'claude'])
  })

  it.each(MINIMUM_TOOLS)('is false when %s is missing', (missing) => {
    const found = { ...allRequired, [missing]: false }
    expect(minimumDepsMet(info(found))).toBe(false)
  })
})

describe('shouldDivertToSystemDeps', () => {
  beforeEach(() => {
    resetDepsGateCache()
    getSystemDeps.mockReset()
    useConfigStore.setState({ personalMode: true, authEnabled: false, loaded: true })
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('diverts in personal mode when a required tool is missing', async () => {
    getSystemDeps.mockResolvedValue(info({ node: true, git: true, claude: false }))
    expect(await shouldDivertToSystemDeps()).toBe(true)
  })

  it('does not divert when the minimum set is satisfied', async () => {
    getSystemDeps.mockResolvedValue(info(allRequired))
    expect(await shouldDivertToSystemDeps()).toBe(false)
  })

  it('does not divert in team edition even with missing deps', async () => {
    useConfigStore.setState({ personalMode: false, authEnabled: true, loaded: true })
    getSystemDeps.mockResolvedValue(info({ node: false, git: false, claude: false }))
    expect(await shouldDivertToSystemDeps()).toBe(false)
    // Team edition short-circuits before probing.
    expect(getSystemDeps).not.toHaveBeenCalled()
  })

  it('fails open (no divert) when the probe rejects', async () => {
    getSystemDeps.mockRejectedValue(new Error('network down'))
    expect(await shouldDivertToSystemDeps()).toBe(false)
  })

  it('probes only once across repeated calls (cache)', async () => {
    getSystemDeps.mockResolvedValue(info(allRequired))
    await shouldDivertToSystemDeps()
    await shouldDivertToSystemDeps()
    expect(getSystemDeps).toHaveBeenCalledTimes(1)
  })
})
