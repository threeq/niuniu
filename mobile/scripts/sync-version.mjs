#!/usr/bin/env node
// sync-version.mjs — write the current git tag into mobile/app.json so the
// EAS build embeds it and Constants.expoConfig?.version exposes it at
// runtime (rendered in the About row of the Settings screen).
//
// EAS validates expo.version as plain semver (M.m.p), so we split git
// describe into two fields:
//
//   - expo.version              → semver only, e.g. "0.1.2"
//   - expo.extra.niuniuBuildId  → full git describe, e.g. "v0.1.2-3-gabcdef-dirty"
//
// Run before every native build. The Makefile mobile-build target wires it
// in; running by hand: `node scripts/sync-version.mjs`.
//
// Idempotent: re-running with the same git state writes the same content.
// Skips writes when the file already matches to keep `git status` clean
// during local dev.

import { execSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const APP_JSON = resolve(__dirname, '..', 'app.json');

function gitDescribe() {
  try {
    return execSync('git describe --tags --always --dirty', {
      cwd: resolve(__dirname, '..', '..'),
      encoding: 'utf8',
    }).trim();
  } catch {
    return '';
  }
}

function extractSemverFromTag(describe) {
  // git describe shapes:
  //   "v0.1.2"                 → tagged → semver "0.1.2"
  //   "v0.1.2-3-gabcdef"       → 3 commits past tag → semver still "0.1.2"
  //   "v0.1.2-3-gabcdef-dirty" → past tag + uncommitted → still "0.1.2"
  //   "abcdef" / "abcdef-dirty" → no tag, SHA only → returns null
  // We only overwrite expo.version when an actual tag is reachable; for
  // tagless dev builds we keep whatever the human last set (typically the
  // upcoming release version) and only stamp the build id.
  const match = describe.match(/^v?(\d+)\.(\d+)\.(\d+)/);
  if (!match) return null;
  return `${match[1]}.${match[2]}.${match[3]}`;
}

function main() {
  const describe = gitDescribe();
  const semverFromTag = extractSemverFromTag(describe);
  const buildId = describe || 'dev';

  const raw = readFileSync(APP_JSON, 'utf8');
  const json = JSON.parse(raw);
  json.expo ??= {};
  json.expo.extra ??= {};

  const before = { version: json.expo.version, buildId: json.expo.extra.niuniuBuildId };

  // Only overwrite expo.version when git has a tag — otherwise we'd downgrade
  // the human-managed version (e.g. "1.0.0") to "0.0.0" on a tagless repo.
  if (semverFromTag) {
    json.expo.version = semverFromTag;
  }
  json.expo.extra.niuniuBuildId = buildId;

  if (before.version === json.expo.version && before.buildId === buildId) {
    console.log(`sync-version: app.json already at ${json.expo.version} (${buildId})`);
    return;
  }

  // Preserve trailing newline + 2-space indent (matches the file's existing
  // shape; verified by reading app.json before writing).
  const formatted = JSON.stringify(json, null, 2) + '\n';
  writeFileSync(APP_JSON, formatted);
  console.log(`sync-version: wrote ${json.expo.version} (${buildId}) to app.json`);
}

main();
