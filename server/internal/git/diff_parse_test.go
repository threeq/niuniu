package git

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The parser is the single source of truth for diff structure — the frontend
// renders FileDiff.Hunks directly and no longer re-parses RawPatch. These tests
// pin the metadata the client used to recover on its own (binary marker, mode
// pair, rename paths, missing trailing newline) plus the line-number resolution
// the viewer indexes by.

func parseOne(t *testing.T, patch string) FileDiff {
	t.Helper()
	diffs, err := parseDiff(patch)
	if err != nil {
		t.Fatalf("parseDiff: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("parseDiff returned %d files, want 1: %+v", len(diffs), diffs)
	}
	return diffs[0]
}

func joinLines(lines ...string) string { return strings.Join(lines, "\n") }

func TestParseDiff_ContentChangeLinesAndNumbers(t *testing.T) {
	fd := parseOne(t, joinLines(
		"diff --git a/s.sh b/s.sh",
		"index 69d7334..4614cec 100644",
		"--- a/s.sh",
		"+++ b/s.sh",
		"@@ -1,3 +1,4 @@ func main()",
		" #!/usr/bin/env bash",
		"-echo old",
		"+echo new",
		"+echo bye",
		" tail",
		"",
	))

	if fd.Path != "s.sh" || fd.Status != "modified" {
		t.Errorf("path/status = %q/%q, want s.sh/modified", fd.Path, fd.Status)
	}
	if fd.Additions != 2 || fd.Deletions != 1 {
		t.Errorf("+%d/-%d, want +2/-1", fd.Additions, fd.Deletions)
	}
	if len(fd.Hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(fd.Hunks))
	}
	h := fd.Hunks[0]
	if h.Header != "func main()" {
		t.Errorf("hunk header = %q, want %q", h.Header, "func main()")
	}
	if h.OldStart != 1 || h.OldCount != 3 || h.NewStart != 1 || h.NewCount != 4 {
		t.Errorf("hunk range = -%d,%d +%d,%d, want -1,3 +1,4", h.OldStart, h.OldCount, h.NewStart, h.NewCount)
	}

	want := []DiffLine{
		{Type: DiffLineContext, Content: "#!/usr/bin/env bash", OldLine: 1, NewLine: 1},
		{Type: DiffLineDelete, Content: "echo old", OldLine: 2},
		{Type: DiffLineAdd, Content: "echo new", NewLine: 2},
		{Type: DiffLineAdd, Content: "echo bye", NewLine: 3},
		{Type: DiffLineContext, Content: "tail", OldLine: 3, NewLine: 4},
	}
	if len(h.Lines) != len(want) {
		t.Fatalf("lines = %d, want %d: %+v", len(h.Lines), len(want), h.Lines)
	}
	for i, w := range want {
		if h.Lines[i] != w {
			t.Errorf("line %d = %+v, want %+v", i, h.Lines[i], w)
		}
	}
}

// Regression: git's "Binary files ... differ" marker used to be dropped by the
// backend, so only the frontend parser knew a file was binary. Losing it here is
// what forced the client to keep a second parser.
func TestParseDiff_Binary(t *testing.T) {
	for _, tc := range []struct{ name, marker string }{
		{"textual marker", "Binary files a/logo.png and b/logo.png differ"},
		{"binary patch", "GIT binary patch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fd := parseOne(t, joinLines(
				"diff --git a/logo.png b/logo.png",
				"index 0000000..4d8e0bf 100644",
				tc.marker,
				"",
			))
			if !fd.IsBinary {
				t.Error("IsBinary = false, want true")
			}
			if len(fd.Hunks) != 0 {
				t.Errorf("hunks = %d, want 0", len(fd.Hunks))
			}
			if fd.Path != "logo.png" {
				t.Errorf("path = %q, want logo.png", fd.Path)
			}
		})
	}
}

// A binary marker appearing as hunk CONTENT (a text file that literally contains
// the words) must not flag the file as binary.
func TestParseDiff_BinaryMarkerInsideHunkIsNotBinary(t *testing.T) {
	fd := parseOne(t, joinLines(
		"diff --git a/notes.txt b/notes.txt",
		"--- a/notes.txt",
		"+++ b/notes.txt",
		"@@ -1,1 +1,2 @@",
		" intro",
		"+Binary files a/x and b/x differ",
		"",
	))
	if fd.IsBinary {
		t.Error("IsBinary = true for a context/added line that merely mentions the marker")
	}
	if fd.Additions != 1 {
		t.Errorf("additions = %d, want 1", fd.Additions)
	}
}

