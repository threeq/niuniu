// Relay + mobile-pairing HTTP client.  These endpoints replace the old
// Wails bindings (window.go.main.App.*) — the business logic now lives in
// niuniu-server itself, so the same UI works in a plain browser and inside
// the Wails webview.
//
// All endpoints are under /api/relay and use the normal /api auth middleware.
// The auth bearer token is injected via the shared apiFetch helper so we
// honor the same session as the rest of the app.

import { apiFetch } from '@/lib/api'

export type PairingPhase =
  | 'idle'
  | 'starting'
  | 'waiting_claim'
  | 'ready_to_confirm'
  | 'confirmed'
  | 'failed'

export interface PairingState {
  phase: PairingPhase
  qr_url?: string
  qr_png?: string
  session_id?: string
  sas?: string
  mobile_name?: string
  error?: string
  // Machine-readable tag set on phase === 'failed' so the UI can branch, e.g.
  // offer a "resend verification email" button for 'email_verification_required'.
  error_code?: string
}

export interface TrustedMobileInfo {
  xpub_hex: string
  name: string
  platform: string
  paired_at: string
  last_seen_at: string
}

export interface RelayStatus {
  logged_in: boolean
  email?: string
  url: string
  connected: boolean
}

// Mirrors relayclient.Plan / relay's GET /api/billing/plans entries.
export interface BillingPlan {
  plan_id: string
  name: string
  description: string
  max_desktops: number
  max_mobiles: number
  max_pairings_per_day: number
  max_bandwidth_mb_month: number
  max_concurrent_tunnels: number
  price_cents_usd: number
  billing_interval: string
}

// Mirrors relayclient.UsageSummary / relay's GET /api/billing/my-usage.
export interface BillingUsage {
  plan_id: string
  desktops_used: number
  desktops_limit: number
  mobiles_used: number
  mobiles_limit: number
  pairings_today: number
  pairings_daily_limit: number
  bandwidth_mb_used: number
  bandwidth_mb_limit: number
}

// Shape of POST /api/relay/billing/change-plan response.
// Free plans: { applied: true }. Paid plans: { checkout_url, target_plan_id }.
export interface ChangePlanResult {
  applied?: boolean
  checkout_url?: string
  target_plan_id?: string
}

// Relay-side mobile_devices row (non-revoked). The `id` is what DELETE expects.
export interface RemoteMobileDevice {
  id: string
  name: string
  platform: string
  created_at: string
}

// Relay-side desktops row (non-revoked). Mirrors
// server/internal/relayclient/billing.go DesktopSummary. The desktop UI
// shows these alongside RemoteMobileDevice in the device list so stale
// desktops from earlier installs (which a mobile pair may still point at)
// are visible and revocable.
//
// `last_seen_at` is intentionally absent — the relay marshals it as a
// sql.NullTime object (`{Time, Valid}`) which doesn't fit a string field
// and historically 502'd the whole list endpoint when the Go side
// declared it as `string`. Re-add it here only when you also wire the
// `sql.NullTime` shape on the Go side (DesktopSummary).
export interface RemoteDesktopDevice {
  id: string
  name: string
  os: string
  arch: string
  version: string
  created_at: string
}

// Thin wrapper over the shared apiFetch so we get:
//   • Authorization: Bearer <token>  injected
//   • 401 -> refresh -> retry  handled
//   • network-error toast        centralized
//
// We still need to translate relay-specific error bodies (`{error:"..."}`)
// into Error.message so callers can show Chinese error strings from the
// service layer. apiFetch already surfaces `error.message` / `message`; the
// relay handler emits `error` as a plain string, so we wrap to pick it up.
async function apiJSON<T>(path: string, init?: RequestInit): Promise<T> {
  try {
    // suppressError=true because the relay UI renders inline error messages;
    // the global toast duplicates them.  The thrown ApiError still carries
    // the body so we can re-extract.
    return await apiFetch<T>(`/relay${path}`, {
      ...init,
      suppressError: true,
    })
  } catch (err) {
    // apiFetch wraps HTTP errors in ApiError; the relay handler uses a raw
    // `{error:"..."}` body, not the `{error:{message:...}}` envelope that
    // apiFetch knows how to flatten.  Re-extract here.
    const body = (err as { body?: { error?: string } }).body
    if (body && typeof body.error === 'string' && body.error) {
      throw new Error(body.error)
    }
    throw err
  }
}

