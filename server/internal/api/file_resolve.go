package api

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ResolveFile resolves an agent-emitted file reference to a real
// workspace-relative path. Agent output rarely matches the workspace layout
// byte-for-byte: paths come back repo-relative (`server/web/src/lib/api.ts`
// while the file lives at `.worktrees/niuniu-main/server/web/src/lib/api.ts`),
// with the repo name glued on, missing middle segments, with the workspace
// directory baked in, or as a bare filename. Clicking such a reference in chat
// should open the intended file, not a 404.
//
// GET /workspaces/:id/resolve-file?ref=<raw reference>
//
// Resolution ladder (first hit wins, deterministic at every rung):
//  1. exact          — the ref, normalized, exists under the workspace root
//  2. worktree       — `.worktrees/<repo>/<ref>` exists for some repo
//  3. repo-prefixed  — ref starts with a repo name; strip it and re-try rung 2
//  4. ws-prefixed    — ref embeds the workspace dir name; cut through its last
//                      occurrence (handles `<…>/workspaces/942/CLAUDE.md`)
//  5. suffix         — workspace file index ends with `/`+ref (case-insensitive);
//                      fixes repo-relative AND missing-middle-segment refs
//  6. basename       — index entry's filename equals the ref's filename
//
// Rungs 5-6 rank candidates by (fewest path segments, shortest, alphabetical)
// so a repo-root README beats a deep lookalike. Response:
//
//	{"path": "<workspace-relative>", "strategy": "<rung>", "candidates": [...]}
//
// 404 when nothing matches — the client then falls back to opening the
// normalized ref as-is (same behavior as before this endpoint existed).
func (h *FileTreeHandler) ResolveFile(c *gin.Context) {
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

	raw := strings.TrimSpace(c.Query("ref"))
	if raw == "" {
		BadRequest(c, "ref parameter is required")
		return
	}

	ctx := c.Request.Context()
	ws, err := h.queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		InternalError(c, err)
		return
	}

	ref := normalizeFileRef(raw)
	if ref == "" || hasDotDotSegment(ref) {
		BadRequest(c, "invalid ref")
		return
	}

	// Rung 1: exact under the workspace root.
	if isWorkspaceFile(filepath.Join(ws.Path, filepath.FromSlash(ref))) {
		respondResolved(c, ref, "exact", nil)
		return
	}

	// Rung 2: joined under each repo worktree (sorted for determinism).
	repos := workspaceRepoNames(ws.Path)
	for _, repo := range repos {
		cand := ".worktrees/" + repo + "/" + ref
		if isWorkspaceFile(filepath.Join(ws.Path, filepath.FromSlash(cand))) {
			respondResolved(c, cand, "worktree", nil)
			return
		}
	}

	// Rung 3: ref names a repo itself ("niuniu-main/server/…") — re-anchor the
	// remainder under that repo's worktree root.
	if slash := strings.Index(ref, "/"); slash > 0 {
		first, rest := ref[:slash], ref[slash+1:]
		if containsString(repos, first) && rest != "" && !hasDotDotSegment(rest) {
			cand := ".worktrees/" + first + "/" + rest
			if isWorkspaceFile(filepath.Join(ws.Path, filepath.FromSlash(cand))) {
				respondResolved(c, cand, "repo-prefixed", nil)
				return
			}
		}
	}

	// Rung 4: the workspace's own directory name is embedded in the ref
	// ("<…>/workspaces/942/CLAUDE.md") — cut through its LAST occurrence.
	if base := filepath.Base(filepath.Clean(ws.Path)); base != "" && base != "." && base != string(filepath.Separator) {
		needle := "/" + base + "/"
		if idx := strings.LastIndex("/"+ref, needle); idx >= 0 {
			// idx is the slash BEFORE the ws-name segment (offset by the
			// prepended "/"); the remainder starts after "<slash><name>/".
			rest := ("/" + ref)[idx+len(needle):]
			if rest != "" && !hasDotDotSegment(rest) {
				if isWorkspaceFile(filepath.Join(ws.Path, filepath.FromSlash(rest))) {
					respondResolved(c, rest, "ws-prefixed", nil)
					return
				}
			}
		}
	}

	// Rungs 5-6 need the workspace file index. If enumeration fails (broken
	// git, unreadable dir) we report no-match rather than erroring: the client
	// falls back to opening the normalized ref as-is.
	index, indexErr := listGitFiles(ws.Path)
	if indexErr != nil {
		NotFound(c, "file")
		return
	}

	// Rung 5: suffix match — full index path ends with "/"+ref. Also catches
	// refs with missing middle segments ("src/lib/api.ts" ⊂
	// ".worktrees/niuniu-main/server/web/src/lib/api.ts").
	refLower := strings.ToLower(ref)
	var suffixHits []fileEntry
	for _, e := range index {
		if e.IsDir {
			continue
		}
		p := strings.ToLower(e.Path)
		if p == refLower || strings.HasSuffix(p, "/"+refLower) {
			suffixHits = append(suffixHits, e)
		}
	}
	if len(suffixHits) > 0 {
		sortFileEntryCandidates(suffixHits)
		respondResolved(c, suffixHits[0].Path, "suffix", candidatePaths(suffixHits))
		return
	}

	// Rung 6: bare-filename match ("chat-file-links.ts").
	if slash := strings.LastIndex(ref, "/"); slash >= 0 {
		baseLower := refLower[slash+1:]
		var nameHits []fileEntry
		for _, e := range index {
			if e.IsDir {
				continue
			}
			if strings.ToLower(e.Name) == baseLower {
				nameHits = append(nameHits, e)
			}
		}
		if len(nameHits) > 0 {
			sortFileEntryCandidates(nameHits)
			respondResolved(c, nameHits[0].Path, "basename", candidatePaths(nameHits))
			return
		}
	}

	NotFound(c, "file")
}

