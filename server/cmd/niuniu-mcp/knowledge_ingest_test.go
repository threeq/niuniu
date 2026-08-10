package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestChunkText_PacksAndSplits(t *testing.T) {
	// Two small paragraphs pack into a single chunk under the budget.
	got := chunkText("alpha para one\n\nbeta para two", 1000)
	if len(got) != 1 {
		t.Fatalf("want 1 chunk, got %d: %#v", len(got), got)
	}
	if !strings.Contains(got[0], "alpha") || !strings.Contains(got[0], "beta") {
		t.Fatalf("chunk lost content: %q", got[0])
	}

	// A block larger than the budget hard-splits, never exceeding it.
	big := strings.Repeat("x", 2500)
	parts := chunkText(big, 1000)
	if len(parts) < 3 {
		t.Fatalf("want >=3 chunks for oversized block, got %d", len(parts))
	}
	for i, p := range parts {
		if len(p) > 1000 {
			t.Fatalf("chunk %d exceeds budget: %d bytes", i, len(p))
		}
	}
}

func TestChunkText_BreaksOnHeadings(t *testing.T) {
	md := "# Title\nintro line\n## Section A\nbody a\n## Section B\nbody b"
	// Tiny budget forces each heading-led block into its own chunk.
	got := chunkText(md, 20)
	if len(got) < 3 {
		t.Fatalf("expected headings to break into >=3 chunks, got %d: %#v", len(got), got)
	}
}

func TestSafeCut_RuneBoundary(t *testing.T) {
	s := strings.Repeat("世", 100) // 3 bytes each, no newlines
	cut := safeCut(s, 50)
	if cut <= 0 || cut > 50 {
		t.Fatalf("cut out of range: %d", cut)
	}
	if !utf8.ValidString(s[:cut]) {
		t.Fatalf("cut produced invalid UTF-8 at %d", cut)
	}
}

func TestIngestible(t *testing.T) {
	exts := defaultTextExts
	cases := map[string]bool{
		"/a/b/readme.md":  true,
		"/a/b/notes.txt":  true,
		"/a/b/deck.pptx":  true, // via detectFormat
		"/a/b/sheet.xlsx": true,
		"/a/b/photo.png":  false,
		"/a/b/program.go": false,
	}
	for p, want := range cases {
		if got := ingestible(p, exts); got != want {
			t.Errorf("ingestible(%q)=%v want %v", p, got, want)
		}
	}
}

func TestParseExtList(t *testing.T) {
	got := parseExtList(".md, txt,.PDF")
	for _, want := range []string{".md", ".txt", ".pdf"} {
		if !got[want] {
			t.Errorf("missing %q in %#v", want, got)
		}
	}
}

func TestReadIngestText_SkipsBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "blob.txt")
	if err := os.WriteFile(bin, []byte{0xff, 0xfe, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, ok, err := readIngestText(bin, defaultTextExts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected binary text file to be skipped (ok=false)")
	}
}

// fakeServer records memory/extract writes and serves a blackboard manifest
// that persists across requests, mimicking the niuniu server endpoints the
// ingest tool depends on.
type fakeServer struct {
	mu         sync.Mutex
	extracts   []map[string]any
	blackboard map[string]string // key -> content
}

func newFakeServer() (*fakeServer, *httptest.Server) {
	fs := &fakeServer{blackboard: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/memory/extract"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			fs.extracts = append(fs.extracts, body)
			w.Write([]byte(`{"id":1}`))
		case strings.HasSuffix(r.URL.Path, "/team/blackboard") && r.Method == http.MethodGet:
			key := r.URL.Query().Get("key")
			content, ok := fs.blackboard[key]
			if !ok {
				w.Write([]byte("null"))
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"key": key, "content": content})
		case strings.HasSuffix(r.URL.Path, "/team/blackboard") && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			fs.blackboard[body["key"]] = body["content"]
			w.Write([]byte(`{"ok":true}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	return fs, srv
}

func TestRunIngest_EndToEndAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.md"), "# Doc A\nThis is the body of document A.")
	mustWrite(t, filepath.Join(dir, "sub", "b.txt"), "plain text in a subdirectory")
	mustWrite(t, filepath.Join(dir, ".hidden", "secret.md"), "should be skipped")
	mustWrite(t, filepath.Join(dir, "image.png"), "\x89PNG\x00binary") // unsupported ext

	fs, srv := newFakeServer()
	defer srv.Close()
	api := &apiClient{base: strings.TrimRight(srv.URL, "/"), client: &http.Client{Timeout: 5 * time.Second}}

	opts := ingestOptions{Path: dir, MemType: "reference", Recursive: true, MaxChunkChars: 1200, TextExts: defaultTextExts}

	sum, err := runIngest(context.Background(), api, "7", "tester", opts)
	if err != nil {
		t.Fatalf("runIngest: %v", err)
	}
	if sum.FilesScanned != 2 { // a.md + sub/b.txt; .hidden and .png excluded
		t.Fatalf("FilesScanned=%d want 2 (summary=%+v)", sum.FilesScanned, sum)
	}
	if sum.FilesIngested != 2 || sum.ChunksWritten < 2 {
		t.Fatalf("unexpected ingest counts: %+v", sum)
	}
	if len(fs.extracts) != sum.ChunksWritten {
		t.Fatalf("server saw %d extracts, summary says %d", len(fs.extracts), sum.ChunksWritten)
	}
	// source_path must be traceable: "<abs>#<idx>".
	if sp, _ := fs.extracts[0]["source_path"].(string); !strings.Contains(sp, "#") {
		t.Fatalf("source_path not traceable: %q", sp)
	}

	// Second run: nothing changed -> everything skipped as unchanged, no new writes.
	before := len(fs.extracts)
	sum2, err := runIngest(context.Background(), api, "7", "tester", opts)
	if err != nil {
		t.Fatalf("runIngest 2: %v", err)
	}
	if sum2.FilesUnchanged != 2 || sum2.FilesIngested != 0 {
		t.Fatalf("re-run not idempotent: %+v", sum2)
	}
	if len(fs.extracts) != before {
		t.Fatalf("re-run wrote %d new extracts, want 0", len(fs.extracts)-before)
	}

	// Changing a file re-ingests only that file and flags stale chunks.
	mustWrite(t, filepath.Join(dir, "a.md"), "# Doc A v2\nCompletely new content here.")
	sum3, err := runIngest(context.Background(), api, "7", "tester", opts)
	if err != nil {
		t.Fatalf("runIngest 3: %v", err)
	}
	if sum3.FilesIngested != 1 || sum3.FilesUnchanged != 1 {
		t.Fatalf("changed-file re-run wrong: %+v", sum3)
	}
	if sum3.Note == "" {
		t.Fatalf("expected stale-chunk note on changed-file re-run")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
