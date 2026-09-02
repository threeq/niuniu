package api

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// Content search limits. A workspace can hold several full checkouts, so a wide
// query ("e", ".") would otherwise stream tens of thousands of lines into the
// SPA. Every limit below is enforced server-side and surfaced through the
// response's `truncated` flag — a truncated result is never presented as a
// complete one.
const (
	// contentMaxResults caps the total number of MATCH lines returned across all
	// files (context lines don't count).
	contentMaxResults = 200
	// contentMaxPerFile caps match lines within a single file, so one generated
	// file can't crowd out every other hit.
	contentMaxPerFile = 20
	// contentMaxLineLen caps the rune length of any returned line (match or
	// context). Longer lines are cut and flagged per-line.
	contentMaxLineLen = 400
	// contentMaxContext is the ceiling for the caller-supplied context radius.
	contentMaxContext = 3
	// contentTimeout bounds the whole search. The underlying process is started
	// with exec.CommandContext, so expiry kills it rather than leaking it.
	contentTimeout = 10 * time.Second
	// contentMaxQueryLen rejects absurd patterns before they reach a regex engine.
	contentMaxQueryLen = 512
	// contentScanBuf is the per-line read buffer. ripgrep's JSON envelope wraps
	// the matched line, so a single minified-JS line can be very long.
	contentScanBuf = 1 << 20
)

// Truncation reasons reported in ContentSearchResponse.TruncatedReason.
const (
	truncReasonLimit   = "limit"   // hit contentMaxResults
	truncReasonTimeout = "timeout" // contentTimeout expired mid-search
)

// Search engine identifiers reported in ContentSearchResponse.Engine.
const (
	engineRipgrep = "ripgrep"
	engineGitGrep = "git-grep"
)

// contentMatch is a single matching line plus its surrounding context.
type contentMatch struct {
	Line int    `json:"line"`
	Text string `json:"text"`
	// Before/After hold up to `context` lines around the match. The git-grep
	// fallback cannot label context lines unambiguously, so it returns none.
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
	// Columns are [start,end) BYTE offsets of each match within Text, letting
	// the client highlight the hit. Only ripgrep reports these.
	Columns [][2]int `json:"columns,omitempty"`
	// LineTruncated marks a line cut to contentMaxLineLen.
	LineTruncated bool `json:"lineTruncated,omitempty"`
}

// contentFileResult groups every match found in one file.
type contentFileResult struct {
	// Path is workspace-root-relative with forward slashes, e.g.
	// ".worktrees/niuniu-main/internal/api/filetree.go".
	Path string `json:"path"`
	// Repo is the `.worktrees/<name>` subdir, or "" for workspace-root files.
	Repo    string         `json:"repo"`
	Matches []contentMatch `json:"matches"`
	// Truncated means this file hit contentMaxPerFile and has more matches.
	Truncated bool `json:"truncated,omitempty"`
}

// ContentSearchResponse is the payload of GET /workspaces/:id/search/content.
type ContentSearchResponse struct {
	Engine string              `json:"engine"`
	Files  []contentFileResult `json:"files"`
	// TotalMatches counts match lines actually returned (post-truncation).
	TotalMatches int `json:"totalMatches"`
	// Truncated is true when results were cut short for ANY reason. Clients must
	// show this — a silently truncated list reads as "that's all there is".
	Truncated       bool   `json:"truncated"`
	TruncatedReason string `json:"truncatedReason,omitempty"`
}

// contentSearchOpts is the parsed, validated query.
type contentSearchOpts struct {
	Query         string
	CaseSensitive bool
	WholeWord     bool
	Regex         bool
	Context       int
}

// lookPath is indirected so tests can simulate a host without ripgrep.
var contentLookPath = exec.LookPath

// errNoSearchEngine is returned when neither ripgrep nor git is on PATH. This
// surfaces to the client as an explicit 501 — never as an empty result set,
// which would read as "no matches" instead of "cannot search".
var errNoSearchEngine = errors.New("no search engine available: neither ripgrep (rg) nor git is on PATH")