// normalizeFileRef canonicalizes a raw reference for comparison: backslashes
// to forward slashes, no leading "./" or "/", no surrounding whitespace, and
// a trailing `:NNN` line suffix stripped (client usually sends it split off,
// but the endpoint accepts the raw form too). Returns "" when nothing remains.
func normalizeFileRef(raw string) string {
	p := strings.TrimSpace(raw)
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	if m := refLineSuffix.FindStringSubmatch(p); m != nil && m[1] != "" {
		p = m[1]
	}
	return strings.TrimSpace(p)
}

// refLineSuffix matches a trailing `:NNN` line-number suffix.
var refLineSuffix = regexp.MustCompile(`^(.*):(\d{1,6})$`)

// hasDotDotSegment reports whether any path segment escapes the workspace —
// the resolver only ever returns workspace-relative paths.
func hasDotDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// isWorkspaceFile stats a full path and reports a readable regular file.
func isWorkspaceFile(full string) bool {
	info, err := os.Stat(full)
	return err == nil && !info.IsDir()
}

// workspaceRepoNames lists `.worktrees/<name>` directory names, sorted.
func workspaceRepoNames(wsPath string) []string {
	entries, err := os.ReadDir(filepath.Join(wsPath, ".worktrees"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// sortFileEntryCandidates orders by fewest segments, then alphabetical — the
// repo-root README should beat `.worktrees/x/docs/README.md`, and among
// structurally identical hits the first repo name wins deterministically.
func sortFileEntryCandidates(entries []fileEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		si, sj := strings.Count(entries[i].Path, "/"), strings.Count(entries[j].Path, "/")
		if si != sj {
			return si < sj
		}
		return entries[i].Path < entries[j].Path
	})
}

func candidatePaths(entries []fileEntry) []string {
	out := make([]string, 0, len(entries))
	for i, e := range entries {
		if i >= 5 {
			break
		}
		out = append(out, e.Path)
	}
	return out
}

func respondResolved(c *gin.Context, path, strategy string, candidates []string) {
	if candidates == nil {
		candidates = []string{}
	}
	c.JSON(http.StatusOK, gin.H{
		"path":       path,
		"strategy":   strategy,
		"candidates": candidates,
	})
}
