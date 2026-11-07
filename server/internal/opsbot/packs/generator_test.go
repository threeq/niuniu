package packs

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func newTestGenerator(t *testing.T) *Generator {
	t.Helper()
	return &Generator{
		Registry: &Registry{DataDir: t.TempDir()},
		Owner:    Owner{Type: "user", ID: 1},
		Now:      func() time.Time { return time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) },
	}
}

func TestGenerateFromOpenAPI(t *testing.T) {
	g := newTestGenerator(t)
	draft, err := g.Draft(context.Background(), GenerateInput{
		Channel:     "facebook",
		OpenAPIPath: "testdata/openapi.json",
	})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if draft.Manifest.ID != "facebook" {
		t.Fatalf("id = %q, want facebook", draft.Manifest.ID)
	}
	if got := draft.Manifest.Bindings["publish"].Transport; got != "openapi" {
		t.Fatalf("publish transport = %q, want openapi", got)
	}
	if len(draft.Matrix.Capabilities) == 0 {
		t.Fatal("capability matrix is empty")
	}
	if draft.Contract == nil || draft.Contract.BaseURL != "https://graph.example.com/v1" {
		t.Fatalf("contract = %+v", draft.Contract)
	}
	if !contains(draft.Manifest.Capabilities, CapMetrics) {
		t.Fatal("metrics should be bound from the insights operation")
	}
	if got := draft.Manifest.MetricsMap.Fields["fans_add"]; got != "follower_delta" {
		t.Fatalf("fans_add maps to %q, want follower_delta (map %+v)", got, draft.Manifest.MetricsMap.Fields)
	}
	if len(draft.Questions) != 0 {
		t.Fatalf("no question should remain for a complete document, got %+v", draft.Questions)
	}

	// The document's free text is data, never instruction: it must not be
	// carried into the generated contract.
	raw, _ := json.Marshal(draft.Contract)
	if strings.Contains(string(raw), "IGNORE ALL PREVIOUS") {
		t.Fatal("document free text leaked into the provider contract")
	}

	if draft.Root == "" {
		t.Fatal("draft was not persisted")
	}
	if _, err := g.Registry.Load("user", 1, "facebook", 1); err != nil {
		t.Fatalf("load persisted draft: %v", err)
	}
}

func TestGenerateDraftsProviderContract(t *testing.T) {
	g := newTestGenerator(t)
	var drafted ProviderContractDraft
	calls := 0
	g.DraftContract = func(_ context.Context, _ Owner, contract ProviderContractDraft, _, _ string) (int64, error) {
		calls++
		drafted = contract
		return 42, nil
	}
	in := GenerateInput{Channel: "facebook", OpenAPIPath: "testdata/openapi.json"}

	draft, err := g.Draft(context.Background(), in)
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if drafted.BaseURL != "https://graph.example.com/v1" || len(drafted.Operations) == 0 {
		t.Fatalf("provider contract draft = %+v", drafted)
	}
	if draft.ContractDraftID != 42 {
		t.Fatalf("contract draft id = %d, want 42", draft.ContractDraftID)
	}

	// Regenerating the same document returns the existing version and must not
	// create a second integration draft.
	again, err := g.Draft(context.Background(), in)
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if calls != 1 {
		t.Fatalf("provider contract drafted %d times, want 1", calls)
	}
	if again.ContractDraftID != 42 {
		t.Fatalf("regenerated draft id = %d, want 42", again.ContractDraftID)
	}
}

func TestGenerateWithoutAPIFallsBackToRecipeDraft(t *testing.T) {
	g := newTestGenerator(t)
	draft, err := g.Draft(context.Background(), GenerateInput{Channel: "zhihu"})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	publish := draft.Manifest.Bindings[CapPublish]
	if publish.Transport != "browser" {
		t.Fatalf("publish transport = %q, want browser", publish.Transport)
	}
	if strings.TrimSpace(publish.Recipe) == "" {
		t.Fatal("browser draft has no publish recipe")
	}
	if draft.Manifest.Status != "draft" {
		t.Fatalf("status = %q, want draft", draft.Manifest.Status)
	}
	if len(draft.Questions) == 0 {
		t.Fatal("a recipe draft with no answers must ask for the missing facts")
	}
	if contains(draft.Manifest.Capabilities, CapMetrics) {
		t.Fatal("metrics must not be declared without a field mapping")
	}
}

