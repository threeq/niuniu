package browser

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const zhihuPackRoot = "../packs/builtin/zhihu/v1"

var zhihuHosts = []string{"www.zhihu.com", "zhuanlan.zhihu.com", "zhihu.com"}

func zhihuRecipe(t *testing.T, name string) Recipe {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(zhihuPackRoot, "browser", "recipes", name+".json"))
	require.NoError(t, err)
	var recipe Recipe
	require.NoError(t, json.Unmarshal(raw, &recipe))
	return recipe
}

func zhihuPublishValues() map[string]string {
	return map[string]string{
		"title":  "如何系统评估一次技术选型",
		"body":   "先结论后论证，给出适用边界与数据来源。",
		"cover":  "fixtures/cover.png",
		"topics": "职场,技术管理",
	}
}

func zhihuReplyValues(engagementID string) map[string]string {
	return map[string]string{
		"engagement_id": engagementID,
		"reply_text":    "谢谢关注，数据来源已补充在文末。",
	}
}

// TestZhihuSandboxRecipes runs every Zhihu recipe end to end against the
// pack's fixtures through the official sandbox entry point.
func TestZhihuSandboxRecipes(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
	}{
		{name: "login"},
		{name: "publish", values: zhihuPublishValues()},
		{name: "metrics", values: map[string]string{"range": "7d"}},
		{name: "engagements"},
		{name: "reply", values: zhihuReplyValues("c_10001")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recipe := zhihuRecipe(t, tc.name)
			require.NotEmpty(t, recipe.Fixtures)

			sandbox := &Sandbox{
				Root:         zhihuPackRoot,
				AllowedHosts: zhihuHosts,
				Values:       tc.values,
			}
			require.NoError(t, sandbox.Run(context.Background(), recipe, recipe.Fixtures))
		})
	}
}

// TestZhihuRecipeOutputs checks the values each recipe produces, so a recipe
// that merely clicks through without extracting usable data fails the test.
func TestZhihuRecipeOutputs(t *testing.T) {
	t.Run("login", func(t *testing.T) {
		out := runZhihuRecipe(t, zhihuPackRoot, "login", nil)
		require.NotEmpty(t, out.Screenshots["login_state"])
	})

	t.Run("publish", func(t *testing.T) {
		out := runZhihuRecipe(t, zhihuPackRoot, "publish", zhihuPublishValues())
		require.True(t, strings.HasPrefix(out.Values["article_url"], "https://zhuanlan.zhihu.com/p/"),
			"article_url = %q", out.Values["article_url"])
		require.NotEmpty(t, out.Screenshots["publish_result"])
	})

	t.Run("metrics", func(t *testing.T) {
		out := runZhihuRecipe(t, zhihuPackRoot, "metrics", map[string]string{"range": "7d"})

		// The metrics recipe must cover every key the pack maps to a unified
		// metric, otherwise the adapter silently drops a metric.
		var mm struct {
			Fields map[string]string `json:"fields"`
		}
		raw, err := os.ReadFile(filepath.Join(zhihuPackRoot, "metrics_map.json"))
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &mm))
		require.NotEmpty(t, mm.Fields)

		for native := range mm.Fields {
			require.NotEmpty(t, out.Values[native], "metrics recipe must extract %q", native)
			require.GreaterOrEqual(t, zhihuNumber(t, out.Values[native]), 0.0, "metric %q", native)
		}
		require.Equal(t, 12345.0, zhihuNumber(t, out.Values["read_num"]))
		require.NotEmpty(t, out.Screenshots["metrics_snapshot"])
	})

	t.Run("engagements", func(t *testing.T) {
		out := runZhihuRecipe(t, zhihuPackRoot, "engagements", nil)

		var items []map[string]string
		require.NoError(t, json.Unmarshal([]byte(out.Values["engagements"]), &items))
		require.Len(t, items, 3)
		require.Equal(t, "c_10001", items[0]["id"])
		require.Equal(t, "comment", items[0]["kind"])
		require.Equal(t, "知友A", items[0]["author"])
		require.Contains(t, items[0]["content"], "数据来源")
		require.Equal(t, "d_20001", items[2]["id"])
		require.Equal(t, "dm", items[2]["kind"])
		require.NotEmpty(t, out.Screenshots["engagements_snapshot"])
	})

	t.Run("reply", func(t *testing.T) {
		out := runZhihuRecipe(t, zhihuPackRoot, "reply", zhihuReplyValues("c_10001"))
		require.Equal(t, "回复已发送", out.Values["receipt"])
		require.NotEmpty(t, out.Screenshots["reply_result"])
	})
}

