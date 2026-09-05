package service

// Regression tests for KB ingest of binary document formats.
//
// The upload dialog advertises ".txt/.md and PDF/Word/Excel/PowerPoint", but the
// ingest walker used to allow-list only plain-text extensions. An uploaded .docx
// was therefore written into the dataset dir and then silently skipped: the KB
// reported ready with 0 documents and no error anywhere. These tests pin both
// halves of the fix — the file is now walked (kbIngestible) and its text is
// extracted rather than indexed as raw zip bytes (Ingest).

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildDocx returns minimal but valid .docx bytes whose body contains text.
func buildDocx(t *testing.T, body string) []byte {
	t.Helper()
	doc := `<?xml version="1.0"?>` +
		`<w:document xmlns:w="x"><w:body><w:p><w:r><w:t>` + body +
		`</w:t></w:r></w:p></w:body></w:document>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte(doc)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestKBIngestible_IncludesDocumentFormats(t *testing.T) {
	cases := map[string]bool{
		"notes.md":    true,
		"data.json":   true,
		"report.pdf":  true,
		"memo.docx":   true,
		"sheet.xlsx":  true,
		"deck.pptx":   true,
		"photo.png":   false,
		"archive.zip": false,
		"binary.exe":  false,
	}
	for name, want := range cases {
		if got := kbIngestible(name); got != want {
			t.Errorf("kbIngestible(%q) = %v, want %v", name, got, want)
		}
	}
}

// An uploaded .docx must end up indexed and searchable by its *text*, not
// skipped and not indexed as zip bytes.
func TestKBIngestExtractsDocxText(t *testing.T) {
	allowLocal(t) // reading a local corpus dir is a personal-edition feature
	svc, owner := newKBTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	// A distinctive phrase that exists only inside the docx body. If the file were
	// skipped, or indexed as raw zip bytes, this search would find nothing (the
	// text is deflate-compressed inside the archive).
	const phrase = "asparagus telemetry"
	if err := os.WriteFile(filepath.Join(dir, "memo.docx"), buildDocx(t, phrase+" quarterly notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A sibling text file proves mixed corpora still work.
	writeKBFile(t, dir, "readme.md", "plain text sibling")

	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "mixed", SourceKind: "local", SourceAddr: dir,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	res, err := svc.Ingest(ctx, owner, kb.ID, IngestOptions{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected ingest errors: %v", res.Errors)
	}
	if res.FilesIngested != 2 {
		t.Fatalf("FilesIngested = %d, want 2 (docx + md); scanned=%d", res.FilesIngested, res.FilesScanned)
	}

	hits, err := svc.Search(ctx, owner, kb.ID, phrase, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("docx text is not searchable: the extracted body was never indexed")
	}
	if !strings.Contains(hits[0].RelPath, "memo.docx") {
		t.Errorf("hit came from %q, want memo.docx", hits[0].RelPath)
	}
}

// A document the extractor cannot parse must be reported as an ingest error and
// skipped — never indexed as binary noise, and never silently dropped.
func TestKBIngestReportsUnparseableDocument(t *testing.T) {
	allowLocal(t) // reading a local corpus dir is a personal-edition feature
	svc, owner := newKBTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	// .docx extension, but the bytes are not a zip at all.
	if err := os.WriteFile(filepath.Join(dir, "broken.docx"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeKBFile(t, dir, "good.md", "readable content")

	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "broken", SourceKind: "local", SourceAddr: dir,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	res, err := svc.Ingest(ctx, owner, kb.ID, IngestOptions{})
	if err != nil {
		t.Fatalf("Ingest should tolerate one bad file: %v", err)
	}
	if res.FilesIngested != 1 {
		t.Errorf("FilesIngested = %d, want 1 (only good.md)", res.FilesIngested)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "broken.docx") {
		t.Errorf("want one error naming broken.docx, got %v", res.Errors)
	}
}

// ReadDocumentText is the browse path: for a binary document it must return
// extracted text and flag it, and it must refuse to escape the KB root.
func TestReadDocumentTextExtractsAndContains(t *testing.T) {
	allowLocal(t) // reading a local corpus dir is a personal-edition feature
	svc, owner := newKBTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	const phrase = "rhubarb ledger"
	if err := os.WriteFile(filepath.Join(dir, "memo.docx"), buildDocx(t, phrase), 0o644); err != nil {
		t.Fatal(err)
	}
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "browse", SourceKind: "local", SourceAddr: dir,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	text, truncated, extracted, err := svc.ReadDocumentText(ctx, owner, kb.ID, "memo.docx", "", 0)
	if err != nil {
		t.Fatalf("ReadDocumentText: %v", err)
	}
	if !extracted {
		t.Error("extracted = false, want true for a .docx")
	}
	if truncated {
		t.Error("truncated = true for a tiny document")
	}
	if !strings.Contains(text, phrase) {
		t.Errorf("text missing %q; got %q", phrase, text)
	}

	// Traversal must fail closed, even though the target file exists.
	if _, _, _, err := svc.ReadDocumentText(ctx, owner, kb.ID, "../../etc/passwd", "", 0); err == nil {
		t.Error("expected traversal to be rejected")
	}
}

// Truncation must report the flag and must not cut a multi-byte rune in half —
// the corpora these KBs are built from are largely Chinese.
func TestReadDocumentTextTruncatesAtRuneBoundary(t *testing.T) {
	allowLocal(t) // reading a local corpus dir is a personal-edition feature
	svc, owner := newKBTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	// Each rune is 3 bytes in UTF-8, so a byte cap of 10 lands mid-rune.
	writeKBFile(t, dir, "poem.txt", strings.Repeat("字", 100))
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "cjk", SourceKind: "local", SourceAddr: dir,
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	text, truncated, _, err := svc.ReadDocumentText(ctx, owner, kb.ID, "poem.txt", "", 10)
	if err != nil {
		t.Fatalf("ReadDocumentText: %v", err)
	}
	if !truncated {
		t.Error("truncated = false, want true")
	}
	if len(text) > 10 {
		t.Errorf("text is %d bytes, want <= 10", len(text))
	}
	if !utf8ValidString(text) {
		t.Errorf("truncated text is not valid UTF-8: %q", text)
	}
	// 10 bytes / 3 bytes-per-rune = 3 whole runes.
	if want := strings.Repeat("字", 3); text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
