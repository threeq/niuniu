import { api } from './api'

interface EditorConfig {
  vscode_mode: string
  vscode_remote_url: string
}

let cachedConfig: EditorConfig | null = null
let cacheTime = 0
let inflight: Promise<EditorConfig> | null = null
const CACHE_TTL = 30_000

async function getEditorConfig(): Promise<EditorConfig> {
  if (cachedConfig && Date.now() - cacheTime < CACHE_TTL) {
    return cachedConfig
  }
  if (!inflight) {
    inflight = api.get<{ editor: EditorConfig }>('/config').then(cfg => {
      cachedConfig = cfg.editor ?? { vscode_mode: 'local', vscode_remote_url: '' }
      cacheTime = Date.now()
      inflight = null
      return cachedConfig
    }).catch(err => {
      inflight = null
      throw err
    })
  }
  return inflight
}

export function invalidateEditorConfigCache() {
  cachedConfig = null
  inflight = null
}

export async function openInVSCode(path: string) {
  const config = await getEditorConfig()
  if (config.vscode_mode === 'remote' && config.vscode_remote_url) {
    let url: URL
    try {
      url = new URL(config.vscode_remote_url)
    } catch {
      return
    }
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return
    const base = url.origin + url.pathname.replace(/\/+$/, '')
    window.open(`${base}/?folder=${encodeURIComponent(path)}`, '_blank')
  } else {
    window.open(`vscode://file/${path}?windowId=_blank`)
  }
}
