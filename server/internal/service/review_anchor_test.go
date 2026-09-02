package service

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

func nullInt(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }

// The regression these tests exist for: a review comment used to store only
// (file_path, line_number). Once the agent edited the file, "line 42" kept
// rendering as line 42 — of whatever code now lived there — with nothing in the
// UI to suggest anything had moved. A reviewer reading the thread got a
// confident anchor pointing at unrelated source.
//
// The contract asserted below is: after any change, a comment is either
// relocated to where its content actually went, or marked outdated. It is never
// silently left pointing at a line it no longer describes.

// anchorRepo creates a git worktree containing one file and returns its dir.
func anchorRepo(t *testing.T, filename, content string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writeAnchorFile(t, dir, filename, content)
	if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "commit", "-qm", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	return dir
}

func writeAnchorFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// anchorComment builds a stored comment the way CreateComment would: pinned to
// the file's current blob with a context snapshot around the commented line.
func anchorComment(t *testing.T, dir, file string, line int64) store.Comment {
	t.Helper()
	return store.Comment{
		ID:           1,
		FilePath:     file,
		LineNumber:   nullInt(line),
		Side:         CommentSideNew,
		BlobSha:      workingBlob(t, dir, file),
		ContextLines: captureCommentContext(dir, file, line),
	}
}

func workingBlob(t *testing.T, dir, file string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "hash-object", "--", filepath.Join(dir, file)).Output()
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	return strings.TrimSpace(string(out))
}

const anchorSrc = `package main

import "fmt"

func greet(name string) string {
	return fmt.Sprintf("hello %s", name)
}

func main() {
	fmt.Println(greet("world"))
}
`

// Baseline: an untouched file leaves the anchor exactly where it was written.
func TestResolveCommentAnchor_UnchangedFileStaysCurrent(t *testing.T) {
	dir := anchorRepo(t, "main.go", anchorSrc)
	c := anchorComment(t, dir, "main.go", 6) // the fmt.Sprintf line

	got := ResolveCommentAnchor(dir, c)

	if got.Status != AnchorStatusCurrent {
		t.Errorf("status: want %q, got %q", AnchorStatusCurrent, got.Status)
	}
	if got.EffectiveLine != 6 {
		t.Errorf("effective line: want 6, got %d", got.EffectiveLine)
	}
}

// The headline case: content is inserted ABOVE the commented line, so the code
// the reviewer wrote about is now at a different line number. The comment must
// FOLLOW its content rather than staying on a number that now holds something
// else.
func TestResolveCommentAnchor_ShiftedContentRelocates(t *testing.T) {
	dir := anchorRepo(t, "main.go", anchorSrc)
	c := anchorComment(t, dir, "main.go", 6)

	// Insert three lines at the top: the commented line slides 6 -> 9.
	writeAnchorFile(t, dir, "main.go", "// added\n// added\n// added\n"+anchorSrc)

	got := ResolveCommentAnchor(dir, c)

	if got.Status != AnchorStatusRelocated {
		t.Fatalf("status: want %q, got %q", AnchorStatusRelocated, got.Status)
	}
	if got.EffectiveLine != 9 {
		t.Errorf("effective line: want 9 (content moved down 3), got %d", got.EffectiveLine)
	}
	if got.OriginalLine != 6 {
		t.Errorf("original line must be preserved for the reader: want 6, got %d", got.OriginalLine)
	}
}

// The anti-drift guarantee, stated directly. The commented line is DELETED and
// unrelated code now occupies line 6. The old behaviour reported line 6 as if it
// were still the reviewed code; the requirement is that it must not.
func TestResolveCommentAnchor_DeletedContentGoesOutdatedNotDrifted(t *testing.T) {
	dir := anchorRepo(t, "main.go", anchorSrc)
	c := anchorComment(t, dir, "main.go", 6)

	// Rewrite the function body: the commented line is gone, and line 6 now holds
	// entirely different code.
	writeAnchorFile(t, dir, "main.go", `package main

import "fmt"

func greet(name string) string {
	return "hi " + name
}

func main() {
	fmt.Println(greet("world"))
}
`)

	got := ResolveCommentAnchor(dir, c)

	if got.Status != AnchorStatusOutdated {
		t.Fatalf("status: want %q, got %q — a vanished anchor must be flagged, never silently re-pointed",
			AnchorStatusOutdated, got.Status)
	}
	if got.EffectiveLine != 0 {
		t.Errorf("an outdated comment has no honest current position: want 0, got %d", got.EffectiveLine)
	}
	// The original position and snapshot survive so a human can still see what was
	// being discussed.
	if got.OriginalLine != 6 {
		t.Errorf("original line: want 6, got %d", got.OriginalLine)
	}
	if got.Context == nil || !strings.Contains(got.Context.Line, "Sprintf") {
		t.Errorf("outdated comment must retain its original context, got %+v", got.Context)
	}
}

