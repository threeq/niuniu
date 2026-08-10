package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validCuratedCategories mirrors the 分工分类 the frontend renders a section for.
var validCuratedCategories = map[string]bool{
	"engineering": true,
	"security":    true,
	"design":      true,
	"marketing":   true,
}

// TestCuratedSource_Loads asserts the embedded catalog parses and every entry is
// well-formed: slug name, localized display name + description, a known category,
// and an upstream provenance URL pointing at agency-agents.
func TestCuratedSource_Loads(t *testing.T) {
	s := NewCuratedSource()
	agents, err := s.List(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, agents, "curated catalog should embed at least one agent")

	seen := map[string]bool{}
	for _, a := range agents {
		assert.Equal(t, "curated", a.Source, "source tag")
		assert.NotEmpty(t, a.Name, "slug name")
		assert.False(t, strings.ContainsAny(a.Name, ` /\`), "slug %q must be filesystem-safe", a.Name)
		assert.NotEmpty(t, a.DisplayName, "%s: 汉化 display_name required", a.Name)
		assert.NotEmpty(t, a.Description, "%s: 汉化 description required", a.Name)
		require.Len(t, a.Tags, 1, "%s: exactly one category tag", a.Name)
		assert.True(t, validCuratedCategories[a.Tags[0]], "%s: unknown category %q", a.Name, a.Tags[0])
		assert.Contains(t, a.SourceURL, "agency-agents", "%s: provenance source_url", a.Name)
		assert.False(t, seen[a.Name], "duplicate slug %q", a.Name)
		seen[a.Name] = true
	}
}

// TestCuratedSource_GetReturnsBodyOnly is the safety vet: importing a curated
// agent must NOT carry the upstream frontmatter — no name/description/color/emoji
// scaffolding and, critically, no `tools:` capability grant (a few marketing
// personas declare one upstream). Get returns the persona body only.
func TestCuratedSource_GetReturnsBodyOnly(t *testing.T) {
	s := NewCuratedSource()
	agents, err := s.List(context.Background())
	require.NoError(t, err)

	for _, a := range agents {
		detail, err := s.Get(context.Background(), a.Name)
		require.NoError(t, err, "Get %s", a.Name)
		require.NotEmpty(t, strings.TrimSpace(detail.Content), "%s: body must not be empty", a.Name)

		assert.False(t, strings.HasPrefix(strings.TrimSpace(detail.Content), "---"),
			"%s: body must not start with a frontmatter block", a.Name)

		// No frontmatter capability declaration leaks through. Scan only the head
		// of the content where a stray frontmatter line would land.
		head := detail.Content
		if len(head) > 200 {
			head = head[:200]
		}
		for _, line := range strings.Split(head, "\n") {
			assert.NotRegexp(t, `^\s*tools\s*:`, line,
				"%s: tools capability must not survive import", a.Name)
		}
	}
}

// TestCuratedSource_GetUnknown returns a not-found error the handler maps to 404.
func TestCuratedSource_GetUnknown(t *testing.T) {
	s := NewCuratedSource()
	_, err := s.Get(context.Background(), "does-not-exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
