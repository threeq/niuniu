// Update-check helpers.
//
// Two complementary pieces of info:
//
//   - Server version: comes from GET /api/health (see api/health.go). The
//     SPA always shows this, regardless of edition or transport, because
//     "what version of the server am I talking to" is useful even in the
//     standalone-server browser case.
//
//   - App update available: only relevant in personal mode (the bundled
//     desktop app distributes via GitHub Releases). Detected by asking our
//     OWN server's `/api/app-update/latest`, which proxies the latest release
//     off the official website changelog (www.niu6ai.com/changelog). We used
//     to poll api.github.com directly from the browser, but that returns
//     HTTP 403 from mainland China (shared-IP rate limit / regional blocking)
//     — exactly where personal-edition users are. The local server reaches the
//     Aliyun-hosted site fine and the server-to-server hop sidesteps CORS. In
//     team-edition / standalone-server mode the user manages binaries
//     themselves, so we suppress this entirely.

// Server health is owned by stores/config-store.ts (which fetches /api/health
// at boot). This module focuses on release polling — kept out of config-store
// because it depends on network reachability we can't assume (GFW, offline,
// upstream down) and shouldn't block boot.

// Our server-side proxy. Returns the GitHub-shaped release JSON (tag_name,
// html_url, assets[], published_at), parsed from the official changelog.
const LATEST_RELEASE_API = '/api/app-update/latest'
// Public-facing changelog page (China-friendly; also offers a Baidu Netdisk
// mirror). Used as the "view release notes" / fallback target.
const RELEASES_URL = 'https://www.niu6ai.com/changelog'

export interface ReleaseAsset {
  name: string
  browser_download_url: string
}

export interface LatestRelease {
  tag_name: string
  html_url: string
  assets: ReleaseAsset[]
  published_at: string
}

export interface UpdateCheckResult {
  available: boolean
  current: string
  latest: string
  release_url: string
  download_url: string // platform-matched asset; empty if no match
  published_at: string
}

export async function fetchLatestRelease(): Promise<LatestRelease> {
  // Plain `fetch` (not `apiFetch`): this endpoint is unauthenticated and we
  // want the raw release JSON, not apiFetch's envelope unwrapping / auth
  // refresh machinery. Update checks only run in personal mode (auth off).
  const res = await fetch(LATEST_RELEASE_API)
  if (!res.ok) {
    throw new Error(`update check: HTTP ${res.status}`)
  }
  return res.json()
}

// Heuristic for "we couldn't determine the latest version" — either the
// browser→local-server call failed (offline, server down) or the server→
// official-site upstream fetch failed (the proxy answers `HTTP 502` then).
// Both surface the same friendly About-panel state pointing users at the
// official changelog rather than a raw error string.
export function isNetworkError(err: unknown): boolean {
  const msg = err instanceof Error ? err.message : String(err)
  return /Failed to fetch|TypeError|NetworkError|ERR_NETWORK|ERR_CONNECTION|HTTP 50\d/i.test(msg)
}

// Compare two semver-ish version strings. Returns +1 / 0 / -1 like
// String.compare. Tolerant of `v` prefix and `-suffix` git-describe metadata
// (so `v1.0.0-3-gabcdef` parses to [1,0,0] and compares equal to `v1.0.0`,
// matching the user's intent: a build past the latest tag is "current"
// against that tag).
//
// When either side fails to parse (e.g. server `Version="dev"` because
// ldflags weren't applied), we return +1 — i.e. "the comparison input we
// got from the network is treated as newer than the unparseable local
// version". This biases toward showing the toast for unstamped builds,
// which is the population most likely to want an upgrade. The alternative
// (string compare) yields alphabetical luck-based answers and is hard to
// reason about.
//
// We deliberately do NOT depend on a semver lib — bundle weight is more
// expensive than the small risk of a corner-case mismatch (e.g. RC tags).
export function compareVersions(a: string, b: string): number {
  // Strip leading `v` and any `-<suffix>` (pre-release / git-describe
  // metadata). `1.0.0-rc.1` and `v1.0.0-3-gabcdef-dirty` both reduce to
  // `1.0.0` and compare equal to `1.0.0`.
  const norm = (v: string) =>
    v
      .replace(/^v/, '')
      .split('-')[0]
      .split('.')
      .map((p) => parseInt(p, 10))
  const aa = norm(a)
  const bb = norm(b)
  if (aa.some(Number.isNaN) || bb.some(Number.isNaN)) {
    // Equal raw strings → 0 (no spurious "update available" when both
    // sides are e.g. `dev`). Otherwise default to "available" so an
    // unstamped local build still sees the upgrade toast.
    return a === b ? 0 : 1
  }
  for (let i = 0; i < Math.max(aa.length, bb.length); i++) {
    const av = aa[i] ?? 0
    const bv = bb[i] ?? 0
    if (av !== bv) return av < bv ? -1 : 1
  }
  return 0
}

