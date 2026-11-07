package packs

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestBuiltinsZhihuManifest(t *testing.T) {
	p := findBuiltin(t, "zhihu")
	m := loadBuiltinManifest(t, p.root)

	if m.ID != "zhihu" || m.Version != 1 || m.Status != "published" || m.DisplayName != "知乎" {
		t.Fatalf("manifest identity = %s v%d %s %q", m.ID, m.Version, m.Status, m.DisplayName)
	}

	wantCapabilities := []string{"engagements", "metrics", "publish", "reply"}
	gotCapabilities := append([]string(nil), m.Capabilities...)
	sort.Strings(gotCapabilities)
	if !reflect.DeepEqual(gotCapabilities, wantCapabilities) {
		t.Fatalf("capabilities = %v, want %v", gotCapabilities, wantCapabilities)
	}
	for _, capability := range wantCapabilities {
		binding := m.Bindings[capability]
		if binding.Transport != "browser" {
			t.Errorf("%s transport = %q, want browser", capability, binding.Transport)
		}
		if !strings.HasPrefix(binding.Recipe, "browser/recipes/") {
			t.Errorf("%s recipe = %q, want under browser/recipes/", capability, binding.Recipe)
		}
		if _, err := os.Stat(filepath.Join(p.root, filepath.FromSlash(binding.Recipe))); err != nil {
			t.Errorf("%s recipe file: %v", capability, err)
		}
	}

	slot, ok := m.CredentialSlots["account"]
	if !ok || !slot.Required || slot.Scope != "account" {
		t.Errorf("account credential slot = %+v, want required account-scope", slot)
	}

	for _, host := range []string{"www.zhihu.com", "zhuanlan.zhihu.com", "zhihu.com"} {
		if !contains(m.AllowedHosts, host) {
			t.Errorf("allowed_hosts %v is missing %q", m.AllowedHosts, host)
		}
	}

	article, ok := m.ContentSchema.Formats["article"]
	if !ok {
		t.Fatal("zhihu has no article format")
	}
	for _, field := range []string{"title", "body", "cover", "topics"} {
		if !contains(article.Fields, field) {
			t.Errorf("article fields %v is missing %q", article.Fields, field)
		}
	}
	if article.MaxTitleChars != 100 {
		t.Errorf("article max_title_chars = %d, want 100", article.MaxTitleChars)
	}
	if article.MaxChars != 100000 {
		t.Errorf("article max_chars = %d, want 100000", article.MaxChars)
	}
	if article.ImageRatio != "16:9" {
		t.Errorf("article image_ratio = %q, want 16:9", article.ImageRatio)
	}
	if article.MaxImages != 50 {
		t.Errorf("article max_images = %d, want 50", article.MaxImages)
	}

	wantMetrics := map[string]string{
		"read_num":    "views",
		"upvote_num":  "likes",
		"comment_num": "comments",
		"collect_num": "saves",
		"share_num":   "shares",
		"fans_add":    "follower_delta",
	}
	if !reflect.DeepEqual(m.MetricsMap.Fields, wantMetrics) {
		t.Errorf("zhihu metrics_map = %v, want %v", m.MetricsMap.Fields, wantMetrics)
	}

	checks := strings.Join(m.Compliance.RequiredChecks, "\n")
	for _, want := range []string{"知乎社区规范", "广告法", "版权"} {
		if !strings.Contains(checks, want) {
			t.Errorf("compliance checks do not mention %q", want)
		}
	}
	for _, term := range []string{"最佳", "第一", "100%"} {
		if !contains(m.Compliance.ForbiddenTerms, term) {
			t.Errorf("forbidden_terms %v is missing %q", m.Compliance.ForbiddenTerms, term)
		}
	}
}