// Deleting the whole file is the same class of event: nothing to anchor to.
func TestResolveCommentAnchor_DeletedFileGoesOutdated(t *testing.T) {
	dir := anchorRepo(t, "main.go", anchorSrc)
	c := anchorComment(t, dir, "main.go", 6)

	if err := os.Remove(filepath.Join(dir, "main.go")); err != nil {
		t.Fatal(err)
	}

	if got := ResolveCommentAnchor(dir, c); got.Status != AnchorStatusOutdated {
		t.Errorf("status: want %q for a deleted file, got %q", AnchorStatusOutdated, got.Status)
	}
}

// Ambiguity must not be resolved by guessing. When the commented line's text
// appears several times and no surrounding context survives, there is no
// evidence which occurrence was meant — outdated is the honest answer.
func TestResolveCommentAnchor_AmbiguousMatchGoesOutdated(t *testing.T) {
	dir := anchorRepo(t, "dup.go", "alpha\n\tclose()\nbeta\n")
	c := anchorComment(t, dir, "dup.go", 2) // "\tclose()"

	// Rewrite so the line text appears three times with all context gone.
	writeAnchorFile(t, dir, "dup.go", "x\n\tclose()\ny\n\tclose()\nz\n\tclose()\nw\n")

	got := ResolveCommentAnchor(dir, c)

	if got.Status != AnchorStatusOutdated {
		t.Errorf("status: want %q when several lines match equally, got %q (line %d)",
			AnchorStatusOutdated, got.Status, got.EffectiveLine)
	}
}

// Nearest-match wins: when context survives at one of several identical lines,
// that one is chosen rather than a lookalike elsewhere.
func TestResolveCommentAnchor_PrefersContextMatchOverLookalike(t *testing.T) {
	dir := anchorRepo(t, "dup.go", "func a() {\n\treturn nil\n}\n\nfunc b() {\n\treturn nil\n}\n")
	c := anchorComment(t, dir, "dup.go", 6) // the "return nil" inside func b

	// Insert two lines at the top; both "return nil" lines shift by 2, and only
	// the b() one keeps the surrounding context the snapshot recorded.
	writeAnchorFile(t, dir, "dup.go", "// h\n// h\nfunc a() {\n\treturn nil\n}\n\nfunc b() {\n\treturn nil\n}\n")

	got := ResolveCommentAnchor(dir, c)

	if got.Status != AnchorStatusRelocated {
		t.Fatalf("status: want %q, got %q", AnchorStatusRelocated, got.Status)
	}
	if got.EffectiveLine != 8 {
		t.Errorf("effective line: want 8 (func b's return, shifted by 2), got %d", got.EffectiveLine)
	}
}

// A legacy comment (no blob, no snapshot — every row that predates this feature)
// cannot be verified against anything. It must report outdated rather than
// claiming to be current, which would be exactly the false confidence the
// feature removes.
func TestResolveCommentAnchor_LegacyCommentCannotClaimCurrent(t *testing.T) {
	dir := anchorRepo(t, "main.go", anchorSrc)

	legacy := store.Comment{
		ID: 1, FilePath: "main.go", LineNumber: nullInt(6), Side: "new",
		// No BlobSha, no ContextLines — the shape addColumnIfNotExists leaves behind.
	}

	if got := ResolveCommentAnchor(dir, legacy); got.Status != AnchorStatusOutdated {
		t.Errorf("status: want %q for an unverifiable legacy comment, got %q", AnchorStatusOutdated, got.Status)
	}
}

// An unresolvable worktree means the content was never read; the anchor cannot
// be confirmed and must not present itself as current.
func TestResolveCommentAnchor_NoWorktreeIsOutdated(t *testing.T) {
	c := store.Comment{ID: 1, FilePath: "main.go", LineNumber: nullInt(6), Side: "new", BlobSha: "abc"}

	if got := ResolveCommentAnchor("", c); got.Status != AnchorStatusOutdated {
		t.Errorf("status: want %q when the worktree is unknown, got %q", AnchorStatusOutdated, got.Status)
	}
}

