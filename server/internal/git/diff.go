package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// FileDiff represents the diff for a single file.
//
// This is the single source of truth for diff structure: clients render from
// Hunks/Lines and never re-parse RawPatch. RawPatch is retained only for
// consumers that must replay the patch verbatim (the local-runner sync applies
// it with git apply), so it MUST stay byte-identical to git's output.
type FileDiff struct {
	Path   string `json:"path"`
	Status string `json:"status"` // added|modified|deleted|renamed|copied
	// OldPath is the pre-change path, set only for renames and copies (where it
	// differs from Path). Empty otherwise.
	OldPath   string `json:"old_path,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// IsBinary is true only when git reported the file as an actual binary blob
	// ("Binary files ... differ" / "GIT binary patch"). A diff with zero hunks is
	// NOT necessarily binary — mode-only changes (chmod +x), pure renames and
	// empty files also produce no hunks. Clients must gate the "can't preview"
	// message on this flag, never on len(Hunks) == 0.
	IsBinary bool `json:"is_binary,omitempty"`
	// OldMode/NewMode carry the file mode pair when the diff reports one, so a
	// content-less mode change can be described instead of shown as binary.
	OldMode  string     `json:"old_mode,omitempty"`
	NewMode  string     `json:"new_mode,omitempty"`
	Hunks    []DiffHunk `json:"hunks"`
	RawPatch string     `json:"raw_patch"`
}

// DiffHunk represents a single hunk in a diff, fully decomposed into lines.
type DiffHunk struct {
	OldStart int `json:"old_start"`
	OldCount int `json:"old_count"`
	NewStart int `json:"new_start"`
	NewCount int `json:"new_count"`
	// Header is the optional section heading git appends after the closing "@@"
	// (usually the enclosing function), with surrounding spaces trimmed.
	Header string     `json:"header,omitempty"`
	Lines  []DiffLine `json:"lines"`
}

// Diff line types, mirrored verbatim in the client's DiffLine union.
const (
	DiffLineContext = "context"
	DiffLineAdd     = "add"
	DiffLineDelete  = "delete"
)

// DiffLine is one line inside a hunk, with its resolved line numbers already
// computed so clients need no running counters to index or virtualize the view.
type DiffLine struct {
	Type    string `json:"type"` // context|add|delete
	Content string `json:"content"`
	// OldLine/NewLine are 1-based line numbers, 0 when the line does not exist
	// on that side (adds have no OldLine, deletes have no NewLine).
	OldLine int `json:"old_line,omitempty"`
	NewLine int `json:"new_line,omitempty"`
	// NoNewline marks a line followed by git's "\ No newline at end of file".
	NoNewline bool `json:"no_newline,omitempty"`
}

// Diff returns the tracked changes for a worktree relative to a baseline and
// parses them into structured data.
//
// When base is non-empty it diffs the merge-base of base and HEAD against the
// current working tree. That yields the three-dot "base...HEAD" view (changes
// made on this branch since it diverged from base), extended to also include
// uncommitted work — i.e. all committed + uncommitted tracked changes vs the
// baseline. When base is empty (or it cannot be resolved) it falls back to
// "git diff HEAD" (uncommitted tracked changes only), preserving the original
// behavior for callers that only care about pre-commit output.
//
// Neither form lists untracked files; callers building a full "vs baseline"
// file list merge those in via UntrackedDiffs.
func Diff(worktreePath, base string) ([]FileDiff, error) {
	rev := mergeBaseDiffRev(worktreePath, base)
	if rev == "" {
		rev = "HEAD"
	}

	cmd := exec.Command("git", "-C", worktreePath, "diff", rev)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	return parseDiff(string(out))
}

// ResolveDiffBase picks a base revision to diff a worktree against, trying the
// preferred base first and then common mainline names (main/master and their
// origin/* forms). It returns the first candidate that has a merge-base with
// HEAD — i.e. a real, related ref — or "" if none do.
//
// This is the fallback for worktrees whose recorded base branch is unreliable
// (e.g. a stale association): without it git.Diff would silently collapse to
// "git diff HEAD" (uncommitted changes only), under-reporting the true change
// magnitude. Diffing against main instead surfaces everything since the fork.
func ResolveDiffBase(worktreePath, preferred string) string {
	seen := make(map[string]bool, 6)
	for _, c := range []string{preferred, "main", "master", "origin/HEAD", "origin/main", "origin/master"} {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if _, err := exec.Command("git", "-C", worktreePath, "merge-base", c, "HEAD").Output(); err == nil {
			return c
		}
	}
	return ""
}

// mergeBaseDiffRev resolves the revision to diff the working tree against so the
// result equals "base...HEAD" plus uncommitted changes: the merge-base of base
// and HEAD. Returns "" when base is empty or cannot be resolved (e.g. the base
// branch no longer exists), signalling the caller to fall back to "HEAD".
func mergeBaseDiffRev(worktreePath, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", worktreePath, "merge-base", base, "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// UntrackedDiffs returns synthetic added-file diffs for every untracked file in
// the worktree. The committed/uncommitted diff produced by Diff never lists
// untracked files, so callers assembling a full "vs baseline" view merge these
// in. Ignored files are excluded; binary files are reported correctly by the
// underlying "git diff --no-index".
func UntrackedDiffs(worktreePath string) ([]FileDiff, error) {
	cmd := exec.Command("git", "-C", worktreePath, "-c", "core.quotepath=false",
		"status", "--porcelain", "--untracked-files=all")
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}

	var diffs []FileDiff
	for _, line := range strings.Split(strings.TrimRight(string(out), " \r\n"), "\n") {
		// Porcelain v1 marks untracked entries with the "?? " prefix.
		if !strings.HasPrefix(line, "?? ") {
			continue
		}
		path := decodeQuotedPath(strings.TrimSpace(line[3:]))
		if path == "" {
			continue
		}
		fd, err := untrackedFileDiff(worktreePath, path)
		if err != nil {
			return nil, err
		}
		if fd != nil {
			diffs = append(diffs, *fd)
		}
	}
	return diffs, nil
}

// untrackedFileDiff builds an added-file FileDiff for a single untracked path
// using "git diff --no-index" against /dev/null, which produces a normal
// unified diff (and reports binary files as "Binary files ... differ").
func untrackedFileDiff(worktreePath, filePath string) (*FileDiff, error) {
	cmd := exec.Command("git", "-C", worktreePath, "-c", "core.quotepath=false",
		"diff", "--no-index", "--", "/dev/null", filePath)
	out, err := cmd.Output()
	// --no-index uses diff(1) exit semantics: 1 means "files differ" (always the
	// case here, since the source is empty). Only a different code is a real error.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("git diff --no-index %s: %w", filePath, err)
		}
	}

	diffs, err := parseDiff(string(out))
	if err != nil {
		return nil, err
	}
	if len(diffs) == 0 {
		return nil, nil
	}
	fd := diffs[0]
	fd.Status = "added"
	fd.Path = filePath
	return &fd, nil
}

// parseDiff parses unified diff output into structured FileDiff objects.
//
// This is the ONLY diff parser in the product — clients consume FileDiff.Hunks
// directly instead of re-parsing RawPatch, so every git metadata line that a
// renderer needs to distinguish (binary marker, mode pair, rename paths, missing
// trailing newline) has to be captured here.
func parseDiff(diffOutput string) ([]FileDiff, error) {
	if strings.TrimSpace(diffOutput) == "" {
		return []FileDiff{}, nil
	}

	lines, offsets := splitLinesWithOffsets(diffOutput)

	var (
		diffs   []FileDiff
		cur     *FileDiff
		curHunk *DiffHunk
		start   int // byte offset of the current file's "diff --git" line
		oldNo   int
		newNo   int
	)

	// flush finalizes the file being parsed, slicing its RawPatch out of the
	// original text so it stays byte-identical to git's output (the runner sync
	// feeds it to `git apply`).
	flush := func(end int) {
		if cur == nil {
			return
		}
		if curHunk != nil {
			cur.Hunks = append(cur.Hunks, *curHunk)
			curHunk = nil
		}
		cur.RawPatch = diffOutput[start:end]
		diffs = append(diffs, *cur)
		cur = nil
	}

	for i, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			flush(offsets[i])
			start = offsets[i]
			oldPath, newPath := parseGitHeaderPaths(line)
			cur = &FileDiff{Path: newPath, Status: "modified"}
			if oldPath != "" && oldPath != newPath {
				cur.OldPath = oldPath
			}
			continue
		}
		if cur == nil {
			continue
		}

		// Inside a hunk, the payload lines are prefix-coded and must be consumed
		// before the metadata checks below — a context line can legitimately hold
		// text like "rename from x" or "Binary files ... differ".
		if curHunk != nil {
			switch {
			case strings.HasPrefix(line, "+"):
				curHunk.Lines = append(curHunk.Lines, DiffLine{
					Type: DiffLineAdd, Content: line[1:], NewLine: newNo,
				})
				newNo++
				cur.Additions++
				continue
			case strings.HasPrefix(line, "-"):
				curHunk.Lines = append(curHunk.Lines, DiffLine{
					Type: DiffLineDelete, Content: line[1:], OldLine: oldNo,
				})
				oldNo++
				cur.Deletions++
				continue
			case strings.HasPrefix(line, " "):
				curHunk.Lines = append(curHunk.Lines, DiffLine{
					Type: DiffLineContext, Content: line[1:], OldLine: oldNo, NewLine: newNo,
				})
				oldNo++
				newNo++
				continue
			case strings.HasPrefix(line, `\`):
				// "\ No newline at end of file" annotates the line just emitted.
				if n := len(curHunk.Lines); n > 0 {
					curHunk.Lines[n-1].NoNewline = true
				}
				continue
			case line == "":
				// An empty line is ambiguous: git writes an empty context line as
				// a single space, so a truly empty line is normally the newline
				// terminating the diff body. But some producers strip that trailing
				// space, and dropping the line would desynchronize every line number
				// after it. Treat it as an empty context line while the hunk is still
				// short of its declared counts, and as the terminator once it is full.
				// oldNo/newNo advanced from the hunk's declared starts, so the lines
				// consumed so far are exactly the difference.
				if oldNo-curHunk.OldStart >= curHunk.OldCount &&
					newNo-curHunk.NewStart >= curHunk.NewCount {
					continue
				}
				curHunk.Lines = append(curHunk.Lines, DiffLine{
					Type: DiffLineContext, OldLine: oldNo, NewLine: newNo,
				})
				oldNo++
				newNo++
				continue
			}
			// A non-payload line closes the hunk and falls through to metadata.
			cur.Hunks = append(cur.Hunks, *curHunk)
			curHunk = nil
		}

		if m := hunkHeaderPattern.FindStringSubmatch(line); m != nil {
			oldStart, oldCount := parseInt(m[1]), 1
			if m[2] != "" {
				oldCount = parseInt(m[2])
			}
			newStart, newCount := parseInt(m[3]), 1
			if m[4] != "" {
				newCount = parseInt(m[4])
			}
			curHunk = &DiffHunk{
				OldStart: oldStart, OldCount: oldCount,
				NewStart: newStart, NewCount: newCount,
				Header: strings.TrimSpace(m[5]),
			}
			oldNo, newNo = oldStart, newStart
			continue
		}

		switch {
		case strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
			cur.NewMode = strings.TrimSpace(strings.TrimPrefix(line, "new file mode"))
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"
			cur.OldMode = strings.TrimSpace(strings.TrimPrefix(line, "deleted file mode"))
		case strings.HasPrefix(line, "old mode"):
			cur.OldMode = strings.TrimSpace(strings.TrimPrefix(line, "old mode"))
		case strings.HasPrefix(line, "new mode"):
			cur.NewMode = strings.TrimSpace(strings.TrimPrefix(line, "new mode"))
		case strings.HasPrefix(line, "rename from "):
			cur.Status = "renamed"
			cur.OldPath = decodeQuotedPath(strings.TrimSpace(strings.TrimPrefix(line, "rename from ")))
		case strings.HasPrefix(line, "rename to "):
			cur.Status = "renamed"
			cur.Path = decodeQuotedPath(strings.TrimSpace(strings.TrimPrefix(line, "rename to ")))
		case strings.HasPrefix(line, "copy from "):
			cur.Status = "copied"
			cur.OldPath = decodeQuotedPath(strings.TrimSpace(strings.TrimPrefix(line, "copy from ")))
		case strings.HasPrefix(line, "copy to "):
			cur.Status = "copied"
			cur.Path = decodeQuotedPath(strings.TrimSpace(strings.TrimPrefix(line, "copy to ")))
		case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
			// The ONLY signal that a file is genuinely non-textual. Previously
			// dropped on the floor here, which forced the client to keep its own
			// parser just to recover it.
			cur.IsBinary = true
		case strings.HasPrefix(line, "--- "):
			if p, ok := diffSidePath(line[4:], "a/"); ok {
				if cur.OldPath == "" && p != cur.Path {
					cur.OldPath = p
				}
			} else {
				// "--- /dev/null": the file is new, so any OldPath seeded from the
				// ambiguous "diff --git" header (e.g. the "a/dev/null" that
				// --no-index emits for untracked files) is spurious.
				cur.OldPath = ""
			}
		case strings.HasPrefix(line, "+++ "):
			// The b-side name is unambiguous (one path per line), unlike the
			// "diff --git" header, so it wins when both are present.
			if p, ok := diffSidePath(line[4:], "b/"); ok {
				cur.Path = p
			}
		}
	}
	flush(len(diffOutput))

	if diffs == nil {
		return []FileDiff{}, nil
	}
	return diffs, nil
}

// hunkHeaderPattern matches "@@ -l,s +l,s @@ optional section heading".
var hunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

// splitLinesWithOffsets splits s on "\n", returning each line (without the
// terminator) alongside its byte offset in s, so a slice of the original text
// can be recovered verbatim.
func splitLinesWithOffsets(s string) ([]string, []int) {
	var lines []string
	var offsets []int
	for pos := 0; pos <= len(s); {
		nl := strings.IndexByte(s[pos:], '\n')
		if nl < 0 {
			lines = append(lines, s[pos:])
			offsets = append(offsets, pos)
			break
		}
		lines = append(lines, s[pos:pos+nl])
		offsets = append(offsets, pos)
		pos += nl + 1
	}
	return lines, offsets
}

// diffSidePath extracts the real path from a "---"/"+++" header body, stripping
// git's a//b/ prefix. It reports false for "/dev/null" (the absent side of an
// add or delete) and for the timestamp-suffixed forms git never emits here.
func diffSidePath(body, prefix string) (string, bool) {
	body = strings.TrimRight(body, "\r")
	if body == "" || body == "/dev/null" {
		return "", false
	}
	p := decodeQuotedPath(body)
	if p == "/dev/null" {
		return "", false
	}
	return strings.TrimPrefix(p, prefix), true
}

// parseGitHeaderPaths pulls the a-side and b-side paths out of a "diff --git"
// line. The unquoted form is genuinely ambiguous when a path contains " b/", so
// this prefers the split whose two sides agree (the overwhelmingly common
// same-path case) and otherwise falls back to the last candidate. Callers treat
// the result as a seed: the "---"/"+++" and "rename to" lines override it.
func parseGitHeaderPaths(line string) (oldPath, newPath string) {
	body := strings.TrimRight(strings.TrimPrefix(line, "diff --git "), "\r")

	// Quoted form: git C-quotes either side independently when it needs to.
	if strings.HasPrefix(body, `"`) {
		if end := closingQuote(body); end > 0 {
			a := decodeQuotedPath(body[:end+1])
			rest := strings.TrimSpace(body[end+1:])
			b := decodeQuotedPath(rest)
			return strings.TrimPrefix(a, "a/"), strings.TrimPrefix(b, "b/")
		}
	}

	best := -1
	for idx := strings.Index(body, " b/"); idx >= 0; {
		a := strings.TrimPrefix(body[:idx], "a/")
		b := strings.TrimPrefix(body[idx+1:], "b/")
		if a == b {
			return a, b
		}
		best = idx
		next := strings.Index(body[idx+1:], " b/")
		if next < 0 {
			break
		}
		idx += next + 1
	}
	if best >= 0 {
		return strings.TrimPrefix(body[:best], "a/"),
			strings.TrimPrefix(decodeQuotedPath(strings.TrimSpace(body[best+1:])), "b/")
	}
	return "", ""
}

