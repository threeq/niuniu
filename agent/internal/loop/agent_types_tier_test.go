package loop

import (
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// Builtin subagent presets carry model-tier hints resolved through
// AgentFactory.ModelFor — explore is bulk mechanical search (fast tier),
// plan is deep reasoning (high tier), worker/reviewer stay on the base
// model unless a tier env overrides them.
func TestBuiltinAgentTypes_TierHints(t *testing.T) {
	byName := map[string]string{}
	for _, at := range BuiltinAgentTypes() {
		byName[at.Name] = at.ModelTier
	}
	if byName["explore"] != model.TierFast {
		t.Errorf("explore tier = %q, want %q", byName["explore"], model.TierFast)
	}
	if byName["plan"] != model.TierHigh {
		t.Errorf("plan tier = %q, want %q", byName["plan"], model.TierHigh)
	}
	if byName["worker"] != "" || byName["reviewer"] != "" {
		t.Errorf("worker/reviewer must stay on the base model, got worker=%q reviewer=%q", byName["worker"], byName["reviewer"])
	}
}

// ModelFor receives the requested tier and its result replaces the base
// model for the typed child.
func TestApplyType_ModelForReceivesTier(t *testing.T) {
	var seen []string
	tierModel := &fakeModel{}
	f := newAgentFactory(&fakeModel{})
	f.ModelFor = func(tier string) model.Model {
		seen = append(seen, tier)
		return tierModel
	}
	_, m, err := f.applyType(tools.NewRegistry(&stubTool{}), "sys", "plan")
	if err != nil {
		t.Fatalf("applyType: %v", err)
	}
	if len(seen) != 1 || seen[0] != model.TierHigh {
		t.Fatalf("ModelFor tiers seen = %v, want [high]", seen)
	}
	if m != model.Model(tierModel) {
		t.Fatal("applyType did not use the ModelFor result as the child model")
	}
}

// An untyped child keeps the base model — ModelFor must not be consulted.
func TestApplyType_EmptyTypeUsesBaseModel(t *testing.T) {
	base := &fakeModel{}
	f := newAgentFactory(base)
	f.ModelFor = func(tier string) model.Model {
		t.Fatalf("ModelFor consulted for empty tier %q", tier)
		return base
	}
	_, m, err := f.applyType(tools.NewRegistry(&stubTool{}), "sys", "")
	if err != nil {
		t.Fatalf("applyType: %v", err)
	}
	if m != model.Model(base) {
		t.Fatal("untyped child must use the base model")
	}
}
