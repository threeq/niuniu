// Package curated embeds a hand-picked, vetted, Chinese-localized subset of the
// agency-agents persona catalog (github.com/msitarzewski/agency-agents, MIT).
//
// The catalog is an OPT-IN preset source for niuniu's agent registry: agents are
// installed only on explicit user click, never bundled by default. Each agent
// .md under agents/ is the upstream persona body verbatim; manifest.json adds the
// niuniu curation layer (分工分类 category + 汉化 display_name/description + emoji +
// upstream source_url provenance).
package curated

import "embed"

//go:embed manifest.json agents/*.md
var files embed.FS

// Files returns the embedded catalog filesystem (manifest.json + agents/*.md).
func Files() embed.FS { return files }
