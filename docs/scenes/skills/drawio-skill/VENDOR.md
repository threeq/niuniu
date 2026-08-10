# Vendored skill: drawio-skill

This directory is a **vendored (in-repo) copy** of the upstream MIT-licensed
Claude skill `drawio-skill`. It is checked into the niuniu repo so the skill can
be projected into workspaces **local-first**, without a runtime network fetch.
This keeps builds reproducible and offline-safe.

The skill itself is unmodified upstream content. Only this `VENDOR.md` file is
niuniu-local — it is metadata about the vendoring, not part of the skill payload.

## Why this skill (selection rationale)

This is the **draw.io half** of the "Vendor draw.io + excalidraw skill — only
emit editable files, don't embed an editor" workspace (step of the fireworks
integration epic, sibling of the `fireworks-tech-graph` vendor).

The workspace named `GBSOSS/ai-drawio` as the *class* of skill to vendor
("ai-drawio 类 skill"), with an explicit selection preference: prefer a skill
that **exports both PNG and SVG** (so the existing image-preview panel can show
the result with no native `.drawio` renderer), and a hard constraint: **do not
introduce any editor runtime / canvas dependency**.

`GBSOSS/ai-drawio` fails both: it renders by starting a Python HTTP server and
driving a Chrome browser (Claude-in-Chrome MCP) against the diagrams.net viewer,
and it emits HTML-embedded XML rather than exporting PNG/SVG files. That is a
browser/canvas render path, which the workspace explicitly rules out.

`Agents365-ai/drawio-skill` was selected as the same-class skill that satisfies
every criterion:

- Generates editable `.drawio` XML (the required re-editable artifact).
- **Exports PNG / SVG / PDF / JPG** via the native draw.io **desktop CLI** — an
  offline *export tool*, not an embedded editor or canvas (directly parallel to
  how `fireworks-tech-graph` uses `cairosvg`/`rsvg-convert`/`puppeteer` purely as
  renderers).
- With `--embed-diagram` (`-e`) the exported PNG/SVG/PDF embeds the full diagram
  XML, so the preview image *is itself* re-openable and re-editable in draw.io
  (double-extension convention: `name.drawio.png`).
- MIT licensed (same vendor family as the excalidraw half), so it is cleanly
  redistributable in-repo — unlike several otherwise-good community skills that
  ship with no LICENSE file.

## Source / provenance

| Field           | Value                                                       |
| --------------- | ----------------------------------------------------------- |
| Upstream repo   | https://github.com/Agents365-ai/drawio-skill                |
| Upstream commit | `ad1b9c03c64442a22a4d8943b719bc1e9b232f44`                  |
| Commit date     | 2026-06-27 (`docs: add edge label overlap guidance (#49)`)  |
| Skill version   | `1.14.0` (see `SKILL.md` frontmatter / `CHANGELOG.md`)      |
| License         | MIT — `Copyright (c) 2026 Agents365-ai`, preserved in `./LICENSE` |
| Vendored on     | 2026-06-28                                                  |

## What was vendored

The runtime skill payload (the contents of upstream `skills/drawio-skill/`,
flattened to this directory root so `SKILL.md` sits at the skill root — the same
layout used for `fireworks-tech-graph`), plus license, readmes, changelog, and
the demo assets:

- `SKILL.md` — skill entrypoint (frontmatter `name: drawio-skill`)
- `references/` — `diagram-types`, `shapes`, `style-presets`, `style-extraction`,
  `autolayout`, `troubleshooting`
- `scripts/` — `shapesearch.py`, `aiicons.py`, `autolayout.py`, `validate.py`,
  `repair_png.py`, `encode_drawio_url.py`, and the project-import extractors
  (`pyimports`/`jsimports`/`goimports`/`rustimports`/`pyclasses`)
- `data/` — `shape-index.json.gz` (10,000+ official shapes, gzipped),
  `lobe-icons.json` (AI/LLM brand logos), `SHAPE-INDEX-NOTICE.md`
- `styles/` — `built-in/` presets (`default`, `corporate`, `handdrawn`) + `schema.json`
- `assets/` — rendered demo `.drawio` + `.png` samples
- `README.md`, `README_CN.md`, `CHANGELOG.md`, `LICENSE`

### Intentionally excluded (upstream dev-only / site files)

`.github/`, `.gitignore`, `tests/`, and `docs/` (the GitHub-Pages site
`index.html`/`zh.html` plus the long-form human docs `USAGE`/`STYLE_PRESETS`/
`INSTALL_*`/`COMPARISON`). None are needed to project or run the skill — the
`references/` directory is the skill's own in-context documentation.

## Dependency manifest (for the scene-projection / runtime-wiring step)

Split into a **zero-dependency source path** and an optional **export path**:

- **`.drawio` XML generation** — zero external dependencies. Pure text. The
  diagram source is fully editable on its own.
- **PNG / SVG / PDF / JPG export** — requires the **draw.io desktop app CLI**
  (`drawio`, or `draw.io`) on `PATH`. No browser automation.
  - macOS: `brew install --cask drawio`
  - Windows / Linux: installer / `.deb`·`.rpm` from
    https://github.com/jgraph/drawio-desktop/releases — on Linux **do not use
    snap** (its AppArmor sandbox denies keyring access and crashes).
  - Sandbox note: some sandboxed environments crash the draw.io CLI even on
    `--version`; treat it as unavailable there and fall back to XML-only output
    or the browser-URL fallback (`scripts/encode_drawio_url.py`).
- **`--embed-diagram` (`-e`)** — embeds editable XML into the exported
  PNG/SVG/PDF (recommended so the preview image stays re-editable).
- **Optional auto-layout** — `scripts/autolayout.py` needs **Graphviz** (`dot`)
  on `PATH` (`brew install graphviz` / distro package).
- **AI/LLM brand logos** — `scripts/aiicons.py` resolves logos from the bundled
  `data/lobe-icons.json`; image styles reference the lobe-icons CDN unless
  `--embed` is used to inline them (network optional).
- **Vision self-check** — the workflow's render-review step needs a
  vision-capable model; it is gracefully skipped if unavailable.
- **Python scripts** — Python 3 standard library only.

### Output formats (for #474 scene projection)

| Editable source | Preview exports        | Renderer dependency        | Embedded editor? |
| --------------- | ---------------------- | -------------------------- | ---------------- |
| `.drawio` (XML) | PNG, SVG, PDF, JPG     | draw.io desktop CLI (local)| No               |

PNG/SVG feed the existing image-preview panel directly; no native `.drawio`
renderer is needed in-product.

## How to upgrade this vendor

```bash
git clone --depth 1 https://github.com/Agents365-ai/drawio-skill.git /tmp/drawio-skill
DEST=docs/scenes/skills/drawio-skill
# refresh the skill payload (flattened) + license/readmes/changelog/assets
cp -r /tmp/drawio-skill/skills/drawio-skill/. "$DEST/"
cp /tmp/drawio-skill/{LICENSE,README.md,README_CN.md,CHANGELOG.md} "$DEST/"
cp -r /tmp/drawio-skill/assets "$DEST/assets"
# then update the "Upstream commit" / "Skill version" / "Vendored on" fields above
```

Record the new upstream commit hash so the vendor stays traceable to its source.