func TestBuiltinsZhihuRecipes(t *testing.T) {
	p := findBuiltin(t, "zhihu")
	manifest := loadBuiltinManifest(t, p.root)
	recipeDir := filepath.Join(p.root, "browser", "recipes")

	wantExtracts := map[string][]string{
		"publish":     {"article_url"},
		"metrics":     {"read_num", "upvote_num", "comment_num", "collect_num", "share_num", "fans_add"},
		"engagements": {"engagements"},
		"reply":       {"receipt"},
	}

	for _, name := range []string{"login", "publish", "metrics", "engagements", "reply"} {
		path := filepath.Join(recipeDir, name+".json")
		recipe := readJSON[Recipe](t, path)

		if recipe.Name != name {
			t.Errorf("%s recipe name = %q", name, recipe.Name)
		}
		if len(recipe.Steps) == 0 {
			t.Errorf("%s recipe has no steps", name)
			continue
		}

		extracts := map[string]bool{}
		asserts := map[string]bool{}
		hasGoto := false
		for i, step := range recipe.Steps {
			if !recipeActions[step.Action] {
				t.Errorf("%s step %d uses action %q outside the DSL whitelist", name, i+1, step.Action)
			}
			switch step.Action {
			case "goto":
				hasGoto = true
				if !isAllowedHost(step.Value, manifest.AllowedHosts) {
					t.Errorf("%s step %d navigates to %q outside allowed_hosts", name, i+1, step.Value)
				}
			case "extract":
				if strings.TrimSpace(step.Value) == "" {
					t.Errorf("%s step %d extract has no output name", name, i+1)
				}
				extracts[step.Value] = true
			case "assert":
				asserts[step.Value] = true
			}
		}
		if !hasGoto {
			t.Errorf("%s recipe never navigates", name)
		}
		for _, want := range wantExtracts[name] {
			if !extracts[want] {
				t.Errorf("%s recipe does not extract %q", name, want)
			}
		}
		if name == "login" {
			for _, want := range []string{"qrcode_visible", "logged_in"} {
				if !asserts[want] {
					t.Errorf("login recipe does not assert %q", want)
				}
			}
		}

		if len(recipe.Fixtures) == 0 {
			t.Errorf("%s recipe declares no sandbox fixture", name)
		}
		for _, fixture := range recipe.Fixtures {
			info, err := os.Stat(filepath.Join(p.root, filepath.FromSlash(fixture)))
			if err != nil {
				t.Errorf("%s fixture %q: %v", name, fixture, err)
				continue
			}
			if info.Size() == 0 {
				t.Errorf("%s fixture %q is empty", name, fixture)
			}
		}
	}
}

func TestBuiltinsZhihuPrompts(t *testing.T) {
	p := findBuiltin(t, "zhihu")
	prompts, err := filepath.Glob(filepath.Join(p.root, "prompts", "*.md"))
	if err != nil {
		t.Fatalf("prompts glob: %v", err)
	}
	if len(prompts) < 5 {
		t.Fatalf("zhihu has %d prompt files, want at least 5", len(prompts))
	}
	for _, path := range prompts {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Errorf("prompt %s is empty", path)
		}
	}
}

func TestBuiltinsZhihuEmbeddedSeed(t *testing.T) {
	m, fsys, err := BuiltinPack("zhihu")
	if err != nil {
		t.Fatalf("embedded seed: %v", err)
	}
	if m.ID != "zhihu" || m.Version != 1 {
		t.Fatalf("embedded seed = %s v%d", m.ID, m.Version)
	}
	if len(m.ContentSchema.Formats) == 0 {
		t.Error("embedded seed did not load content_schema.json")
	}
	if len(m.MetricsMap.Fields) == 0 {
		t.Error("embedded seed did not load metrics_map.json")
	}
	if _, err := fs.ReadFile(fsys, "browser/recipes/publish.json"); err != nil {
		t.Errorf("embedded seed is missing publish recipe: %v", err)
	}
	if _, err := fs.ReadFile(fsys, "fixtures/publish.html"); err != nil {
		t.Errorf("embedded seed is missing publish fixture: %v", err)
	}
}

func TestBuiltinsZhihuInstall(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	if err := InstallBuiltins(r, "user", 1); err != nil {
		t.Fatalf("install builtins: %v", err)
	}

	root, err := r.Root("user", 1, "zhihu", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"login", "publish", "metrics", "engagements", "reply"} {
		if _, err := os.Stat(filepath.Join(root, "browser", "recipes", name+".json")); err != nil {
			t.Errorf("installed zhihu recipe %s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(root, "fixtures", name+".html")); err != nil {
			t.Errorf("installed zhihu fixture %s: %v", name, err)
		}
	}

	m, err := r.Load("user", 1, "zhihu", 1)
	if err != nil {
		t.Fatalf("load installed pack: %v", err)
	}
	if err := Validate(root, m); err != nil {
		t.Fatalf("validate installed pack: %v", err)
	}
}
