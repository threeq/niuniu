package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// ConsolidateOptions tunes the three passes. Zero fields disable their
// pass; MergeSimilar groups by (type + exact tag set) and folds each group
// into one entry that keeps every original text.
type ConsolidateOptions struct {
	MaxAgeDays   int  // expire entries whose updated is older than N days
	MaxEntries   int  // LRU cap (evict oldest-updated beyond it); 0 → store default
	MergeSimilar bool // merge same-topic entries
}

// ConsolidateResult reports what each pass did.
type ConsolidateResult struct {
	Merged  int // entries folded away by the merge pass
	Expired int // entries deleted by the age pass
	Evicted int // entries dropped by the LRU pass
	Before  int
	After   int
}

// Consolidate runs the housekeeping passes over the project layer, in
// order: expire old → merge same-topic → LRU evict. Every pass rewrites
// only the project layer (user memories belong to the user).
func (s *Store) Consolidate(opts ConsolidateOptions) (*ConsolidateResult, error) {
	res := &ConsolidateResult{}
	all := s.loadAll()
	var project []Entry
	for _, e := range all {
		if e.Layer == "project" {
			project = append(project, e)
		}
	}
	res.Before = len(project)

	// Pass 1: expire by age.
	if opts.MaxAgeDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -opts.MaxAgeDays)
		var kept []Entry
		for _, e := range project {
			if e.Updated.Before(cutoff) {
				os.Remove(filepath.Join(s.projectDir, e.ID+".md"))
				res.Expired++
				continue
			}
			kept = append(kept, e)
		}
		project = kept
	}

	// Pass 2: merge same-topic (same type + identical sorted tag set).
	if opts.MergeSimilar {
		groups := map[string][]Entry{}
		for _, e := range project {
			tags := append([]string(nil), e.Tags...)
			sort.Strings(tags)
			groups[e.Type+"|"+strings.Join(tags, ",")] = append(groups[e.Type+"|"+strings.Join(tags, ",")], e)
		}
		mergedSeen := map[string]bool{}
		var kept []Entry
		for _, e := range project {
			tags := append([]string(nil), e.Tags...)
			sort.Strings(tags)
			key := e.Type + "|" + strings.Join(tags, ",")
			g := groups[key]
			if len(g) < 2 || mergedSeen[key] {
				if len(g) < 2 {
					kept = append(kept, e)
				}
				continue
			}
			// Fold the whole group into one entry at the newest member.
			sort.Slice(g, func(i, j int) bool { return g[i].Updated.After(g[j].Updated) })
			merged := g[0]
			var b strings.Builder
			for _, m := range g {
				fmt.Fprintf(&b, "- %s: %s\n", m.Title, m.Content)
				if m.ID != merged.ID {
					os.Remove(filepath.Join(s.projectDir, m.ID+".md"))
					res.Merged++
				}
			}
			merged.Content = strings.TrimRight(b.String(), "\n")
			merged.ID = slug(merged.Title)
			merged.Updated = time.Now()
			kept = append(kept, merged)
			mergedSeen[key] = true
		}
		project = kept
	}

	// Pass 3: LRU eviction (oldest-updated first).
	cap := opts.MaxEntries
	if cap <= 0 {
		cap = s.maxEntriesPerLayer()
	}
	if len(project) > cap {
		sort.Slice(project, func(i, j int) bool { return project[i].Updated.Before(project[j].Updated) })
		for _, e := range project[:len(project)-cap] {
			os.Remove(filepath.Join(s.projectDir, e.ID+".md"))
			res.Evicted++
		}
		project = project[len(project)-cap:]
	}
	sort.Slice(project, func(i, j int) bool { return project[i].Title < project[j].Title })

	// Persist survivors (rewrites merged content, drops removed ones).
	for _, e := range project {
		if err := os.WriteFile(filepath.Join(s.projectDir, e.ID+".md"), []byte(render(e)), 0o644); err != nil {
			return res, err
		}
	}
	// Orphaned files (renamed merges) are already removed above.
	res.After = len(project)
	return res, nil
}

// ConsolidateTool runs the housekeeping on demand.
type ConsolidateTool struct{ store *Store }

// NewConsolidateTool builds the MemoryConsolidate tool.
func NewConsolidateTool(s *Store) tools.Tool { return ConsolidateTool{s} }

func (ConsolidateTool) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "MemoryConsolidate",
		Description: "Housekeeping for long-term memory: merge same-topic entries, expire stale ones, and evict beyond the capacity limit. Run when memory feels cluttered or after finishing a large task.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"max_age_days":{"type":"integer","description":"Expire entries untouched for N days (optional)"},` +
			`"merge_similar":{"type":"boolean","description":"Merge same-type same-tags entries (default true)"}}}`),
	}
}

func (t ConsolidateTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		MaxAgeDays   int   `json:"max_age_days"`
		MergeSimilar *bool `json:"merge_similar"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
	}
	merge := in.MergeSimilar == nil || *in.MergeSimilar
	res, err := t.store.Consolidate(ConsolidateOptions{
		MaxAgeDays:   in.MaxAgeDays,
		MaxEntries:   t.store.MaxEntriesPerLayer,
		MergeSimilar: merge,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("consolidated: before=%d after=%d merged=%d expired=%d evicted=%d",
		res.Before, res.After, res.Merged, res.Expired, res.Evicted), nil
}
