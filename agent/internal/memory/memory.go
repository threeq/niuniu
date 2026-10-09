// Package memory implements niuniu-agent's native memory: self-contained,
// two-layer markdown storage that works with nothing but env and local
// files — no niuniu server required. Entries are markdown files with a tiny
// frontmatter (title / type / domain / tags / lifecycle / expires_at /
// created / updated) under
//
//	~/.niuniu-agent/memory/      (user layer — global across workspaces)
//	<cwd>/.niuniu-agent/memory/  (project layer — this workspace only)
//
// Titles are the dedupe key (same title updates in place). At session
// start a scored selection (keyword hits, recency, project layer first) is
// injected into the system prompt under a byte cap; the MemorySave /
// MemorySearch tools give the model explicit read/write access. Memory is
// ADVISORY context: it may be stale — the model judges, never trusts
// blindly.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry types (advisory taxonomy; open set — unknown values are kept).
const (
	TypePattern  = "pattern"
	TypeGotcha   = "gotcha"
	TypeDecision = "decision"
	TypeUser     = "user"
	TypeRef      = "ref"
)

// Entry lifecycle states. Lifecycle marks where an entry sits in its life
// cycle: open entries are live context; done/cancelled/expired/deprecated
// ones are kept for the record but should stop being recalled (the recall
// filter lives in the injection path). Unknown values are kept as-is — the
// taxonomy is advisory, never a hard enum.
const (
	LifecycleOpen       = "open"       // live context (default)
	LifecycleDone       = "done"       // completed or settled
	LifecycleCancelled  = "cancelled"  // withdrawn — no longer applies
	LifecycleExpired    = "expired"    // past its ExpiresAt
	LifecycleDeprecated = "deprecated" // superseded by a corrected entry
)

// Entry domains (life areas). Domain is a separate field rather than an
// extension of Type: Type is already an open taxonomy persisted in existing
// files (pattern/gotcha/decision/user/ref, where `user` already overlaps
// "preference" and `decision` already means "project decision"). Folding the
// six domain values into Type would make stored old data ambiguous between
// two overlapping taxonomies and break the existing Type semantics; an
// orthogonal field keeps old files compatible ("" = unclassified, treated
// like other) and lets both taxonomies evolve independently.
const (
	DomainDecision      = "decision"      // settled project decision
	DomainPreference    = "preference"    // durable user preference
	DomainEnvironment   = "environment"   // user's environment habits (OS, shell, tooling)
	DomainOpenItem      = "open_item"     // in-progress matter worth revisiting
	DomainCollaboration = "collaboration" // how the user wants to work together
	DomainOther         = "other"         // anything else
)

// Anti-pollution limits. MaxEntryBytes caps one entry's whole file;
// MaxEntriesPerLayer caps how many entries one layer may hold.
const (
	DefaultMaxEntryBytes      = 8 << 10 // 8KB
	DefaultMaxEntriesPerLayer = 200
)

// Entry is one stored memory.
type Entry struct {
	ID      string // slug — also the filename base
	Title   string
	Type    string
	Domain  string // life area (see Domain* constants); "" = unclassified
	Tags    []string
	Content string

	// Lifecycle defaults to open (both on new entries and when reading old
	// files that predate the field); unknown values are kept as-is.
	// ExpiresAt is an optional deadline (zero = none); past it the entry
	// counts as expired even while still lifecycle=open.
	Lifecycle string
	ExpiresAt time.Time

	Created time.Time
	Updated time.Time
	Layer   string // "project" | "user"
}

// Store is the two-layer memory store for one workspace.
type Store struct {
	projectDir string // <cwd>/.niuniu-agent/memory
	userDir    string // ~/.niuniu-agent/memory

	MaxEntryBytes      int
	MaxEntriesPerLayer int
}

// NewStore builds a store rooted at cwd: project layer <cwd>/.niuniu-agent/
// memory, user layer from the OS home dir.
// ProjectDir returns the project-layer directory (for staging rollbacks).
func (s *Store) ProjectDir() string { return s.projectDir }

// NewStoreDir builds a store over an explicit project-layer directory
// (user layer still resolved from home).
func NewStoreDir(projectDir string) *Store {
	s := &Store{
		projectDir:         projectDir,
		MaxEntryBytes:      DefaultMaxEntryBytes,
		MaxEntriesPerLayer: DefaultMaxEntriesPerLayer,
	}
	if home, err := os.UserHomeDir(); err == nil {
		s.userDir = filepath.Join(home, ".niuniu-agent", "memory")
	}
	return s
}

