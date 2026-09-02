package service

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Review-comment anchoring (#683 wave 1).
//
// The bug this replaces: a comment stored only (file_path, line_number). After
// the agent edited the file, "line 42" kept rendering as line 42 — of whatever
// code now lived there. The UI showed no sign anything had moved, so a reviewer
// reading the thread got a confident anchor pointing at unrelated source.
//
// The fix is to store enough to re-derive the anchor at read time: the commit
// the comment was written against, the blob hash of the exact bytes shown, and a
// small snapshot of the lines around the comment. On every read the resolver
// compares the stored blob to the file's current blob and reports one of three
// honest states:
//
//	current   — file unchanged since the comment (blob matches); line stands.
//	relocated — file changed, but the snapshot was found at a new offset; the
//	            line moves there and the caller may re-pin the blob.
//	outdated  — file changed and the snapshot is gone (or was never taken); the
//	            original line + snapshot are preserved for a human to read.
//
// There is deliberately no fourth "assume it's still fine" state. Marking a
// comment outdated costs a reviewer one glance; silently drifting costs them
// their trust in every other anchor on the page.

// Anchor states. Stringly-typed to cross the JSON boundary unchanged.
const (
	AnchorStatusCurrent   = "current"
	AnchorStatusRelocated = "relocated"
	AnchorStatusOutdated  = "outdated"
)

// Comment sides. An old-side comment anchors to a line the diff DELETES, which
// has no new-line-number to hang on — that is why deletion lines were previously
// un-commentable.
const (
	CommentSideOld = "old"
	CommentSideNew = "new"
)

// anchorContextRadius is how many lines on each side of the commented line get
// snapshotted. Three is the GitHub-ish default: wide enough that the window is
// near-unique in a source file, narrow enough that an edit a few lines away
// doesn't destroy it.
const anchorContextRadius = 3

// bareLineDriftLimit bounds how far a WEAKLY-evidenced match may sit from where
// the comment was written. A match is weak when the only thing corroborating it
// is the commented line's own text, or that text plus a single neighbour with no
// real identity of its own (a lone `}` or a blank line matches almost anywhere).
//
// Without a bound, such a match wins at any distance — so deleting the reviewed
// function and having an identical call appear 200 lines away in unrelated code
// relocates the comment onto source the reviewer never saw. That is silent drift
// wearing a "relocated" label, which is worse than outdated because it looks
// verified. A full-window match (stage 1) is exempt: several consecutive
// matching lines are strong evidence regardless of distance.
const bareLineDriftLimit = 25

// isWeakContextLine reports whether a context line carries too little identity
// to corroborate a match on its own. Punctuation-only lines (`}`, `)`, `},`) and
// blanks recur constantly in source, so pairing one with the commented line is
// barely better evidence than the line alone.
func isWeakContextLine(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	return strings.IndexFunc(t, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
	}) < 0
}

// CommentContext is the snapshot stored in comments.context_lines. Line is the
// commented line's own text; Before/After are up to anchorContextRadius lines of
// surrounding source. Persisted as JSON so the shape can grow without another
// migration.
type CommentContext struct {
	Before []string `json:"before"`
	Line   string   `json:"line"`
	After  []string `json:"after"`
}

// CommentAnchor is the resolved position of a comment against current content.
// EffectiveLine is where the comment should render NOW; OriginalLine is where it
// was written. For an outdated comment EffectiveLine is 0 — there is no honest
// current position, and callers must render the snapshot instead of a line.
type CommentAnchor struct {
	Status        string          `json:"status"`
	OriginalLine  int64           `json:"original_line,omitempty"`
	EffectiveLine int64           `json:"effective_line,omitempty"`
	Side          string          `json:"side"`
	Context       *CommentContext `json:"context,omitempty"`
	// CurrentBlobSha is the file's blob hash right now; "" when the file is gone
	// or unreadable. Callers re-pin a relocated comment to it.
	CurrentBlobSha string `json:"current_blob_sha,omitempty"`
	// Current is what the anchored line looks like NOW, when that differs from the
	// snapshot in Context. Judging "did this actually get fixed?" needs both sides:
	// Context is what the reviewer commented on, Current is what replaced it. Nil
	// when the line is unchanged (Context already shows it) or when the anchor is
	// outdated (there is no current line to show).
	Current *CommentContext `json:"current,omitempty"`
}

