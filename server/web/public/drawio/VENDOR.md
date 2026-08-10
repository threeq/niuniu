# Vendored draw.io webapp

- Version: v30.2.7
- Source: https://github.com/jgraph/drawio/archive/refs/tags/v30.2.7.tar.gz
- tarball SHA256: b735b902564fdd970b47a1177983545a5b4bf0fe60ac892f4ca047ed8042bff6
- NOTE: verify this SHA256 against the GitHub release digest once before committing a version bump.
- License: Apache-2.0 (draw.io Ltd / draw.io AG)

Self-hosted so the editor runs fully offline (no embed.diagrams.net at
runtime). Served same-origin at /drawio/ ; embedded into the server binary
via server/web `//go:embed all:dist`.

## Refresh

    cd server/web && pnpm vendor:drawio        # verify pinned sha, re-vendor
    cd server/web && node scripts/vendor-drawio.mjs --init  # bump version

## Removed (cloud connectors / servlet-only, not needed offline)

- dropbox.html
- onedrive3.html
- github.html
- gitlab.html
- teams.html
- connect
- monday-app-association.json
- WEB-INF
- META-INF