func TestParseDiff_Rename(t *testing.T) {
	t.Run("pure rename has no hunks and is not binary", func(t *testing.T) {
		fd := parseOne(t, joinLines(
			"diff --git a/old.sh b/new.sh",
			"similarity index 100%",
			"rename from old.sh",
			"rename to new.sh",
			"",
		))
		if fd.Status != "renamed" {
			t.Errorf("status = %q, want renamed", fd.Status)
		}
		if fd.OldPath != "old.sh" || fd.Path != "new.sh" {
			t.Errorf("paths = %q -> %q, want old.sh -> new.sh", fd.OldPath, fd.Path)
		}
		if fd.IsBinary {
			t.Error("IsBinary = true for a pure rename")
		}
		if len(fd.Hunks) != 0 {
			t.Errorf("hunks = %d, want 0", len(fd.Hunks))
		}
	})

	t.Run("rename with edits keeps both paths and the hunk", func(t *testing.T) {
		fd := parseOne(t, joinLines(
			"diff --git a/src/old.go b/src/new.go",
			"similarity index 87%",
			"rename from src/old.go",
			"rename to src/new.go",
			"--- a/src/old.go",
			"+++ b/src/new.go",
			"@@ -1,2 +1,2 @@",
			" package main",
			"-old",
			"+new",
			"",
		))
		if fd.Status != "renamed" {
			t.Errorf("status = %q, want renamed", fd.Status)
		}
		if fd.OldPath != "src/old.go" || fd.Path != "src/new.go" {
			t.Errorf("paths = %q -> %q", fd.OldPath, fd.Path)
		}
		if len(fd.Hunks) != 1 || len(fd.Hunks[0].Lines) != 3 {
			t.Fatalf("expected 1 hunk of 3 lines, got %+v", fd.Hunks)
		}
	})

	t.Run("copy is reported as copied", func(t *testing.T) {
		fd := parseOne(t, joinLines(
			"diff --git a/a.txt b/b.txt",
			"similarity index 100%",
			"copy from a.txt",
			"copy to b.txt",
			"",
		))
		if fd.Status != "copied" {
			t.Errorf("status = %q, want copied", fd.Status)
		}
		if fd.OldPath != "a.txt" || fd.Path != "b.txt" {
			t.Errorf("paths = %q -> %q", fd.OldPath, fd.Path)
		}
	})
}

func TestParseDiff_EmptyFile(t *testing.T) {
	t.Run("new empty file: added, zero hunks, not binary", func(t *testing.T) {
		fd := parseOne(t, joinLines(
			"diff --git a/empty.txt b/empty.txt",
			"new file mode 100644",
			"index 0000000..e69de29",
			"",
		))
		if fd.Status != "added" {
			t.Errorf("status = %q, want added", fd.Status)
		}
		if fd.IsBinary {
			t.Error("IsBinary = true for an empty new file")
		}
		if len(fd.Hunks) != 0 || fd.Additions != 0 {
			t.Errorf("hunks = %d, additions = %d, want 0/0", len(fd.Hunks), fd.Additions)
		}
		if fd.NewMode != "100644" {
			t.Errorf("NewMode = %q, want 100644", fd.NewMode)
		}
	})

	t.Run("file truncated to empty counts its deletions", func(t *testing.T) {
		fd := parseOne(t, joinLines(
			"diff --git a/x.txt b/x.txt",
			"index 1234567..e69de29 100644",
			"--- a/x.txt",
			"+++ b/x.txt",
			"@@ -1,2 +0,0 @@",
			"-one",
			"-two",
			"",
		))
		if fd.Deletions != 2 || fd.Additions != 0 {
			t.Errorf("+%d/-%d, want +0/-2", fd.Additions, fd.Deletions)
		}
		if len(fd.Hunks) != 1 || fd.Hunks[0].NewCount != 0 {
			t.Errorf("expected one hunk with NewCount 0, got %+v", fd.Hunks)
		}
	})

	t.Run("deleting an empty file yields no hunks", func(t *testing.T) {
		fd := parseOne(t, joinLines(
			"diff --git a/empty.txt b/empty.txt",
			"deleted file mode 100644",
			"index e69de29..0000000",
			"",
		))
		if fd.Status != "deleted" {
			t.Errorf("status = %q, want deleted", fd.Status)
		}
		if len(fd.Hunks) != 0 || fd.IsBinary {
			t.Errorf("hunks = %d, binary = %v, want 0/false", len(fd.Hunks), fd.IsBinary)
		}
	})
}

