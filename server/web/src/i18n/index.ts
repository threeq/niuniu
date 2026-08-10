import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { resources, NAMESPACES } from './resources'

export const SUPPORTED_LANGUAGES = ['zh-CN', 'en', 'zh-TW'] as const
export type Language = typeof SUPPORTED_LANGUAGES[number]

// localStorage 值是 RAW STRING（与 niuniu.theme 一致）。index.html 内联 FOUC
// 脚本直接读 raw 值。任何替换为 zustand persist 会改 JSON 序列化格式 —— 禁止。
export const LANGUAGE_STORAGE_KEY = 'niuniu.language'

export const LANGUAGE_LABELS: Record<Language, string> = {
  'zh-CN': '简体中文',
  en: 'English',
  'zh-TW': '繁體中文',
}

/**
 * Normalize any BCP-47 code the browser/localStorage hands us to one of the
 * three SUPPORTED_LANGUAGES. `navigator.language` yields region/script variants
 * (en-US, en-GB, zh-Hant, zh-HK, zh-Hans-CN…) that don't equal our codes, so
 * without this the detector falls through to fallbackLng and a first-time
 * English-browser user lands on Chinese instead of their own language.
 * Mapping: traditional-script/region zh (Hant/TW/HK/MO) → zh-TW; any other zh →
 * zh-CN; any en* → en; everything else (unsupported language, or no detectable
 * language at all) → en, the neutral default for non-Chinese-speaking users.
 * Wired as the detector's convertDetectedLanguage so it applies to both the
 * navigator lookup and any cached localStorage value (idempotent on the latter).
 * Mirror of the inline bootstrap in index.html — keep in sync.
 */
export function normalizeLanguage(lng: string | null | undefined): Language {
  const lower = (lng ?? '').toLowerCase()
  if (lower.startsWith('zh')) {
    return /hant|tw|hk|mo/.test(lower) ? 'zh-TW' : 'zh-CN'
  }
  if (lower.startsWith('en')) return 'en'
  return 'en'
}

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    fallbackLng: 'zh-CN',
    supportedLngs: SUPPORTED_LANGUAGES,
    interpolation: { escapeValue: false },
    detection: {
      order: ['localStorage', 'navigator'],
      lookupLocalStorage: LANGUAGE_STORAGE_KEY,
      caches: ['localStorage'],
      // Collapse en-US / zh-Hant / zh-HK / … to a supported code so first-open
      // browser-language detection actually resolves instead of falling back.
      convertDetectedLanguage: normalizeLanguage,
    },
    ns: NAMESPACES as unknown as string[],
    defaultNS: 'common',
    react: {
      // 静态 import 全量预加载使资源同步可用。useSuspense:false 让
      // useTranslation 在未就绪时返回原 key 而不是 throw promise，避免无
      // Suspense 边界时首屏红屏。
      useSuspense: false,
    },
  })

export default i18n
