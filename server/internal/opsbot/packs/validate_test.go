package packs

import (
	"os"
	"path/filepath"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		ID:          "facebook",
		Version:     1,
		Status:      "published",
		DisplayName: "Facebook Page",
		Capabilities: []string{
			"publish",
			"metrics",
		},
		Bindings: map[string]Binding{
			"publish": {Transport: "openapi", Provider: "facebook-graph"},
			"metrics": {Transport: "openapi", Provider: "facebook-graph"},
		},
		CredentialSlots: map[string]CredentialSlot{
			"app":     {Required: true, Scope: "owner"},
			"account": {Required: true, Scope: "account"},
		},
		ContentSchema: ContentSchema{
			Formats: map[string]ContentFormat{
				"post": {Fields: []string{"title", "body"}, MaxChars: 5000},
			},
		},
		MetricsMap: MetricsMap{
			Fields: map[string]string{"impressions": "views"},
		},
	}
}

func TestValidateAcceptsValidPack(t *testing.T) {
	if err := Validate(t.TempDir(), validManifest()); err != nil {
		t.Fatalf("valid pack must pass, got %v", err)
	}
}

func TestValidateAcceptsBrowserBindingWithRecipeFile(t *testing.T) {
	root := t.TempDir()
	writeRecipe(t, root, "publish.json")
	m := validManifest()
	m.Capabilities = []string{"publish"}
	m.Bindings = map[string]Binding{
		"publish": {Transport: "browser", Recipe: "publish.json"},
	}
	m.MetricsMap = MetricsMap{}
	if err := Validate(root, m); err != nil {
		t.Fatalf("browser binding with recipe file must pass, got %v", err)
	}
}

func TestValidateRejectsBrowserBindingWithoutRecipeFile(t *testing.T) {
	m := Manifest{
		ID: "zhihu", Version: 1, Status: "published",
		Capabilities: []string{"publish"},
		Bindings:     map[string]Binding{"publish": {Transport: "browser", Recipe: "publish.json"}},
		ContentSchema: ContentSchema{
			Formats: map[string]ContentFormat{"article": {Fields: []string{"title", "body"}}},
		},
	}
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("missing recipe file must fail")
	}
}

func TestValidateRejectsUnparseableRecipe(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "publish.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := validManifest()
	m.Capabilities = []string{"publish"}
	m.Bindings = map[string]Binding{"publish": {Transport: "browser", Recipe: "publish.json"}}
	m.MetricsMap = MetricsMap{}
	if err := Validate(root, m); err == nil {
		t.Fatal("unparseable recipe must fail")
	}
}

