# Vendored skill: archify

This directory is a **vendored (in-repo) copy** of the upstream MIT-licensed
Agent Skill `archify`. It is checked into the niuniu repo so the skill can be
projected into workspaces **local-first**, without a runtime network fetch.
This keeps builds reproducible and offline-safe.

Unlike `drawio-skill` / `excalidraw-skill`, this copy is **NOT unmodified
upstream content** — the upstream update checker was removed. See
[Local-first modifications](#local-first-modifications) below for the exact diff.

## Why this skill (selection rationale)

Selected in issue #697 after evaluating whether archify adds anything over the
three drawing skills the `viz-architecture` scene already projects. Full analysis:
`docs/product-analysis/2026-09-06-archify-skill-evaluation.md`.

The short version: the existing three (`fireworks-tech-graph`, `drawio-skill`,
`excalidraw-skill`) are **one-shot drawing tools** — they produce a picture and
stop. Archify produces an artifact that stays tied to the code:

- **SRC evidence nodes** pin diagram nodes to a file + line range at one commit,
  so the diagram is "the architecture *at this revision*", not an impression.
  Directly meaningful in niuniu, where every workspace *is* a `git worktree` at a
  known commit.
- **Architecture Delta** (`compare architecture base.json head.json`) diffs two
  validated snapshots into added / removed / changed / moved / rerouted facts with
  a machine receipt — an architecture-level input for the kanban 审查 column. It
  states authored topology differences only; it infers no impact, risk, or merge
  safety.
- **A real validation gate**: `validate` / `deliver` emit stable rule codes plus
  `subject` / `evidence` / `supportedFixes`, and `deliver` atomically replaces the
  target only after every check passes (a failed delivery preserves the previous
  last-good artifact). Same design taste as `internal/harness/`: machine-readable
  failure instead of an unstructured retry guess.

It is complementary, not a replacement: `drawio-skill` / `excalidraw-skill` remain
the answer for "a source file someone else can keep editing by hand", and
`fireworks-tech-graph` for a lightweight static image to paste into a doc.

## Source / provenance

| Field           | Value                                                              |
| --------------- | ------------------------------------------------------------------ |
| Upstream repo   | https://github.com/tt-a1i/archify                                  |
| Upstream commit | `ed7f4d4b48d4424d36edfed8043de3de8dea6b45`                         |
| Commit date     | 2026-09-06 (`fix(cli): prevent non-HTML output overwrites without restricting directories (#322)`) |
| Skill version   | `2.17` (see `SKILL.md` frontmatter `metadata.version`)             |
| License         | MIT — `Copyright (c) 2026 tt-a1i`, preserved in `./LICENSE`        |
| Upstream lineage| Based on `Cocoon-AI/architecture-diagram-generator` (MIT, v1.0)    |
| Vendored on     | 2026-09-06                                                          |

## What was vendored

The contents of upstream `archify/` (the skill payload — `SKILL.md` already sits
at its root, so no flattening was needed), minus the exclusions below:

- `SKILL.md` — skill entrypoint (frontmatter `name: archify`)
- `bin/archify.mjs` — the zero-dependency CLI (`doctor` `guide` `validate`
  `preview` `deliver` `compare` `brands` `demo`)
- `renderers/` — per-type renderers (`architecture` / `workflow` / `sequence` /
  `dataflow` / `lifecycle` / `shared`)
- `assets/template.html` — **the viewer template the renderer emits into**; not a
  sample, do not treat it as excludable HTML (see the Makefile note)
- `schemas/` — JSON IR schemas per diagram type + `common.schema.json`
- `examples/*.json` — the typed JSON sources `SKILL.md` step 2 requires reading
- `references/` `recipes/` — in-context authoring documentation
- `delta/` — Architecture Delta compare runtime
- `brand-marks/` — offline brand-mark catalog
- `migrations/` `scripts/` — schema migration + build/check helpers
- `LICENSE`, `THIRD_PARTY_NOTICES.md`, `package.json`, `skill-release.json`

### Intentionally excluded

| Path | Size | Why |
| ---- | ---: | --- |
| `test/` | 1.6 MB | Upstream test suite — not needed to run the skill |
| `examples/*.html` (5 files) | 3.5 MB | Pre-rendered demo diagrams. Each archify output is a ~700 KB self-contained HTML, so five of them dwarf the actual skill |
| `package-lock.json` | 5 KB | Lockfile for devDependencies only; the skill has **zero runtime dependencies** |
| `scripts/check-update.mjs`, `scripts/update-contract.mjs` | 100 KB | Update checker — removed, see below |

Upstream is 7.5 MB; this vendored copy is **2.4 MB**.

> These exclusions are applied **here, in the source tree**, not by a suffix rule
> in `make builtin-skills-sync`. A blanket `*.html` exclusion in that target would
> silently drop `assets/template.html` and ship a broken renderer.

## Local-first modifications

Upstream ships an update checker that performs a background HTTPS GET against a
release manifest, and `SKILL.md` **instructs the agent to run it** after the first
candidate is produced. That conflicts with two niuniu contracts: the product
promise that no data leaves the machine unless the user connects an external
source, and `scene_skills.go`'s design premise that skill projection is a pure
local file copy that never spawns an installer or touches the network.

Setting `ARCHIFY_UPDATE_CHECK_DISABLED=1` would suppress the request, but the
agent would still read the instruction and reason about it. So both were removed:

1. **Deleted** `scripts/check-update.mjs` and `scripts/update-contract.mjs`
   (which exported `DEFAULT_MANIFEST_URL`). Verified nothing else references
   them — no `bin/`, `renderers/`, `delta/`, or `package.json` entry does, so the
   CLI is unaffected. `node bin/archify.mjs doctor` passes all checks afterwards.
2. **Rewrote** the `## Update awareness` section of `SKILL.md` to state that this
   copy is vendored, is refreshed only by a repo-side vendor bump, and that the
   agent must never look for an update checker or fetch a manifest.

**Remaining network capability** (intentionally kept, not a phone-home):
`renderers/shared/brand-marks.mjs` can fetch a logo over HTTP(S) — but only for an
**unknown brand from a URL the user supplies**. Known brands resolve offline from
the bundled `brand-marks/` catalog via `bin/archify.mjs brands "<name>" --json`.
This is user-initiated, equivalent to a `WebFetch`, and never fires on its own.

## Dependency manifest

- **Node.js ≥ 18** — required for the whole pipeline (generate / validate /
  deliver / compare). `package.json` declares **no `dependencies`**; ajv, parse5,
  saxes and simple-icons are devDependencies used only in upstream's build, so the
  vendored copy runs with nothing installed.
  - Users driving the `claude` CLI effectively always have Node (Claude Code is
    itself a Node application). `codex` / `qwen` / `goose` / `cursor` users may not.
  - `node bin/archify.mjs doctor` is the availability probe. The scene prompt
    requires the agent to run it and **fall back to `fireworks-tech-graph` with an
    explicit heads-up** if it fails — matching the scene's existing
    "degrade gracefully, never hard-error" rule for missing `cairosvg`.
- **No browser, no editor runtime, no canvas dependency** — same constraint the
  drawio/excalidraw vendors were selected under. `preview` is an opt-in
  loopback-only desktop mode that is not used by the scene's quick actions.

## Output & preview notes

- One delivered diagram is a **~700 KB self-contained HTML**; an Architecture
  Delta is ~2 MB. Artifacts belong in the workspace directory and should be
  registered in `.niuniu/artifacts.json` — **not committed to a repo**.
- The artifact preview panel renders HTML in an iframe with `sandbox="allow-scripts"`
  (`server/web/src/pages/workspaces/components/file-preview.tsx`). Scripts run, so
  the interactive viewer (search / focus / route / theme) works in-product. But the
  sandbox has no `allow-downloads` → **the viewer's Export menu cannot save files**,
  and no `allow-same-origin` → clipboard copy and localStorage theme memory are
  inert. The scene prompt therefore tells the agent to export PNG/SVG *files* via
  the CLI rather than relying on in-viewer export.

## How to upgrade this vendor

```bash
git clone --depth 1 --filter=blob:none --sparse https://github.com/tt-a1i/archify.git /tmp/archify-src
cd /tmp/archify-src && git sparse-checkout set archify && git log -1 --format=%H

DEST=docs/scenes/skills/archify
rm -rf "$DEST" && mkdir -p "$DEST"
cp -r /tmp/archify-src/archify/. "$DEST/"

# re-apply the exclusions
rm -rf "$DEST/test"
rm -f "$DEST"/examples/*.html "$DEST/package-lock.json"
rm -f "$DEST/scripts/check-update.mjs" "$DEST/scripts/update-contract.mjs"

# re-apply the SKILL.md "Update awareness" rewrite (see section above), then:
(cd "$DEST" && node bin/archify.mjs doctor)   # must print "Archify is ready."
make builtin-skills-sync
(cd server/internal/service/builtin_skills/archify && node bin/archify.mjs doctor)
```

Record the new upstream commit hash above so the vendor stays traceable, and
re-check whether upstream reintroduced an update-check instruction in `SKILL.md`.
