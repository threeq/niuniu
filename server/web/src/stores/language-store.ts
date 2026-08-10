import { create } from 'zustand'
import { toast } from 'sonner'
import i18n, { SUPPORTED_LANGUAGES, LANGUAGE_STORAGE_KEY, type Language } from '@/i18n'

interface LanguageState {
  language: Language
  setLanguage: (lang: Language) => Promise<void>
}

function isSupported(v: string): v is Language {
  return (SUPPORTED_LANGUAGES as readonly string[]).includes(v)
}

function readInitial(): Language {
  return isSupported(i18n.language) ? i18n.language : 'zh-CN'
}

function applySideEffects(lang: Language) {
  document.documentElement.setAttribute('lang', lang)
  // Favicon swap: en uses default icon (N|N wordmark), zh-* uses 牛|牛 variant.
  // Mirror of bootstrap logic in index.html — keep in sync.
  const favicon = document.querySelector('link[rel="icon"]') as HTMLLinkElement | null
  if (favicon) {
    const href = lang === 'en' ? '/icon.svg' : '/icon-cn.svg'
    if (!favicon.href.endsWith(href)) favicon.href = href
  }
  // Sonner: clear stale toasts that were rendered in the previous language
  toast.dismiss()
}

// Monotonically increasing request id. Each setLanguage call captures its id
// at start; after the awaited changeLanguage resolves, the call only commits
// state if it is still the latest request. Prevents stale resolutions from
// a fast click-through (e.g. en → zh-TW → en) from winning over the latest.
let pendingRequestId = 0

export const useLanguageStore = create<LanguageState>((set) => {
  const initial = readInitial()
  applySideEffects(initial)

  return {
    language: initial,
    setLanguage: async (lang) => {
      if (!isSupported(lang)) return
      const myId = ++pendingRequestId
      await i18n.changeLanguage(lang)
      // A newer setLanguage call has superseded us; do not commit stale state.
      if (myId !== pendingRequestId) return
      // i18next LanguageDetector writes localStorage; we don't double-write.
      applySideEffects(lang)
      set({ language: lang })
    },
  }
})

// Cross-tab sync: when another tab writes niuniu.language to localStorage,
// mirror the change into this tab without reloading. Listener is registered
// at module top-level (rather than inside the zustand factory) so test
// re-imports via vi.resetModules() don't accumulate duplicate listeners.
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e) => {
    if (e.key !== LANGUAGE_STORAGE_KEY) return
    // newValue=null means the other tab cleared niuniu.language. We do NOT
    // fall back to a default — clearing storage in another tab is a rare
    // operation and a silent default-revert would surprise the user.
    if (e.newValue == null) return
    if (!isSupported(e.newValue)) return
    const newLang = e.newValue
    const myId = ++pendingRequestId
    void i18n.changeLanguage(newLang).then(() => {
      if (myId !== pendingRequestId) return
      applySideEffects(newLang)
      useLanguageStore.setState({ language: newLang })
    })
  })
}