// SearchContent handles GET /workspaces/:id/search/content?q=…
//
// It greps the workspace's files for `q`, preferring ripgrep and falling back to
// `git grep` per worktree. Results are always bounded (see the content* consts)
// and any truncation is reported explicitly.
func (h *FileTreeHandler) SearchContent(c *gin.Context) {
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if userID := c.GetInt64("auth_user_id"); userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	opts, perr := parseContentSearchOpts(c)
	if perr != nil {
		BadRequest(c, perr.Error())
		return
	}

	ctx := c.Request.Context()
	ws, err := h.queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		InternalError(c, fmt.Errorf("workspace not found: %w", err))
		return
	}

	// Resolve the workspace root through symlinks once; every result path is
	// re-validated against this boundary before it leaves the handler.
	root, err := resolveWorkspaceRoot(ws.Path)
	if err != nil {
		InternalError(c, fmt.Errorf("resolve workspace path: %w", err))
		return
	}

	res, err := runContentSearch(ctx, root, opts)
	if err != nil {
		if errors.Is(err, errNoSearchEngine) {
			// 501: the request was fine, the host just cannot serve it. Distinct
			// from "no matches" so the UI can say so.
			RespondError(c, http.StatusNotImplemented, "SEARCH_ENGINE_MISSING", err.Error())
			return
		}
		InternalError(c, err)
		return
	}

	// A nil slice marshals to JSON null, which the SPA cannot consume; force [].
	if res.Files == nil {
		res.Files = []contentFileResult{}
	}
	c.JSON(http.StatusOK, res)
}

// resolveWorkspaceRoot returns the symlink-resolved, absolute workspace root and
// verifies it is a directory.
func resolveWorkspaceRoot(wsPath string) (string, error) {
	if wsPath == "" {
		return "", errors.New("workspace has no path")
	}
	resolved, err := filepath.EvalSymlinks(wsPath)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory: %s", abs)
	}
	return abs, nil
}

// parseContentSearchOpts validates the query string.
func parseContentSearchOpts(c *gin.Context) (contentSearchOpts, error) {
	q := c.Query("q")
	// Only trim newlines/CR: a query may legitimately hunt for leading or
	// trailing spaces, but a newline would break every line-oriented engine.
	q = strings.Trim(q, "\r\n")
	if strings.TrimSpace(q) == "" {
		return contentSearchOpts{}, errors.New("q parameter is required")
	}
	if len(q) > contentMaxQueryLen {
		return contentSearchOpts{}, fmt.Errorf("q is too long (max %d bytes)", contentMaxQueryLen)
	}

	opts := contentSearchOpts{
		Query:         q,
		CaseSensitive: c.Query("case") == "1" || c.Query("case") == "true",
		WholeWord:     c.Query("word") == "1" || c.Query("word") == "true",
		Regex:         c.Query("regex") == "1" || c.Query("regex") == "true",
		Context:       1,
	}

	if raw := c.Query("context"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return contentSearchOpts{}, errors.New("context must be a non-negative integer")
		}
		if n > contentMaxContext {
			n = contentMaxContext
		}
		opts.Context = n
	}

	return opts, nil
}

// runContentSearch picks an engine and runs it. ripgrep is preferred (RE2 — no
// catastrophic backtracking, native .gitignore + binary handling); `git grep`
// per worktree is the fallback. If neither exists it returns errNoSearchEngine
// rather than an empty result.
func runContentSearch(ctx context.Context, root string, opts contentSearchOpts) (*ContentSearchResponse, error) {
	if rgPath, err := contentLookPath("rg"); err == nil {
		return searchWithRipgrep(ctx, rgPath, root, opts)
	}
	if gitPath, err := contentLookPath("git"); err == nil {
		return searchWithGitGrep(ctx, gitPath, root, opts)
	}
	return nil, errNoSearchEngine
}

