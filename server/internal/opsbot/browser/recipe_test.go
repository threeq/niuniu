package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecipeActionsAndSelectorFallback(t *testing.T) {
	r := Recipe{Name: "publish", Steps: []RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/publish"},
		{Action: "fill", Selector: "#missing", Fallback: []string{"textarea[placeholder]"}, Value: "标题"},
		{Action: "click", Selector: "button.submit"},
		{Action: "assert", Selector: ".success"},
		{Action: "extract", Selector: ".note-url", Value: "url"},
	}}
	out, err := newTestRunner(t).Run(context.Background(), r, Input{Session: "s1"})
	require.NoError(t, err)
	require.Contains(t, out.Values["url"], "note/")
}

func TestRecipeRejectsUnknownActionAndForeignDomain(t *testing.T) {
	_, err := newTestRunner(t).Run(context.Background(),
		Recipe{Steps: []RecipeStep{{Action: "eval", Value: "alert(1)"}}}, Input{})
	require.Error(t, err)
	require.Equal(t, ClassUnknownAction, FailureClassOf(err))
	require.False(t, errors.Is(err, ErrRecipeBroken), "an unknown action is a broken pack, not a retryable fault")

	_, err = newTestRunner(t).Run(context.Background(),
		Recipe{Steps: []RecipeStep{{Action: "goto", Value: "https://evil.example"}}}, Input{})
	require.Error(t, err)
	require.Equal(t, ClassHostNotAllowed, FailureClassOf(err))
}

func TestRecipeSubstitutesInput(t *testing.T) {
	r := Recipe{Steps: []RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/publish"},
		{Action: "fill", Selector: "textarea[placeholder]", Value: "{{title}}"},
		{Action: "extract", Selector: "textarea[placeholder]", Value: "body"},
	}}
	out, err := newTestRunner(t).Run(context.Background(), r, Input{
		Values: map[string]string{"title": "你好世界"},
	})
	require.NoError(t, err)
	require.Equal(t, "你好世界", out.Values["body"])
}

func TestRecipeRejectsMissingInput(t *testing.T) {
	r := Recipe{Steps: []RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/publish"},
		{Action: "fill", Selector: "textarea[placeholder]", Value: "{{unknown}}"},
	}}
	_, err := newTestRunner(t).Run(context.Background(), r, Input{})
	require.ErrorIs(t, err, ErrMissingInput)
}

func TestRecipeClassifiesSelectorNotFoundAndAssertFailed(t *testing.T) {
	gotoStep := RecipeStep{Action: "goto", Value: "https://creator.example.com/publish"}

	_, err := newTestRunner(t).Run(context.Background(), Recipe{Steps: []RecipeStep{
		gotoStep,
		{Action: "click", Selector: ".not-there", Fallback: []string{".also-missing"}},
	}}, Input{})
	require.Equal(t, ClassSelectorNotFound, FailureClassOf(err))
	require.ErrorIs(t, err, ErrRecipeBroken)

	_, err = newTestRunner(t).Run(context.Background(), Recipe{Steps: []RecipeStep{
		gotoStep,
		{Action: "assert", Selector: ".not-there"},
	}}, Input{})
	require.Equal(t, ClassAssertFailed, FailureClassOf(err))
	require.ErrorIs(t, err, ErrRecipeBroken)
}

func TestRecipeWaitTimeoutIsClassified(t *testing.T) {
	_, err := newTestRunner(t).Run(context.Background(), Recipe{Steps: []RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/publish"},
		{Action: "wait", Selector: ".never-appears", Timeout: 50},
	}}, Input{})
	require.Equal(t, ClassTimeout, FailureClassOf(err))
	require.ErrorIs(t, err, ErrTimeout)
}

func TestRecipeDetectsBlockedPage(t *testing.T) {
	_, err := testRunner(t, "testdata/blocked.html", "creator.example.com").
		Run(context.Background(), Recipe{Steps: []RecipeStep{
			{Action: "goto", Value: "https://creator.example.com/publish"},
			{Action: "click", Selector: ".not-there"},
		}}, Input{})
	require.Error(t, err)
	require.Equal(t, ClassBlocked, FailureClassOf(err))
	require.ErrorIs(t, err, ErrBlocked)
}

func TestRecipeLoopStopsWhenNoNewItems(t *testing.T) {
	r := Recipe{Steps: []RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/publish"},
		{Action: "loop", Selector: "button.submit", Value: "scroll", Args: map[string]string{
			"max_iterations": "20",
			"until":          "no_new_items",
		}},
	}}
	_, err := newTestRunner(t).Run(context.Background(), r, Input{})
	require.NoError(t, err)
}

