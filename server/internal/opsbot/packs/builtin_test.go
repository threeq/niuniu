package packs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// builtinRoot is the on-disk seed directory. Tests run with CWD set to this
// package, which is also the root Validate resolves recipe paths against.
const builtinRoot = "builtin"

// recipeActions mirrors the Browser Recipe DSL whitelist; a seed recipe using
// anything else would be rejected at runtime.
var recipeActions = map[string]bool{
	"goto":       true,
	"wait":       true,
	"click":      true,
	"fill":       true,
	"upload":     true,
	"extract":    true,
	"screenshot": true,
	"assert":     true,
	"loop":       true,
	"branch":     true,
}

type builtinPack struct {
	id   string
	root string
}

func builtinPacks(t *testing.T) []builtinPack {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(builtinRoot, "*", "*", "manifest.json"))
	if err != nil {
		t.Fatalf("glob builtin manifests: %v", err)
	}
	packs := make([]builtinPack, 0, len(matches))
	for _, match := range matches {
		packs = append(packs, builtinPack{
			id:   filepath.Base(filepath.Dir(filepath.Dir(match))),
			root: filepath.Dir(match),
		})
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].id < packs[j].id })
	return packs
}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return value
}

func findBuiltin(t *testing.T, id string) builtinPack {
	t.Helper()
	for _, p := range builtinPacks(t) {
		if p.id == id {
			return p
		}
	}
	t.Fatalf("builtin pack %q not found", id)
	return builtinPack{}
}

func TestBuiltinsValidate(t *testing.T) {
	packs := builtinPacks(t)
	if len(packs) < 2 {
		t.Fatalf("expected at least 2 builtin packs, found %d", len(packs))
	}

	found := map[string]bool{}
	for _, p := range packs {
		manifest := readJSON[Manifest](t, filepath.Join(p.root, "manifest.json"))
		if manifest.ID != p.id {
			t.Errorf("pack dir %q holds manifest id %q", p.id, manifest.ID)
		}
		if err := Validate(p.root, manifest); err != nil {
			t.Errorf("%s v%d: %v", manifest.ID, manifest.Version, err)
		}
		found[p.id] = true
	}
	for _, id := range []string{"wechat_mp", "xiaohongshu"} {
		if !found[id] {
			t.Errorf("builtin pack %q is missing", id)
		}
	}
}

func TestBuiltinsWechatManifest(t *testing.T) {
	p := findBuiltin(t, "wechat_mp")
	m := readJSON[Manifest](t, filepath.Join(p.root, "manifest.json"))

	for _, capability := range []string{"publish", "metrics", "engagements", "reply"} {
		binding, ok := m.Bindings[capability]
		if !ok {
			t.Fatalf("wechat_mp missing binding for %q", capability)
		}
		if binding.Transport != "mcp" || binding.Provider != "wechat-mp" {
			t.Errorf("wechat_mp %s binding = %+v, want mcp/wechat-mp", capability, binding)
		}
	}

	article, ok := m.ContentSchema.Formats["article"]
	if !ok {
		t.Fatal("wechat_mp has no article format")
	}
	if article.MaxTitleChars != 64 {
		t.Errorf("article max_title_chars = %d, want 64", article.MaxTitleChars)
	}
	if article.MaxImages != 20 {
		t.Errorf("article max_images = %d, want 20", article.MaxImages)
	}
	if article.ImageRatio != "900x383" {
		t.Errorf("article image_ratio = %q, want 900x383", article.ImageRatio)
	}
	if article.MaxChars != 0 {
		t.Errorf("article max_chars = %d, want 0 (unlimited body)", article.MaxChars)
	}

	wantMetrics := map[string]string{
		"read_count":  "views",
		"share_count": "shares",
		"like_count":  "likes",
		"net_fans":    "follower_delta",
	}
	if !reflect.DeepEqual(m.MetricsMap.Fields, wantMetrics) {
		t.Errorf("wechat_mp metrics_map = %v, want %v", m.MetricsMap.Fields, wantMetrics)
	}
}