// collector accumulates results while enforcing every limit. It is engine-
// agnostic: both backends feed it line by line.
type collector struct {
	root string
	// ctxRadius is the caller's requested context radius. Before/After are capped
	// at this, not at contentMaxContext — asking for 1 must not yield 2.
	ctxRadius int
	files     []contentFileResult
	// index maps a workspace-relative path to its slot in files, preserving the
	// engine's discovery order (ripgrep walks in parallel, so it is not sorted).
	index map[string]int
	// lastMatchLine tracks, per file, the line of the most recently STORED match.
	// Trailing context is only attached when it is contiguous with that line, so
	// a later match's leading context can't be mis-filed onto an earlier match.
	lastMatchLine map[string]int
	// capped marks files that hit contentMaxPerFile. Once capped, no further
	// context is attached — the lines streaming past belong to matches that were
	// dropped, not to the last one we kept.
	capped    map[string]bool
	total     int
	truncated bool
	reason    string
	// pathOK caches the per-path boundary check — a file with many matches would
	// otherwise re-stat on every line.
	pathOK map[string]bool
}

func newCollector(root string, ctxRadius int) *collector {
	return &collector{
		root:          root,
		ctxRadius:     ctxRadius,
		index:         map[string]int{},
		lastMatchLine: map[string]int{},
		capped:        map[string]bool{},
		pathOK:        map[string]bool{},
	}
}

// full reports whether the total-results ceiling has been reached. Callers stop
// reading (and kill the child process) once it returns true.
func (co *collector) full() bool { return co.total >= contentMaxResults }

// safePath validates that a path reported by the search engine really lives
// inside the workspace. Neither engine follows symlinks by default, but a
// malicious or merely surprising repo layout must not be able to leak a file
// from outside the boundary, so this is enforced independently of the engine.
func (co *collector) safePath(rel string) bool {
	if ok, seen := co.pathOK[rel]; seen {
		return ok
	}
	ok := verifyWithinRoot(co.root, rel)
	co.pathOK[rel] = ok
	return ok
}

// verifyWithinRoot resolves root/rel through symlinks and confirms the result is
// still under root. Mirrors the attachment handler's file-content check.
func verifyWithinRoot(root, rel string) bool {
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || filepath.IsAbs(cleaned) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, cleaned))
	if err != nil {
		return false
	}
	// root is already symlink-resolved by resolveWorkspaceRoot.
	return strings.HasPrefix(resolved, root+string(filepath.Separator))
}

// addMatch records one match line. It returns false once the global cap is hit
// AND a further match was actually dropped — that is the signal for callers to
// stop reading. Reaching the cap exactly, with nothing left over, is a complete
// result and is NOT flagged truncated.
func (co *collector) addMatch(rel string, m contentMatch) bool {
	if co.full() {
		// This match exists but has nowhere to go: results really are cut short.
		co.truncated = true
		co.reason = truncReasonLimit
		return false
	}
	if !co.safePath(rel) {
		return true // skip this path, keep searching
	}

	idx, ok := co.index[rel]
	if !ok {
		co.files = append(co.files, contentFileResult{Path: rel, Repo: repoFromRelPath(rel)})
		idx = len(co.files) - 1
		co.index[rel] = idx
	}

	f := &co.files[idx]
	if len(f.Matches) >= contentMaxPerFile {
		// Per-file cap: flag it and drop the line, but keep scanning other files.
		// From here on this file's context lines belong to dropped matches.
		f.Truncated = true
		co.capped[rel] = true
		co.truncated = true
		if co.reason == "" {
			co.reason = truncReasonLimit
		}
		return true
	}

	f.Matches = append(f.Matches, m)
	co.lastMatchLine[rel] = m.Line
	co.total++
	// NOTE: deliberately no truncated=true here. Filling the last slot exactly
	// means nothing was lost; only a subsequent addMatch (above) proves otherwise.
	return true
}

// addContext attaches a trailing context line to a file's most recent stored
// match.
//
// `line` is the context line's own 1-based number, and it is only accepted when
// contiguous with the match it would attach to. Without that check, the LEADING
// context of a later match (which ripgrep emits after the earlier match, with
// no marker distinguishing the two) would be appended as TRAILING context to
// the earlier one — silently showing the reader lines that don't surround the
// hit they're looking at.
//
// Returns false when the line was rejected, so the caller can buffer it as
// leading context for the next match instead.
func (co *collector) addContext(rel string, line int, text string) bool {
	if co.capped[rel] {
		// Matches are being dropped for this file; its context belongs to them.
		return false
	}
	idx, ok := co.index[rel]
	if !ok {
		return false
	}
	f := &co.files[idx]
	if len(f.Matches) == 0 {
		return false
	}
	m := &f.Matches[len(f.Matches)-1]

	if len(m.After) >= co.ctxRadius {
		return false
	}
	// Must directly follow the match or the trailing lines already attached.
	if line != m.Line+len(m.After)+1 {
		return false
	}
	m.After = append(m.After, text)
	return true
}

