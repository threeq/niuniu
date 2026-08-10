import { readFile, readdir } from 'node:fs/promises'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const ROOT = join(__dirname, '../src/i18n/locales')
const GLOSSARY_FILE = join(__dirname, '../src/i18n/glossary.json')

// Must match eslint-rules/no-chinese-literal.js — keep these in sync.
const CJK = /[㐀-䶿一-鿿豈-﫿、。《》「」【】！，：；？]/u
const PLACEHOLDER = /\{\{\s*([^}]+?)\s*\}\}/g

function flatKeys(obj, prefix = '', acc = []) {
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) flatKeys(v, path, acc)
    else acc.push(path)
  }
  return acc
}

function valueByPath(obj, path) {
  return path.split('.').reduce((o, k) => o?.[k], obj)
}

function placeholders(s) {
  if (typeof s !== 'string') return new Set()
  const out = new Set()
  let m
  while ((m = PLACEHOLDER.exec(s)) !== null) out.add(m[1])
  return out
}

const errors = []
const namespaces = (await readdir(join(ROOT, 'zh-CN')))
  .filter((f) => f.endsWith('.json')).map((f) => f.replace(/\.json$/, ''))

for (const ns of namespaces) {
  const zh = JSON.parse(await readFile(join(ROOT, 'zh-CN', `${ns}.json`), 'utf8'))
  const en = JSON.parse(await readFile(join(ROOT, 'en', `${ns}.json`), 'utf8'))

  const zhKeys = new Set(flatKeys(zh))
  const enKeys = new Set(flatKeys(en))

  // 1. Key parity
  for (const k of zhKeys) if (!enKeys.has(k)) errors.push(`[${ns}] missing in en: ${k}`)
  for (const k of enKeys) if (!zhKeys.has(k)) errors.push(`[${ns}] extra in en: ${k}`)

  // 2. Placeholder parity
  for (const k of zhKeys) {
    if (!enKeys.has(k)) continue
    const zhPh = placeholders(valueByPath(zh, k))
    const enPh = placeholders(valueByPath(en, k))
    for (const p of zhPh) if (!enPh.has(p)) errors.push(`[${ns}] en/${k} missing placeholder {{${p}}}`)
    for (const p of enPh) if (!zhPh.has(p)) errors.push(`[${ns}] en/${k} extra placeholder {{${p}}}`)
  }

  // 3. __TODO__ guard
  for (const lang of ['zh-CN', 'en']) {
    const obj = lang === 'zh-CN' ? zh : en
    for (const k of flatKeys(obj)) {
      const v = valueByPath(obj, k)
      if (typeof v === 'string' && v.startsWith('__TODO__')) {
        errors.push(`[${lang}/${ns}] __TODO__ at ${k}`)
      }
    }
  }

  // 4. Residual CJK in en/*
  for (const k of enKeys) {
    const v = valueByPath(en, k)
    if (typeof v === 'string' && CJK.test(v)) {
      errors.push(`[en/${ns}] residual CJK in value at ${k}: "${v}"`)
    }
  }
}

// 5. Glossary consistency (warn-only, surfaced in errors)
const glossary = JSON.parse(await readFile(GLOSSARY_FILE, 'utf8'))
const violations = []
for (const ns of namespaces) {
  const zh = JSON.parse(await readFile(join(ROOT, 'zh-CN', `${ns}.json`), 'utf8'))
  const en = JSON.parse(await readFile(join(ROOT, 'en', `${ns}.json`), 'utf8'))
  for (const [zhTerm, mapping] of Object.entries(glossary)) {
    const enExpected = mapping.en
    for (const k of flatKeys(zh)) {
      const zhVal = valueByPath(zh, k)
      const enVal = valueByPath(en, k)
      if (typeof zhVal !== 'string' || typeof enVal !== 'string') continue
      // Case-insensitive + plural-tolerant: accept singular and common plural
      // forms (-s, -es, and y→ies). Avoids false positives for sentence-case
      // variants and English plural nouns.
      const expLow = enExpected.toLowerCase()
      const enLow = enVal.toLowerCase()
      const expLowIes = expLow.endsWith('y')
        ? expLow.slice(0, -1) + 'ies'
        : null
      const ok = enLow.includes(expLow)
              || enLow.includes(expLow + 's')
              || enLow.includes(expLow + 'es')
              || (expLowIes !== null && enLow.includes(expLowIes))
      if (zhVal.includes(zhTerm) && !ok) {
        violations.push(`[${ns}/${k}] glossary: "${zhTerm}" should map to "${enExpected}" — got "${enVal}"`)
      }
    }
  }
}

if (errors.length > 0 || violations.length > 0) {
  if (errors.length) console.error('[i18n-check] FAIL (errors):')
  for (const e of errors) console.error('  ' + e)
  if (violations.length) console.warn('[i18n-check] WARN (glossary):')
  for (const v of violations) console.warn('  ' + v)
  if (errors.length) process.exit(1)
}
console.log('[i18n-check] OK')
