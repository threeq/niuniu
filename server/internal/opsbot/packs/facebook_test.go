package packs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFacebookBuiltinManifest(t *testing.T) {
	p := findBuiltin(t, "facebook")
	m := loadBuiltinManifest(t, p.root)

	if m.Status != "published" {
		t.Errorf("facebook status = %q, want published", m.Status)
	}
	if m.DisplayName != "Facebook" {
		t.Errorf("facebook display_name = %q", m.DisplayName)
	}
	if len(m.Capabilities) != 4 {
		t.Errorf("facebook capabilities = %v, want 4", m.Capabilities)
	}

	for _, capability := range []string{"publish", "metrics", "engagements", "reply"} {
		binding, ok := m.Bindings[capability]
		if !ok {
			t.Fatalf("facebook missing binding for %q", capability)
		}
		if binding.Transport != "openapi" || binding.Provider != "facebook-graph" {
			t.Errorf("facebook %s binding = %+v, want openapi/facebook-graph", capability, binding)
		}
	}

	app, ok := m.CredentialSlots["app"]
	if !ok || !app.Required || app.Scope != "owner" {
		t.Errorf("facebook app credential slot = %+v, want required owner", app)
	}
	account, ok := m.CredentialSlots["account"]
	if !ok || !account.Required || account.Scope != "account" {
		t.Errorf("facebook account credential slot = %+v, want required account", account)
	}

	if !containsString(m.AllowedHosts, "graph.facebook.com") {
		t.Errorf("facebook allowed_hosts = %v, want graph.facebook.com", m.AllowedHosts)
	}
}

func TestFacebookRejectsInvalidManifest(t *testing.T) {
	p := findBuiltin(t, "facebook")
	base := loadBuiltinManifest(t, p.root)

	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"openapi without provider", func(m *Manifest) {
			m.Bindings["publish"] = Binding{Transport: "openapi"}
		}},
		{"unknown transport", func(m *Manifest) {
			m.Bindings["publish"] = Binding{Transport: "api", Provider: "facebook-graph"}
		}},
		{"publish without content format", func(m *Manifest) {
			m.ContentSchema = ContentSchema{}
		}},
		{"metrics without map", func(m *Manifest) {
			m.MetricsMap = MetricsMap{}
		}},
		{"duplicate capability", func(m *Manifest) {
			m.Capabilities = append(m.Capabilities, "publish")
		}},
		{"invalid credential scope", func(m *Manifest) {
			m.CredentialSlots["account"] = CredentialSlot{Required: true, Scope: "team"}
		}},
		{"capability without binding", func(m *Manifest) {
			delete(m.Bindings, "reply")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := cloneFacebookManifest(base)
			tc.mutate(&m)
			if err := Validate(p.root, m); err == nil {
				t.Fatalf("manifest mutation %q must be rejected", tc.name)
			}
		})
	}
}

func cloneFacebookManifest(m Manifest) Manifest {
	clone := m
	clone.Capabilities = append([]string(nil), m.Capabilities...)
	clone.AllowedHosts = append([]string(nil), m.AllowedHosts...)

	clone.Bindings = make(map[string]Binding, len(m.Bindings))
	for name, binding := range m.Bindings {
		clone.Bindings[name] = binding
	}

	clone.CredentialSlots = make(map[string]CredentialSlot, len(m.CredentialSlots))
	for name, slot := range m.CredentialSlots {
		clone.CredentialSlots[name] = slot
	}

	clone.ContentSchema.Formats = make(map[string]ContentFormat, len(m.ContentSchema.Formats))
	for name, format := range m.ContentSchema.Formats {
		clone.ContentSchema.Formats[name] = format
	}

	clone.MetricsMap.Fields = make(map[string]string, len(m.MetricsMap.Fields))
	for key, value := range m.MetricsMap.Fields {
		clone.MetricsMap.Fields[key] = value
	}
	return clone
}

func TestFacebookContentSchema(t *testing.T) {
	p := findBuiltin(t, "facebook")
	m := loadBuiltinManifest(t, p.root)

	post, ok := m.ContentSchema.Formats["post"]
	if !ok {
		t.Fatal("facebook has no post format")
	}
	if post.MaxChars != 63206 {
		t.Errorf("post max_chars = %d, want 63206", post.MaxChars)
	}
	for _, field := range []string{"message", "link", "image"} {
		if !containsString(post.Fields, field) {
			t.Errorf("post fields = %v, missing %q", post.Fields, field)
		}
	}

	reel, ok := m.ContentSchema.Formats["reel"]
	if !ok {
		t.Fatal("facebook has no reel format")
	}
	if reel.VideoSec != 90 {
		t.Errorf("reel video_sec = %d, want 90", reel.VideoSec)
	}
	if reel.ImageRatio != "9:16" {
		t.Errorf("reel image_ratio = %q, want 9:16", reel.ImageRatio)
	}
	for _, field := range []string{"description", "video"} {
		if !containsString(reel.Fields, field) {
			t.Errorf("reel fields = %v, missing %q", reel.Fields, field)
		}
	}
}

func TestFacebookMetricsMap(t *testing.T) {
	p := findBuiltin(t, "facebook")
	m := loadBuiltinManifest(t, p.root)

	want := map[string]string{
		"post_impressions":             "views",
		"post_reactions_by_type_total": "likes",
		"post_comments":                "comments",
		"post_shares":                  "shares",
		"page_fans":                    "followers",
	}
	if !reflect.DeepEqual(m.MetricsMap.Fields, want) {
		t.Errorf("facebook metrics_map = %v, want %v", m.MetricsMap.Fields, want)
	}
}

func TestFacebookPromptsAndCompliance(t *testing.T) {
	p := findBuiltin(t, "facebook")

	prompts, err := filepath.Glob(filepath.Join(p.root, "prompts", "*.md"))
	if err != nil {
		t.Fatalf("prompts glob: %v", err)
	}
	if len(prompts) < 5 {
		t.Errorf("facebook has %d prompt files, want at least 5", len(prompts))
	}
	for _, path := range prompts {
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			t.Errorf("prompt %s is empty or unreadable", path)
		}
	}

	m := loadBuiltinManifest(t, p.root)
	if len(m.Compliance.ForbiddenTerms) == 0 {
		t.Error("facebook has no forbidden terms")
	}
	if len(m.Compliance.RequiredChecks) == 0 {
		t.Error("facebook has no required compliance checks")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