// closingQuote returns the index of the quote closing the C-quoted string that
// starts at s[0], skipping backslash escapes, or -1 when unterminated.
func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// parseInt safely parses a string to int, returning 0 on error.
func parseInt(s string) int {
	var result int
	fmt.Sscanf(s, "%d", &result)
	return result
}

// FileContent returns the content of a file at a given path.
// The path must be within the worktree (no path traversal).
func FileContent(worktreePath, filePath string) (string, error) {
	// Resolve the absolute path and verify it's within worktree
	absPath := filepath.Join(worktreePath, filePath)
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return "", fmt.Errorf("resolve worktree path: %w", err)
	}
	// Clean the path and verify it starts with worktree prefix
	cleanPath := filepath.Clean(absPath)
	if !strings.HasPrefix(cleanPath, absWorktree) {
		return "", fmt.Errorf("path escape attempt detected")
	}

	cmd := exec.Command("git", "-C", worktreePath, "show", "HEAD:"+filePath)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show: %w", err)
	}
	return string(out), nil
}

// DiffFile returns the diff for a single file relative to a baseline, using the
// same merge-base semantics as Diff (see Diff for how base is interpreted).
// This is more efficient than getting the full diff and filtering. Untracked
// files have no entry in the tracked diff, so they fall back to a synthetic
// added-file diff. The path must be within the worktree (no path traversal).
func DiffFile(worktreePath, base, filePath string) (*FileDiff, error) {
	// Resolve the absolute path and verify it's within worktree
	absPath := filepath.Join(worktreePath, filePath)
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree path: %w", err)
	}
	// Clean the path and verify it starts with worktree prefix
	cleanPath := filepath.Clean(absPath)
	if !strings.HasPrefix(cleanPath, absWorktree) {
		return nil, fmt.Errorf("path escape attempt detected")
	}

	rev := mergeBaseDiffRev(worktreePath, base)
	if rev == "" {
		rev = "HEAD"
	}

	cmd := exec.Command("git", "-C", worktreePath, "diff", rev, "--", filePath)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	diffs, err := parseDiff(string(out))
	if err != nil {
		return nil, err
	}

	if len(diffs) == 0 {
		// The file has no tracked diff vs base. If it is untracked, surface it
		// as an added file so the single-file viewer matches the file list.
		if isUntracked(worktreePath, filePath) {
			fd, uerr := untrackedFileDiff(worktreePath, filePath)
			if uerr == nil && fd != nil {
				return fd, nil
			}
		}
		return nil, fmt.Errorf("file not found in diff: %s", filePath)
	}
	return &diffs[0], nil
}