// normalizeSide coerces a stored/incoming side to a known value. Anything that
// isn't explicitly "old" is treated as new-side — matching the migration default
// that every legacy comment carries.
func normalizeSide(side string) string {
	if strings.EqualFold(strings.TrimSpace(side), CommentSideOld) {
		return CommentSideOld
	}
	return CommentSideNew
}

// captureCommentContext snapshots the lines around a 1-based line number in the
// worktree file. Returns "" when the file is unreadable or the line is out of
// range — an empty snapshot is honest ("no anchor recorded") and downgrades the
// comment to outdated on the first change, which beats fabricating a window.
func captureCommentContext(worktreePath, filePath string, line int64) string {
	if line <= 0 {
		return ""
	}
	lines, ok := git.WorkingFileLines(worktreePath, filePath)
	if !ok || line > int64(len(lines)) {
		return ""
	}
	idx := int(line - 1)
	snap := CommentContext{Line: lines[idx]}
	for i := max(0, idx-anchorContextRadius); i < idx; i++ {
		snap.Before = append(snap.Before, lines[i])
	}
	for i := idx + 1; i < min(len(lines), idx+1+anchorContextRadius); i++ {
		snap.After = append(snap.After, lines[i])
	}
	encoded, err := json.Marshal(snap)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// decodeCommentContext parses a stored snapshot. A blank or malformed value
// yields nil — the resolver then has nothing to relocate against and reports
// outdated rather than guessing.
func decodeCommentContext(raw string) *CommentContext {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var snap CommentContext
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return nil
	}
	return &snap
}

// fileStateCache memoises per-file reads for one batch of anchor resolutions.
//
// Resolving an anchor needs the file's blob hash and, on the relocate path, its
// lines. The blob comes from `git hash-object`, a SUBPROCESS costing ~40ms —
// roughly 700x the file read beside it. Resolving comments one at a time spawns
// one per comment, so a review panel holding 30 comments on a single file spent
// ~1.6s re-hashing the same bytes 30 times. Comments cluster on files by nature
// (that is what a review is), so caching per (worktree, path) collapses that to
// one spawn per distinct file.
//
// Scope is deliberately ONE batch, not a long-lived cache: the worktree is
// mutable and a stale blob would resurrect exactly the false-confidence bug this
// whole mechanism exists to prevent. A nil cache is valid and simply disables
// memoisation, so single-comment callers pass nothing.
type fileStateCache struct {
	blob  map[string]string
	lines map[string][]string
	// lines can legitimately be nil for a missing file, so presence needs its own
	// marker rather than a nil check.
	linesOK map[string]bool
}

func newFileStateCache() *fileStateCache {
	return &fileStateCache{
		blob:    map[string]string{},
		lines:   map[string][]string{},
		linesOK: map[string]bool{},
	}
}

func (fc *fileStateCache) blobSHA(worktreePath, filePath string) string {
	if fc == nil {
		return git.WorkingBlobSHA(worktreePath, filePath)
	}
	key := worktreePath + "\x00" + filePath
	if v, ok := fc.blob[key]; ok {
		return v
	}
	v := git.WorkingBlobSHA(worktreePath, filePath)
	fc.blob[key] = v
	return v
}

func (fc *fileStateCache) fileLines(worktreePath, filePath string) ([]string, bool) {
	if fc == nil {
		return git.WorkingFileLines(worktreePath, filePath)
	}
	key := worktreePath + "\x00" + filePath
	if ok, seen := fc.linesOK[key]; seen {
		return fc.lines[key], ok
	}
	v, ok := git.WorkingFileLines(worktreePath, filePath)
	fc.lines[key] = v
	fc.linesOK[key] = ok
	return v, ok
}

// ResolveCommentAnchor re-derives a comment's position against the worktree's
// current content. worktreePath "" (the repo could not be resolved) means the
// content cannot be inspected at all, so the comment reports outdated — an
// unverifiable anchor must not present itself as current.
func ResolveCommentAnchor(worktreePath string, c store.Comment) CommentAnchor {
	return resolveCommentAnchor(worktreePath, c, nil)
}

