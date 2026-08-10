import * as opencc from 'opencc-js'
import { readFile, writeFile, readdir, mkdir } from 'node:fs/promises'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const SRC = join(__dirname, '../src/i18n/locales/zh-CN')
const DST = join(__dirname, '../src/i18n/locales/zh-TW')
const OVERRIDES_FILE = join(__dirname, '../src/i18n/opencc-overrides.json')

const converter = opencc.Converter({ from: 'cn', to: 'twp' })

const overrides = JSON.parse(await readFile(OVERRIDES_FILE, 'utf8'))
// Order matters for .split().join() chains: longer keys must run first to
// avoid shorter keys eating substrings (e.g. replacing "工作" before
// "工作區" would corrupt the latter).
const overrideEntries = Object.entries(overrides).sort(([a], [b]) => b.length - a.length)

function applyOverrides(s) {
  for (const [from, to] of overrideEntries) {
    s = s.split(from).join(to)
  }
  return s
}

function convertDeep(node) {
  if (typeof node === 'string') return applyOverrides(converter(node))
  if (Array.isArray(node)) return node.map(convertDeep)
  if (node && typeof node === 'object') {
    return Object.fromEntries(
      Object.entries(node).map(([k, v]) => [k, convertDeep(v)]),
    )
  }
  return node
}

await mkdir(DST, { recursive: true })
const files = (await readdir(SRC)).filter((f) => f.endsWith('.json'))
for (const f of files) {
  const src = JSON.parse(await readFile(join(SRC, f), 'utf8'))
  await writeFile(
    join(DST, f),
    JSON.stringify(convertDeep(src), null, 2) + '\n',
    'utf8',
  )
}
console.log(`[i18n-build-twn] generated ${files.length} zh-TW files (overrides=${overrideEntries.length})`)