func TestValidateRejectsRecipeWithoutSteps(t *testing.T) {
	root := t.TempDir()
	body := `{"name":"publish","steps":[]}`
	if err := os.WriteFile(filepath.Join(root, "publish.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := validManifest()
	m.Capabilities = []string{"publish"}
	m.Bindings = map[string]Binding{"publish": {Transport: "browser", Recipe: "publish.json"}}
	m.MetricsMap = MetricsMap{}
	if err := Validate(root, m); err == nil {
		t.Fatal("recipe without steps must fail")
	}
}

func TestValidateRejectsRecipeStepWithoutAction(t *testing.T) {
	root := t.TempDir()
	body := `{"name":"publish","steps":[{"selector":"#title","value":"hi"}]}`
	if err := os.WriteFile(filepath.Join(root, "publish.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := validManifest()
	m.Capabilities = []string{"publish"}
	m.Bindings = map[string]Binding{"publish": {Transport: "browser", Recipe: "publish.json"}}
	m.MetricsMap = MetricsMap{}
	if err := Validate(root, m); err == nil {
		t.Fatal("recipe step without action must fail")
	}
}

func TestValidateRejectsRecipeEscapingRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "escape-recipe.json")
	if err := os.WriteFile(outside, []byte(`{"name":"x","steps":[{"action":"open"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	m := validManifest()
	m.Capabilities = []string{"publish"}
	m.Bindings = map[string]Binding{"publish": {Transport: "browser", Recipe: "../escape-recipe.json"}}
	m.MetricsMap = MetricsMap{}
	if err := Validate(root, m); err == nil {
		t.Fatal("recipe path escaping pack root must fail")
	}
}

func TestValidateRejectsRecipeSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "recipe.json")
	body := `{"name":"publish","steps":[{"action":"open"}]}`
	if err := os.WriteFile(outside, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "publish.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	m := validManifest()
	m.Capabilities = []string{"publish"}
	m.Bindings = map[string]Binding{"publish": {Transport: "browser", Recipe: "publish.json"}}
	m.MetricsMap = MetricsMap{}
	if err := Validate(root, m); err == nil {
		t.Fatal("recipe symlink escaping pack root must fail")
	}
}

func TestValidateRejectsOpenAPIWithoutProvider(t *testing.T) {
	m := Manifest{
		ID: "facebook", Version: 1, Status: "published",
		Capabilities: []string{"publish"},
		Bindings:     map[string]Binding{"publish": {Transport: "openapi"}},
		ContentSchema: ContentSchema{
			Formats: map[string]ContentFormat{"post": {Fields: []string{"body"}}},
		},
	}
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("openapi binding without provider must fail")
	}
}

func TestValidateRejectsMCPWithoutProvider(t *testing.T) {
	m := validManifest()
	m.Capabilities = []string{"publish"}
	m.MetricsMap = MetricsMap{}
	m.Bindings = map[string]Binding{"publish": {Transport: "mcp"}}
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("mcp binding without provider must fail")
	}
}

func TestValidateRejectsCapabilityWithoutBinding(t *testing.T) {
	m := validManifest()
	delete(m.Bindings, "publish")
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("capability without binding must fail")
	}
}

func TestValidateRejectsUnknownTransport(t *testing.T) {
	m := validManifest()
	m.Bindings["publish"] = Binding{Transport: "carrier-pigeon", Provider: "x"}
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("unknown transport must fail")
	}
}

func TestValidateRejectsEmptyCapabilities(t *testing.T) {
	m := validManifest()
	m.Capabilities = nil
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("empty capabilities must fail")
	}
}

func TestValidateRejectsDuplicateCapabilities(t *testing.T) {
	m := validManifest()
	m.Capabilities = []string{"publish", "publish"}
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("duplicate capabilities must fail")
	}
}

func TestValidateRejectsInvalidID(t *testing.T) {
	for _, id := range []string{"", "Facebook", "face book", "-facebook", "facebook!"} {
		m := validManifest()
		m.ID = id
		if err := Validate(t.TempDir(), m); err == nil {
			t.Fatalf("invalid id %q must fail", id)
		}
	}
}

func TestValidateAcceptsIDWithDashAndUnderscore(t *testing.T) {
	m := validManifest()
	m.ID = "wechat_mp-v2"
	if err := Validate(t.TempDir(), m); err != nil {
		t.Fatalf("id with dash/underscore must pass, got %v", err)
	}
}

func TestValidateRejectsNonPositiveVersion(t *testing.T) {
	for _, v := range []int{0, -1} {
		m := validManifest()
		m.Version = v
		if err := Validate(t.TempDir(), m); err == nil {
			t.Fatalf("version %d must fail", v)
		}
	}
}

func TestValidateRejectsInvalidStatus(t *testing.T) {
	for _, status := range []string{"", "live", "PUBLISHED", "archived"} {
		m := validManifest()
		m.Status = status
		if err := Validate(t.TempDir(), m); err == nil {
			t.Fatalf("status %q must fail", status)
		}
	}
}

func TestValidateAcceptsAllValidStatuses(t *testing.T) {
	for _, status := range []string{"draft", "testing", "published", "deprecated"} {
		m := validManifest()
		m.Status = status
		if err := Validate(t.TempDir(), m); err != nil {
			t.Fatalf("status %q must pass, got %v", status, err)
		}
	}
}

func TestValidateRejectsInvalidCredentialScope(t *testing.T) {
	m := validManifest()
	m.CredentialSlots["app"] = CredentialSlot{Required: true, Scope: "team"}
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("invalid credential scope must fail")
	}
}

func TestValidateRejectsPublishWithoutContentFormat(t *testing.T) {
	m := validManifest()
	m.ContentSchema = ContentSchema{}
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("publish capability without content format must fail")
	}
}

func TestValidateRejectsMetricsCapabilityWithoutMetricsMap(t *testing.T) {
	m := validManifest()
	m.MetricsMap = MetricsMap{}
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("metrics capability without metrics map must fail")
	}
}

func writeRecipe(t *testing.T, root, name string) {
	t.Helper()
	body := `{"name":"publish","steps":[{"action":"open","value":"https://example.com"}]}`
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