func TestParseDiff_NoNewlineAtEOF(t *testing.T) {
	fd := parseOne(t, joinLines(
		"diff --git a/x.txt b/x.txt",
		"index 1234567..89abcde 100644",
		"--- a/x.txt",
		"+++ b/x.txt",
		"@@ -1,2 +1,2 @@",
		" keep",
		"-old",
		`\ No newline at end of file`,
		"+new",
		`\ No newline at end of file`,
		"",
	))
	if len(fd.Hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(fd.Hunks))
	}
	lines := fd.Hunks[0].Lines
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3: %+v", len(lines), lines)
	}
	if lines[0].NoNewline {
		t.Error("context line wrongly marked NoNewline")
	}
	if !lines[1].NoNewline || lines[1].Type != DiffLineDelete {
		t.Errorf("deleted line = %+v, want NoNewline delete", lines[1])
	}
	if !lines[2].NoNewline || lines[2].Type != DiffLineAdd {
		t.Errorf("added line = %+v, want NoNewline add", lines[2])
	}
	// The marker itself must never be counted as a diff line.
	if fd.Additions != 1 || fd.Deletions != 1 {
		t.Errorf("+%d/-%d, want +1/-1", fd.Additions, fd.Deletions)
	}
}

// Regression: a mode-only change (chmod +x) produces a zero-hunk diff that must
// be reported as a text file with a mode pair, NOT as binary.
func TestParseDiff_ModeOnlyChange(t *testing.T) {
	fd := parseOne(t, joinLines(
		"diff --git a/deploy/smoke.sh b/deploy/smoke.sh",
		"old mode 100644",
		"new mode 100755",
		"",
	))
	if fd.IsBinary {
		t.Error("IsBinary = true for a mode-only change")
	}
	if len(fd.Hunks) != 0 {
		t.Errorf("hunks = %d, want 0", len(fd.Hunks))
	}
	if fd.OldMode != "100644" || fd.NewMode != "100755" {
		t.Errorf("modes = %q -> %q, want 100644 -> 100755", fd.OldMode, fd.NewMode)
	}
	if fd.Status != "modified" {
		t.Errorf("status = %q, want modified", fd.Status)
	}
}

// RawPatch must stay byte-identical to git's output: the local-runner sync feeds
// it to `git apply`, so a re-serialized approximation would corrupt the mirror.
func TestParseDiff_MultiFileRawPatchIsVerbatim(t *testing.T) {
	patch := joinLines(
		"diff --git a/a.txt b/a.txt",
		"--- a/a.txt",
		"+++ b/a.txt",
		"@@ -1 +1 @@",
		"-a",
		"+A",
		"diff --git a/b.txt b/b.txt",
		"--- a/b.txt",
		"+++ b/b.txt",
		"@@ -1 +1 @@",
		"-b",
		"+B",
		"",
	)
	diffs, err := parseDiff(patch)
	if err != nil {
		t.Fatalf("parseDiff: %v", err)
	}
	if len(diffs) != 2 {
		t.Fatalf("files = %d, want 2", len(diffs))
	}
	if got := diffs[0].RawPatch + diffs[1].RawPatch; got != patch {
		t.Errorf("concatenated RawPatch != input\n got: %q\nwant: %q", got, patch)
	}
	for _, d := range diffs {
		if !strings.HasPrefix(d.RawPatch, "diff --git ") {
			t.Errorf("%s RawPatch does not start at its own header: %q", d.Path, d.RawPatch)
		}
	}
}

func TestParseDiff_Empty(t *testing.T) {
	for _, in := range []string{"", "   \n\n"} {
		diffs, err := parseDiff(in)
		if err != nil {
			t.Fatalf("parseDiff(%q): %v", in, err)
		}
		if diffs == nil || len(diffs) != 0 {
			t.Errorf("parseDiff(%q) = %+v, want empty non-nil slice", in, diffs)
		}
	}
}