// isUntracked reports whether filePath is an untracked (and not ignored) file in
// the worktree.
func isUntracked(worktreePath, filePath string) bool {
	out, err := exec.Command("git", "-C", worktreePath, "-c", "core.quotepath=false",
		"ls-files", "--others", "--exclude-standard", "--", filePath).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// CommitDiff returns the structured, line-level diff a single commit introduced.
//
// The commit-detail view previously listed only file NAMES (`git diff-tree
// --name-status`), so "what did this commit actually change" was unanswerable
// without leaving the app. This reuses the same `parseDiff` the workspace diff
// goes through, which means the Repository page and the review panel render
// identical structures — no second parser, no second set of edge cases.
//
// A root commit (no parent) needs no special handling: `git show` diffs it
// against the empty tree, so it renders as "this commit created these files".
func CommitDiff(repoPath, commitHash string) ([]FileDiff, error) {
	// `--first-parent` makes a merge commit report what the merge brought in
	// rather than the empty diff `show` gives a merge by default. `-m` is
	// deliberately NOT used instead: it emits one diff section per parent, and
	// parseDiff would then return the same file twice.
	cmd := exec.Command("git", "-C", repoPath, "-c", "core.quotepath=false",
		"show", "--format=", "--first-parent", commitHash)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s: %w", commitHash, err)
	}
	return parseDiff(string(out))
}