func TestRecipeLoopRespectsMaxIterations(t *testing.T) {
	page := &growingPage{count: 0}
	runner := &RecipeRunner{
		AllowedHosts: []string{"creator.example.com"},
		Open:         func(context.Context, Input) (Page, error) { return page, nil },
	}
	_, err := runner.Run(context.Background(), Recipe{Steps: []RecipeStep{
		{Action: "loop", Selector: ".item", Value: "scroll", Args: map[string]string{
			"max_iterations": "1000",
			"until":          "no_new_items",
		}},
	}}, Input{})
	require.ErrorIs(t, err, ErrLoopLimit)
	require.LessOrEqual(t, page.calls, 100, "the loop must never exceed the hard iteration cap")
}

func TestRecipeBranchRunsConditionalAction(t *testing.T) {
	r := Recipe{Steps: []RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/publish"},
		{Action: "branch", Args: map[string]string{
			"condition":     "selector_exists",
			"selector":      ".not-here",
			"then":          "click",
			"then_selector": ".not-here-either",
			"else":          "fill",
			"else_selector": "textarea[placeholder]",
			"else_value":    "branch-else",
		}},
		{Action: "extract", Selector: "textarea[placeholder]", Value: "body"},
	}}
	out, err := newTestRunner(t).Run(context.Background(), r, Input{})
	require.NoError(t, err)
	require.Equal(t, "branch-else", out.Values["body"])
}

func TestRecipeRejectsNestedLoopInBranch(t *testing.T) {
	r := Recipe{Steps: []RecipeStep{
		{Action: "branch", Args: map[string]string{
			"condition": "selector_exists",
			"selector":  ".success",
			"then":      "loop",
		}},
	}}
	_, err := newTestRunner(t).Run(context.Background(), r, Input{})
	require.ErrorIs(t, err, ErrInvalidRecipe)
}

func TestSandboxRunsBuiltinRecipes(t *testing.T) {
	root := filepath.Join("..", "packs", "builtin", "xiaohongshu", "v1")
	allowedHosts := []string{"creator.xiaohongshu.com", "www.xiaohongshu.com", "xiaohongshu.com"}

	cases := []struct {
		name   string
		values map[string]string
	}{
		{name: "login"},
		{name: "publish", values: map[string]string{
			"images": "img1.png,img2.png",
			"title":  "秋日穿搭",
			"body":   "今天的搭配分享",
			"tags":   "#穿搭 #秋天",
		}},
		{name: "metrics", values: map[string]string{"range": "7d"}},
		{name: "engagements"},
		{name: "reply", values: map[string]string{"engagement_id": "c_10001", "reply_text": "感谢支持"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, "browser", "recipes", tc.name+".json"))
			require.NoError(t, err)
			var recipe Recipe
			require.NoError(t, json.Unmarshal(raw, &recipe))

			sandbox := &Sandbox{
				Root:         root,
				AllowedHosts: allowedHosts,
				Values:       tc.values,
			}
			require.NoError(t, sandbox.Run(context.Background(), recipe, recipe.Fixtures))
		})
	}
}

func TestSandboxRejectsFixtureOutsideRoot(t *testing.T) {
	sandbox := &Sandbox{Root: t.TempDir()}
	err := sandbox.Run(context.Background(), Recipe{
		Name:     "escape",
		Steps:    []RecipeStep{{Action: "goto", Value: "https://creator.example.com/publish"}},
		Fixtures: []string{"../escape.html"},
	}, nil)
	require.Error(t, err)
}

// newTestRunner builds a runner over the shared publish fixture. The page is
// the same DOM implementation the sandbox uses, so the tests exercise real
// selector/HTTP behaviour rather than a hand-written mock.
func newTestRunner(t *testing.T) *RecipeRunner {
	t.Helper()
	return testRunner(t, "testdata/publish.html", "creator.example.com")
}

func testRunner(t *testing.T, fixture string, hosts ...string) *RecipeRunner {
	t.Helper()
	data, err := os.ReadFile(fixture)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)

	hostMap := make(map[string]string, len(hosts))
	for _, host := range hosts {
		hostMap[host] = srv.URL
	}
	return &RecipeRunner{
		AllowedHosts: hosts,
		Open: func(context.Context, Input) (Page, error) {
			return newDOMPage(hostMap), nil
		},
	}
}

// growingPage simulates a list that never stops yielding new items, which is
// exactly what the loop cap must contain.
type growingPage struct {
	Page
	count int
	calls int
}

func (p *growingPage) Count(context.Context, string) (int, error) {
	p.calls++
	p.count++
	return p.count, nil
}

func (p *growingPage) Scroll(context.Context, string) error { return nil }

func (p *growingPage) BlockReason(context.Context) (string, bool) { return "", false }
