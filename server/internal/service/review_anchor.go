package service

import (
	"encoding/json"
	"strings"

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

// ResolveCommentAnchor re-derives a comment's position against the worktree's
// current content. worktreePath "" (the repo could not be resolved) means the
// content cannot be inspected at all, so the comment reports outdated — an
// unverifiable anchor must not present itself as current.
func ResolveCommentAnchor(worktreePath string, c store.Comment) CommentAnchor {
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
	// exist in current content by definition, so blob comparison and relocation
	// are both meaningless: its snapshot IS the anchor, permanently. Report it as
	// current (the deletion it refers to is exactly what the reviewer meant) and
	// never move it.
	if anchor.Side == CommentSideOld {
		anchor.Status = AnchorStatusCurrent
		anchor.EffectiveLine = anchor.OriginalLine
		return anchor
	}

	currentBlob := git.WorkingBlobSHA(worktreePath, c.FilePath)
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
	lines, ok := git.WorkingFileLines(worktreePath, c.FilePath)
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
		return anchor
	}
	anchor.Status = AnchorStatusOutdated
	return anchor
}

// relocateContext finds where a snapshot now sits in changed content, returning
// a 1-based line number.
//
// Matching is deliberately staged from strict to loose, and every stage requires
// the commented line's own text to match exactly. A stage that matched on
// context alone could land the comment on a line the reviewer never wrote about,
// which is the exact failure this whole mechanism exists to prevent.
//
//	stage 1 — the line plus its full before/after window (near-unique).
//	stage 2 — the line plus at least one adjacent neighbour (survives an edit
//	          that rewrote part of the window).
//	stage 3 — a UNIQUE exact occurrence of the line text alone. Ambiguity is
//	          rejected: several identical lines (a bare "}") give no evidence
//	          which one was meant, so the comment goes outdated instead.
//
// Candidates are ordered by distance from the original line, so when several
// positions satisfy a stage the nearest wins — a line that shifted by an
// inserted import should relocate to its shifted self, not a lookalike far away.
func relocateContext(lines []string, snap CommentContext, originalLine int64) (int64, bool) {
	candidates := exactLineMatches(lines, snap.Line, originalLine)
	if len(candidates) == 0 {
		return 0, false
	}

	// Stage 1: full window.
	for _, idx := range candidates {
		if matchesWindow(lines, idx, snap, len(snap.Before), len(snap.After)) {
			return int64(idx + 1), true
		}
	}
	// Stage 2: line + one neighbour on either side.
	for _, idx := range candidates {
		if matchesWindow(lines, idx, snap, 1, 0) || matchesWindow(lines, idx, snap, 0, 1) {
			return int64(idx + 1), true
		}
	}
	// Stage 3: unique exact line, no surviving context.
	if len(candidates) == 1 {
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