// NewStoreLayers builds a store over explicit project- and user-layer
// directories; an empty dir means the layer is absent (loadAll skips it).
// Eval sandboxes use it to recall task fixtures from the sandbox project
// layer without re-injecting the host's user layer alongside the host
// project-layer store.
func NewStoreLayers(projectDir, userDir string) *Store {
	return &Store{
		projectDir:         projectDir,
		userDir:            userDir,
		MaxEntryBytes:      DefaultMaxEntryBytes,
		MaxEntriesPerLayer: DefaultMaxEntriesPerLayer,
	}
}

func NewStore(cwd string) *Store {
	s := &Store{
		projectDir:         filepath.Join(cwd, ".niuniu-agent", "memory"),
		MaxEntryBytes:      DefaultMaxEntryBytes,
		MaxEntriesPerLayer: DefaultMaxEntriesPerLayer,
	}
	if home, err := os.UserHomeDir(); err == nil {
		s.userDir = filepath.Join(home, ".niuniu-agent", "memory")
	}
	return s
}

// Save writes an entry to the PROJECT layer (memories gathered here belong
// to this workspace). An existing entry with the same slug (title) is
// updated in place — created is preserved, updated is refreshed. Returns
// the entry id.
func (s *Store) Save(e Entry) (string, error) {
	e.Title = strings.TrimSpace(e.Title)
	if e.Title == "" {
		return "", fmt.Errorf("memory: title is required")
	}
	id := slug(e.Title)
	if id == "" {
		return "", fmt.Errorf("memory: title %q has no usable characters", e.Title)
	}
	if e.Type == "" {
		e.Type = TypePattern
	}
	e.ID = id
	e.Layer = "project"

	path := filepath.Join(s.projectDir, id+".md")
	if _, err := os.Stat(path); err == nil {
		// Update: preserve the original created timestamp. An absent
		// lifecycle/expires_at keeps the previous values — a content edit
		// must not silently reopen done/deprecated entries; pass an explicit
		// lifecycle to change state.
		if prev, perr := readEntry(path, "project"); perr == nil {
			e.Created = prev.Created
			if e.Lifecycle == "" {
				e.Lifecycle = prev.Lifecycle
			}
			if e.ExpiresAt.IsZero() {
				e.ExpiresAt = prev.ExpiresAt
			}
		}
	}
	if e.Created.IsZero() {
		e.Created = time.Now()
	}
	if e.Lifecycle == "" {
		e.Lifecycle = LifecycleOpen
	}
	e.Updated = time.Now()

	data := []byte(render(e))
	if len(data) > s.maxEntryBytes() {
		return "", fmt.Errorf("memory: entry %q is %d bytes, over the %d-byte cap — write a shorter lesson",
			e.Title, len(data), s.maxEntryBytes())
	}
	if err := os.MkdirAll(s.projectDir, 0o755); err != nil {
		return "", err
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if n := s.countLayer(s.projectDir); n >= s.maxEntriesPerLayer() {
			return "", fmt.Errorf("memory: project layer is full (%d entries) — search and clean up before saving more", n)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return id, nil
}

// Search returns entries matching the keyword query (empty query = all),
// ranked: keyword score desc, then updated desc, then project layer first.
// Search itself does NOT filter lifecycle — callers decide (the recall
// paths and the MemorySearch tool filter; the Save tool relies on Search
// finding closed entries for its update-in-place check).
func (s *Store) Search(query string) ([]Entry, error) {
	all := s.loadAll()
	if strings.TrimSpace(query) == "" {
		sortEntries(all, nil)
		return all, nil
	}
	terms := splitTerms(query)
	scored := make([]Entry, 0, len(all))
	for _, e := range all {
		if score(e, terms) > 0 {
			scored = append(scored, e)
		}
	}
	sortEntries(scored, terms)
	return scored, nil
}

// Recall renders the top-N entries as a system-prompt section body, within
// maxBytes. Empty store → empty string (the section is omitted). Closed
// lifecycles are filtered and domains are routed (see routeRecallDomains).
func (s *Store) Recall(topN, maxBytes int) (string, error) {
	all := recallable(s.loadAll())
	if len(all) == 0 {
		return "", nil
	}
	sortEntries(all, nil)
	return renderRecall(routeRecallDomains(all), topN, maxBytes), nil
}

// RecallFor is the task-aware tiered recall: entries are ranked by
// relevance to the task hint first (keyword score over title/tags/content);
// entries with zero relevance keep recency order but rank BELOW relevant
// ones. Use this when the current task is known (eval tasks, subagent
// hints, explore's deep phase) so only task-relevant experience is
// injected — recall stays small instead of dumping every stored lesson.
// With a no-hit hint it degrades to plain recency. Like Recall, it drops
// closed lifecycles and applies domain routing (relevance orders WITHIN a
// domain tier; the tier order itself is domain-first).
func (s *Store) RecallFor(taskHint string, topN, maxBytes int) (string, error) {
	all := recallable(s.loadAll())
	if len(all) == 0 {
		return "", nil
	}
	terms := splitTerms(taskHint)
	sort.SliceStable(all, func(i, j int) bool {
		if terms == nil {
			return false
		}
		sa, sb := score(all[i], terms) > 0, score(all[j], terms) > 0
		if sa != sb {
			return sa // relevant before irrelevant
		}
		return all[i].Updated.After(all[j].Updated)
	})
	return renderRecall(routeRecallDomains(all), topN, maxBytes), nil
}

// effectiveLifecycle folds the lazy expiry rule into an entry's lifecycle:
// an open entry past its ExpiresAt reads as expired everywhere downstream
// (recall filter, search display, annotations). "明天面试" therefore stops
// being injected the day after, without any background job. The judgment is
// read-path only and never rewrites the file: mutating disk during recall
// would give reads a write side effect (racy across concurrent sessions);
// the on-disk state catches up whenever the entry is next saved explicitly.
func effectiveLifecycle(e Entry, now time.Time) string {
	if e.Lifecycle == LifecycleOpen && !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt) {
		return LifecycleExpired
	}
	return e.Lifecycle
}

// lifecycleClosed reports whether a lifecycle is one of the closed states
// that stop being recalled. Unknown lifecycle values are deliberately NOT
// closed — the taxonomy is advisory and forward-compatible (a future
// "archived" state won't silently vanish from recall until it's taught).
func lifecycleClosed(lc string) bool {
	switch lc {
	case LifecycleDone, LifecycleCancelled, LifecycleExpired, LifecycleDeprecated:
		return true
	}
	return false
}

// recallable drops closed-lifecycle entries; applied at every recall entry
// point (Recall / RecallFor). MemorySearch keeps its own tool-level filter
// so an explicit lifecycle query can still find closed entries.
func recallable(all []Entry) []Entry {
	out := all[:0]
	for _, e := range all {
		if lifecycleClosed(e.Lifecycle) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// MaxLowPriorityRecallSlots caps how many low-priority-domain entries a
// single recall may inject WHEN at least one signal-domain entry exists.
//
// Domain routing trade-off (deliberately minimal): signal domains
// (decision/preference/open_item/collaboration + unknown domains — don't
// hide what we don't understand) rank above noise domains
// (environment/other/"" — "" is unclassified, which the Domain* constants
// already define as "treated like other"), and noise is capped at one slot
// so it can still surface but never crowd a window that is only 2–5 lines.
// When NO signal entry exists the cap lifts: an all-legacy store (pre-domain
// files are all "") would otherwise recall almost nothing. Type is not a
// routing axis — pattern/gotcha carry no noise/signal split. No background
// task, no config knob: pure ordering + quota over the loaded entries.
func routeRecallDomains(entries []Entry) []Entry {
	var high, low []Entry
	for _, e := range entries {
		if lowRecallDomain(e.Domain) {
			low = append(low, e)
		} else {
			high = append(high, e)
		}
	}
	if len(high) == 0 {
		return entries
	}
	if len(low) > MaxLowPriorityRecallSlots {
		low = low[:MaxLowPriorityRecallSlots]
	}
	return append(high, low...)
}

const MaxLowPriorityRecallSlots = 1

func lowRecallDomain(d string) bool {
	switch d {
	case DomainEnvironment, DomainOther, "":
		return true
	}
	return false
}

// renderRecall truncates to topN, then renders lines within maxBytes. Live
// open items carry a status annotation so the model sees a revisit signal
// (e.g. "[open, due 2026-10-20]"); closed/past-due entries never get here.
func renderRecall(entries []Entry, topN, maxBytes int) string {
	if topN > 0 && len(entries) > topN {
		entries = entries[:topN]
	}
	var b strings.Builder
	for _, e := range entries {
		line := "- [" + e.Type + "] " + e.Title + ": " + e.Content + recallAnnotation(e) + "\n"
		if b.Len()+len(line) > maxBytes {
			break
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n")
}

func recallAnnotation(e Entry) string {
	if e.Lifecycle != LifecycleOpen {
		return ""
	}
	if e.Domain != DomainOpenItem && e.ExpiresAt.IsZero() {
		return ""
	}
	if e.ExpiresAt.IsZero() {
		return " [open]"
	}
	return " [open, due " + e.ExpiresAt.Format("2006-01-02") + "]"
}

// loadAll reads project layer first, then user layer; a project entry
// shadows a user entry with the same slug.
func (s *Store) loadAll() []Entry {
	var out []Entry
	seen := map[string]bool{}
	for _, layer := range []struct {
		dir, name string
	}{
		{s.projectDir, "project"},
		{s.userDir, "user"},
	} {
		entries, err := os.ReadDir(layer.dir)
		if err != nil {
			continue
		}
		for _, f := range entries {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			e, err := readEntry(filepath.Join(layer.dir, f.Name()), layer.name)
			if err != nil {
				continue // corrupt entry: skip, never block the session
			}
			if seen[e.ID] {
				continue // project shadows user
			}
			seen[e.ID] = true
			e.Lifecycle = effectiveLifecycle(e, time.Now())
			out = append(out, e)
		}
	}
	return out
}

func readEntry(path, layer string) (Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	text := string(data)
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Entry{}, fmt.Errorf("no frontmatter")
	}
	fm, body, found := strings.Cut(rest, "\n---")
	if !found {
		return Entry{}, fmt.Errorf("unterminated frontmatter")
	}
	var e Entry
	e.Layer = layer
	for _, line := range strings.Split(fm, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "title":
			e.Title = val
		case "type":
			e.Type = val
		case "domain":
			e.Domain = val
		case "lifecycle":
			e.Lifecycle = val // unknown values kept as-is, never an error
		case "expires_at":
			e.ExpiresAt = parseTime(val)
		case "tags":
			if val != "" {
				e.Tags = strings.Split(val, ",")
			}
		case "created":
			e.Created = parseTime(val)
		case "updated":
			e.Updated = parseTime(val)
		}
	}
	if e.Title == "" {
		return Entry{}, fmt.Errorf("entry without title")
	}
	if e.Lifecycle == "" {
		e.Lifecycle = LifecycleOpen // pre-lifecycle files read as open
	}
	if e.ID == "" {
		e.ID = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	e.Content = strings.TrimSpace(body)
	return e, nil
}

func render(e Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: %s\ntype: %s\n", e.Title, e.Type)
	if e.Domain != "" {
		fmt.Fprintf(&b, "domain: %s\n", e.Domain)
	}
	fmt.Fprintf(&b, "tags: %s\nlifecycle: %s\n", strings.Join(e.Tags, ","), e.Lifecycle)
	if !e.ExpiresAt.IsZero() {
		fmt.Fprintf(&b, "expires_at: %s\n", e.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	fmt.Fprintf(&b, "created: %s\nupdated: %s\n---\n\n%s\n",
		e.Created.UTC().Format(time.RFC3339Nano), e.Updated.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(e.Content))
	return b.String()
}

// score sums term hits: title ×3, tags ×2, content ×1.
func score(e Entry, terms []string) int {
	n := 0
	for _, t := range terms {
		if strings.Contains(strings.ToLower(e.Title), t) {
			n += 3
		}
		for _, tag := range e.Tags {
			if strings.Contains(strings.ToLower(tag), t) {
				n += 2
				break
			}
		}
		if strings.Contains(strings.ToLower(e.Content), t) {
			n++
		}
	}
	return n
}

// sortEntries orders by keyword score (when terms given), then updated
// desc, then project layer first.
func sortEntries(entries []Entry, terms []string) {
	scores := map[string]int{}
	if terms != nil {
		for _, e := range entries {
			scores[e.ID+"/"+e.Layer] = score(e, terms)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if terms != nil {
			sa, sb := scores[a.ID+"/"+a.Layer], scores[b.ID+"/"+b.Layer]
			if sa != sb {
				return sa > sb
			}
		}
		if !a.Updated.Equal(b.Updated) {
			return a.Updated.After(b.Updated)
		}
		if a.Layer != b.Layer {
			return a.Layer == "project"
		}
		return a.Title < b.Title
	})
}

func splitTerms(q string) []string {
	fields := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '，' || r == '、'
	})
	return fields
}

func (s *Store) countLayer(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range entries {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".md") {
			n++
		}
	}
	return n
}

func (s *Store) maxEntryBytes() int {
	if s.MaxEntryBytes > 0 {
		return s.MaxEntryBytes
	}
	return DefaultMaxEntryBytes
}

func (s *Store) maxEntriesPerLayer() int {
	if s.MaxEntriesPerLayer > 0 {
		return s.MaxEntriesPerLayer
	}
	return DefaultMaxEntriesPerLayer
}

// slug converts a title into a filesystem-safe id: lowercased ASCII,
// whitespace/illegal filename characters → '-', trimmed. Non-ASCII
// (Chinese titles included) is preserved.
func slug(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 0x80:
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		default:
			b.WriteRune('-') // whitespace & illegal filename chars
		}
	}
	out := strings.Trim(b.String(), "-")
	out = strings.ReplaceAll(out, "--", "-")
	return out
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
