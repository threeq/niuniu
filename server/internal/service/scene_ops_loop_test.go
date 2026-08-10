package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpsLoop_SharedColumnsContract asserts the platform-agnostic运营闭环 board
// skeleton (opsLoopColumns) holds the invariant contract every运营 loop blueprint
// and its tests rely on — regardless of platform params. This is the single
// implementation issue #637 §1 requires: parameterize, don't copy-paste six lanes.
func TestOpsLoop_SharedColumnsContract(t *testing.T) {
	for _, p := range opsLoopPlatforms() {
		t.Run(p.PlatformKey, func(t *testing.T) {
			cols := opsLoopColumns(p)
			require.Len(t, cols, 6, "loop is exactly 6 lanes")
			assert.Equal(t, "none", cols[0].OpPrimitive, "first lane is a non-acting intake")
			assert.Equal(t, "complete", cols[5].OpPrimitive, "last lane completes the loop")
			// Lane 3 is the发布前审校 gate lane, carrying the review discipline needle.
			assert.Contains(t, cols[3].Instruction, "门禁")
			assert.Contains(t, cols[3].Instruction, "合规")
			// The loop's fan-out (A) + 回流 (C) conventions are baked into the
			// first/last lanes so they reach the agent as phase prompts at runtime.
			assert.Contains(t, cols[0].Instruction, "batch_create_issues", "采集/选题 lane teaches fan-out")
			assert.Contains(t, cols[5].Instruction, "回流", "数据复盘 lane teaches 回流")
			// Platform phrasing is actually woven in (not a generic stub).
			assert.Contains(t, cols[5].WhenToUse, p.MetricsFocus)
		})
	}
}

// TestOpsLoop_ChildTemplates asserts the three child-issue types (A 采集·选题 /
// B 每内容 / C 复盘·回流) are first-class parameterized structures per issue #637 §1
// (Epic 父 + A/B/C 三类子issue): exactly three kinds, A/C periodic (cron) and B
// parallel on-demand fan-out, only B carries the生产·发布·效果 lifecycle board, and
// each phase prompt weaves in the platform + names the primitive it drives.
func TestOpsLoop_ChildTemplates(t *testing.T) {
	for _, p := range opsLoopPlatforms() {
		t.Run(p.PlatformKey, func(t *testing.T) {
			kids := opsLoopChildTemplates(p)
			require.Len(t, kids, 3, "Epic → exactly three child-issue types")

			byKind := map[OpsLoopChildKind]OpsLoopChildTemplate{}
			for _, c := range kids {
				byKind[c.Kind] = c
				assert.NotEmptyf(t, c.PhasePrompt, "%s child needs a phase prompt", c.Kind)
			}

			a, b, c := byKind[OpsLoopCollect], byKind[OpsLoopContent], byKind[OpsLoopRecap]
			require.NotZero(t, a.Kind)
			require.NotZero(t, b.Kind)
			require.NotZero(t, c.Kind)

			// A 采集·选题: periodic, drives fan-out of B under the Epic parent.
			assert.True(t, a.Periodic, "A is cron-periodic")
			assert.Nil(t, a.Lifecycle, "A is single-purpose (no lifecycle board)")
			assert.Contains(t, a.PhasePrompt, "batch_create_issues(parent_issue_id=运营Epic")
			assert.Contains(t, a.PhasePrompt, "start_workspace")

			// B 每内容: parallel, and the ONLY type with a生产·发布·效果 lifecycle board.
			assert.True(t, b.Parallel, "B runs in parallel (one per topic)")
			assert.Len(t, b.Lifecycle, 6, "B carries the shared 6-lane lifecycle board")
			assert.Contains(t, b.PhasePrompt, "advance_issue")

			// C 复盘·回流: periodic, aggregates B's ⑤ and 回流s into the next round.
			assert.True(t, c.Periodic, "C is cron-periodic")
			assert.Nil(t, c.Lifecycle, "C is single-purpose (no lifecycle board)")
			assert.Contains(t, c.PhasePrompt, "回灌①")
			assert.Contains(t, c.PhasePrompt, p.MetricsFocus, "C names the platform's ⑤ metrics")
		})
	}
}

