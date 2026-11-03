package intake

import (
	"slices"
	"testing"
)

func TestPolicyEvaluate(t *testing.T) {
	policy := Policy{
		ProjectID:  1,
		SourceKind: SourceKindIMBot,
		Version:    7,
		Mode:       ModeAuto,
		AllowList:  []string{"ou_alice"},
		BotList:    []string{"bot_release"},
		Rules: []Rule{
			{ID: 11, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy", "发布"}}},
			{ID: 12, Enabled: true, Conditions: RuleConditions{Actors: []string{"ou_bob"}}},
			{ID: 13, Enabled: false, Conditions: RuleConditions{Actors: []string{"ou_carol"}}},
			{ID: 14, Enabled: true},
			{ID: 15, Enabled: true, Conditions: RuleConditions{Actors: []string{"ou_dave"}, Keywords: []string{"deploy"}}},
		},
	}

	tests := []struct {
		name       string
		actorID    string
		isBot      bool
		text       string
		want       Strength
		wantRules  []int64
		wantReason string
	}{
		{"allow listed actor is explicit", "ou_alice", false, "hello", StrengthExplicit, nil, ReasonAllowList},
		{"allow list wins over bot list", "ou_alice", true, "hello", StrengthExplicit, nil, ReasonAllowList},
		{"bot list actor is inferred", "bot_release", true, "build it", StrengthInferred, nil, ReasonBotList},
		{"bot outside bot list is denied", "bot_other", true, "build it", StrengthDenied, nil, ReasonDenied},
		{"keyword rule is case-insensitive", "ou_dave", false, "please DEPLOY now", StrengthInferred, []int64{11}, ReasonRule},
		{"chinese keyword rule matches", "ou_dave", false, "帮我发布一下", StrengthInferred, []int64{11}, ReasonRule},
		{"actor rule matches", "ou_bob", false, "anything", StrengthInferred, []int64{12}, ReasonRule},
		{"disabled rule never matches", "ou_carol", false, "deploy", StrengthInferred, []int64{11}, ReasonRule},
		{"empty rule is not a catch-all", "ou_erin", false, "hello", StrengthDenied, nil, ReasonDenied},
		{"all condition groups must match", "ou_dave", false, "ship it", StrengthDenied, nil, ReasonDenied},
		{"unknown actor is denied", "", false, "hello", StrengthDenied, nil, ReasonDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := policy.Evaluate(Trigger{
				SourceKind: SourceKindIMBot,
				Actor:      ActorRef{ExtID: tt.actorID, IsBot: tt.isBot},
				Text:       tt.text,
			})
			if got.Strength != tt.want {
				t.Fatalf("strength = %q, want %q", got.Strength, tt.want)
			}
			if got.ReasonCode != tt.wantReason {
				t.Fatalf("reason = %q, want %q", got.ReasonCode, tt.wantReason)
			}
			if !slices.Equal(got.MatchedRuleIDs, tt.wantRules) {
				t.Fatalf("matched rules = %v, want %v", got.MatchedRuleIDs, tt.wantRules)
			}
		})
	}
}

func TestPolicyEvaluateFirstMatchingRuleWins(t *testing.T) {
	policy := Policy{
		Rules: []Rule{
			{ID: 1, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy"}}},
			{ID: 2, Enabled: true, Conditions: RuleConditions{Keywords: []string{"deploy", "release"}}},
		},
	}
	got := policy.Evaluate(Trigger{Text: "deploy and release"})
	if !slices.Equal(got.MatchedRuleIDs, []int64{1}) {
		t.Fatalf("matched rules = %v, want [1]", got.MatchedRuleIDs)
	}
}