export const relayAPI = {
  getStatus(): Promise<RelayStatus> {
    return apiJSON<RelayStatus>('/status')
  },
  // Passwordless email-code login (mirrors the mobile flow). Stage 1: ask
  // the relay to send a 6-digit code to `email`. Stage 2: verifyEmailCode
  // exchanges the code for a refresh token (persisted server-side) and
  // returns the resulting RelayStatus. The relay auto-creates accounts
  // for unknown addresses, so there is no separate register step.
  requestEmailCode(email: string, url: string): Promise<void> {
    return apiJSON<void>('/email-code/request', {
      method: 'POST',
      body: JSON.stringify({ email, url }),
    })
  },
  verifyEmailCode(email: string, code: string, url: string): Promise<RelayStatus> {
    return apiJSON<RelayStatus>('/email-code/verify', {
      method: 'POST',
      body: JSON.stringify({ email, code, url }),
    })
  },
  logout(): Promise<void> {
    return apiJSON<void>('/logout', { method: 'POST' })
  },
  startPairing(): Promise<PairingState> {
    return apiJSON<PairingState>('/pairing/start', { method: 'POST' })
  },
  pairingStatus(): Promise<PairingState> {
    return apiJSON<PairingState>('/pairing/status')
  },
  confirmPairing(): Promise<void> {
    return apiJSON<void>('/pairing/confirm', { method: 'POST' })
  },
  rejectPairing(): Promise<void> {
    return apiJSON<void>('/pairing/reject', { method: 'POST' })
  },
  requestEmailVerification(): Promise<void> {
    return apiJSON<void>('/verify-email/request', { method: 'POST' })
  },
  listTrustedMobiles(): Promise<TrustedMobileInfo[]> {
    return apiJSON<TrustedMobileInfo[]>('/trusted-mobiles')
  },
  removeTrustedMobile(xpubHex: string): Promise<void> {
    return apiJSON<void>(`/trusted-mobiles/${encodeURIComponent(xpubHex)}`, {
      method: 'DELETE',
    })
  },

  // Billing + remote mobile management. All route through niuniu-server,
  // which proxies the scoped relay client — frontend doesn't need a relay
  // access token directly.
  getBillingUsage(): Promise<BillingUsage> {
    return apiJSON<BillingUsage>('/billing/usage')
  },
  getBillingPlan(): Promise<BillingPlan> {
    return apiJSON<BillingPlan>('/billing/my-plan')
  },
  listBillingPlans(): Promise<BillingPlan[]> {
    return apiJSON<BillingPlan[]>('/billing/plans')
  },
  changeBillingPlan(targetPlanID: string): Promise<ChangePlanResult> {
    return apiJSON<ChangePlanResult>('/billing/change-plan', {
      method: 'POST',
      body: JSON.stringify({ target_plan_id: targetPlanID }),
    })
  },
  listRemoteMobileDevices(): Promise<RemoteMobileDevice[]> {
    return apiJSON<RemoteMobileDevice[]>('/mobile-devices')
  },
  revokeRemoteMobileDevice(id: string): Promise<void> {
    return apiJSON<void>(`/mobile-devices/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    })
  },

  // Desktops — same shape as mobiles but the relay's `desktops` table.
  // Surfacing these next to mobiles lets the user spot a stale desktop
  // registration that no mobile actually pairs to (for example after a
  // reinstall the old desktop_id lingers and the mobile's pair points at
  // a desktop that no longer exists, which manifests as "fetch POST
  // .../d/<id>/rpc failed: Network request failed" with a desktop_id
  // that doesn't appear anywhere else in the device list).
  listRemoteDesktopDevices(): Promise<RemoteDesktopDevice[]> {
    return apiJSON<RemoteDesktopDevice[]>('/desktops')
  },
  revokeRemoteDesktopDevice(id: string): Promise<void> {
    return apiJSON<void>(`/desktops/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    })
  },
}