// resolveCommentAnchor is ResolveCommentAnchor with an optional per-batch file
// cache (nil = no memoisation).
func resolveCommentAnchor(worktreePath string, c store.Comment, fc *fileStateCache) CommentAnchor {
	anchor := CommentAnchor{
		Side:    normalizeSide(c.Side),
		Context: decodeCommentContext(c.ContextLines),
	}
	if c.LineNumber.Valid {
		anchor.OriginalLine = c.LineNumber.Int64
	}

	if strings.TrimSpace(worktreePath) == "" {
		anchor.Status = AnchorStatusOutdated
		return anchor
	}

	// An old-side comment anchors to a line the diff deleted. That line does not
	// exist in current content by definition, so blob comparison and relocation are
	// both meaningless: its snapshot IS the anchor. But that reasoning only holds
	// while the snapshot actually exists and the file it refers to still does —
	// without either, there is nothing anchoring the comment, and reporting
	// `current` would present a bare line number as verified. Fall through to
	// outdated in both cases.
	if anchor.Side == CommentSideOld {
		hasSnapshot := anchor.Context != nil && strings.TrimSpace(anchor.Context.Line) != ""
		if !hasSnapshot {
			anchor.Status = AnchorStatusOutdated
			return anchor
		}
		if _, ok := fc.fileLines(worktreePath, c.FilePath); !ok {
			// The file itself is gone; a comment about one of its deleted lines has
			// no surviving context to sit in.
			anchor.Status = AnchorStatusOutdated
			return anchor
		}
		anchor.Status = AnchorStatusCurrent
		anchor.EffectiveLine = anchor.OriginalLine
		return anchor
	}

	currentBlob := fc.blobSHA(worktreePath, c.FilePath)
	anchor.CurrentBlobSha = currentBlob

	// Unchanged file: the stored line is still literally correct. Requires a
	// recorded blob — a legacy comment with no blob has nothing to compare and
	// must not claim to be verified.
	if c.BlobSha != "" && currentBlob != "" && c.BlobSha == currentBlob {
		anchor.Status = AnchorStatusCurrent
		anchor.EffectiveLine = anchor.OriginalLine
		return anchor
	}

	// File changed (or was never pinned). Try to find the snapshot in the new
	// content. No snapshot -> nothing to search for -> outdated.
	if anchor.Context == nil || anchor.OriginalLine <= 0 {
		anchor.Status = AnchorStatusOutdated
		return anchor
	}
	lines, ok := fc.fileLines(worktreePath, c.FilePath)
	if !ok {
		// File deleted or unreadable: the comment's target is gone.
		anchor.Status = AnchorStatusOutdated
		return anchor
	}
	if newLine, found := relocateContext(lines, *anchor.Context, anchor.OriginalLine); found {
		anchor.EffectiveLine = newLine
		if newLine == anchor.OriginalLine {
			// Same line, different file elsewhere — the anchor itself never moved.
			anchor.Status = AnchorStatusCurrent
		} else {
			anchor.Status = AnchorStatusRelocated
		}
		// The file changed, so show what the anchored region looks like NOW next to
		// what it looked like when the comment was written. That side-by-side is what
		// answers "was this actually fixed?" — note relocation matched the commented
		// line EXACTLY, so an unchanged Line here is direct evidence it was not.
		if now := windowAt(lines, int(newLine)-1); now != nil && !sameContext(*now, *anchor.Context) {
			anchor.Current = now
		}
		return anchor
	}
	anchor.Status = AnchorStatusOutdated
	return anchor
}

// windowAt snapshots the same shape captureCommentContext produces, but from
// already-loaded lines. Returns nil when idx is out of range.
func windowAt(lines []string, idx int) *CommentContext {
	if idx < 0 || idx >= len(lines) {
		return nil
	}
	snap := CommentContext{Line: lines[idx]}
	for i := max(0, idx-anchorContextRadius); i < idx; i++ {
		snap.Before = append(snap.Before, lines[i])
	}
	for i := idx + 1; i < min(len(lines), idx+1+anchorContextRadius); i++ {
		snap.After = append(snap.After, lines[i])
	}
	return &snap
}

// sameContext reports whether two snapshots are identical, so an unchanged
// region is not sent twice as "before" and "after".
func sameContext(a, b CommentContext) bool {
	return a.Line == b.Line && slices.Equal(a.Before, b.Before) && slices.Equal(a.After, b.After)
}

