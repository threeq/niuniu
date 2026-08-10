// Package service: SceneMatcher signal implementations.
//
// Each signal is a pure function (ctx, *WorkspaceFeatures, args) → bool. The
// matcher iterates a scene's match.rules, accumulating weight for each hit
// signal. WorkspaceFeatures is populated once per Rank() call by
// SceneMatcher.collectFeatures from sqlc queries + worktree filesystem scan.
package service

import (
	"context"
	"strings"
)

// WorkspaceFeatures is the snapshot SceneMatcher rules evaluate against.
// Populated by SceneMatcher.collectFeatures; signal funcs are pure.
type WorkspaceFeatures struct {
	OwnerType        string
	OwnerID          int64
	RepoLanguages    []string        // detected languages across all worktrees
	RepoCount        int             // number of attached repos
	BoundCredentials map[string]bool // provider → bound (any alias)
	TemplateSlug     string          // project_templates.slug of bound workflow ("" if unbound)
	LabelTerms       map[string]int  // term → count from kanban issue labels + titles (30d)
	OrgMemberCount   int             // 0 for user owner
}

// SignalFunc is the contract every signal exports. ctx is reserved for future
// signals that may consult external state; M1 implementations ignore it.
type SignalFunc func(ctx context.Context, f *WorkspaceFeatures, args map[string]any) bool

// SceneSignalRegistry maps signal name → implementation. SceneMatcher looks
// up by name; unknown signals are silently skipped (the scene YAML stays
// valid even when authoring against an older niuniu version).
var SceneSignalRegistry = map[string]SignalFunc{
	"workspace.has_repo_language":  signalHasRepoLanguage,
	"workspace.has_repo_count":     signalHasRepoCount,
	"workspace.has_credential":     signalHasCredential,
	"workspace.template_kind":      signalTemplateKind,
	"kanban.label_term_frequency":  signalLabelTermFrequency,
	"org.has_member_count":         signalOrgHasMemberCount,
}

// signalHasRepoLanguage matches if the workspace's repos include the given
// language. `language: any` matches if ANY language was detected.
func signalHasRepoLanguage(_ context.Context, f *WorkspaceFeatures, args map[string]any) bool {
	lang, _ := args["language"].(string)
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return false
	}
	if lang == "any" {
		return len(f.RepoLanguages) > 0
	}
	for _, l := range f.RepoLanguages {
		if strings.EqualFold(l, lang) {
			return true
		}
	}
	return false
}

// signalHasRepoCount matches if RepoCount satisfies the args. Supports {min:N},
// {max:N}, or both. Examples:
//
//	{ min: 1 }       → at least 1 repo
//	{ max: 0 }       → zero repos (catch-all for non-code workspaces)
//	{ min: 2, max: 5 } → 2–5 repos
func signalHasRepoCount(_ context.Context, f *WorkspaceFeatures, args map[string]any) bool {
	hasMin, min := readIntArg(args, "min")
	hasMax, max := readIntArg(args, "max")
	if !hasMin && !hasMax {
		return false
	}
	if hasMin && f.RepoCount < min {
		return false
	}
	if hasMax && f.RepoCount > max {
		return false
	}
	return true
}

// signalHasCredential matches if the owner has a bound credential for the
// given provider. `provider: any` matches if ANY credential is bound.
func signalHasCredential(_ context.Context, f *WorkspaceFeatures, args map[string]any) bool {
	prov, _ := args["provider"].(string)
	prov = strings.ToLower(strings.TrimSpace(prov))
	if prov == "" {
		return false
	}
	if prov == "any" {
		return len(f.BoundCredentials) > 0
	}
	return f.BoundCredentials[prov]
}

// signalTemplateKind matches by project_templates.slug (the workspace's bound
// workflow). args: { kind: "<slug>" }. Note: workspaces don't directly bind a
// template; this signal returns true when the workspace's project default
// template matches. Empty TemplateSlug → false.
func signalTemplateKind(_ context.Context, f *WorkspaceFeatures, args map[string]any) bool {
	want, _ := args["kind"].(string)
	want = strings.TrimSpace(want)
	if want == "" || f.TemplateSlug == "" {
		return false
	}
	return strings.EqualFold(f.TemplateSlug, want)
}

// signalLabelTermFrequency matches if the workspace's kanban issue
// labels/titles contain at least `min_count` total occurrences of any of the
// given `terms` (case-insensitive substring match).
// Default min_count: 1.
//
//	{ terms: ["客服","support"], min_count: 1 }
func signalLabelTermFrequency(_ context.Context, f *WorkspaceFeatures, args map[string]any) bool {
	raw, _ := args["terms"].([]any)
	if len(raw) == 0 || len(f.LabelTerms) == 0 {
		return false
	}
	minCount := 1
	if hasMin, n := readIntArg(args, "min_count"); hasMin {
		minCount = n
	}
	total := 0
	for _, t := range raw {
		needle, ok := t.(string)
		if !ok {
			continue
		}
		needleLow := strings.ToLower(strings.TrimSpace(needle))
		if needleLow == "" {
			continue
		}
		for term, count := range f.LabelTerms {
			if strings.Contains(strings.ToLower(term), needleLow) {
				total += count
				if total >= minCount {
					return true
				}
			}
		}
	}
	return total >= minCount
}

// signalOrgHasMemberCount matches if the owner is an org and its member count
// satisfies the bound. args: { min: N }, optional { max: N }.
func signalOrgHasMemberCount(_ context.Context, f *WorkspaceFeatures, args map[string]any) bool {
	if f.OwnerType != "org" {
		return false
	}
	hasMin, min := readIntArg(args, "min")
	hasMax, max := readIntArg(args, "max")
	if !hasMin && !hasMax {
		return false
	}
	if hasMin && f.OrgMemberCount < min {
		return false
	}
	if hasMax && f.OrgMemberCount > max {
		return false
	}
	return true
}

// EvaluateMatch runs every rule in MatchRules against features and returns
// (totalScore, []RuleHit). Unknown signal names are skipped (no error).
// Score is BaseWeight + sum of weights of matched rules.
type RuleHit struct {
	Signal string
	Weight int
}

func EvaluateMatch(ctx context.Context, rules MatchRules, f *WorkspaceFeatures) (int, []RuleHit) {
	score := rules.BaseWeight
	hits := make([]RuleHit, 0, len(rules.Rules))
	for _, r := range rules.Rules {
		fn, ok := SceneSignalRegistry[r.Signal]
		if !ok {
			continue
		}
		if fn(ctx, f, r.Args) {
			score += r.Weight
			hits = append(hits, RuleHit{Signal: r.Signal, Weight: r.Weight})
		}
	}
	return score, hits
}

// readIntArg accepts both int (legacy YAML parse) and float64 (JSON parse).
func readIntArg(args map[string]any, key string) (bool, int) {
	v, ok := args[key]
	if !ok {
		return false, 0
	}
	switch n := v.(type) {
	case int:
		return true, n
	case int64:
		return true, int(n)
	case float64:
		return true, int(n)
	}
	return false, 0
}
