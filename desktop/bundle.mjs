import * as esbuild from 'esbuild';
import { copyFileSync, mkdirSync, existsSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(__dirname, 'cmd/personal/frontend');
const distDir = join(frontendDir, 'dist');

// Clean dist
if (existsSync(distDir)) {
  require('fs').rmSync(distDir, { recursive: true });
}
mkdirSync(distDir, { recursive: true });

// Bundle the entry point with all dependencies
try {
  await esbuild.build({
    entryPoints: [join(frontendDir, 'bundle-entry.js')],
    bundle: true,
    format: 'iife',
    outfile: join(distDir, 'bundle.js'),
    platform: 'browser',
    target: 'es2020',
    minify: false,
    sourcemap: true,
    alias: {
      '@wailsio/runtime': join(__dirname, 'node_modules', '@wailsio', 'runtime', 'dist', 'calls.js')
    },
    define: {
      'process.env.NODE_ENV': '"production"'
    },
    logLevel: 'info'
  });
  console.log('Bundle created successfully');
} catch (e) {
  console.error('Bundle failed:', e);
  process.exit(1);
}

// Copy HTML and CSS
copyFileSync(
  join(frontendDir, 'index.html'),
  join(distDir, 'index.html')
);

copyFileSync(
  join(frontendDir, 'style.css'),
  join(distDir, 'style.css')
);

// Copy bindings directory for models (if needed)
function copyDir(src, dest) {
  if (!existsSync(src)) return;
  mkdirSync(dest, { recursive: true });
  for (const item of require('fs').readdirSync(src)) {
    const srcPath = join(src, item);
    const destPath = join(dest, item);
    if (require('fs').statSync(srcPath).isDirectory()) {
      copyDir(srcPath, destPath);
    } else {
      copyFileSync(srcPath, destPath);
    }
  }
}

// Copy bindings (for models)
copyDir(join(frontendDir, 'bindings'), join(distDir, 'bindings'));

console.log('Frontend build complete');
