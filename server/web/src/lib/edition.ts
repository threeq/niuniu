// Edition detection — distinguishes personal (embedded, single-user, local) from
// team edition. Resolved from /api/health's auth_enabled flag: personal mode runs
// with Auth.Enabled=false (forced by applyEmbeddedOverrides), team mode requires
// JWT auth.
//
// Backed by stores/config-store.ts, which is loaded once at SPA boot in
// main.tsx; subsequent calls are synchronous. Components that need to react to
// the value should subscribe to useConfigStore directly.

import { useConfigStore } from '../stores/config-store'

export async function isAuthEnabled(): Promise<boolean> {
  const { loaded, load } = useConfigStore.getState()
  if (!loaded) await load()
  return useConfigStore.getState().authEnabled
}

export async function isPersonalEdition(): Promise<boolean> {
  return !(await isAuthEnabled())
}
