package main

// Local knowledge-base ingestion (directory -> slice -> memory RAG).
//
// `ingest_directory` walks a LOCAL directory, extracts text from each supported
// file (plain text / Markdown directly, and PDF / Word / Excel / PowerPoint via
// the same pure-Go extractor that powers read_document), slices the text into
// retrieval-sized chunks, and writes each chunk as a durable memory through the
// existing /mcp/.../memory/extract endpoint. Retrieval reuses memory_search —
// no new index, no embeddings, nothing leaves the machine. This is the
// "local-first" differentiator: the whole pipeline is filesystem + SQLite.
//
// Re-running on the same directory is idempotent for unchanged files: a content
// hash per file is stored on the shared blackboard (reusing blackboard_* as the
// RAG bookkeeping store), so a second pass skips files whose bytes did not
// change. Changed files get fresh chunks appended; their stale chunks are NOT
// auto-removed in this iteration (the MCP token cannot delete memories) — note
// printed in the summary. FTS / vector retrieval is a documented follow-up;
// memory_search is literal case-insensitive substring matching today.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// defaultChunkChars is the target slice size. ~1200 bytes keeps a chunk small
// enough to be a precise memory_search hit yet large enough to carry context.
const defaultChunkChars = 1200

// maxChunksPerRun bounds a single ingest so a huge tree cannot spawn an
// unbounded number of memory writes (and HTTP round-trips) in one call.
const maxChunksPerRun = 10000

// defaultTextExts are the extensions read as UTF-8 text directly. Office/PDF
// formats are handled separately via detectFormat + extractDocument.
var defaultTextExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".mdx": true,
	".rst": true, ".text": true, ".csv": true, ".tsv": true,
	".log": true, ".json": true, ".yaml": true, ".yml": true,
}

