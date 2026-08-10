// Canonical external links shared across the SPA. Centralised so a slug change
// is a one-line edit, not a grep-and-replace across components.

// Public privacy/telemetry notice for the personal-edition anonymous "app
// opened" statistics (Epic #329). The in-repo source of truth is
// docs/telemetry-privacy.md (#368); this is its published home on the official
// site, matching the existing /docs/* convention (see system-deps-settings.tsx).
// Referenced from the Settings privacy switch and the first-run ConsentGate
// telemetry section.
export const TELEMETRY_PRIVACY_URL = 'https://www.niu6ai.com/docs/telemetry-privacy'
