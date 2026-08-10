package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFrontmatter(t *testing.T) {
	t.Run("basic frontmatter", func(t *testing.T) {
		content := "---\nname: code-reviewer\ndescription: Reviews code\n---\n\nYou are a code reviewer."
		fm, body, err := parseFrontmatter(content)
		require.NoError(t, err)
		assert.Equal(t, "code-reviewer", fm["name"])
		assert.Equal(t, "Reviews code", fm["description"])
		assert.Equal(t, "\nYou are a code reviewer.", body)
	})

	t.Run("quoted values", func(t *testing.T) {
		content := "---\nname: \"my agent\"\ndescription: 'has quotes'\n---\nbody"
		fm, body, err := parseFrontmatter(content)
		require.NoError(t, err)
		assert.Equal(t, "my agent", fm["name"])
		assert.Equal(t, "has quotes", fm["description"])
		assert.Equal(t, "body", body)
	})

	t.Run("no frontmatter", func(t *testing.T) {
		content := "Just a plain file"
		fm, body, err := parseFrontmatter(content)
		require.NoError(t, err)
		assert.Empty(t, fm)
		assert.Equal(t, "Just a plain file", body)
	})

	t.Run("empty content", func(t *testing.T) {
		fm, body, err := parseFrontmatter("")
		require.NoError(t, err)
		assert.Empty(t, fm)
		assert.Equal(t, "", body)
	})
}
