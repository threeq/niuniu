import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { useLanguageStore } from './language-store'
import i18n, { LANGUAGE_STORAGE_KEY } from '@/i18n'

describe('language-store', () => {
  beforeEach(async () => {
    localStorage.clear()
    await i18n.changeLanguage('zh-CN')
    // i18next LanguageDetector caches the language back into localStorage.
    // Clear after changeLanguage so per-test localStorage assertions only
    // observe writes performed by the test body itself.
    localStorage.clear()
    useLanguageStore.setState({ language: 'zh-CN' })
    document.documentElement.lang = 'zh-CN'
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('default language is zh-CN', () => {
    expect(useLanguageStore.getState().language).toBe('zh-CN')
  })

  it('setLanguage updates store, syncs i18next, sets <html lang>', async () => {
    await useLanguageStore.getState().setLanguage('en')
    expect(useLanguageStore.getState().language).toBe('en')
    expect(i18n.language).toBe('en')
    expect(document.documentElement.lang).toBe('en')
    expect(localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe('en')
  })

  it('setLanguage no-op for unsupported language', async () => {
    await useLanguageStore.getState().setLanguage('fr' as never)
    expect(useLanguageStore.getState().language).toBe('zh-CN')
    expect(localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBeNull()
  })

  it('storage event from another tab triggers cross-tab sync', async () => {
    // Simulate tab B writing the value
    localStorage.setItem(LANGUAGE_STORAGE_KEY, 'en')
    window.dispatchEvent(new StorageEvent('storage', {
      key: LANGUAGE_STORAGE_KEY,
      newValue: 'en',
      oldValue: 'zh-CN',
    }))
    // Allow the listener's microtask to settle
    await new Promise((r) => setTimeout(r, 0))
    expect(useLanguageStore.getState().language).toBe('en')
    expect(i18n.language).toBe('en')
  })

  it('rapid setLanguage calls: only the latest request wins', async () => {
    // Fire 3 in flight; the latest should be the resolved language
    const p1 = useLanguageStore.getState().setLanguage('en')
    const p2 = useLanguageStore.getState().setLanguage('zh-TW')
    const p3 = useLanguageStore.getState().setLanguage('zh-CN')
    await Promise.all([p1, p2, p3])
    expect(useLanguageStore.getState().language).toBe('zh-CN')
    expect(i18n.language).toBe('zh-CN')
  })
})