func TestGenerateAsksForEachMissingFactInsteadOfGuessing(t *testing.T) {
	g := newTestGenerator(t)
	draft, err := g.Draft(context.Background(), GenerateInput{Channel: "zhihu"})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}

	// The unresolved placeholders stay visible in the recipe: nothing is
	// silently filled with an invented value.
	publishRecipe := string(draft.Files["browser/recipes/publish.json"])
	for _, placeholder := range []string{"{{login_url}}", "{{publish_url}}", "{{submit_selector}}"} {
		// login_url lives in the login recipe, not publish.
		if placeholder == "{{login_url}}" {
			if !strings.Contains(string(draft.Files["browser/recipes/login.json"]), placeholder) {
				t.Fatalf("login recipe lost placeholder %s", placeholder)
			}
			continue
		}
		if !strings.Contains(publishRecipe, placeholder) {
			t.Fatalf("publish recipe lost placeholder %s: %s", placeholder, publishRecipe)
		}
	}

	if err := g.Publish(context.Background(), g.Owner, "zhihu", 1); !errors.Is(err, ErrMissingInfo) {
		t.Fatalf("publish before answers = %v, want ErrMissingInfo", err)
	}
}

func TestGenerateTestBeforePublish(t *testing.T) {
	g := newTestGenerator(t)
	g.RecipeTester = okRecipeTester
	draft, err := g.Draft(context.Background(), GenerateInput{
		Channel: "zhihu",
		Answers: completeRecipeAnswers(),
	})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if len(draft.Questions) != 0 {
		t.Fatalf("complete answers still leave questions: %+v", draft.Questions)
	}

	if err := g.Publish(context.Background(), g.Owner, "zhihu", 1); !errors.Is(err, ErrNotTested) {
		t.Fatalf("publish without test = %v, want ErrNotTested", err)
	}
	if err := g.Test(context.Background(), g.Owner, "zhihu", 1); err != nil {
		t.Fatalf("test: %v", err)
	}
	if err := g.Publish(context.Background(), g.Owner, "zhihu", 1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	m, err := g.Registry.LatestPublished("user", 1, "zhihu")
	if err != nil {
		t.Fatalf("latest published: %v", err)
	}
	if m.Version != 1 || m.Status != "published" {
		t.Fatalf("published = %+v", m)
	}
}

func TestGenerateSandboxFailureBlocksPublish(t *testing.T) {
	g := newTestGenerator(t)
	g.RecipeTester = func(context.Context, string, Recipe, map[string]string) error {
		return errors.New("selector changed")
	}
	if _, err := g.Draft(context.Background(), GenerateInput{
		Channel: "zhihu",
		Answers: completeRecipeAnswers(),
	}); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if err := g.Test(context.Background(), g.Owner, "zhihu", 1); err == nil {
		t.Fatal("test should fail")
	}
	if err := g.Publish(context.Background(), g.Owner, "zhihu", 1); !errors.Is(err, ErrNotTested) {
		t.Fatalf("publish after failed test = %v, want ErrNotTested", err)
	}
}

func TestGenerateRegenerationUpgradesVersion(t *testing.T) {
	g := newTestGenerator(t)
	g.RecipeTester = okRecipeTester
	answers := completeRecipeAnswers()

	first, err := g.Draft(context.Background(), GenerateInput{Channel: "zhihu", Answers: answers})
	if err != nil {
		t.Fatalf("first draft: %v", err)
	}
	if first.Manifest.Version != 1 {
		t.Fatalf("first version = %d", first.Manifest.Version)
	}

	// Identical input is idempotent and returns the existing draft.
	again, err := g.Draft(context.Background(), GenerateInput{Channel: "zhihu", Answers: answers})
	if err != nil {
		t.Fatalf("second draft: %v", err)
	}
	if again.Manifest.Version != 1 {
		t.Fatalf("identical draft created version %d, want 1", again.Manifest.Version)
	}

	// A changed fact is a new version; history is never mutated.
	answers["publish_url"] = "https://example.test/publish-v2"
	upgraded, err := g.Draft(context.Background(), GenerateInput{Channel: "zhihu", Answers: answers})
	if err != nil {
		t.Fatalf("upgraded draft: %v", err)
	}
	if upgraded.Manifest.Version != 2 {
		t.Fatalf("upgraded version = %d, want 2", upgraded.Manifest.Version)
	}
	if _, err := g.Registry.Load("user", 1, "zhihu", 1); err != nil {
		t.Fatalf("old version must survive: %v", err)
	}
}

func TestGenerateMCPDraftReusesProvider(t *testing.T) {
	g := newTestGenerator(t)
	draft, err := g.Draft(context.Background(), GenerateInput{
		Channel:     "wechat_mp",
		ExistingMCP: "wechat-mp",
		Answers:     map[string]string{"metrics_field_map": "read_count=views, like_count=likes"},
	})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	for _, capability := range draft.Manifest.Capabilities {
		binding := draft.Manifest.Bindings[capability]
		if binding.Transport != "mcp" || binding.Provider != "wechat-mp" {
			t.Fatalf("%s binding = %+v", capability, binding)
		}
	}
	packID := draft.Manifest.ID
	if err := g.Test(context.Background(), g.Owner, packID, 1); err != nil {
		t.Fatalf("test: %v", err)
	}
	if err := g.Publish(context.Background(), g.Owner, packID, 1); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func TestGenerateRejectsUnusableDocument(t *testing.T) {
	g := newTestGenerator(t)
	_, err := g.Draft(context.Background(), GenerateInput{
		Channel:      "broken",
		OpenAPIBytes: []byte("this is not json"),
	})
	if !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("err = %v, want ErrInvalidDocument", err)
	}
}

func TestGenerateOpenAPIWithoutServerURLAsksForIt(t *testing.T) {
	g := newTestGenerator(t)
	draft, err := g.Draft(context.Background(), GenerateInput{
		Channel: "noserver",
		OpenAPIBytes: []byte(`{
			"openapi": "3.0.0",
			"paths": {"/posts": {"post": {"operationId": "createPost"}}}
		}`),
	})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if len(draft.Questions) == 0 || draft.Questions[0].Key != "base_url" {
		t.Fatalf("questions = %+v, want a base_url question", draft.Questions)
	}
	if err := g.Test(context.Background(), g.Owner, "noserver", 1); err == nil {
		t.Fatal("test should fail without a base URL")
	}
	if err := g.Publish(context.Background(), g.Owner, "noserver", 1); !errors.Is(err, ErrMissingInfo) {
		t.Fatalf("publish = %v, want ErrMissingInfo", err)
	}
}

// --- helpers ---------------------------------------------------------------

var recipePlaceholder = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)

