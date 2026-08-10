// Same-origin embed of the self-hosted draw.io webapp. The static assets are
// vendored under `server/web/public/drawio/` and ship offline via the SPA's
// `//go:embed all:dist`. We speak draw.io's JSON postMessage protocol in embed
// mode; `offline=1` disables cloud storage and `stealth=1` blocks every
// external network call, so the editor is fully local. Save/Exit chrome is
// hidden — persistence + "send to Agent" is the panel's single CTA.
// `configure=1` lets us turn off XML compression before init so the exported
// `.drawio` source stays human-diffable.
//
// Entry point is `drawio.html` (a copy of draw.io's `index.html`, produced by
// scripts/vendor-drawio.mjs), NOT `index.html`. The embedded Go server serves
// the SPA via `http.FileServer`, whose stdlib rule 301-redirects any request
// ending in `/index.html` to `./` — which then falls through to the SPA and
// renders a "Not Found" route inside the iframe. Vite dev serves index.html
// directly and hides this, so the path MUST stay a non-index filename to work
// in the shipped binary. Do not change `drawio.html` back to `index.html`.
export const DRAWIO_EMBED_SRC =
  '/drawio/drawio.html?embed=1&proto=json&configure=1&spin=1&modified=1&libraries=1&noSaveBtn=1&noExitBtn=1&offline=1&stealth=1';
