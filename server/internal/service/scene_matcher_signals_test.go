package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func feats() *WorkspaceFeatures {
	return &WorkspaceFeatures{
		BoundCredentials: map[string]bool{},
		LabelTerms:       map[string]int{},
	}
}

func TestSignal_HasRepoLanguage_SpecificMatch(t *testing.T) {
	f := feats()
	f.RepoLanguages = []string{"go", "typescript"}
	assert.True(t, signalHasRepoLanguage(context.Background(), f, map[string]any{"language": "go"}))
	assert.False(t, signalHasRepoLanguage(context.Background(), f, map[string]any{"language": "python"}))
}

func TestSignal_HasRepoLanguage_AnyMatchesIfAnyDetected(t *testing.T) {
	f := feats()
	f.RepoLanguages = []string{"rust"}
	assert.True(t, signalHasRepoLanguage(context.Background(), f, map[string]any{"language": "any"}))
}

func TestSignal_HasRepoLanguage_AnyFalseWhenEmpty(t *testing.T) {
	f := feats()
	assert.False(t, signalHasRepoLanguage(context.Background(), f, map[string]any{"language": "any"}))
}

func TestSignal_HasRepoLanguage_CaseInsensitive(t *testing.T) {
	f := feats()
	f.RepoLanguages = []string{"Go"}
	assert.True(t, signalHasRepoLanguage(context.Background(), f, map[string]any{"language": "go"}))
}

func TestSignal_HasRepoCount_MinOnly(t *testing.T) {
	f := feats()
	f.RepoCount = 2
	assert.True(t, signalHasRepoCount(context.Background(), f, map[string]any{"min": 1}))
	assert.False(t, signalHasRepoCount(context.Background(), f, map[string]any{"min": 5}))
}

func TestSignal_HasRepoCount_MaxZeroForZeroRepoWorkspace(t *testing.T) {
	f := feats()
	f.RepoCount = 0
	assert.True(t, signalHasRepoCount(context.Background(), f, map[string]any{"max": 0}))
	f.RepoCount = 1
	assert.False(t, signalHasRepoCount(context.Background(), f, map[string]any{"max": 0}))
}

func TestSignal_HasRepoCount_MinAndMax(t *testing.T) {
	f := feats()
	f.RepoCount = 3
	assert.True(t, signalHasRepoCount(context.Background(), f, map[string]any{"min": 2, "max": 5}))
	assert.False(t, signalHasRepoCount(context.Background(), f, map[string]any{"min": 4, "max": 5}))
}

func TestSignal_HasRepoCount_NoArgsReturnsFalse(t *testing.T) {
	f := feats()
	f.RepoCount = 3
	assert.False(t, signalHasRepoCount(context.Background(), f, map[string]any{}))
}

func TestSignal_HasCredential_Specific(t *testing.T) {
	f := feats()
	f.BoundCredentials["slack"] = true
	assert.True(t, signalHasCredential(context.Background(), f, map[string]any{"provider": "slack"}))
	assert.False(t, signalHasCredential(context.Background(), f, map[string]any{"provider": "github"}))
}

func TestSignal_HasCredential_AnyMatchesIfAny(t *testing.T) {
	f := feats()
	f.BoundCredentials["github"] = true
	assert.True(t, signalHasCredential(context.Background(), f, map[string]any{"provider": "any"}))
}

func TestSignal_TemplateKind(t *testing.T) {
	f := feats()
	f.TemplateSlug = "qa-flow"
	assert.True(t, signalTemplateKind(context.Background(), f, map[string]any{"kind": "qa-flow"}))
	assert.False(t, signalTemplateKind(context.Background(), f, map[string]any{"kind": "design"}))
}

func TestSignal_TemplateKind_EmptyWorkspace(t *testing.T) {
	f := feats()
	assert.False(t, signalTemplateKind(context.Background(), f, map[string]any{"kind": "qa-flow"}))
}