// searchRoot is one directory to grep, plus the prefix that turns a hit's
// root-relative path back into a workspace-relative one.
type searchRoot struct {
	Dir string
	// Prefix is "" for the workspace root itself, ".worktrees/<name>/" otherwise.
	Prefix string
	// TopOnly restricts the search to this directory's own files, not its
	// subdirectories. Used for the workspace root so `.worktrees/` isn't walked
	// twice (once here, once as its own root).
	TopOnly bool
}

// searchRoots enumerates what to grep for a workspace.
//
// Each worktree is searched AS ITS OWN ROOT rather than as a subdirectory of one
// walk from the workspace root. That matters for correctness, not just tidiness:
// ignore files are resolved relative to the search root, so a workspace-root
// `.gitignore`/`.ignore` listing `.worktrees/` would otherwise make every
// worktree invisible — a silent empty result, which is precisely the failure
// mode this endpoint is meant to rule out. Per-root searching also matches how
// file-NAME search already works (listGitFiles runs git per worktree), so the
// two halves of the panel agree on what exists.
//
// The workspace root is still searched, shallowly, so root files (CLAUDE.md,
// .mcp.json) remain findable.
func searchRoots(root string) []searchRoot {
	worktrees := filepath.Join(root, ".worktrees")
	entries, err := os.ReadDir(worktrees)
	if err != nil {
		// No .worktrees/: the workspace root IS the repo, so search it fully.
		return []searchRoot{{Dir: root}}
	}

	roots := make([]searchRoot, 0, len(entries)+1)
	for _, e := range entries {
		// A worktree may itself be a symlink; resolve and bound-check it before
		// handing it to a search process.
		if !isSearchableDir(filepath.Join(worktrees, e.Name())) {
			continue
		}
		roots = append(roots, searchRoot{
			Dir:    filepath.Join(worktrees, e.Name()),
			Prefix: ".worktrees/" + e.Name() + "/",
		})
	}
	if len(roots) == 0 {
		return []searchRoot{{Dir: root}}
	}
	// Root-level files, without descending into .worktrees/ again.
	roots = append(roots, searchRoot{Dir: root, TopOnly: true})
	return roots
}

// isSearchableDir reports whether path is a directory after symlink resolution.
// os.ReadDir reports a symlink-to-dir as a non-dir entry, so this resolves first.
func isSearchableDir(path string) bool {
	info, err := os.Stat(path) // follows symlinks
	return err == nil && info.IsDir()
}

func (co *collector) response(engine string) *ContentSearchResponse {
	return &ContentSearchResponse{
		Engine:          engine,
		Files:           co.files,
		TotalMatches:    co.total,
		Truncated:       co.truncated,
		TruncatedReason: co.reason,
	}
}

// repoFromRelPath extracts the `.worktrees/<name>` segment; workspace-root files
// (CLAUDE.md, .mcp.json, …) get "".
func repoFromRelPath(rel string) string {
	const prefix = ".worktrees/"
	if !strings.HasPrefix(rel, prefix) {
		return ""
	}
	rest := rel[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return rest[:i]
	}
	return ""
}

// clipLine trims a trailing newline and caps the line at contentMaxLineLen
// runes, reporting whether it was cut. Invalid UTF-8 (a binary file that slipped
// past the engine's detector) is replaced so the JSON encoder can't fail.
func clipLine(s string) (string, bool) {
	s = strings.TrimRight(s, "\r\n")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if utf8.RuneCountInString(s) <= contentMaxLineLen {
		return s, false
	}
	runes := []rune(s)
	return string(runes[:contentMaxLineLen]), true
}