// End-to-end against real git output: the fixtures above are hand-written, so
// this pins them to what git actually emits for the same four edge cases.
func TestParseDiff_AgainstRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	runGit(t, "", "init", "-q", "-b", "main", dir)
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	// Baseline: a text file, a to-be-renamed file, a binary blob, an empty file.
	writeFile(t, filepath.Join(dir, "text.txt"), "line1\nline2\n")
	writeFile(t, filepath.Join(dir, "old-name.txt"), strings.Repeat("stable content\n", 20))
	writeFile(t, filepath.Join(dir, "blob.bin"), "\x00\x01\x02binary\x00\xff")
	writeFile(t, filepath.Join(dir, "empty.txt"), "")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "init")

	// Mutate each: no trailing newline, rename, binary edit, new empty file.
	writeFile(t, filepath.Join(dir, "text.txt"), "line1\nline2-changed")
	runGit(t, dir, "mv", "old-name.txt", "new-name.txt")
	writeFile(t, filepath.Join(dir, "blob.bin"), "\x00\x01\x02CHANGED\x00\xfe")
	writeFile(t, filepath.Join(dir, "new-empty.txt"), "")
	runGit(t, dir, "add", "-A")

	out, err := exec.Command("git", "-C", dir, "diff", "--cached", "-M").Output()
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	diffs, err := parseDiff(string(out))
	if err != nil {
		t.Fatalf("parseDiff: %v", err)
	}
	byPath := indexByPath(diffs)

	t.Run("binary", func(t *testing.T) {
		fd, ok := byPath["blob.bin"]
		if !ok {
			t.Fatalf("blob.bin missing from %v", paths(diffs))
		}
		if !fd.IsBinary {
			t.Errorf("blob.bin IsBinary = false; raw patch:\n%s", fd.RawPatch)
		}
	})

	t.Run("rename", func(t *testing.T) {
		fd, ok := byPath["new-name.txt"]
		if !ok {
			t.Fatalf("new-name.txt missing from %v", paths(diffs))
		}
		if fd.Status != "renamed" {
			t.Errorf("status = %q, want renamed", fd.Status)
		}
		if fd.OldPath != "old-name.txt" {
			t.Errorf("OldPath = %q, want old-name.txt", fd.OldPath)
		}
		if fd.IsBinary {
			t.Error("a pure rename must not be flagged binary")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		fd, ok := byPath["new-empty.txt"]
		if !ok {
			t.Fatalf("new-empty.txt missing from %v", paths(diffs))
		}
		if fd.Status != "added" {
			t.Errorf("status = %q, want added", fd.Status)
		}
		if len(fd.Hunks) != 0 || fd.IsBinary {
			t.Errorf("hunks = %d, binary = %v, want 0/false", len(fd.Hunks), fd.IsBinary)
		}
	})

	t.Run("no newline at EOF", func(t *testing.T) {
		fd, ok := byPath["text.txt"]
		if !ok {
			t.Fatalf("text.txt missing from %v", paths(diffs))
		}
		if len(fd.Hunks) != 1 {
			t.Fatalf("hunks = %d, want 1", len(fd.Hunks))
		}
		var flagged int
		for _, l := range fd.Hunks[0].Lines {
			if l.NoNewline {
				flagged++
			}
			if strings.HasPrefix(l.Content, `\ No newline`) {
				t.Errorf("the no-newline marker leaked in as a diff line: %+v", l)
			}
		}
		// Only the added final line lacks the trailing newline.
		if flagged != 1 {
			t.Errorf("NoNewline lines = %d, want 1; raw patch:\n%s", flagged, fd.RawPatch)
		}
		if fd.Additions != 1 || fd.Deletions != 1 {
			t.Errorf("+%d/-%d, want +1/-1", fd.Additions, fd.Deletions)
		}
	})
}

// Untracked files reach the client through a "git diff --no-index /dev/null"
// patch, whose header differs from a normal tracked diff. The structured output
// must land in the same shape (path, added status, real hunk lines) — the
// frontend has no parser left to paper over a difference.
func TestUntrackedDiffs_StructuredLikeTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	runGit(t, "", "init", "-q", "-b", "main", dir)
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "seed.txt"), "seed\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "init")

	writeFile(t, filepath.Join(dir, "fresh.txt"), "alpha\nbeta\n")

	diffs, err := UntrackedDiffs(dir)
	if err != nil {
		t.Fatalf("UntrackedDiffs: %v", err)
	}
	byPath := indexByPath(diffs)
	fd, ok := byPath["fresh.txt"]
	if !ok {
		t.Fatalf("fresh.txt missing from %v", paths(diffs))
	}
	if fd.Status != "added" {
		t.Errorf("status = %q, want added", fd.Status)
	}
	if fd.OldPath != "" {
		t.Errorf("OldPath = %q, want empty (an untracked file has no old side)", fd.OldPath)
	}
	if fd.IsBinary {
		t.Error("a text file must not be flagged binary")
	}
	if len(fd.Hunks) != 1 {
		t.Fatalf("hunks = %d, want 1; raw patch:\n%s", len(fd.Hunks), fd.RawPatch)
	}
	lines := fd.Hunks[0].Lines
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2: %+v", len(lines), lines)
	}
	for i, want := range []DiffLine{
		{Type: DiffLineAdd, Content: "alpha", NewLine: 1},
		{Type: DiffLineAdd, Content: "beta", NewLine: 2},
	} {
		if lines[i] != want {
			t.Errorf("line %d = %+v, want %+v", i, lines[i], want)
		}
	}
}