// TestOpsLoop_OrchestrationPromptBuiltFromChildren asserts the runtime convention
// is BUILT from the child templates (single source) — every child's label + phase
// prompt appears in the orchestration prompt body, so the three types and the prose
// can never drift apart.
func TestOpsLoop_OrchestrationPromptBuiltFromChildren(t *testing.T) {
	for _, p := range opsLoopPlatforms() {
		body := opsLoopOrchestrationPrompt(p).Body
		for _, c := range opsLoopChildTemplates(p) {
			assert.Containsf(t, body, c.Label, "%s: orchestration body must include child label %q", p.PlatformKey, c.Label)
			assert.Containsf(t, body, c.PhasePrompt, "%s: orchestration body must include child phase prompt", p.PlatformKey)
		}
	}
}

// TestOpsLoop_ReusedByMultiplePlatforms is the concrete "≥2 scenes reuse ONE
// capability" proof (issue #637 §2): the registry holds ≥2 distinct platforms,
// each mapping to a distinct loop blueprint + scene, all built from the same
// opsLoopColumns / opsLoopBlueprintDef code path — not hand-rolled per platform.
func TestOpsLoop_ReusedByMultiplePlatforms(t *testing.T) {
	platforms := opsLoopPlatforms()
	require.GreaterOrEqual(t, len(platforms), 2, "generality needs ≥2 platforms reusing the loop")

	seenSlug := map[string]bool{}
	seenScene := map[string]bool{}
	for _, p := range platforms {
		bp := opsLoopBlueprintDef(p, "desc")
		assert.Equal(t, opsLoopBlueprintSlug(p), bp.slug)
		assert.Len(t, bp.columns, 6, "%s reuses the 6-lane skeleton", p.PlatformKey)
		require.Len(t, bp.scenes, 1)
		assert.Equal(t, p.SceneSlug, bp.scenes[0].Slug)
		assert.False(t, seenSlug[bp.slug], "distinct blueprint slug per platform")
		assert.False(t, seenScene[p.SceneSlug], "distinct scene per platform")
		seenSlug[bp.slug], seenScene[p.SceneSlug] = true, true

		// And each platform's loop blueprint is actually registered as a builtin.
		_, ok := builtinBlueprintDefBySlug(opsLoopBlueprintSlug(p))
		assert.Truef(t, ok, "%s loop blueprint must be a seeded builtin", p.PlatformKey)
	}
	// The two shipped platforms today.
	assert.True(t, seenScene["wechat-mp"] && seenScene["content-marketing"],
		"公众号 + 内容营销 both reuse the loop")
}

// TestOpsLoop_SceneOrchestrationPromptNoDrift asserts each运营 scene's static
// ops-loop-orchestration prompt (authored in YAML, seeded via the YAML path)
// matches the canonical Go source opsLoopOrchestrationPrompt — so the runtime
// convention has ONE source of truth and the two never drift.
func TestOpsLoop_SceneOrchestrationPromptNoDrift(t *testing.T) {
	sceneFile := map[string]string{
		"wechat-mp":         "wechat-mp.yaml",
		"content-marketing": "content-marketing.yaml",
	}
	for _, p := range opsLoopPlatforms() {
		t.Run(p.PlatformKey, func(t *testing.T) {
			_, def := loadBuiltinSceneDef(t, sceneFile[p.PlatformKey])
			var frag *PromptFragment
			for i := range def.Prompts {
				if def.Prompts[i].ID == "ops-loop-orchestration" {
					frag = &def.Prompts[i]
				}
			}
			require.NotNilf(t, frag, "%s scene must carry the ops-loop-orchestration prompt", p.PlatformKey)
			want := opsLoopOrchestrationPrompt(p)
			assert.Equal(t, want.Title, frag.Title, "title single-sourced")
			assert.Equal(t, strings.TrimSpace(want.Body), strings.TrimSpace(frag.Body),
				"%s scene prompt must match ops_loop.go (no drift)", p.PlatformKey)
			// Platform name is woven in — proving parameterization, not a copy.
			assert.Contains(t, frag.Body, p.PlatformName)
		})
	}
}
