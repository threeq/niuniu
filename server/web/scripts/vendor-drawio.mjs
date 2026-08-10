// Vendors the draw.io webapp static assets into server/web/public/drawio so the
// editor is fully self-hosted / offline (no embed.diagrams.net at runtime).
//
// Usage:
//   node scripts/vendor-drawio.mjs --init   # download, RECORD the tarball
//                                            # SHA256 into drawio-version.json,
//                                            # then vendor
//   node scripts/vendor-drawio.mjs          # verify pinned SHA256, then vendor
//
// Requires Node 20+ (global fetch) and a system `tar` (present on Win10+,
// macOS, Linux). Pinned version + trim list live in drawio-version.json.
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync, cpSync, mkdirSync, writeFileSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const webRoot = path.resolve(here, '..');
const cfgPath = path.join(here, 'drawio-version.json');
const cfg = JSON.parse(readFileSync(cfgPath, 'utf8'));
const init = process.argv.includes('--init');
const destDir = path.join(webRoot, 'public', 'drawio');

console.log(`[vendor-drawio] version=${cfg.version} url=${cfg.tarballUrl}`);
const res = await fetch(cfg.tarballUrl);
if (!res.ok) throw new Error(`download failed: HTTP ${res.status}`);
const buf = Buffer.from(await res.arrayBuffer());
const sha = createHash('sha256').update(buf).digest('hex');
console.log(`[vendor-drawio] sha256=${sha} bytes=${buf.length}`);

if (init) {
  cfg.tarballSha256 = sha;
  writeFileSync(cfgPath, JSON.stringify(cfg, null, 2) + '\n');
  console.log('[vendor-drawio] recorded sha256 into drawio-version.json');
} else if (cfg.tarballSha256 !== sha) {
  throw new Error(
    `sha256 mismatch: pinned ${cfg.tarballSha256} != downloaded ${sha}. ` +
      'Re-run with --init only if you intend to bump the pinned tarball.',
  );
}

const tmp = mkdtempSync(path.join(tmpdir(), 'drawio-'));
try {
  const tgz = path.join(tmp, 'drawio.tar.gz');
  writeFileSync(tgz, buf);
  // GitHub source tarball extracts to `drawio-<version-without-v>/`.
  execFileSync('tar', ['-xzf', tgz, '-C', tmp]);
  const inner = `drawio-${cfg.version.replace(/^v/, '')}`;
  const webapp = path.join(tmp, inner, 'src', 'main', 'webapp');

  rmSync(destDir, { recursive: true, force: true });
  mkdirSync(destDir, { recursive: true });
  cpSync(webapp, destDir, { recursive: true });

  for (const rel of cfg.remove) {
    rmSync(path.join(destDir, rel), { recursive: true, force: true });
  }

  // Entry alias: the embedded Go server serves the SPA via http.FileServer,
  // whose stdlib rule 301-redirects any request ending in `/index.html` to
  // `./` — which then falls through to the SPA and renders "Not Found" inside
  // the draw.io iframe. Expose a non-index entry `drawio.html` (identical
  // content; draw.io resolves its assets relative to the URL, so the filename
  // is irrelevant to draw.io). The frontend embeds /drawio/drawio.html.
  cpSync(path.join(destDir, 'index.html'), path.join(destDir, 'drawio.html'));

  const vendorDoc =
    `# Vendored draw.io webapp\n\n` +
    `- Version: ${cfg.version}\n` +
    `- Source: ${cfg.tarballUrl}\n` +
    `- tarball SHA256: ${cfg.tarballSha256 || sha}\n` +
    `- NOTE: verify this SHA256 against the GitHub release digest once before committing a version bump.\n` +
    `- License: Apache-2.0 (draw.io Ltd / draw.io AG)\n\n` +
    `Self-hosted so the editor runs fully offline (no embed.diagrams.net at\n` +
    `runtime). Served same-origin at /drawio/ ; embedded into the server binary\n` +
    `via server/web \`//go:embed all:dist\`.\n\n` +
    `## Refresh\n\n` +
    `    cd server/web && pnpm vendor:drawio        # verify pinned sha, re-vendor\n` +
    `    cd server/web && node scripts/vendor-drawio.mjs --init  # bump version\n\n` +
    `## Removed (cloud connectors / servlet-only, not needed offline)\n\n` +
    cfg.remove.map((r) => `- ${r}`).join('\n') +
    `\n`;
  writeFileSync(path.join(destDir, 'VENDOR.md'), vendorDoc);
  console.log(`[vendor-drawio] wrote ${destDir}`);
} finally {
  rmSync(tmp, { recursive: true, force: true });
}