func TestBuiltinsXiaohongshuManifest(t *testing.T) {
	p := findBuiltin(t, "xiaohongshu")
	m := readJSON[Manifest](t, filepath.Join(p.root, "manifest.json"))

	for _, capability := range []string{"publish", "metrics", "engagements", "reply"} {
		binding, ok := m.Bindings[capability]
		if !ok {
			t.Fatalf("xiaohongshu missing binding for %q", capability)
		}
		if binding.Transport != "browser" {
			t.Errorf("xiaohongshu %s transport = %q, want browser", capability, binding.Transport)
		}
		if !strings.HasPrefix(binding.Recipe, "browser/recipes/") {
			t.Errorf("xiaohongshu %s recipe = %q, want under browser/recipes/", capability, binding.Recipe)
		}
	}

	if len(m.AllowedHosts) == 0 {
		t.Error("xiaohongshu has no allowed_hosts; browser navigation would be denied")
	}
	note, ok := m.ContentSchema.Formats["note"]
	if !ok {
		t.Fatal("xiaohongshu has no note format")
	}
	if note.MaxTitleChars != 20 {
		t.Errorf("note max_title_chars = %d, want 20", note.MaxTitleChars)
	}
	if note.MaxChars != 1000 {
		t.Errorf("note max_chars = %d, want 1000", note.MaxChars)
	}
	if note.ImageRatio != "3:4" {
		t.Errorf("note image_ratio = %q, want 3:4", note.ImageRatio)
	}
	if note.MaxImages != 18 {
		t.Errorf("note max_images = %d, want 18", note.MaxImages)
	}
	video, ok := m.ContentSchema.Formats["video"]
	if !ok {
		t.Fatal("xiaohongshu has no video format")
	}
	if video.VideoSec != 900 {
		t.Errorf("video video_sec = %d, want 900 (15 minutes)", video.VideoSec)
	}

	wantMetrics := map[string]string{
		"view_num":    "views",
		"like_num":    "likes",
		"collect_num": "saves",
		"comment_num": "comments",
		"share_num":   "shares",
		"fans_add":    "follower_delta",
	}
	if !reflect.DeepEqual(m.MetricsMap.Fields, wantMetrics) {
		t.Errorf("xiaohongshu metrics_map = %v, want %v", m.MetricsMap.Fields, wantMetrics)
	}
}

func TestBuiltinsXiaohongshuRecipes(t *testing.T) {
	p := findBuiltin(t, "xiaohongshu")
	recipeDir := filepath.Join(p.root, "browser", "recipes")
	manifest := readJSON[Manifest](t, filepath.Join(p.root, "manifest.json"))

	for _, name := range []string{"login", "publish", "metrics", "engagements", "reply"} {
		path := filepath.Join(recipeDir, name+".json")
		recipe := readJSON[Recipe](t, path)

		if strings.TrimSpace(recipe.Name) == "" {
			t.Errorf("%s recipe has no name", name)
		}
		if len(recipe.Steps) == 0 {
			t.Errorf("%s recipe has no steps", name)
			continue
		}
		for i, step := range recipe.Steps {
			if !recipeActions[step.Action] {
				t.Errorf("%s recipe step %d uses action %q outside the DSL whitelist", name, i+1, step.Action)
			}
			if step.Action == "goto" && !isAllowedHost(step.Value, manifest.AllowedHosts) {
				t.Errorf("%s recipe step %d navigates to %q outside allowed_hosts", name, i+1, step.Value)
			}
		}
		if len(recipe.Fixtures) == 0 {
			t.Errorf("%s recipe declares no sandbox fixture", name)
		}
		for _, fixture := range recipe.Fixtures {
			if _, err := os.Stat(filepath.Join(p.root, fixture)); err != nil {
				t.Errorf("%s recipe fixture %q: %v", name, fixture, err)
			}
		}
	}
}