// Detect the running OS from UA + navigator.platform. Returns the same
// tokens the Go-side Makefile bakes into asset names: `windows`, `darwin`,
// `linux`. Empty string when none of the three match (mobile browsers,
// exotic UAs).
function detectOS(): string {
  const ua = navigator.userAgent.toLowerCase()
  const platform = navigator.platform.toLowerCase()
  if (ua.includes('windows') || platform.includes('win')) return 'windows'
  if (ua.includes('mac') || platform.includes('mac')) return 'darwin'
  if (ua.includes('linux') || platform.includes('linux')) return 'linux'
  return ''
}

// Best-effort sync arch detection from UA strings. Catches:
//   - Windows ARM ("Windows ARM64" UA hint)
//   - Linux aarch64
//   - x86_64 / x64 hints from desktop Linux/Windows
//
// Apple Silicon is the painful exception: macOS WebKit (and Chromium on
// macOS, for compat) deliberately reports `Intel` in `navigator.platform`
// and the UA, so we cannot reliably distinguish M-series from Intel Macs
// in a synchronous call. UA-CH `getHighEntropyValues({architecture:true})`
// works on Chromium but is async + not supported in Safari / Wails-native
// WebKit, so we don't rely on it. Result: Apple Silicon users may get the
// amd64 asset when both arm64 and amd64 are published; that runs under
// Rosetta and the About panel surfaces a "View release notes" button so
// users can pick the right asset by hand.
//
// Returns empty when arch isn't confidently detectable; caller falls back
// to OS-only asset matching.
function detectArch(): string {
  const ua = navigator.userAgent.toLowerCase()
  if (ua.includes('arm64') || ua.includes('aarch64')) return 'arm64'
  if (ua.includes('x86_64') || ua.includes('x64') || ua.includes('wow64')) return 'amd64'
  return ''
}

// Pick a download asset matching the current platform. Mirrors
// updater.findPlatformAsset on the Go side (desktop/internal/updater/
// updater.go): match os+arch first, fall back to os-only. Empty when no
// asset matches — caller should fall back to the release html_url.
export function findPlatformAsset(assets: ReleaseAsset[]): string {
  const os = detectOS()
  if (!os) return ''
  const arch = detectArch()

  // Pass 1: exact os+arch. Skipped silently when arch is ''.
  if (arch) {
    for (const a of assets) {
      const name = a.name.toLowerCase()
      if (name.includes(os) && name.includes(arch)) return a.browser_download_url
    }
  }
  // Pass 2: os-only. Returns the first OS-matching asset — Apple Silicon
  // users typically land here and get whichever (arm64 vs amd64) appears
  // first in the GitHub release; the About panel's "View release notes"
  // button is the manual override.
  for (const a of assets) {
    if (a.name.toLowerCase().includes(os)) return a.browser_download_url
  }
  return ''
}

export async function checkForUpdates(currentVersion: string): Promise<UpdateCheckResult> {
  const release = await fetchLatestRelease()
  const cmp = compareVersions(release.tag_name, currentVersion)
  return {
    available: cmp > 0,
    current: currentVersion,
    latest: release.tag_name,
    release_url: release.html_url || RELEASES_URL,
    download_url: findPlatformAsset(release.assets),
    published_at: release.published_at,
  }
}

export const releasesPageURL = RELEASES_URL
