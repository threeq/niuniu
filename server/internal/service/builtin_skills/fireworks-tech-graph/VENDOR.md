# Vendored skill: fireworks-tech-graph

This directory is a **vendored (in-repo) copy** of the upstream MIT-licensed
Claude skill `fireworks-tech-graph`. It is checked into the niuniu repo so the
skill can be projected into workspaces **local-first**, without a runtime
`npx skills add` network fetch. This keeps builds reproducible and offline-safe.

The skill itself is unmodified upstream content. Only this `VENDOR.md` file is
niuniu-local — it is metadata about the vendoring, not part of the skill payload.

## Source / provenance

| Field           | Value                                                            |
| --------------- | --------------------------------------------------------------- |
| Upstream repo   | https://github.com/yizhiyanhua-ai/fireworks-tech-graph          |
| Upstream commit | `8925283897d1281e3f12d68b67ad5f9ac7db1820`                      |
| Commit date     | 2026-06-03 (`Merge pull request #26 … feat/style-8-dark-luxury`)|
| Package version | `@yizhiyanhua-ai/fireworks-tech-graph@1.0.4` (see package.json) |
| License         | MIT — preserved verbatim in `./LICENSE`                         |
| npm page        | https://www.npmjs.com/package/@yizhiyanhua-ai/fireworks-tech-graph |
| Vendored on     | 2026-06-28                                                      |

## What was vendored

The complete npm-published payload (the `files` whitelist from upstream
`package.json`), plus `package.json` itself for version provenance:

- `SKILL.md` — skill entrypoint (frontmatter `name: fireworks-tech-graph`)
- `references/` — `style-1`…`style-8` style guides + `icons.md`,
  `style-diagram-matrix.md`, `svg-layout-best-practices.md`
- `scripts/` — `generate-diagram.sh`, `validate-svg.sh`, `test-all-styles.sh`,
  `generate-from-template.py`, `README.md`
- `templates/` — 10 SVG diagram templates (architecture, flowchart, sequence, …)
- `fixtures/` — sample input JSON for the styles
- `assets/samples/` — rendered PNG samples (style 1–8)
- `README.md`, `README.zh.md`, `LICENSE`, `package.json`

### Intentionally excluded (upstream dev-only files, not part of the runtime skill)

`.gitignore`, `.npmignore`, `agents/openai.yaml`, and the stray root sample
`agentloop-core.svg` — none are in the upstream `package.json` `files` list and
none are needed to project or run the skill.

## Dependency manifest (for step 2 — runtime wiring)

The skill is split into a **zero-dependency SVG path** and an optional
**PNG export path**:

- **SVG generation / validation** — zero external dependencies. Pure text +
  the bundled templates. `validate-svg.sh` only needs a POSIX shell.
- **PNG export** — requires **one of** the following renderers (the skill
  probes them in this preference order, see `scripts/generate-diagram.sh`):
  1. **cairosvg** (recommended — best CSS / `<foreignObject>` fidelity)
     `pip install cairosvg` — Python 3.
  2. **librsvg / `rsvg-convert`** (system package, fallback — may drop CSS
     styles or `<foreignObject>`)
     macOS: `brew install librsvg` · Debian/Ubuntu: `apt install librsvg2-bin`
  3. **puppeteer** (Node, alternative headless-Chrome renderer)
- **`generate-from-template.py`** — Python 3 standard library only.
- Upstream `engines.node`: `>=14.0.0` (only relevant if the puppeteer path is used).

## How to upgrade this vendor

```bash
git clone --depth 1 https://github.com/yizhiyanhua-ai/fireworks-tech-graph.git /tmp/fwtg
# copy the published payload over this directory, then update the
# "Upstream commit" / "Package version" / "Vendored on" fields above
rsync -a --delete \
  --include='SKILL.md' --include='README.md' --include='README.zh.md' \
  --include='LICENSE' --include='package.json' \
  --include='references/***' --include='scripts/***' --include='fixtures/***' \
  --include='templates/***' --include='assets/***' \
  --exclude='*' /tmp/fwtg/ docs/scenes/skills/fireworks-tech-graph/
```

Record the new upstream commit hash so the vendor stays traceable to its source.
