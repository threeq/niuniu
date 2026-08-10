package service

import (
	"path/filepath"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceAgentTargetFor(t *testing.T) {
	claude := workspaceAgentTargetFor("claude")
	assert.Equal(t, filepath.Join(".claude", "agents"), claude.dir)
	assert.Equal(t, ".md", claude.ext)

	qwen := workspaceAgentTargetFor("qwen")
	assert.Equal(t, filepath.Join(".qwen", "agents"), qwen.dir)
	assert.Equal(t, ".md", qwen.ext)

	codex := workspaceAgentTargetFor("codex")
	assert.Equal(t, filepath.Join(".codex", "agents"), codex.dir)
	assert.Equal(t, ".toml", codex.ext)

	// Unknown CLI falls back to the Claude layout.
	unknown := workspaceAgentTargetFor("mystery")
	assert.Equal(t, claude.dir, unknown.dir)
	assert.Equal(t, claude.ext, unknown.ext)
}

// qwen reuses the markdown renderer, so its output is byte-identical to the
// Claude materialization for the same input.
func TestWorkspaceAgentTargetFor_QwenRendersMarkdown(t *testing.T) {
	in := "---\nname: architect\ndescription: old\n---\n# Body\n"
	qwen := workspaceAgentTargetFor("qwen")
	out := qwen.render(in, "niuniu-architect", "system designer")
	assert.Equal(t, RewriteNiuniuAgentContent(in, "niuniu-architect", "system designer"), out)
	assert.Contains(t, out, "name: niuniu-architect")
	assert.Contains(t, out, "managed_by: niuniu")
	assert.True(t, isManagedByNiuniu(out))
}

// codexDoc mirrors the keys renderCodexAgentTOML emits, used to assert the
// output is valid, parseable TOML.
type codexDoc struct {
	ManagedBy             string `toml:"managed_by"`
	Name                  string `toml:"name"`
	Description           string `toml:"description"`
	Model                 string `toml:"model"`
	DeveloperInstructions string `toml:"developer_instructions"`
}

func TestRenderCodexAgentTOML_ValidAndComplete(t *testing.T) {
	in := `---
name: architect
description: frontmatter-desc
model: opus
tools:
  - Read
  - Edit
---
You are the architect.

Design the system carefully.
`
	out := renderCodexAgentTOML(in, "scene-architect", "db-desc")

	var doc codexDoc
	require.NoError(t, toml.Unmarshal([]byte(out), &doc), "rendered output must be valid TOML")

	assert.Equal(t, "niuniu", doc.ManagedBy)
	// DB-authoritative name/description win over frontmatter.
	assert.Equal(t, "scene-architect", doc.Name)
	assert.Equal(t, "db-desc", doc.Description)
	// model is mapped through from frontmatter.
	assert.Equal(t, "opus", doc.Model)
	// Body (post-frontmatter) lands fully in developer_instructions; the YAML
	// frontmatter and its tools list are dropped.
	assert.Equal(t, "You are the architect.\n\nDesign the system carefully.\n", doc.DeveloperInstructions)
	assert.True(t, isManagedByNiuniuTOML(out))
}

func TestRenderCodexAgentTOML_NoFrontmatter(t *testing.T) {
	in := "Just instructions, no frontmatter.\n"
	out := renderCodexAgentTOML(in, "plain", "")

	var doc codexDoc
	require.NoError(t, toml.Unmarshal([]byte(out), &doc))
	assert.Equal(t, "plain", doc.Name)
	assert.Empty(t, doc.Description) // empty description is omitted
	assert.Empty(t, doc.Model)       // no model key when absent
	assert.Equal(t, in, doc.DeveloperInstructions)
}

// The body may contain the multi-line string delimiter and backslashes; both
// must be escaped so the output stays valid TOML and round-trips exactly.
func TestRenderCodexAgentTOML_EscapesDelimiterAndBackslash(t *testing.T) {
	body := `Quote block: """fenced""" and a path C:\tmp\x and a trailing quote"`
	in := "---\nname: x\n---\n" + body
	out := renderCodexAgentTOML(in, "tricky", "d")

	var doc codexDoc
	require.NoError(t, toml.Unmarshal([]byte(out), &doc), "escaped output must be valid TOML")
	assert.Equal(t, body, doc.DeveloperInstructions)
}

// A body carrying a bare CR (old-Mac line ending) or other C0 control bytes
// must still render to valid, round-tripping TOML rather than silently writing
// a .toml that Codex cannot parse.
func TestRenderCodexAgentTOML_EscapesControlChars(t *testing.T) {
	body := "alpha\rbeta\x1bgamma\x00deltaend\nlast"
	in := "---\nname: x\n---\n" + body
	out := renderCodexAgentTOML(in, "ctrl", "d")

	var doc codexDoc
	require.NoError(t, toml.Unmarshal([]byte(out), &doc), "control chars must be escaped into valid TOML")
	assert.Equal(t, body, doc.DeveloperInstructions, "body must round-trip exactly")
}

func TestIsManagedByNiuniuTOML(t *testing.T) {
	assert.True(t, isManagedByNiuniuTOML("managed_by = \"niuniu\"\nname = \"a\"\n"))
	assert.False(t, isManagedByNiuniuTOML("managed_by = \"someone-else\"\n"))
	assert.False(t, isManagedByNiuniuTOML("name = \"a\"\n"))
	assert.False(t, isManagedByNiuniuTOML(""))
	// A managed_by under a table is NOT a top-level marker.
	assert.False(t, isManagedByNiuniuTOML("[meta]\nmanaged_by = \"niuniu\"\n"))
	// Comments and blank lines before the marker are tolerated.
	assert.True(t, isManagedByNiuniuTOML("# header\n\nmanaged_by = \"niuniu\"\n"))
	// managed_by must be the FIRST top-level key: a user file whose
	// developer_instructions body merely *mentions* the marker is not managed
	// and must be preserved (not deleted) on recompute.
	userFile := "name = \"mine\"\ndeveloper_instructions = \"\"\"\nmanaged_by = \"niuniu\"\n\"\"\"\n"
	assert.False(t, isManagedByNiuniuTOML(userFile))
}

func TestUnquoteTOMLBasic(t *testing.T) {
	assert.Equal(t, "niuniu", unquoteTOMLBasic(`"niuniu"`))
	assert.Equal(t, `a"b`, unquoteTOMLBasic(`"a\"b"`))
	assert.Equal(t, `a\b`, unquoteTOMLBasic(`"a\\b"`))
	assert.Equal(t, "line1\nline2", unquoteTOMLBasic(`"line1\nline2"`))
	// Trailing inline comment after the closing quote is ignored.
	assert.Equal(t, "v", unquoteTOMLBasic(`"v" # comment`))
	// Non-string values yield "".
	assert.Equal(t, "", unquoteTOMLBasic(`123`))
	assert.Equal(t, "", unquoteTOMLBasic(``))
}
