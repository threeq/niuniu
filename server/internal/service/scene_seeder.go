// Package service: SceneSeeder — seeds builtin scenes from embedded YAML at
// boot time.
//
// YAML files live in docs/scenes/builtin/*.yaml (source of truth, human-edited)
// and are mirrored into server/internal/service/builtin_scenes/ via the
// `make builtin-scenes-sync` Makefile target. The mirrored copy is what
// //go:embed picks up — docs/ is outside the server module root so it can't
// be embedded directly.
//
// Seeding semantics (spec §2.4 + UpsertBuiltinScene query):
//   - Each YAML's slug → row with (owner_type='user', owner_id=0, source='builtin').
//   - On every boot, UpsertBuiltinScene refreshes the row IF AND ONLY IF the
//     existing row has source='builtin'. User forks (which copy the slug into
//     source_slug but live under a different (owner_type, owner_id) tuple) are
//     never touched. A user who modified the *builtin* row directly via SQL
//     (impossible through the API since SceneService.Update guards on source)
//     would get clobbered — that's acceptable.
//   - Failures on individual files are logged and skipped; one broken YAML
//     should not block niuniu boot.
package service

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
	"gopkg.in/yaml.v3"
)

//go:embed builtin_scenes/*.yaml
var builtinScenesFS embed.FS

// builtinSceneYAML is the on-disk YAML schema. It mirrors SceneDefinition with
// the addition of top-level metadata fields (slug, display_name, tags, etc.)
// that map to scenes table columns rather than the JSON-encoded definition
// blob.
type builtinSceneYAML struct {
	Slug                string               `yaml:"slug"`
	DisplayName         string               `yaml:"display_name"`
	Description         string               `yaml:"description"`
	Tags                []string             `yaml:"tags"`
	MCP                 []MCPDecl            `yaml:"mcp"`
	Plugins             []PluginDecl         `yaml:"plugins"`
	Skills              []SkillDecl          `yaml:"skills"`
	Assets              SceneAssets          `yaml:"assets"`
	Prompts             []PromptFragment     `yaml:"prompts"`
	RequiredCredentials []RequiredCredential `yaml:"required_credentials"`
	DisableToolGroups   []string             `yaml:"disable_tool_groups"`
	EnableToolGroups    []string             `yaml:"enable_tool_groups"`
	KnowledgeBases      []SceneKBRef         `yaml:"knowledge_bases"`
	Match               MatchRules           `yaml:"match"`
}

// SceneSeeder drives boot-time upsert of builtin scenes. The fsys field is
// injectable so tests can pass a fstest.MapFS without touching the real
// embed.FS.
type SceneSeeder struct {
	q    *store.Queries
	fsys fs.FS
	// rootDir is the directory inside fsys containing the YAMLs. For the
	// production embed it's "builtin_scenes"; tests can override.
	rootDir string
}

// NewSceneSeeder returns a seeder backed by the embedded YAML directory.
func NewSceneSeeder(q *store.Queries) *SceneSeeder {
	return &SceneSeeder{
		q:       q,
		fsys:    builtinScenesFS,
		rootDir: "builtin_scenes",
	}
}

// NewSceneSeederFromFS is the test-only constructor. Pass any fs.FS and the
// root directory inside it (use "." for a flat MapFS).
func NewSceneSeederFromFS(q *store.Queries, fsys fs.FS, rootDir string) *SceneSeeder {
	return &SceneSeeder{q: q, fsys: fsys, rootDir: rootDir}
}

// Run iterates every *.yaml under rootDir, parses + validates each, and calls
// UpsertBuiltinScene. Returns the count of successfully seeded scenes plus
// any errors. The boot path treats a non-nil error as non-fatal (see
// server.New caller).
func (s *SceneSeeder) Run(ctx context.Context) error {
	if s.q == nil {
		return errors.New("scene seeder: nil queries")
	}
	entries, err := fs.ReadDir(s.fsys, s.rootDir)
	if err != nil {
		return fmt.Errorf("read builtin scenes dir: %w", err)
	}
	var firstErr error
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		full := path.Join(s.rootDir, name)
		if err := s.seedOne(ctx, full); err != nil {
			slog.Warn("scene seeder: skip", "file", name, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		count++
	}
	slog.Info("scene seeder: completed", "seeded", count, "errors", firstErr != nil)
	if count == 0 && firstErr != nil {
		return fmt.Errorf("scene seeder: no scenes seeded (first error: %w)", firstErr)
	}
	return nil
}

// seedOne parses a single YAML file and upserts it. Errors here propagate up
// to Run which decides whether they're fatal.
func (s *SceneSeeder) seedOne(ctx context.Context, file string) error {
	raw, err := fs.ReadFile(s.fsys, file)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	var doc builtinSceneYAML
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("yaml parse: %w", err)
	}
	if doc.Slug == "" {
		return errors.New("missing slug")
	}
	if doc.DisplayName == "" {
		return errors.New("missing display_name")
	}
	def := &SceneDefinition{
		MCP:                 doc.MCP,
		Plugins:             doc.Plugins,
		Skills:              doc.Skills,
		Assets:              doc.Assets,
		Prompts:             doc.Prompts,
		RequiredCredentials: doc.RequiredCredentials,
		DisableToolGroups:   doc.DisableToolGroups,
		EnableToolGroups:    doc.EnableToolGroups,
		KnowledgeBases:      doc.KnowledgeBases,
		Match:               doc.Match,
	}
	if err := ValidateSceneDefinition(def); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshal definition: %w", err)
	}
	tagsJSON, err := marshalTags(doc.Tags)
	if err != nil {
		return err
	}
	return s.q.UpsertBuiltinScene(ctx, store.UpsertBuiltinSceneParams{
		Slug:        doc.Slug,
		DisplayName: doc.DisplayName,
		Description: doc.Description,
		Tags:        tagsJSON,
		SourceSlug:  doc.Slug,
		Definition:  string(defJSON),
		ContentHash: HashDefinition(def),
	})
}