// Old-side comments — the whole point of the `side` column. A comment on a line
// the diff DELETES has no new-side line number to anchor to, which is why such
// lines were previously impossible to comment on at all ("you shouldn't have
// deleted this" being a very common review note). Its snapshot IS its anchor, so
// it never relocates and never goes outdated from later edits.
func TestResolveCommentAnchor_OldSideCommentIsStable(t *testing.T) {
	dir := anchorRepo(t, "main.go", anchorSrc)

	c := store.Comment{
		ID: 1, FilePath: "main.go", LineNumber: nullInt(6),
		Side:         CommentSideOld,
		ContextLines: `{"before":["func greet(name string) string {"],"line":"\treturn fmt.Sprintf(\"hello %s\", name)","after":["}"]}`,
	}

	// Change the file substantially — an old-side anchor is unaffected.
	writeAnchorFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	got := ResolveCommentAnchor(dir, c)

	if got.Status != AnchorStatusCurrent {
		t.Errorf("status: want %q (an old-side anchor is held by its snapshot), got %q", AnchorStatusCurrent, got.Status)
	}
	if got.Side != CommentSideOld {
		t.Errorf("side: want %q, got %q", CommentSideOld, got.Side)
	}
	if got.EffectiveLine != 6 {
		t.Errorf("effective line: want the original 6, got %d", got.EffectiveLine)
	}
}

// File boundaries: a comment on the first or last line has a one-sided context
// window. Those must still relocate — a window that runs off the end of the file
// is a normal shape, not a reason to give up and call the anchor outdated.
func TestResolveCommentAnchor_RelocatesAtFileBoundaries(t *testing.T) {
	cases := []struct {
		name         string
		before       string
		after        string
		line         int64
		wantEffected int64
	}{
		{"first line", "ZZZ_FIRST\nbbb\nccc\n", "hdr\nZZZ_FIRST\nbbb\nccc\n", 1, 2},
		{"last line", "aaa\nbbb\nccc\nZZZ_LAST\n", "hdr\nhdr\naaa\nbbb\nccc\nZZZ_LAST\n", 4, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := anchorRepo(t, "t.go", tc.before)
			c := anchorComment(t, dir, "t.go", tc.line)
			writeAnchorFile(t, dir, "t.go", tc.after)

			got := ResolveCommentAnchor(dir, c)

			if got.Status != AnchorStatusRelocated {
				t.Errorf("status: want %q, got %q", AnchorStatusRelocated, got.Status)
			}
			if got.EffectiveLine != tc.wantEffected {
				t.Errorf("effective line: want %d, got %d", tc.wantEffected, got.EffectiveLine)
			}
		})
	}
}

// The snapshot must actually capture the surrounding source — a silently empty
// snapshot would make every comment go outdated on first edit.
func TestCaptureCommentContext_SnapshotsSurroundingLines(t *testing.T) {
	dir := anchorRepo(t, "main.go", anchorSrc)

	snap := decodeCommentContext(captureCommentContext(dir, "main.go", 6))
	if snap == nil {
		t.Fatal("captureCommentContext returned nothing")
	}
	if !strings.Contains(snap.Line, "Sprintf") {
		t.Errorf("line: want the commented line, got %q", snap.Line)
	}
	if len(snap.Before) != 3 || len(snap.After) != 3 {
		t.Errorf("window: want 3 before and 3 after, got %d/%d", len(snap.Before), len(snap.After))
	}
	if snap.Before[2] != "func greet(name string) string {" {
		t.Errorf("nearest preceding line: got %q", snap.Before[2])
	}
}

// Out-of-range / missing input yields an empty snapshot rather than a fabricated
// window.
func TestCaptureCommentContext_OutOfRangeIsEmpty(t *testing.T) {
	dir := anchorRepo(t, "main.go", anchorSrc)

	if got := captureCommentContext(dir, "main.go", 9999); got != "" {
		t.Errorf("line past EOF: want empty snapshot, got %q", got)
	}
	if got := captureCommentContext(dir, "nope.go", 1); got != "" {
		t.Errorf("missing file: want empty snapshot, got %q", got)
	}
}
