// Package service: SceneMatcher.Rank — produces a sorted recommendation
// list of scenes for one workspace based on weighted signal rules.
//
// Two responsibilities:
//
//   - collectFeatures: snapshot the workspace's signals (repo languages,
//     bound credentials, kanban label term frequencies, org member count).
//   - Rank: iterate accessible-but-unattached scenes, score via the
//     signal registry in scene_matcher_signals.go, sort, limit.
//
// Stateless except for a *store.Queries handle; safe to share.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// SceneMatcher computes per-workspace scene rankings.
type SceneMatcher struct {
	q       *store.Queries
	dataDir string
}

// NewSceneMatcher constructs a matcher. dataDir is needed so we can resolve
// owner-scoped workspace + repository paths and scan their file extensions
// for language detection (repositories.detected_languages does not exist;
// see architecture-review note in the spec §14).
func NewSceneMatcher(db *sql.DB, dataDir string) *SceneMatcher {
	// store.NewQueries — driver-aware; see CLAUDE.md "Driver-aware DB access".
	return &SceneMatcher{q: store.NewQueries(db), dataDir: dataDir}
}

// RankedScene is one entry in the recommendation list. Hits explain why the
// score landed where it did (UI tooltip surface).
type RankedScene struct {
	Scene store.Scene
	Score int
	Hits  []RuleHit
}

// Rank returns up to `limit` scenes (0 = no limit) ordered by score desc.
// Already-attached scenes are excluded. Scenes with non-positive score are
// dropped (recommendation is opinion: only show what we think helps).
func (m *SceneMatcher) Rank(ctx context.Context, wsID, userID int64, limit int) ([]RankedScene, error) {
	feats, err := m.collectFeatures(ctx, wsID)
	if err != nil {
		return nil, err
	}
	scenes, err := m.q.ListScenesAccessible(ctx, store.ListScenesAccessibleParams{
		OwnerType: feats.OwnerType,
		OwnerID:   feats.OwnerID,
		UserID:    userID,
	})
	if err != nil {
		return nil, err
	}
	attached, _ := m.q.ListAttachedSceneIDs(ctx, wsID)
	attachedSet := make(map[int64]bool, len(attached))
	for _, row := range attached {
		if row.Valid {
			attachedSet[row.Int64] = true
		}
	}
	out := make([]RankedScene, 0, len(scenes))
	for _, s := range scenes {
		if attachedSet[s.ID] {
			continue
		}
		def, err := DecodeDefinition(s.Definition)
		if err != nil {
			slog.Warn("scene matcher: bad scene definition", "scene_id", s.ID, "err", err)
			continue
		}
		score, hits := EvaluateMatch(ctx, def.Match, feats)
		if score <= 0 {
			continue
		}
		out = append(out, RankedScene{Scene: s, Score: score, Hits: hits})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

// collectFeatures snapshots the workspace's signals. Each step degrades to
// empty/zero on error (matcher should be robust — a missing project doesn't
// kill recommendations for the other signals).
func (m *SceneMatcher) collectFeatures(ctx context.Context, wsID int64) (*WorkspaceFeatures, error) {
	ws, err := m.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return nil, err
	}
	feats := &WorkspaceFeatures{
		OwnerType:        ws.OwnerType,
		OwnerID:          ws.OwnerID,
		BoundCredentials: map[string]bool{},
		LabelTerms:       map[string]int{},
	}
	owner := OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}

	// 1. Repository signals: count + language detection.
	worktrees, err := m.q.ListWorktrees(ctx, wsID)
	if err == nil {
		feats.RepoCount = len(worktrees)
		langCounts := map[string]int{}
		for _, wt := range worktrees {
			repoPath := owner.RepositoryPath(m.dataDir, wt.RepositoryID)
			scanLanguageExtensions(repoPath, 0, langCounts)
		}
		feats.RepoLanguages = topNLanguages(langCounts, 3)
	}

	// 2. Bound credentials by provider (any credential alias counts).
	// Use CreatedBy as the "user_id" axis for credential lookup — the
	// workspaces table doesn't have a generic user_id column. If
	// CreatedBy is null (system-created workspace), skip the lookup.
	if ws.CreatedBy.Valid {
		creds, err := m.q.ListExternalCredentialsForUser(ctx, store.ListExternalCredentialsForUserParams{
			OwnerType: ws.OwnerType,
			OwnerID:   ws.OwnerID,
			UserID:    ws.CreatedBy.Int64,
		})
		if err == nil {
			for _, c := range creds {
				feats.BoundCredentials[strings.ToLower(c.Provider)] = true
			}
		}
	}

	// 3. Org member count (only meaningful for org-owned workspaces).
	if ws.OwnerType == "org" {
		members, _ := m.q.ListOrgMembers(ctx, ws.OwnerID)
		feats.OrgMemberCount = len(members)
	}

	// 4. Kanban label/title term frequency over recent issues. The path is
	//    ws.IssueID → Issue.ColumnID → Column.ProjectID → ListIssuesByProject.
	if ws.IssueID.Valid {
		issue, err := m.q.GetIssue(ctx, ws.IssueID.Int64)
		if err == nil {
			col, err := m.q.GetColumn(ctx, issue.ColumnID)
			if err == nil {
				collectKanbanTerms(ctx, m.q, col.ProjectID, feats.LabelTerms)
			}
		}
	}

	// 5. Workspaces don't bind project_templates directly (architecture
	//    review confirmed no template_id column). Leave TemplateSlug = "".
	return feats, nil
}