// TestZhihuFullOperatorFlow walks publish -> metrics -> engagements -> reply,
// the same sequence the browser adapter drives for an account.
func TestZhihuFullOperatorFlow(t *testing.T) {
	published := runZhihuRecipe(t, zhihuPackRoot, "publish", zhihuPublishValues())
	require.NotEmpty(t, published.Values["article_url"])

	metrics := runZhihuRecipe(t, zhihuPackRoot, "metrics", map[string]string{"range": "7d"})
	require.Equal(t, 678.0, zhihuNumber(t, metrics.Values["upvote_num"]))

	engagements := runZhihuRecipe(t, zhihuPackRoot, "engagements", nil)
	var items []map[string]string
	require.NoError(t, json.Unmarshal([]byte(engagements.Values["engagements"]), &items))
	require.NotEmpty(t, items)
	firstID := items[0]["id"]
	require.NotEmpty(t, firstID)

	replied := runZhihuRecipe(t, zhihuPackRoot, "reply", zhihuReplyValues(firstID))
	require.NotEmpty(t, replied.Values["receipt"])
}

// TestZhihuRecipeRejectsForeignHost proves the pack's allowlist is what bounds
// navigation, not the recipe author's intent.
func TestZhihuRecipeRejectsForeignHost(t *testing.T) {
	_, err := (&RecipeRunner{
		AllowedHosts: zhihuHosts,
		Open: func(context.Context, Input) (Page, error) {
			return nil, ErrNoPage
		},
	}).Run(context.Background(), Recipe{Steps: []RecipeStep{
		{Action: "goto", Value: "https://evil.example/zhihu"},
	}}, Input{})
	require.Error(t, err)
	require.Equal(t, ClassHostNotAllowed, FailureClassOf(err))
}

// TestZhihuRecipeUpgradeIsDataOnly simulates a platform redesign: the recipe
// and its fixture change in a new pack version, while the runner stays
// untouched.
func TestZhihuRecipeUpgradeIsDataOnly(t *testing.T) {
	v2 := filepath.Join(t.TempDir(), "v2")
	copyTree(t, zhihuPackRoot, v2)

	replaceInFile(t, filepath.Join(v2, "browser", "recipes", "publish.json"),
		"PublishSuccess", "PublishResultV2")
	replaceInFile(t, filepath.Join(v2, "fixtures", "publish.html"),
		"PublishSuccess", "PublishResultV2")

	out := runZhihuRecipe(t, v2, "publish", zhihuPublishValues())
	require.True(t, strings.HasPrefix(out.Values["article_url"], "https://zhuanlan.zhihu.com/p/"))

	// The old version keeps working from its own directory.
	old := runZhihuRecipe(t, zhihuPackRoot, "publish", zhihuPublishValues())
	require.True(t, strings.HasPrefix(old.Values["article_url"], "https://zhuanlan.zhihu.com/p/"))
}

// runZhihuRecipe executes one pack recipe against its fixtures and returns the
// extracted output. It mirrors Sandbox.Run but keeps the values.
func runZhihuRecipe(t *testing.T, root, name string, values map[string]string) Output {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(root, "browser", "recipes", name+".json"))
	require.NoError(t, err)
	var recipe Recipe
	require.NoError(t, json.Unmarshal(raw, &recipe))
	require.NotEmpty(t, recipe.Fixtures)

	files := make(map[string][]byte, len(recipe.Fixtures)*2)
	primary := ""
	for _, fixture := range recipe.Fixtures {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(fixture)))
		require.NoError(t, err)
		clean := filepath.ToSlash(filepath.Clean(fixture))
		files[clean] = data
		files[path.Base(clean)] = data
		if primary == "" {
			primary = clean
		}
	}

	srv := httptest.NewServer(fixtureHandler(files, primary))
	t.Cleanup(srv.Close)

	hostMap := make(map[string]string, len(zhihuHosts))
	for _, host := range zhihuHosts {
		hostMap[host] = srv.URL
	}
	runner := &RecipeRunner{
		AllowedHosts: zhihuHosts,
		Open: func(context.Context, Input) (Page, error) {
			return newDOMPage(hostMap), nil
		},
	}
	out, err := runner.Run(context.Background(), recipe, Input{Values: values})
	require.NoError(t, err)
	return out
}

func zhihuNumber(t *testing.T, raw string) float64 {
	t.Helper()
	clean := strings.NewReplacer(",", "", "，", "", " ", "").Replace(raw)
	value, err := strconv.ParseFloat(clean, 64)
	require.NoError(t, err, "metric value %q", raw)
	return value
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, current)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	require.NoError(t, err)
}

func replaceInFile(t *testing.T, name, old, updated string) {
	t.Helper()
	data, err := os.ReadFile(name)
	require.NoError(t, err)
	require.Contains(t, string(data), old)
	require.NoError(t, os.WriteFile(name, []byte(strings.ReplaceAll(string(data), old, updated)), 0o644))
}