// okRecipeTester mirrors the runner's resolution rule: every placeholder in a
// recipe must have a value, otherwise the recipe would fail at runtime.
func okRecipeTester(_ context.Context, _ string, recipe Recipe, values map[string]string) error {
	raw, err := json.Marshal(recipe)
	if err != nil {
		return err
	}
	for _, match := range recipePlaceholder.FindAllStringSubmatch(string(raw), -1) {
		if _, ok := values[match[1]]; !ok {
			return errors.New("unresolved placeholder: " + match[1])
		}
	}
	return nil
}

func completeRecipeAnswers() map[string]string {
	answers := map[string]string{
		"login_url":                "https://example.test/login",
		"logged_in_selector":       ".logged-in",
		"publish_url":              "https://example.test/publish",
		"upload_selector":          "input[type=file]",
		"title_selector":           "#title",
		"body_selector":            "#body",
		"submit_selector":          "#submit",
		"result_selector":          ".result a",
		"engagements_url":          "https://example.test/comments",
		"engagement_list_selector": ".comment",
		"reply_url":                "https://example.test/inbox",
		"reply_input_selector":     "#reply",
		"reply_send_selector":      "#send",
		"metrics_url":              "https://example.test/metrics",
		"metrics_selector":         ".metrics",
		"metrics_field_map":        "view_num=views, like_num=likes",
		"allowed_hosts":            "example.test",
	}
	for _, capability := range []string{"publish", "engagements", "reply", "metrics", "login"} {
		answers["fixture."+capability] = "<html><body><div class=\"fixture\">" + capability + "</div></body></html>"
	}
	return answers
}