// collectKanbanTerms tokenizes recent issue titles + labels into a frequency
// map. "Recent" = whatever ListIssuesByProject returns (no time filter in
// M1 — pageable later via dedicated query).
func collectKanbanTerms(ctx context.Context, q *store.Queries, projectID int64, out map[string]int) {
	issues, err := q.ListIssuesByProject(ctx, projectID)
	if err != nil {
		return
	}
	for _, row := range issues {
		tokenizeInto(row.Title, out)
		// row may carry a flattened label list as a comma-joined string in
		// some sqlc shapes; check both fields defensively.
		if labels := safeIssueLabels(row); labels != "" {
			tokenizeInto(labels, out)
		}
	}
}

// safeIssueLabels returns a comma-joined labels field if the row exposes one,
// else "". Wrapped to keep this matcher resilient to sqlc shape drift.
func safeIssueLabels(row any) string {
	b, _ := json.Marshal(row)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return ""
	}
	for _, key := range []string{"labels", "label_names", "Labels"} {
		if v, ok := m[key]; ok {
			switch t := v.(type) {
			case string:
				return t
			case []any:
				parts := make([]string, 0, len(t))
				for _, x := range t {
					if s, ok := x.(string); ok {
						parts = append(parts, s)
					}
				}
				return strings.Join(parts, ",")
			}
		}
	}
	return ""
}

// tokenizeInto splits on whitespace + punctuation, lowercases, and bumps
// the count for each term of length ≥ 2.
func tokenizeInto(s string, out map[string]int) {
	if s == "" {
		return
	}
	splitter := func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	}
	for _, raw := range strings.FieldsFunc(s, splitter) {
		t := strings.ToLower(raw)
		if utf8RuneCount(t) < 2 {
			continue
		}
		out[t]++
	}
}

func utf8RuneCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// scanLanguageExtensions walks repo path, counts files by language. Reuses
// the depth+skipDirs model from service/mcp_detector.go scanRepo.
func scanLanguageExtensions(root string, depth int, counts map[string]int) {
	if root == "" || depth > maxScanDepth {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lang := languageFromFilename(e.Name())
		if lang != "" {
			counts[lang]++
		}
	}
	for _, e := range entries {
		if !e.IsDir() || skipDirs[e.Name()] {
			continue
		}
		scanLanguageExtensions(filepath.Join(root, e.Name()), depth+1, counts)
	}
}

// languageFromFilename returns the canonical language name for an extension
// the user is likely to want to recommend on. Returns "" for unknown.
func languageFromFilename(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".cs":
		return "csharp"
	case ".swift":
		return "swift"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".hpp", ".cxx":
		return "cpp"
	}
	return ""
}

// topNLanguages picks the top N entries by count.
func topNLanguages(counts map[string]int, n int) []string {
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(counts))
	for k, v := range counts {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	if n > len(all) {
		n = len(all)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, all[i].k)
	}
	return out
}
