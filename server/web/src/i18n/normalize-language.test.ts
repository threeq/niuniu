import { describe, it, expect } from 'vitest'
import { normalizeLanguage, SUPPORTED_LANGUAGES } from './index'

// normalizeLanguage backs first-open browser-language detection: navigator
// hands us region/script variants that never equal our three supported codes,
// so this collapses them. Keep in sync with the inline bootstrap in index.html.
describe('normalizeLanguage', () => {
  it('maps English region variants to "en"', () => {
    for (const lng of ['en', 'en-US', 'en-GB', 'EN-AU', 'en-150']) {
      expect(normalizeLanguage(lng)).toBe('en')
    }
  })

  it('maps simplified / generic Chinese to "zh-CN"', () => {
    for (const lng of ['zh', 'zh-CN', 'zh-Hans', 'zh-Hans-CN', 'zh-SG']) {
      expect(normalizeLanguage(lng)).toBe('zh-CN')
    }
  })

  it('maps traditional-script / region Chinese to "zh-TW"', () => {
    for (const lng of ['zh-TW', 'zh-HK', 'zh-MO', 'zh-Hant', 'zh-Hant-TW']) {
      expect(normalizeLanguage(lng)).toBe('zh-TW')
    }
  })

  it('defaults to "en" for unsupported, empty, or nullish input', () => {
    for (const lng of ['fr', 'ja', 'de-DE', 'ko', '', null, undefined]) {
      expect(normalizeLanguage(lng)).toBe('en')
    }
  })

  it('always returns a supported language code', () => {
    for (const lng of ['pt-BR', 'ko', 'zh-Hant', 'en-US', 'xx']) {
      expect(SUPPORTED_LANGUAGES).toContain(normalizeLanguage(lng))
    }
  })
})
