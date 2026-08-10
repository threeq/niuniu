// Landing gate for system dependencies.
//
// On app entry the index route asks this module whether the user should be
// diverted to Settings → System Dependencies instead of landing on
// /workspaces. The rule: in personal/embedded mode, if the minimum tools to
// run a Claude Code agent aren't installed, send the user to the deps tab so
// they can install them. Team edition and probe failures fall through (the
// gate never traps the user).
//
// This mirrors lib/edition.ts: route guards get a small encapsulated helper
// instead of threading the react-query client into beforeLoad.

import { api } from './api'
import { useConfigStore } from '../stores/config-store'
import type { SystemDepsInfo } from '../types/api'

// Tools that must be present before niuniu can run a Claude Code agent:
//   - node:   the claude CLI is an npm package and won't run without it
//   - git:    workspaces are git worktrees
//   - claude: the agent itself
// python3 and codex are optional (codex only for codex-type workspaces) and
// never gate the initial landing.
export const MINIMUM_TOOLS = ['node', 'git', 'claude'] as const

/** True when every required tool's probe row reports found. */
export function minimumDepsMet(info: SystemDepsInfo): boolean {
  return MINIMUM_TOOLS.every((name) =>
    info.tools.some((t) => t.name === name && t.found),
  )
}

// Cache the probe promise for the lifetime of the SPA load. The index route's
// beforeLoad can run more than once (e.g. re-navigating to "/"), and a page
// reload resets the module anyway, so a per-load cache is the right lifetime.
let probeCache: Promise<SystemDepsInfo | null> | null = null

function probeOnce(): Promise<SystemDepsInfo | null> {
  if (!probeCache) {
    // Fail open: swallow probe errors to null so the gate falls through to
    // /workspaces rather than trapping the user behind a flaky probe.
    probeCache = api.getSystemDeps().catch(() => null)
  }
  return probeCache
}

/** Test seam: drop the cached probe so each test starts clean. */
export function resetDepsGateCache(): void {
  probeCache = null
}

/**
 * Returns true when the initial landing should divert to the System
 * Dependencies tab. Only diverts in personal mode with a successful probe
 * whose minimum set is unmet; everything else (team edition, probe error,
 * deps satisfied) returns false.
 */
export async function shouldDivertToSystemDeps(): Promise<boolean> {
  // Team edition: probe reflects the server host and installs are disabled, so
  // diverting users to a tab they can't act on would be a dead end.
  if (!useConfigStore.getState().personalMode) return false
  const info = await probeOnce()
  if (!info) return false
  return !minimumDepsMet(info)
}
