# Vendored skill: excalidraw-skill

This directory is a **vendored (in-repo) copy** of the upstream MIT-licensed
Claude skill `excalidraw` (repo `excalidraw-skill`). It is checked into the
niuniu repo so the skill can be projected into workspaces **local-first**,
without a runtime network fetch. This keeps builds reproducible and offline-safe.

The skill itself is unmodified upstream content. Only this `VENDOR.md` file is
niuniu-local — it is metadata about the vendoring, not part of the skill payload.

## Why this skill (selection rationale)

This is the **excalidraw half** of the "Vendor draw.io + excalidraw skill — only
emit editable files, don't embed an editor" workspace (sibling of the
`drawio-skill` and `fireworks-tech-graph` vendors).

The workspace named `edwingao28/excalidraw-toolkit` as the *class* of skill to
vendor ("excalidraw-toolkit 类 skill"), with an explicit selection preference:
prefer a skill that **exports PNG/SVG** (so the existing image-preview panel can
show the result), and a hard constraint: **do not introduce any editor runtime /
canvas dependency**.

`edwingao28/excalidraw-toolkit` fails the hard constraint: it is built on an MCP
server plus a **live Excalidraw canvas server** (auto-cloned/built, opened at
`localhost:3000`) whose tools mutate the running canvas. That *is* an embedded
editor/canvas runtime — exactly what the workspace rules out. (The other
standalone community option, `coleam00/excalidraw-diagram-skill`, fits
functionally but ships **without any LICENSE file**, so it is not cleanly
redistributable in-repo.)

`Agents365-ai/excalidraw-skill` was selected as the same-class skill that
satisfies every criterion:

- Generates editable `.excalidraw` JSON with plain Read/Write — no canvas, no
  MCP server, no running editor.
- **Exports PNG/SVG** via either the **Kroki render API** (`curl`, zero install,
  SVG) or the local **`excalidraw-brute-export-cli`** (headless Firefox, PNG +
  SVG). Both are offline/remote *export tools*, not an embedded editor (parallel
  to how `fireworks-tech-graph` treats its renderers as pure export backends).
- MIT licensed (same vendor family as the draw.io half), so it is cleanly
  redistributable in-repo.

## Source / provenance

| Field           | Value                                                                |
| --------------- | -------------------------------------------------------------------- |
| Upstream repo   | https://github.com/Agents365-ai/excalidraw-skill                     |
| Upstream commit | `5a7280cb378cb3734d44a4af60ac1ca0b52c2f51`                           |
| Commit date     | 2026-06-04 (`feat(excalidraw): add a formal review loop after verify-the-render (#18)`) |
| Skill version   | `1.3.0` (see `SKILL.md` frontmatter `metadata.version`)             |
| License         | MIT — `Copyright (c) 2026 Agents365-ai`, preserved in `./LICENSE`    |
| Vendored on     | 2026-06-28                                                          |

## What was vendored

The runtime skill payload (the contents of upstream `skills/excalidraw-skill/`,
flattened to this directory root so `SKILL.md` sits at the skill root — the same
layout used for `fireworks-tech-graph`), plus license, readmes, and demo assets:

- `SKILL.md` — skill entrypoint (frontmatter `name: excalidraw`)
- `references/schema-reference.md` — Excalidraw JSON schema reference
- `scripts/excalidraw_lib.py` — merges Excalidraw community-library items into a
  scene (network optional; fetches + caches from the official libraries repo)
- `assets/` — rendered demo `.excalidraw` + `.png` samples
- `README.md`, `README_CN.md`, `LICENSE`

### Intentionally excluded (upstream dev-only / site files)

`.github/`, `.gitignore`, and `docs/` (the GitHub-Pages site `index.html` /
`zh.html`). None are needed to project or run the skill.

## Dependency manifest (for the scene-projection / runtime-wiring step)

Split into a **zero-dependency source path** and an optional **export path**:

- **`.excalidraw` JSON generation** — zero external dependencies. Pure
  Read/Write text. The scene file is fully editable on its own (open at
  excalidraw.com or any Excalidraw client).
- **SVG export — Option A (Kroki API)** — needs only **`curl`**; rendered
  remotely via `https://kroki.io` (network required). **SVG only.**
- **PNG / SVG export — Option B (local CLI)** — `excalidraw-brute-export-cli`
  (`npm install -g excalidraw-brute-export-cli`) driving headless **Firefox**
  (`npx playwright install firefox`). Produces **PNG + SVG**. macOS needs a
  one-time `sed` patch (Ctrl→Meta key bindings; see `SKILL.md` → Prerequisites).
- **PDF** — **not supported** by this skill.
- **Community-library icons** — `scripts/excalidraw_lib.py` fetches items from
  `raw.githubusercontent.com/excalidraw/excalidraw-libraries` and caches to
  `/tmp` (network optional; only used when pulling library items).
- **Python scripts** — Python 3 standard library only.

### Output formats (for #474 scene projection)

| Editable source     | Preview exports            | Renderer dependency                     | Embedded editor? |
| ------------------- | -------------------------- | --------------------------------------- | ---------------- |
| `.excalidraw` (JSON)| SVG (Kroki) · PNG+SVG (CLI)| Kroki API (curl) **or** excalidraw-brute-export-cli (Firefox, local) | No |

PNG/SVG feed the existing image-preview panel directly; no native `.excalidraw`
renderer is needed in-product. Note PNG requires the local CLI path; the
zero-install Kroki path yields SVG only.

## How to upgrade this vendor

```bash
git clone --depth 1 https://github.com/Agents365-ai/excalidraw-skill.git /tmp/excalidraw-skill
DEST=docs/scenes/skills/excalidraw-skill
# refresh the skill payload (flattened) + license/readmes/assets
cp -r /tmp/excalidraw-skill/skills/excalidraw-skill/. "$DEST/"
cp /tmp/excalidraw-skill/{LICENSE,README.md,README_CN.md} "$DEST/"
cp -r /tmp/excalidraw-skill/assets "$DEST/assets"
# then update the "Upstream commit" / "Skill version" / "Vendored on" fields above
```

Record the new upstream commit hash so the vendor stays traceable to its source.