// ---------------------------------------------------------------------------
// ripgrep backend
// ---------------------------------------------------------------------------

// rgMessage is the subset of ripgrep's --json stream we consume.
type rgMessage struct {
	Type string `json:"type"`
	Data struct {
		Path       rgText `json:"path"`
		Lines      rgText `json:"lines"`
		LineNumber int    `json:"line_number"`
		Submatches []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

// rgText is ripgrep's {"text": …} / {"bytes": …} union. Data that isn't valid
// UTF-8 arrives base64-encoded in `bytes` with `text` absent.
type rgText struct {
	Text  string `json:"text"`
	Bytes string `json:"bytes"`
}

// searchWithRipgrep runs `rg --json` over each search root.
//
// Each worktree is searched as its OWN root rather than as a subdirectory of a
// single walk from the workspace root. That is a correctness requirement, not
// tidiness: ignore files resolve relative to the search root, so a
// workspace-root `.gitignore`/`.ignore` listing `.worktrees/` would otherwise
// make every worktree invisible — a silent empty result, precisely the failure
// mode this endpoint exists to rule out. It also matches how file-NAME search
// already works (git runs per worktree), so both halves of the panel agree on
// what exists.
//
// ripgrep is preferred over git grep: RE2 semantics (no catastrophic
// backtracking even on a hostile pattern), built-in .gitignore and binary-file
// handling, and a JSON protocol that survives paths containing colons.
func searchWithRipgrep(ctx context.Context, rgPath, root string, opts contentSearchOpts) (*ContentSearchResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, contentTimeout)
	defer cancel()

	commonArgs := []string{
		"--no-config", // ignore RIPGREP_CONFIG_PATH: results must not depend on host config
		"--json",
		"--no-messages",    // per-file IO errors go to stderr; don't fail the search over one
		"--hidden",         // the workspace's repos live under `.worktrees/`, a dot-dir
		"--glob", "!.git/", // ...but never grep git's own object/config store
		"--max-count", strconv.Itoa(contentMaxPerFile + 1), // +1 so we can detect "there were more"
		"--context", strconv.Itoa(opts.Context),
	}
	if !opts.CaseSensitive {
		commonArgs = append(commonArgs, "--ignore-case")
	}
	if opts.WholeWord {
		commonArgs = append(commonArgs, "--word-regexp")
	}
	if !opts.Regex {
		commonArgs = append(commonArgs, "--fixed-strings")
	}
	// `-e` keeps a pattern such as "-v" from being parsed as a flag.
	commonArgs = append(commonArgs, "-e", opts.Query)

	co := newCollector(root, opts.Context)
	for _, sr := range searchRoots(root) {
		if co.full() || ctx.Err() != nil {
			break
		}
		// One unsearchable root (vanished mid-search, permissions) must not sink
		// the others.
		_ = ripgrepOneRoot(ctx, rgPath, sr, commonArgs, opts.Context, co)
	}

	res := co.response(engineRipgrep)
	if ctx.Err() != nil {
		// The deadline killed ripgrep. Partial results are still useful, but they
		// must be labelled -- hence truncated, not "done".
		res.Truncated = true
		res.TruncatedReason = truncReasonTimeout
	}
	return res, nil
}

// ripgrepOneRoot greps a single search root, translating each hit's root-
// relative path into a workspace-relative one via sr.Prefix.
func ripgrepOneRoot(
	ctx context.Context,
	rgPath string,
	sr searchRoot,
	commonArgs []string,
	ctxRadius int,
	co *collector,
) error {
	args := make([]string, len(commonArgs), len(commonArgs)+4)
	copy(args, commonArgs)
	if sr.TopOnly {
		// Root-level files only; `.worktrees/` is searched as its own root.
		args = append(args, "--max-depth", "1")
	}
	args = append(args, "--", ".")

	cmd := exec.CommandContext(ctx, rgPath, args...)
	cmd.Dir = sr.Dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ripgrep stdout: %w", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ripgrep: %w", err)
	}
	// Always reap the child and release the pipe, on every exit path below.
	defer func() {
		_ = stdout.Close()
		_ = cmd.Wait()
	}()

	// pendingBefore buffers leading context lines: ripgrep emits them before the
	// match they belong to, so they can only be attached retroactively.
	pendingBefore := map[string][]string{}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), contentScanBuf)

	for scanner.Scan() {
		var msg rgMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue // a line we don't understand is not worth failing the search
		}
		if msg.Type != "begin" && msg.Type != "match" && msg.Type != "context" {
			continue
		}

		sub := normalizeRel(msg.Data.Path.Text)
		if sub == "" {
			continue // non-UTF-8 path (sent as `bytes`) or otherwise unusable
		}
		rel := sr.Prefix + sub

		switch msg.Type {
		case "begin":
			pendingBefore[rel] = nil

		case "match":
			text, cut := clipLine(rgLineText(msg.Data.Lines))
			m := contentMatch{Line: msg.Data.LineNumber, Text: text, LineTruncated: cut}
			for _, sm := range msg.Data.Submatches {
				// Offsets past a clipped line would mis-highlight; drop those.
				if sm.Start <= len(text) && sm.End <= len(text) {
					m.Columns = append(m.Columns, [2]int{sm.Start, sm.End})
				}
			}
			if b := pendingBefore[rel]; len(b) > 0 {
				m.Before = b
				pendingBefore[rel] = nil
			}
			if !co.addMatch(rel, m) {
				// Global cap reached: stop reading rather than draining a
				// potentially huge stream we would only discard. The deferred
				// Close+Wait kills and reaps ripgrep.
				return nil
			}

		case "context":
			text, _ := clipLine(rgLineText(msg.Data.Lines))
			// Try it as trailing context for this file's last stored match. The
			// collector rejects it when it isn't contiguous -- which means it is
			// really the LEADING context of a match still to come, so it is
			// buffered instead.
			if co.addContext(rel, msg.Data.LineNumber, text) {
				continue
			}
			if ctxRadius <= 0 {
				continue
			}
			b := pendingBefore[rel]
			for len(b) >= ctxRadius {
				b = b[1:]
			}
			pendingBefore[rel] = append(b, text)
		}
	}

	return nil
}