// registerKnowledgeTools wires the ingest_directory tool. Workspace-scoped:
// owner is derived server-side from the workspace token, exactly like the
// memory_* and blackboard_* tools it builds on.
func registerKnowledgeTools(s *server.MCPServer, api *apiClient, wsid, agentName string) {
	s.AddTool(
		mcp.NewTool("ingest_directory",
			mcp.WithDescription("Ingest a LOCAL directory (or single file) into the durable memory store for RAG: "+
				"scan -> extract text (.txt/.md and PDF/Word/Excel/PowerPoint) -> slice into chunks -> write each chunk as a memory. "+
				"Retrieve later with memory_search. Nothing leaves the machine. "+
				"Idempotent on re-run: files whose content is unchanged are skipped (hash tracked on the blackboard)."),
			mcp.WithString("path", mcp.Description("Absolute path to a local directory (or a single file) to ingest"), mcp.Required()),
			mcp.WithString("mem_type", mcp.Description("Memory type for the chunks: pattern, gotcha, decision, error_fix, note, or reference (default reference)")),
			mcp.WithBoolean("recursive", mcp.Description("Recurse into subdirectories (default true)")),
			mcp.WithBoolean("force", mcp.Description("Re-ingest even files whose content is unchanged since the last run (default false)")),
			mcp.WithNumber("max_chunk_chars", mcp.Description("Target chunk size in bytes (default 1200)")),
			mcp.WithString("extensions", mcp.Description("Comma-separated extension allow-list (e.g. \".md,.txt\") overriding the default text set; PDF/Office formats are always eligible")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			path, errRes := requireString(args, "path")
			if errRes != nil {
				return errRes, nil
			}
			opts := ingestOptions{
				Path:          path,
				MemType:       "reference",
				Recursive:     true,
				MaxChunkChars: defaultChunkChars,
				TextExts:      defaultTextExts,
			}
			if v, ok := args["mem_type"].(string); ok && strings.TrimSpace(v) != "" {
				opts.MemType = strings.TrimSpace(v)
			}
			if v, ok := args["recursive"].(bool); ok {
				opts.Recursive = v
			}
			if v, ok := args["force"].(bool); ok {
				opts.Force = v
			}
			if v, ok := args["max_chunk_chars"].(float64); ok && int(v) > 0 {
				opts.MaxChunkChars = int(v)
			}
			if v, ok := args["extensions"].(string); ok && strings.TrimSpace(v) != "" {
				opts.TextExts = parseExtList(v)
			}

			summary, err := runIngest(ctx, api, wsid, agentName, opts)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out, _ := json.MarshalIndent(summary, "", "  ")
			return mcp.NewToolResultText(string(out)), nil
		},
	)
}

// ingestOptions is the resolved configuration for one ingest run.
type ingestOptions struct {
	Path          string
	MemType       string
	Recursive     bool
	Force         bool
	MaxChunkChars int
	TextExts      map[string]bool
}

// ingestSummary is the JSON result reported to the agent.
type ingestSummary struct {
	Root           string   `json:"root"`
	FilesScanned   int      `json:"files_scanned"`
	FilesIngested  int      `json:"files_ingested"`
	FilesUnchanged int      `json:"files_skipped_unchanged"`
	FilesSkipped   int      `json:"files_skipped_unsupported"`
	ChunksWritten  int      `json:"chunks_written"`
	Errors         []string `json:"errors,omitempty"`
	Note           string   `json:"note,omitempty"`
	Truncated      bool     `json:"truncated,omitempty"`
}

// ingestManifest is the per-directory bookkeeping stored on the blackboard:
// relative path -> content hash of what was last ingested.
type ingestManifest struct {
	Root  string            `json:"root"`
	Files map[string]string `json:"files"`
}

// fileEntry pairs an absolute path with its path relative to the ingest root
// (used for titles, source_path, and manifest keys).
type fileEntry struct {
	Abs string
	Rel string
}

// runIngest is the testable core: gather -> (skip unchanged) -> extract ->
// chunk -> write memories -> persist manifest. It never aborts on a single
// file error; per-file failures are collected into the summary.
func runIngest(ctx context.Context, api *apiClient, wsid, agentName string, opts ingestOptions) (ingestSummary, error) {
	if opts.MaxChunkChars <= 0 {
		opts.MaxChunkChars = defaultChunkChars
	}
	if opts.TextExts == nil {
		opts.TextExts = defaultTextExts
	}
	if agentName == "" {
		agentName = "knowledge-ingest"
	}

	root, err := filepath.Abs(opts.Path)
	if err != nil {
		return ingestSummary{}, fmt.Errorf("resolve path: %w", err)
	}
	st, err := os.Stat(root)
	if err != nil {
		return ingestSummary{}, fmt.Errorf("cannot stat %q: %w", root, err)
	}

	var files []fileEntry
	manifestKey := "kb-manifest:" + root
	if st.IsDir() {
		files, err = gatherFiles(root, opts.Recursive, opts.TextExts)
		if err != nil {
			return ingestSummary{}, err
		}
	} else {
		// Single file: manifest keyed by the parent dir so re-ingesting the
		// containing directory later reuses the same bookkeeping namespace.
		manifestKey = "kb-manifest:" + filepath.Dir(root)
		if ingestible(root, opts.TextExts) {
			files = []fileEntry{{Abs: root, Rel: filepath.Base(root)}}
		}
	}

	summary := ingestSummary{Root: root}
	manifest := loadManifest(ctx, api, wsid, manifestKey)
	if manifest.Files == nil {
		manifest.Files = map[string]string{}
	}
	manifest.Root = root

	changedStale := false
	for _, fe := range files {
		summary.FilesScanned++
		text, ok, ferr := readIngestText(fe.Abs, opts.TextExts)
		if ferr != nil {
			summary.Errors = append(summary.Errors, fmt.Sprintf("%s: %v", fe.Rel, ferr))
			continue
		}
		if !ok || strings.TrimSpace(text) == "" {
			summary.FilesSkipped++
			continue
		}
		sum := sha256.Sum256([]byte(text))
		hash := hex.EncodeToString(sum[:])
		if !opts.Force && manifest.Files[fe.Rel] == hash {
			summary.FilesUnchanged++
			continue
		}
		if !opts.Force && manifest.Files[fe.Rel] != "" {
			changedStale = true // a prior version's chunks now linger
		}

		chunks := chunkText(text, opts.MaxChunkChars)
		wrote := 0
		for i, chunk := range chunks {
			if summary.ChunksWritten >= maxChunksPerRun {
				summary.Truncated = true
				break
			}
			title := fe.Rel
			if len(chunks) > 1 {
				title = fmt.Sprintf("%s [%d/%d]", fe.Rel, i+1, len(chunks))
			}
			body := map[string]any{
				"title":       capRunes(title, 200),
				"content":     chunk,
				"source_path": fmt.Sprintf("%s#%d", fe.Abs, i+1),
				"mem_type":    opts.MemType,
			}
			if _, err := api.post("/mcp/workspaces/"+wsid+"/memory/extract", body); err != nil {
				summary.Errors = append(summary.Errors, fmt.Sprintf("%s chunk %d: %v", fe.Rel, i+1, err))
				break
			}
			wrote++
			summary.ChunksWritten++
		}
		if wrote > 0 {
			summary.FilesIngested++
			manifest.Files[fe.Rel] = hash
		}
		if summary.Truncated {
			break
		}
	}

	// Persist the manifest so the next run can skip unchanged files. A write
	// failure is non-fatal: ingestion already succeeded, only idempotency on
	// the next run is affected — surface it as a soft error.
	if err := saveManifest(ctx, api, wsid, agentName, manifestKey, manifest); err != nil {
		summary.Errors = append(summary.Errors, "manifest save failed (re-runs may duplicate): "+err.Error())
	}

	if changedStale {
		summary.Note = "Some changed files were re-ingested; their previous chunks remain in memory (MCP cannot delete). Run memory_consolidate or curate manually if duplicates matter."
	}
	return summary, nil
}

// gatherFiles walks root collecting ingestible files. Hidden entries (names
// starting with ".") are skipped so .git, .venv, etc. never get ingested.
func gatherFiles(root string, recursive bool, textExts map[string]bool) ([]fileEntry, error) {
	var out []fileEntry
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than aborting the whole walk
		}
		name := d.Name()
		if d.IsDir() {
			if p == root {
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if !recursive {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if !ingestible(p, textExts) {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			rel = name
		}
		out = append(out, fileEntry{Abs: p, Rel: filepath.ToSlash(rel)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// ingestible reports whether a file is eligible: a known Office/PDF format or a
// text extension in the allow-list.
func ingestible(path string, textExts map[string]bool) bool {
	if detectFormat(path) != "" {
		return true
	}
	return textExts[strings.ToLower(filepath.Ext(path))]
}

// readIngestText returns the extractable text for a file. ok=false means the
// file is recognized but yielded nothing usable (binary text file, empty doc),
// which the caller counts as skipped rather than an error.
func readIngestText(path string, textExts map[string]bool) (string, bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", false, err
	}
	if st.Size() > maxInputFileBytes {
		return "", false, fmt.Errorf("file exceeds %d bytes; skipped", int64(maxInputFileBytes))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	if detectFormat(path) != "" {
		res, derr := extractDocument(path, data, nil)
		if derr != nil {
			if _, ok := derr.(*extractFallback); ok {
				// Scanned PDF / unparsable Office file: not a hard error, just skip.
				return "", false, nil
			}
			return "", false, derr
		}
		return joinSegments(res.Segments), true, nil
	}
	if !utf8.Valid(data) {
		return "", false, nil // binary masquerading as a text extension
	}
	return string(data), true, nil
}

// joinSegments flattens extracted document segments, prefixing each labeled
// unit (page/slide/sheet) so provenance survives into the chunk text.
func joinSegments(segs []docSegment) string {
	var parts []string
	for _, s := range segs {
		t := strings.TrimSpace(s.Text)
		if t == "" {
			continue
		}
		if s.Label != "" {
			parts = append(parts, "["+s.Label+"]\n"+t)
		} else {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// chunkText slices text into <=maxChars pieces, structure-aware: it breaks on
// blank lines and before Markdown ATX headings, packs the resulting blocks up
// to the size budget, and hard-splits any single block that is itself oversized
// (on a rune boundary, never mid-character).
func chunkText(text string, maxChars int) []string {
	if maxChars <= 0 {
		maxChars = defaultChunkChars
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")

	// 1. Split into logical blocks.
	var blocks []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		b := strings.TrimRight(strings.Join(cur, "\n"), "\n")
		if strings.TrimSpace(b) != "" {
			blocks = append(blocks, b)
		}
		cur = cur[:0]
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if isHeading(line) && len(cur) > 0 {
			flush()
		}
		cur = append(cur, line)
	}
	flush()

	// 2. Pack blocks into chunks, hard-splitting oversized blocks.
	var chunks []string
	var buf strings.Builder
	addChunk := func() {
		if s := strings.TrimSpace(buf.String()); s != "" {
			chunks = append(chunks, s)
		}
		buf.Reset()
	}
	for _, b := range blocks {
		for len(b) > maxChars {
			cut := safeCut(b, maxChars)
			addChunk()
			if s := strings.TrimSpace(b[:cut]); s != "" {
				chunks = append(chunks, s)
			}
			b = b[cut:]
		}
		if buf.Len() > 0 && buf.Len()+len(b)+2 > maxChars {
			addChunk()
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(b)
	}
	addChunk()
	return chunks
}

// isHeading reports whether a line is a Markdown ATX heading (#, ##, ...).
func isHeading(line string) bool {
	t := strings.TrimLeft(line, " ")
	return strings.HasPrefix(t, "#")
}

// safeCut picks a byte offset <= max to split s on: a newline in the back half
// if available, otherwise the nearest preceding rune boundary so we never emit
// invalid UTF-8.
func safeCut(s string, max int) int {
	if max >= len(s) {
		return len(s)
	}
	if nl := strings.LastIndexByte(s[:max], '\n'); nl > max/2 {
		return nl + 1
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		return max
	}
	return cut
}

// capRunes truncates s to at most n runes (memory titles stay tidy).
func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// parseExtList turns ".md, txt,.PDF" into a normalized {".md":true, ".txt":true,
// ".pdf":true} set.
func parseExtList(spec string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(spec, ",") {
		e := strings.ToLower(strings.TrimSpace(part))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		out[e] = true
	}
	return out
}

// --- blackboard-backed manifest ---

// blackboardEntry is the subset of the blackboard read response we need.
type blackboardEntry struct {
	Content string `json:"content"`
}

// loadManifest reads the manifest from the blackboard. Any miss (not found,
// empty, malformed) yields an empty manifest — a fresh ingest.
func loadManifest(ctx context.Context, api *apiClient, wsid, key string) ingestManifest {
	var m ingestManifest
	data, err := api.get("/mcp/workspaces/" + wsid + "/team/blackboard?key=" + url.QueryEscape(key))
	if err != nil || len(data) == 0 || string(data) == "null" {
		return m
	}
	var entry blackboardEntry
	if err := json.Unmarshal(data, &entry); err != nil || entry.Content == "" {
		return m
	}
	_ = json.Unmarshal([]byte(entry.Content), &m)
	return m
}

// saveManifest persists the manifest as a blackboard "status" entry.
func saveManifest(ctx context.Context, api *apiClient, wsid, agentName, key string, m ingestManifest) error {
	content, err := json.Marshal(m)
	if err != nil {
		return err
	}
	body := map[string]string{
		"key":      key,
		"type":     "status",
		"content":  string(content),
		"producer": agentName,
	}
	_, err = api.post("/mcp/workspaces/"+wsid+"/team/blackboard", body)
	return err
}