// relocateContext finds where a snapshot now sits in changed content, returning
// a 1-based line number.
//
// Matching is deliberately staged from strict to loose, and every stage requires
// the commented line's own text to match exactly. A stage that matched on
// context alone could land the comment on a line the reviewer never wrote about,
// which is the exact failure this whole mechanism exists to prevent.
//
//	stage 1 — the line plus its full before/after window. Several consecutive
//	          matching lines are strong evidence; accepted at any distance.
//	stage 2 — the line plus one adjacent neighbour (survives an edit that rewrote
//	          part of the window). The neighbour must carry real identity — a lone
//	          `}` corroborates nothing — and, being weaker evidence, the match must
//	          also be near where the comment was written.
//	stage 3 — a UNIQUE exact occurrence of the line text alone, likewise only
//	          nearby. Ambiguity is rejected outright: several identical lines give
//	          no evidence which was meant.
//
// Anything unmatched goes outdated. Candidates are ordered by distance from the
// original line, so when several positions satisfy a stage the nearest wins — a
// line that shifted by an inserted import relocates to its shifted self, not to
// a lookalike elsewhere.
func relocateContext(lines []string, snap CommentContext, originalLine int64) (int64, bool) {
	candidates := exactLineMatches(lines, snap.Line, originalLine)
	if len(candidates) == 0 {
		return 0, false
	}
	origIdx := int(originalLine) - 1
	near := func(idx int) bool { return abs(idx-origIdx) <= bareLineDriftLimit }

	// Stage 1: full window — strong enough to trust at any distance.
	for _, idx := range candidates {
		if matchesWindow(lines, idx, snap, len(snap.Before), len(snap.After)) {
			return int64(idx + 1), true
		}
	}
	// Stage 2: line + one identity-carrying neighbour, and only nearby.
	strongBefore := len(snap.Before) > 0 && !isWeakContextLine(snap.Before[len(snap.Before)-1])
	strongAfter := len(snap.After) > 0 && !isWeakContextLine(snap.After[0])
	for _, idx := range candidates {
		if !near(idx) {
			continue
		}
		if strongBefore && matchesWindow(lines, idx, snap, 1, 0) {
			return int64(idx + 1), true
		}
		if strongAfter && matchesWindow(lines, idx, snap, 0, 1) {
			return int64(idx + 1), true
		}
	}
	// Stage 3: unique exact line, no surviving context — likewise only nearby.
	if len(candidates) == 1 && near(candidates[0]) {
		return int64(candidates[0] + 1), true
	}
	return 0, false
}

// exactLineMatches returns the 0-based indexes whose text equals target, ordered
// nearest-first relative to the original 1-based line. Blank/whitespace-only
// targets never match: a bare empty line carries no identity, and treating it as
// a match would relocate comments essentially at random.
func exactLineMatches(lines []string, target string, originalLine int64) []int {
	if strings.TrimSpace(target) == "" {
		return nil
	}
	origIdx := int(originalLine) - 1
	var out []int
	for i, l := range lines {
		if l == target {
			out = append(out, i)
		}
	}
	// Insertion-sort by |i - origIdx|: candidate counts are tiny (usually 1).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && abs(out[j]-origIdx) < abs(out[j-1]-origIdx); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// matchesWindow reports whether the snapshot's trailing `before` lines and
// leading `after` lines sit around idx in lines. A window that runs off either
// end of the file does not match — a partial window is weaker evidence than the
// caller's stage assumed.
func matchesWindow(lines []string, idx int, snap CommentContext, beforeN, afterN int) bool {
	if beforeN == 0 && afterN == 0 {
		return false
	}
	if beforeN > len(snap.Before) || afterN > len(snap.After) {
		return false
	}
	if idx-beforeN < 0 || idx+afterN >= len(lines) {
		return false
	}
	// snap.Before is ordered oldest-first, so its LAST beforeN entries are the
	// ones adjacent to the commented line.
	for i := 0; i < beforeN; i++ {
		if lines[idx-beforeN+i] != snap.Before[len(snap.Before)-beforeN+i] {
			return false
		}
	}
	for i := 0; i < afterN; i++ {
		if lines[idx+1+i] != snap.After[i] {
			return false
		}
	}
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