// rgLineText extracts a line's text from ripgrep's {"text"} / {"bytes"} union.
// A line containing invalid UTF-8 arrives as base64 `bytes` with no `text`;
// decoding it (rather than returning "") keeps the match from rendering as a
// blank row in the results list.
func rgLineText(l rgText) string {
	if l.Text != "" {
		return l.Text
	}
	if l.Bytes == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(l.Bytes)
	if err != nil {
		return ""
	}
	// clipLine sanitises the invalid sequences; this just recovers the readable
	// parts instead of dropping the whole line.
	return string(raw)
}

// normalizeRel converts an engine-reported path to a clean workspace-relative
// slash path, or "" if it is unusable.
func normalizeRel(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "./")
	// ripgrep on Windows reports `.\dir\file`; ToSlash already handled the
	// separators, so only the leading marker can remain.
	p = strings.TrimPrefix(p, "/")
	if p == "" || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// ---------------------------------------------------------------------------
// git grep fallback
// ---------------------------------------------------------------------------

// searchWithGitGrep greps each worktree with `git grep` when ripgrep is absent.
//
// Two deliberate differences from the ripgrep path:
//
//   - No context lines. `git grep -z` emits match and context lines in the same
//     `path\0line\0text` shape with nothing distinguishing them, and the
//     non-`-z` form (`path:line:` vs `path-line-`) is ambiguous for any path
//     containing a colon. Returning matches without context beats returning
//     context lines mislabelled as matches.
//   - Regex runs through git's POSIX engine, which CAN backtrack. The
//     contentTimeout deadline is the guard: exec.CommandContext kills the
//     process, so a pathological pattern costs time, not a hung request.
func searchWithGitGrep(ctx context.Context, gitPath, root string, opts contentSearchOpts) (*ContentSearchResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, contentTimeout)
	defer cancel()

	// ctxRadius 0: this backend returns no context lines (see the doc comment).
	co := newCollector(root, 0)

	// Roots git could not search at all (not a repo, permission denied). These
	// must be surfaced: a directory that was never searched is not a directory
	// with no matches, and conflating the two is the exact confusion this
	// endpoint is meant to avoid.
	var unsearchable []string

	roots := searchRoots(root)
	for _, sr := range roots {
		if co.full() || ctx.Err() != nil {
			break
		}
		if err := gitGrepDir(ctx, gitPath, sr, opts, co); err != nil {
			label := sr.Prefix
			if label == "" {
				label = "."
			}
			unsearchable = append(unsearchable, strings.TrimSuffix(label, "/"))
		}
	}

	res := co.response(engineGitGrep)
	if ctx.Err() != nil {
		res.Truncated = true
		res.TruncatedReason = truncReasonTimeout
		return res, nil
	}
	// Every root failed and we have nothing: this is "could not search", not
	// "found nothing". Report it as an error so the UI says so.
	if len(unsearchable) == len(roots) && co.total == 0 {
		return nil, fmt.Errorf(
			"git grep could not search this workspace (not a git repository?): %s",
			strings.Join(unsearchable, ", "))
	}
	// Partial coverage: results are real but incomplete, so flag them.
	if len(unsearchable) > 0 {
		res.Truncated = true
		if res.TruncatedReason == "" {
			res.TruncatedReason = truncReasonLimit
		}
	}
	return res, nil
}

