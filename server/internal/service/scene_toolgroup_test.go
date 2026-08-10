package service

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDropToolGroup covers the repo-aware re-enable: a scene hides the harness
// group by default, but dropToolGroup removes it from the disable list when a
// repo is bound, leaving the other (repo-independent) groups untouched.
func TestDropToolGroup(t *testing.T) {
	cases := []struct {
		name   string
		groups []string
		drop   string
		want   []string
	}{
		{"removes harness, keeps multi-agent", []string{"multi-agent", "harness"}, "harness", []string{"multi-agent"}},
		{"no-op when absent", []string{"multi-agent"}, "harness", []string{"multi-agent"}},
		{"empty stays empty", nil, "harness", []string{}},
		{"removes only match", []string{"harness", "multi-agent", "harness"}, "harness", []string{"multi-agent"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dropToolGroup(c.groups, c.drop)
			if !slices.Equal(got, c.want) {
				t.Fatalf("dropToolGroup(%v, %q) = %v, want %v", c.groups, c.drop, got, c.want)
			}
		})
	}
}

// TestProjection_MergeEnableToolGroups covers the opt-in enable-group union
// across layers, deduped, with provenance — the symmetric counterpart of
// disable_tool_groups used to turn on privacy-sensitive tools (browser-history).
func TestProjection_MergeEnableToolGroups(t *testing.T) {
	p := NewProjection()
	p.MergeFrom(&SceneDefinition{EnableToolGroups: []string{"browser-history"}}, LayerOrigin(1))
	p.MergeFrom(&SceneDefinition{EnableToolGroups: []string{"browser-history", "future-group"}}, LayerOrigin(2))

	assert.Equal(t, []string{"browser-history", "future-group"}, p.EnableToolGroups)
	// browser-history was contributed by both layers; future-group only by layer 2.
	assert.Equal(t, []LayerOrigin{1}, p.Provenance["enable_tool_group:browser-history"])
	assert.Equal(t, []LayerOrigin{2}, p.Provenance["enable_tool_group:future-group"])
}
