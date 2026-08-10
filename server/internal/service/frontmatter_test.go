package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetFrontmatterField_NoBlock_Creates(t *testing.T) {
	out := setFrontmatterField("# Hello\n\nbody", "name", "foo")
	assert.True(t, strings.HasPrefix(out, "---\nname: foo\n---\n"))
	assert.Contains(t, out, "# Hello")
}

func TestSetFrontmatterField_ReplacesExistingKey(t *testing.T) {
	in := "---\nname: old\ndescription: keep\n---\nbody\n"
	out := setFrontmatterField(in, "name", "new")
	assert.Contains(t, out, "name: new")
	assert.NotContains(t, out, "name: old")
	assert.Contains(t, out, "description: keep")
	assert.Contains(t, out, "body")
}

func TestSetFrontmatterField_AppendsMissingKey(t *testing.T) {
	in := "---\ndescription: only\n---\nbody\n"
	out := setFrontmatterField(in, "name", "added")
	assert.Contains(t, out, "description: only")
	assert.Contains(t, out, "name: added")
}

// Critical: unknown fields, including multi-line list values, survive an edit
// of a neighboring key. Regression guard for the reviewer's I1/I2 concern.
func TestSetFrontmatterField_PreservesUnknownFields(t *testing.T) {
	in := `---
name: old
description: desc
tools:
  - Read
  - Edit
model: opus
color: green
---
body content
`
	out := setFrontmatterField(in, "name", "new")
	assert.Contains(t, out, "name: new")
	assert.Contains(t, out, "tools:")
	assert.Contains(t, out, "  - Read")
	assert.Contains(t, out, "  - Edit")
	assert.Contains(t, out, "model: opus")
	assert.Contains(t, out, "color: green")
	assert.Contains(t, out, "body content")
}

func TestSetFrontmatterField_ReplacesListWithScalar(t *testing.T) {
	// When the key we're rewriting is currently a list, we clobber the list
	// items (deliberate — name is never a list).
	in := "---\nname:\n  - a\n  - b\ndescription: keep\n---\nbody\n"
	out := setFrontmatterField(in, "name", "scalar")
	assert.Contains(t, out, "name: scalar")
	assert.NotContains(t, out, "  - a")
	assert.Contains(t, out, "description: keep")
}

func TestSetFrontmatterField_CRLFNormalized(t *testing.T) {
	in := "---\r\nname: old\r\n---\r\nbody\r\n"
	out := setFrontmatterField(in, "name", "new")
	assert.Contains(t, out, "name: new")
	assert.Contains(t, out, "body")
}

func TestReadFrontmatterScalar(t *testing.T) {
	in := "---\nname: foo\ndescription: bar: with colon\n---\nbody"
	assert.Equal(t, "foo", readFrontmatterScalar(in, "name"))
	assert.Equal(t, "bar: with colon", readFrontmatterScalar(in, "description"))
	assert.Equal(t, "", readFrontmatterScalar(in, "missing"))
	assert.Equal(t, "", readFrontmatterScalar("no frontmatter here", "name"))
}

func TestEnsureFrontmatter_AddsWhenAbsent(t *testing.T) {
	out := ensureFrontmatter("# Title\nbody", "foo", "desc")
	assert.True(t, strings.HasPrefix(out, "---\n"))
	assert.Contains(t, out, "name: foo")
	assert.Contains(t, out, "description: desc")
	assert.Contains(t, out, "# Title")
}

func TestEnsureFrontmatter_KeepsExistingName(t *testing.T) {
	in := "---\nname: existing\n---\nbody"
	out := ensureFrontmatter(in, "override", "new-desc")
	assert.Contains(t, out, "name: existing")
	assert.NotContains(t, out, "name: override")
	// description was absent; it should have been added.
	assert.Contains(t, out, "description: new-desc")
}

func TestRewriteFrontmatterName_ForcesName(t *testing.T) {
	in := "---\nname: old\ndescription: kept\n---\nbody"
	out := rewriteFrontmatterName(in, "new", "ignored-desc")
	assert.Contains(t, out, "name: new")
	assert.Contains(t, out, "description: kept")
}

func TestRewriteNiuniuAgentContent_StampsAndPreserves(t *testing.T) {
	in := `---
name: architect
description: system designer
tools:
  - Read
  - Grep
---
# Body
`
	out := RewriteNiuniuAgentContent(in, "niuniu-architect", "system designer")
	assert.Contains(t, out, "name: niuniu-architect")
	assert.Contains(t, out, "managed_by: niuniu")
	assert.Contains(t, out, "  - Read")
	assert.Contains(t, out, "  - Grep")
	assert.Contains(t, out, "# Body")
	assert.True(t, isManagedByNiuniu(out))
}

func TestIsManagedByNiuniu_FalseWhenAbsent(t *testing.T) {
	assert.False(t, isManagedByNiuniu("---\nname: foo\n---\nbody"))
	assert.False(t, isManagedByNiuniu("# no frontmatter"))
	assert.False(t, isManagedByNiuniu("---\nmanaged_by: someone-else\n---\nbody"))
}

// When UI edits description and resaves, frontmatter description must follow.
// ensureFrontmatter would leave an existing description untouched — this test
// locks in that Create/Update go through syncFrontmatterMetadata instead.
func TestSyncFrontmatterMetadata_ForcesDescription(t *testing.T) {
	in := "---\nname: foo\ndescription: old\ntools:\n  - Read\n---\nbody\n"
	out := syncFrontmatterMetadata(in, "foo", "new description")
	assert.Contains(t, out, "description: new description")
	assert.NotContains(t, out, "description: old")
	// Extras must survive.
	assert.Contains(t, out, "tools:")
	assert.Contains(t, out, "  - Read")
}

func TestSyncFrontmatterMetadata_EmptyDescriptionIsNoOp(t *testing.T) {
	in := "---\nname: foo\ndescription: keep\n---\nbody\n"
	out := syncFrontmatterMetadata(in, "foo", "")
	assert.Contains(t, out, "description: keep")
}

// Round-trip: setting the same value twice is idempotent in content.
func TestSetFrontmatterField_Idempotent(t *testing.T) {
	in := "---\nname: foo\ndescription: bar\n---\nbody\n"
	once := setFrontmatterField(in, "name", "baz")
	twice := setFrontmatterField(once, "name", "baz")
	assert.Equal(t, once, twice)
}