// gitGrepDir runs one `git grep` over a search root, rewriting hit paths from
// root-relative to workspace-relative. It returns an error only when git could
// not search at all (not a repository, permission denied) — never for "no
// matches", which is git's exit code 1 and a perfectly good answer.
func gitGrepDir(ctx context.Context, gitPath string, sr searchRoot, opts contentSearchOpts, co *collector) error {
	prefix := sr.Prefix

	args := []string{
		"-c", "core.quotepath=false", // keep non-ASCII paths as UTF-8
		"grep",
		"--no-color",
		"-I",          // skip binary files
		"-n",          // line numbers
		"-z",          // NUL-delimit path and line number: unambiguous parsing
		"--untracked", // include new files; still honours .gitignore
	}

	if !opts.CaseSensitive {
		args = append(args, "-i")
	}
	if opts.WholeWord {
		args = append(args, "-w")
	}
	if opts.Regex {
		args = append(args, "-E")
	} else {
		args = append(args, "-F")
	}
	// TopOnly: root-level files only; `.worktrees/` is searched as its own root.
	pathspec := "."
	if sr.TopOnly {
		pathspec = ":(glob,top)*"
	}
	args = append(args, "-e", opts.Query, "--", pathspec)

	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Dir = sr.Dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), contentScanBuf)

	for scanner.Scan() {
		relPath, lineNo, text, ok := parseGitGrepLine(scanner.Text())
		if !ok {
			continue
		}
		clipped, cut := clipLine(text)
		if !co.addMatch(prefix+relPath, contentMatch{Line: lineNo, Text: clipped, LineTruncated: cut}) {
			break
		}
	}

	_ = stdout.Close()
	waitErr := cmd.Wait()

	// git grep's exit codes: 0 = matches, 1 = no matches (a valid answer), and
	// anything higher = it could not search (not a repository, bad pathspec,
	// unreadable tree). Only the last is an error — reporting "no matches" for
	// it would tell the user their code doesn't contain the string when in fact
	// nothing was ever looked at.
	if waitErr != nil {
		if ec := exitCode(waitErr); ec > 1 || ec < 0 {
			return fmt.Errorf("git grep in %s: %w", sr.Dir, waitErr)
		}
	}
	return nil
}

// parseGitGrepLine splits `path\0lineno\0text` as produced by `git grep -z -n`.
func parseGitGrepLine(line string) (path string, lineNo int, text string, ok bool) {
	first := strings.IndexByte(line, 0)
	if first < 0 {
		return "", 0, "", false
	}
	rest := line[first+1:]
	second := strings.IndexByte(rest, 0)
	if second < 0 {
		return "", 0, "", false
	}
	n, err := strconv.Atoi(rest[:second])
	if err != nil {
		return "", 0, "", false
	}
	p := normalizeRel(line[:first])
	if p == "" {
		return "", 0, "", false
	}
	return p, n, rest[second+1:], true
}
