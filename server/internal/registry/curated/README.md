# Curated agent catalog

The persona files under `agents/` are a hand-picked subset vendored verbatim from
**[msitarzewski/agency-agents](https://github.com/msitarzewski/agency-agents)**,
licensed under the **MIT License** (Copyright © agency-agents contributors).

niuniu exposes them as an **opt-in** preset source in the agent registry
(`CuratedSource`, source key `curated`). Nothing here is installed automatically:
agents are written to a user's library only on an explicit "Import" click on the
Agents page.

## Layout

- `agents/<slug>.md` — upstream persona body + frontmatter, unmodified. Each
  filename is the niuniu slug (the agent identifier on import).
- `manifest.json` — the niuniu curation layer: 分工分类 `category`, 汉化
  `display_name`/`description` (sourced from the upstream
  `scripts/i18n/agent-names-zh.json`), a decorative `emoji`, and the upstream
  `source_url` recorded as provenance on import.

## What gets installed

On import, only the persona **body** plus a niuniu-managed `name`/`description`
frontmatter is persisted (see `CuratedSource.Get`). The upstream
`color`/`emoji`/`vibe` and any `tools:` line are **not** carried through, so these
personas bias behavior without granting capabilities. `TestCuratedSource_GetReturnsBodyOnly`
locks that guarantee.

## Updating the catalog

Add/remove a `.md` under `agents/` and a matching entry in `manifest.json`
(keep `slug` == filename stem). `TestCuratedSource_Loads` enforces that every
manifest entry resolves to an embedded, parseable file with a known category and
a provenance URL.
