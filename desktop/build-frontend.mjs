import * as esbuild from 'esbuild';
import { copyFileSync, mkdirSync, existsSync, readdirSync, statSync, rmSync } from 'fs';
import { join, dirname, relative } from 'path';
import { fileURLToPath } from 'url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(__dirname, 'cmd/personal/frontend');
const distDir = join(frontendDir, 'dist');

// Clean dist
if (existsSync(distDir)) {
  rmSync(distDir, { recursive: true });
}
mkdirSync(distDir, { recursive: true });

// Build the main script that imports bindings
const mainScript = `
import * as App from './bindings/github.com/niuniu-dev/niuniu-desktop/app.js';

window.App = App;

// Make functions globally available for the HTML to use
window.GetConnections = App.GetConnections;
window.GetDiscoveredInstances = App.GetDiscoveredInstances;
window.AddConnection = App.AddConnection;
window.RemoveConnection = App.RemoveConnection;
window.SetDefaultConnection = App.SetDefaultConnection;
window.ConnectByID = App.ConnectByID;
window.ConnectToAddress = App.ConnectToAddress;
window.RefreshTrayMenu = App.RefreshTrayMenu;

console.log('Wails bindings loaded');
`;

await esbuild.build({
  entryPoints: [{
    stdin: mainScript,
    bundle: true,
    format: 'esm',
    outfile: join(distDir, 'bundle.js'),
  }],
  platform: 'browser',
  target: 'es2020',
  minify: false,
  sourcemap: true,
  loader: {
    '.js': 'jsx',
  },
});

// Copy HTML
copyFileSync(
  join(frontendDir, 'index.html'),
  join(distDir, 'index.html')
);

// Copy CSS
copyFileSync(
  join(frontendDir, 'style.css'),
  join(distDir, 'style.css')
);

// Copy bindings directory
function copyDir(src, dest) {
  if (!existsSync(src)) return;
  mkdirSync(dest, { recursive: true });
  for (const item of readdirSync(src)) {
    const srcPath = join(src, item);
    const destPath = join(dest, item);
    if (statSync(srcPath).isDirectory()) {
      copyDir(srcPath, destPath);
    } else {
      copyFileSync(srcPath, destPath);
    }
  }
}

// Copy runtime from node_modules
const runtimeSrc = join(__dirname, 'node_modules', '@wailsio', 'runtime', 'dist');
const runtimeDest = join(distDir, 'runtime');
if (existsSync(runtimeSrc)) {
  copyDir(runtimeSrc, runtimeDest);
}

// Copy bindings
copyDir(join(frontendDir, 'bindings'), join(distDir, 'bindings'));

console.log('Frontend built successfully to', distDir);
