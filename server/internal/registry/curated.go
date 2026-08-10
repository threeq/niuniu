package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/registry/curated"
)

// CuratedSource exposes a hand-picked, vetted, Chinese-localized subset of the
// agency-agents persona catalog (MIT) as an opt-in registry source. The catalog
// is embedded in the binary (see internal/registry/curated): no network fetch at
// runtime, offline-friendly, and what we vet is exactly what we ship.
//
// Agents are installed only on explicit user click. niuniu persists just
// name+description on import; the upstream color/emoji/vibe/tools frontmatter is
// dropped because Get returns the persona BODY only — so these personas bias
// behavior without granting any capability, matching the catalog's low-risk vet.
type CuratedSource struct {
	agents []AgentInfo
	bodies map[string]string // slug -> persona body (frontmatter stripped)
}

// manifestFile is the on-disk shape of curated/manifest.json.
type manifestFile struct {
	Agents []manifestEntry `json:"agents"`
}

type manifestEntry struct {
	Slug        string `json:"slug"`
	File        string `json:"file"`
	Category    string `json:"category"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Emoji       string `json:"emoji"`
	SourceURL   string `json:"source_url"`
}

// NewCuratedSource loads and parses the embedded catalog once. A malformed
// manifest or unreadable entry is logged and skipped rather than failing
// construction, so a bad catalog degrades to fewer (or zero) curated agents
// instead of breaking the whole registry. Errors here are caught by the
// package's own vet test, so production builds always carry a clean catalog.
func NewCuratedSource() *CuratedSource {
	s := &CuratedSource{bodies: map[string]string{}}
	if err := s.load(); err != nil {
		slog.Error("curated agent catalog failed to load", "err", err)
	}
	return s
}

func (s *CuratedSource) load() error {
	raw, err := curated.Files().ReadFile("manifest.json")
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var mf manifestFile
	if err := json.Unmarshal(raw, &mf); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	agents := make([]AgentInfo, 0, len(mf.Agents))
	for _, e := range mf.Agents {
		if e.Slug == "" || e.File == "" {
			slog.Warn("curated manifest entry missing slug/file; skipping", "slug", e.Slug, "file", e.File)
			continue
		}
		data, err := curated.Files().ReadFile("agents/" + e.File)
		if err != nil {
			slog.Warn("curated agent file missing; skipping", "slug", e.Slug, "file", e.File, "err", err)
			continue
		}
		// Strip the upstream frontmatter (name/description/color/emoji/vibe and,
		// for a few marketing personas, a tools: line). Only the body is exposed
		// for import so installed agents never carry capabilities.
		_, body, _ := parseFrontmatter(string(data))
		s.bodies[e.Slug] = body

		info := AgentInfo{
			Source:      "curated",
			Name:        e.Slug,
			Description: e.Description,
			DisplayName: e.DisplayName,
			Emoji:       e.Emoji,
			SourceURL:   e.SourceURL,
			Author:      "agency-agents",
		}
		if e.Category != "" {
			info.Tags = []string{e.Category}
		}
		agents = append(agents, info)
	}

	// Stable order: category, then slug — keeps the UI grouping deterministic.
	sort.SliceStable(agents, func(i, j int) bool {
		ci, cj := category(agents[i]), category(agents[j])
		if ci != cj {
			return ci < cj
		}
		return agents[i].Name < agents[j].Name
	})
	s.agents = agents
	return nil
}

func category(a AgentInfo) string {
	if len(a.Tags) > 0 {
		return a.Tags[0]
	}
	return ""
}

func (s *CuratedSource) Type() string { return "curated" }

func (s *CuratedSource) List(ctx context.Context) ([]AgentInfo, error) {
	return s.agents, nil
}

func (s *CuratedSource) Get(ctx context.Context, name string) (*AgentDetail, error) {
	for i := range s.agents {
		if s.agents[i].Name == name {
			return &AgentDetail{
				AgentInfo: s.agents[i],
				Content:   strings.TrimRight(s.bodies[name], "\n") + "\n",
			}, nil
		}
	}
	return nil, fmt.Errorf("curated agent %q not found", name)
}

// Refresh is a no-op: the catalog is embedded and immutable at runtime.
func (s *CuratedSource) Refresh(ctx context.Context) error { return nil }