func TestBuiltinsSidecarsMatchManifest(t *testing.T) {
	for _, p := range builtinPacks(t) {
		m := readJSON[Manifest](t, filepath.Join(p.root, "manifest.json"))

		schema := readJSON[ContentSchema](t, filepath.Join(p.root, "content_schema.json"))
		if !reflect.DeepEqual(schema, m.ContentSchema) {
			t.Errorf("%s content_schema.json drifted from manifest.json", p.id)
		}

		compliance := readJSON[Compliance](t, filepath.Join(p.root, "compliance.json"))
		if !reflect.DeepEqual(compliance, m.Compliance) {
			t.Errorf("%s compliance.json drifted from manifest.json", p.id)
		}

		metrics := readJSON[MetricsMap](t, filepath.Join(p.root, "metrics_map.json"))
		if !reflect.DeepEqual(metrics, m.MetricsMap) {
			t.Errorf("%s metrics_map.json drifted from manifest.json", p.id)
		}
	}
}

func TestBuiltinsPromptsAndCompliance(t *testing.T) {
	for _, p := range builtinPacks(t) {
		prompts, err := filepath.Glob(filepath.Join(p.root, "prompts", "*.md"))
		if err != nil {
			t.Fatalf("%s prompts glob: %v", p.id, err)
		}
		if len(prompts) < 5 {
			t.Errorf("%s has %d prompt files, want at least 5", p.id, len(prompts))
		}
		for _, path := range prompts {
			info, err := os.Stat(path)
			if err != nil || info.Size() == 0 {
				t.Errorf("%s prompt %s is empty or unreadable", p.id, path)
			}
		}

		m := readJSON[Manifest](t, filepath.Join(p.root, "manifest.json"))
		if len(m.Compliance.ForbiddenTerms) == 0 {
			t.Errorf("%s has no forbidden terms", p.id)
		}
		if len(m.Compliance.RequiredChecks) == 0 {
			t.Errorf("%s has no required compliance checks", p.id)
		}
	}
}

func TestBuiltinsInstallAndValidate(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	if err := InstallBuiltins(r, "user", 1); err != nil {
		t.Fatalf("install builtins: %v", err)
	}

	list, err := r.List("user", 1)
	if err != nil {
		t.Fatalf("list installed builtins: %v", err)
	}
	if len(list) < 2 {
		t.Fatalf("installed builtins = %d, want at least 2", len(list))
	}

	seen := map[string]bool{}
	for _, m := range list {
		root, err := r.Root("user", 1, m.ID, m.Version)
		if err != nil {
			t.Errorf("%s v%d root: %v", m.ID, m.Version, err)
			continue
		}
		if err := Validate(root, m); err != nil {
			t.Errorf("installed %s v%d: %v", m.ID, m.Version, err)
		}
		seen[m.ID] = true
	}
	for _, id := range []string{"wechat_mp", "xiaohongshu"} {
		if !seen[id] {
			t.Errorf("installed builtins missing %q", id)
		}
	}

	// A browser pack is only usable if the recipes and fixtures were copied
	// alongside the manifest.
	xhsRoot, err := r.Root("user", 1, "xiaohongshu", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"login", "publish", "metrics", "engagements", "reply"} {
		if _, err := os.Stat(filepath.Join(xhsRoot, "browser", "recipes", name+".json")); err != nil {
			t.Errorf("installed xiaohongshu recipe %s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(xhsRoot, "fixtures", name+".html")); err != nil {
			t.Errorf("installed xiaohongshu fixture %s: %v", name, err)
		}
	}
}

func isAllowedHost(rawURL string, allowed []string) bool {
	for _, host := range allowed {
		if strings.Contains(rawURL, "//"+host+"/") || strings.HasSuffix(rawURL, "//"+host) {
			return true
		}
	}
	return false
}