func TestSignal_LabelTermFrequency_MatchesSubstring(t *testing.T) {
	f := feats()
	f.LabelTerms["客服反馈"] = 3
	f.LabelTerms["bug"] = 5
	assert.True(t, signalLabelTermFrequency(context.Background(), f, map[string]any{
		"terms":     []any{"客服"},
		"min_count": 1,
	}))
}

func TestSignal_LabelTermFrequency_MinCount(t *testing.T) {
	f := feats()
	f.LabelTerms["support-ticket"] = 2
	assert.True(t, signalLabelTermFrequency(context.Background(), f, map[string]any{
		"terms":     []any{"support"},
		"min_count": 2,
	}))
	assert.False(t, signalLabelTermFrequency(context.Background(), f, map[string]any{
		"terms":     []any{"support"},
		"min_count": 5,
	}))
}

func TestSignal_LabelTermFrequency_EmptyTermsReturnFalse(t *testing.T) {
	f := feats()
	f.LabelTerms["x"] = 1
	assert.False(t, signalLabelTermFrequency(context.Background(), f, map[string]any{
		"terms": []any{},
	}))
}

func TestSignal_OrgHasMemberCount_UserOwnerReturnsFalse(t *testing.T) {
	f := feats()
	f.OwnerType = "user"
	f.OrgMemberCount = 100
	assert.False(t, signalOrgHasMemberCount(context.Background(), f, map[string]any{"min": 1}))
}

func TestSignal_OrgHasMemberCount_OrgMatchesMin(t *testing.T) {
	f := feats()
	f.OwnerType = "org"
	f.OrgMemberCount = 10
	assert.True(t, signalOrgHasMemberCount(context.Background(), f, map[string]any{"min": 5}))
	assert.False(t, signalOrgHasMemberCount(context.Background(), f, map[string]any{"min": 20}))
}

func TestEvaluateMatch_AccumulatesWeights(t *testing.T) {
	f := feats()
	f.RepoCount = 1 // not zero — so {max:0} rule does NOT match
	f.RepoLanguages = []string{"go"}
	f.BoundCredentials["slack"] = true
	rules := MatchRules{
		BaseWeight: 5,
		Rules: []MatchRule{
			{Signal: "workspace.has_repo_language", Args: map[string]any{"language": "go"}, Weight: 30},
			{Signal: "workspace.has_credential", Args: map[string]any{"provider": "slack"}, Weight: 20},
			{Signal: "workspace.has_repo_count", Args: map[string]any{"max": 0}, Weight: 25}, // won't match (RepoCount=1)
		},
	}
	score, hits := EvaluateMatch(context.Background(), rules, f)
	assert.Equal(t, 5+30+20, score)
	assert.Len(t, hits, 2)
}

func TestEvaluateMatch_UnknownSignalSilentlySkipped(t *testing.T) {
	f := feats()
	rules := MatchRules{
		Rules: []MatchRule{
			{Signal: "future.unknown_signal", Args: nil, Weight: 100},
		},
	}
	score, hits := EvaluateMatch(context.Background(), rules, f)
	assert.Equal(t, 0, score)
	assert.Empty(t, hits)
}

func TestEvaluateMatch_NegativeWeightSubtracts(t *testing.T) {
	f := feats()
	f.RepoCount = 1
	rules := MatchRules{
		BaseWeight: 50,
		Rules: []MatchRule{
			{Signal: "workspace.has_repo_count", Args: map[string]any{"min": 1}, Weight: -30},
		},
	}
	score, _ := EvaluateMatch(context.Background(), rules, f)
	assert.Equal(t, 20, score)
}

func TestReadIntArg_Float64FromJSON(t *testing.T) {
	args := map[string]any{"min": float64(3)}
	has, n := readIntArg(args, "min")
	assert.True(t, has)
	assert.Equal(t, 3, n)
}

func TestReadIntArg_MissingKey(t *testing.T) {
	has, _ := readIntArg(map[string]any{}, "absent")
	assert.False(t, has)
}
