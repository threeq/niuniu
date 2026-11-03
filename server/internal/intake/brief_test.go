package intake

import "testing"

func TestNewBrief(t *testing.T) {
	prov := Provenance{
		DecisionID:    9,
		PolicyVersion: 3,
		Strength:      StrengthInferred,
		ActorExtID:    "ou_alice",
		RuleIDs:       []int64{4},
	}
	brief := NewBrief(Trigger{
		SourceKind: SourceKindGitHub,
		Text:       "  Fix the flaky login test  ",
		Thread:     ThreadRef{ThreadExtID: "PR-42"},
	}, prov)

	if brief.Goal != "Fix the flaky login test" {
		t.Fatalf("goal = %q", brief.Goal)
	}
	if brief.Delivery != (DeliverySpec{SourceKind: SourceKindGitHub, ThreadExtID: "PR-42", ReplyMode: "thread"}) {
		t.Fatalf("delivery = %+v", brief.Delivery)
	}
	if brief.Provenance.DecisionID != 9 || brief.Provenance.PolicyVersion != 3 {
		t.Fatalf("provenance = %+v", brief.Provenance)
	}
}

func TestTaskBriefGoalCondition(t *testing.T) {
	tests := []struct {
		name  string
		brief TaskBrief
		want  string
	}{
		{
			name:  "goal only",
			brief: TaskBrief{Goal: "Fix login"},
			want:  "Fix login",
		},
		{
			name:  "empty brief",
			brief: TaskBrief{},
			want:  "",
		},
		{
			name:  "goal and acceptance",
			brief: TaskBrief{Goal: "Fix login", Acceptance: []string{"test passes", " no regression "}},
			want:  "Fix login\n\n验收标准（全部满足才算完成）：\n- test passes\n- no regression",
		},
		{
			name:  "acceptance without goal",
			brief: TaskBrief{Acceptance: []string{"test passes"}},
			want:  "验收标准（全部满足才算完成）：\n- test passes",
		},
		{
			name:  "blank acceptance items are skipped",
			brief: TaskBrief{Goal: "Fix login", Acceptance: []string{"", "  "}},
			want:  "Fix login",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.brief.GoalCondition(); got != tt.want {
				t.Fatalf("GoalCondition() = %q, want %q", got, tt.want)
			}
		})
	}
}
