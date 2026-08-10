import { readFile, readdir, writeFile } from 'node:fs/promises'
import { join, dirname, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const SRC = join(__dirname, '../src')
const OUT = join(__dirname, 'i18n-namespace-assignments.json')

const CJK = /[㐀-䶿一-鿿豈-﫿、。《》「」【】！，：；？]/u

const ASSIGNMENT_RULES = [
  // [glob-match-fn, namespace]
  [(p) => p.startsWith('pages/login/') || p.startsWith('components/auth/'), 'login'],
  [(p) => p.startsWith('pages/workspaces/') ||
          (p.startsWith('components/dialogs/') && /workspace/i.test(p)) ||
          p === 'lib/workspace-status.ts', 'workspaces'],
  [(p) => p.startsWith('pages/projects/') || p.startsWith('pages/project/') ||
          p.startsWith('components/issue/') || p.startsWith('components/shared/kanban/') ||
          (p.startsWith('components/dialogs/') && /(issue|project)/i.test(p)) ||
          p === 'lib/hooks/use-learnings.ts', 'projects'],
  [(p) => p.startsWith('pages/repositories/') ||
          (p.startsWith('components/dialogs/') && /repository/i.test(p)), 'repositories'],
  [(p) => p.startsWith('pages/schedules/'), 'schedules'],
  [(p) => p.startsWith('pages/settings/orgs/') || p.startsWith('components/team/') ||
          p === 'stores/org-store.ts' || p === 'stores/team-store.ts', 'orgs'],
  [(p) => p.startsWith('pages/settings/'), 'settings'],
  [(p) => p === 'components/layout/global-nav.tsx' ||
          p === 'components/layout/theme-toggle.tsx', 'nav'],
  [(p) => p.startsWith('components/dialogs/'), 'dialogs'],
  // Default: common (utilities, stores, types, shared error-boundary, etc.)
  [() => true, 'common'],
]

function assignNamespace(relPath) {
  for (const [match, ns] of ASSIGNMENT_RULES) {
    if (match(relPath)) return ns
  }
  return 'common'
}

async function walk(dir, acc = []) {
  for (const ent of await readdir(dir, { withFileTypes: true })) {
    const full = join(dir, ent.name)
    if (ent.isDirectory()) await walk(full, acc)
    else if (/\.(ts|tsx)$/.test(ent.name) &&
             !/\.test\.(ts|tsx)$/.test(ent.name) &&
             !full.includes('/i18n/locales/') &&
             !full.includes('\\i18n\\locales\\')) acc.push(full)
  }
  return acc
}

const files = await walk(SRC)
const phraseCounts = new Map()
const fileAssignments = {}

for (const f of files) {
  const rel = relative(SRC, f).replaceAll('\\', '/')
  const text = await readFile(f, 'utf8')
  // Extract any string literal or JSXText with CJK content
  const matches = text.match(/['"`]([^'"`]*[㐀-鿿][^'"`]*)['"`]/g) || []
  if (matches.length === 0) continue
  const ns = assignNamespace(rel)
  fileAssignments[rel] = { namespace: ns, hits: matches.length }
  for (const m of matches) {
    const phrase = m.slice(1, -1).trim()
    if (!CJK.test(phrase)) continue
    phraseCounts.set(phrase, (phraseCounts.get(phrase) || 0) + 1)
  }
}

const commonCandidates = [...phraseCounts.entries()]
  .filter(([, c]) => c >= 3)
  .sort((a, b) => b[1] - a[1])
  .map(([phrase, count]) => ({ phrase, count }))

await writeFile(OUT, JSON.stringify({
  generatedAt: new Date().toISOString(),
  fileAssignments,
  commonCandidates,
}, null, 2) + '\n', 'utf8')

console.log(`[i18n-analyze] ${files.length} files scanned`)
console.log(`[i18n-analyze] ${Object.keys(fileAssignments).length} files contain CJK`)
console.log(`[i18n-analyze] ${commonCandidates.length} phrases with freq >= 3`)
console.log(`[i18n-analyze] output: ${OUT}`)
