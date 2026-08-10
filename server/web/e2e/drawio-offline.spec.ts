import { test, expect } from '@playwright/test';

// Guards the localization goal: the vendored draw.io must boot entirely from
// same-origin assets with ZERO cross-origin calls.
// Three assertions:
//   1. No request ever reaches *.diagrams.net (aborted at the route level).
//   2. After a blank-diagram boot, the full set of cross-origin requests is empty.
//   3. A deep vendored asset (/drawio/js/app.min.js) is served as real JavaScript,
//      not a fallback HTML 404 page — guards against a broken/missing vendored tree.
test('draw.io boots offline from local assets with no cross-origin calls', async ({ page }) => {
  const pageOrigin = 'http://localhost:5173';
  const crossOriginHits: string[] = [];

  // Intercept every request: abort diagrams.net hits, record any other cross-origin request.
  await page.route('**/*', (route) => {
    const url = route.request().url();

    // Only classify requests whose origin differs from the page origin.
    let requestOrigin: string;
    try {
      requestOrigin = new URL(url).origin;
    } catch {
      return route.continue();
    }

    if (requestOrigin === pageOrigin) {
      // Same-origin (localhost:5173, Vite HMR, etc.) — always allow.
      return route.continue();
    }

    // True cross-origin request: record it.
    crossOriginHits.push(url);

    if (url.includes('diagrams.net')) {
      // Hard-abort diagrams.net so the test cannot accidentally pass due to
      // a network error being swallowed.
      return route.abort();
    }

    // Any other cross-origin host: abort too (we want zero cross-origin).
    return route.abort();
  });

  // Entry is drawio.html (not index.html): the embedded Go server 301-redirects
  // */index.html to ./, which strands the iframe on the SPA "Not Found".
  await page.goto('/drawio/drawio.html?offline=1&stealth=1&db=0');

  // Assert 1 + 2: mxClient boot means the local bundle loaded; cross-origin set must be empty.
  await page.waitForFunction(() => 'mxClient' in window, null, { timeout: 30_000 });

  expect(
    crossOriginHits,
    `unexpected cross-origin requests during offline boot: ${crossOriginHits.join(', ')}`,
  ).toEqual([]);

  // Assert 3: deep vendored asset must be real JavaScript, not a fallback HTML page.
  const assetRes = await page.request.get('/drawio/js/app.min.js');
  expect(assetRes.ok(), '/drawio/js/app.min.js returned non-2xx status').toBe(true);
  const contentType = assetRes.headers()['content-type'] ?? '';
  expect(
    contentType,
    `/drawio/js/app.min.js content-type "${contentType}" does not contain "javascript" — vendored tree may be broken or missing`,
  ).toContain('javascript');
});
