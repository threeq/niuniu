package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// buildZip assembles an in-memory OOXML container from a name->content map.
func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func asFallback(t *testing.T, err error) *extractFallback {
	t.Helper()
	var fb *extractFallback
	if !errors.As(err, &fb) {
		t.Fatalf("expected *extractFallback, got %T: %v", err, err)
	}
	return fb
}

func TestParsePageSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		max     int
		want    []int
		wantErr bool
	}{
		{"empty means all", "", 10, nil, false},
		{"single", "3", 10, []int{3}, false},
		{"range", "2-4", 10, []int{2, 3, 4}, false},
		{"mixed", "1,3-5,8", 10, []int{1, 3, 4, 5, 8}, false},
		{"dedup and sort", "5,1,5,2-3", 10, []int{1, 2, 3, 5}, false},
		{"reversed range", "5-3", 10, []int{3, 4, 5}, false},
		{"clamp to max", "8-12", 10, []int{8, 9, 10}, false},
		{"all out of range", "20-30", 10, nil, true},
		{"garbage", "abc", 10, nil, true},
		{"bad range", "1-x", 10, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePageSpec(tc.spec, tc.max)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !equalInts(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDetectFormat(t *testing.T) {
	cases := map[string]string{
		"/a/b/c.pdf":  "pdf",
		"/a/b/c.PDF":  "pdf",
		"/x.docx":     "docx",
		"/x.xlsx":     "xlsx",
		"/x.pptx":     "pptx",
		"/x.txt":      "",
		"/x.rtf":      "",
		"/no-ext":     "",
		"/x.doc":      "", // legacy binary formats are not handled
	}
	for path, want := range cases {
		if got := detectFormat(path); got != want {
			t.Errorf("detectFormat(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestExtractDocx(t *testing.T) {
	doc := `<?xml version="1.0"?>
<w:document xmlns:w="x"><w:body>
<w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t> World</w:t></w:r></w:p>
<w:p><w:r><w:t>Second</w:t><w:tab/><w:t>line</w:t></w:r></w:p>
</w:body></w:document>`
	data := buildZip(t, map[string]string{"word/document.xml": doc})

	res, err := extractDocument("/tmp/file.docx", data, nil)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "docx" {
		t.Fatalf("format = %q", res.Format)
	}
	if len(res.Segments) != 1 {
		t.Fatalf("want 1 segment, got %d", len(res.Segments))
	}
	text := res.Segments[0].Text
	if !strings.Contains(text, "Hello World") {
		t.Errorf("missing joined run text; got:\n%s", text)
	}
	if !strings.Contains(text, "Second\tline") {
		t.Errorf("missing tab between runs; got:\n%s", text)
	}
}

func TestExtractDocxEmpty(t *testing.T) {
	doc := `<w:document xmlns:w="x"><w:body><w:p/></w:body></w:document>`
	data := buildZip(t, map[string]string{"word/document.xml": doc})
	_, err := extractDocument("/tmp/file.docx", data, nil)
	fb := asFallback(t, err)
	if fb.Reason != "empty_result" {
		t.Fatalf("reason = %q, want empty_result", fb.Reason)
	}
}

func TestExtractDocxMissingPart(t *testing.T) {
	data := buildZip(t, map[string]string{"docProps/core.xml": "<x/>"})
	_, err := extractDocument("/tmp/file.docx", data, nil)
	fb := asFallback(t, err)
	if fb.Reason != "parse_error" {
		t.Fatalf("reason = %q, want parse_error", fb.Reason)
	}
}

func TestExtractXlsx(t *testing.T) {
	shared := `<sst xmlns="x"><si><t>Name</t></si><si><t>Age</t></si><si><t>Alice</t></si></sst>`
	sheet := `<worksheet xmlns="x"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>30</v></c></row>
</sheetData></worksheet>`
	workbook := `<workbook xmlns="x" xmlns:r="rns"><sheets><sheet name="People" sheetId="1" r:id="rId1"/></sheets></workbook>`
	rels := `<Relationships xmlns="rels"><Relationship Id="rId1" Type="t" Target="worksheets/sheet1.xml"/></Relationships>`
	data := buildZip(t, map[string]string{
		"xl/sharedStrings.xml":       shared,
		"xl/worksheets/sheet1.xml":   sheet,
		"xl/workbook.xml":            workbook,
		"xl/_rels/workbook.xml.rels": rels,
	})

	res, err := extractDocument("/tmp/file.xlsx", data, nil)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(res.Segments) != 1 {
		t.Fatalf("want 1 sheet, got %d", len(res.Segments))
	}
	if res.Segments[0].Label != "sheet People" {
		t.Errorf("sheet label = %q, want 'sheet People'", res.Segments[0].Label)
	}
	text := res.Segments[0].Text
	if !strings.Contains(text, "Name\tAge") {
		t.Errorf("header row missing; got:\n%s", text)
	}
	if !strings.Contains(text, "Alice\t30") {
		t.Errorf("data row (shared string + number) missing; got:\n%s", text)
	}
}

func TestExtractXlsxInlineString(t *testing.T) {
	sheet := `<worksheet xmlns="x"><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>Inline</t></is></c></row>
</sheetData></worksheet>`
	// No workbook/rels -> exercises the filename-order fallback path.
	data := buildZip(t, map[string]string{"xl/worksheets/sheet1.xml": sheet})
	res, err := extractDocument("/tmp/file.xlsx", data, nil)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(res.Segments[0].Text, "Inline") {
		t.Errorf("inline string missing; got:\n%s", res.Segments[0].Text)
	}
}

func TestExtractPptx(t *testing.T) {
	slide := func(s string) string {
		return `<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree>
<a:p><a:r><a:t>` + s + `</a:t></a:r></a:p></p:spTree></p:cSld></p:sld>`
	}
	data := buildZip(t, map[string]string{
		"ppt/slides/slide1.xml":  slide("First slide"),
		"ppt/slides/slide2.xml":  slide("Second slide"),
		"ppt/slides/slide10.xml": slide("Tenth slide"),
	})

	res, err := extractDocument("/tmp/deck.pptx", data, nil)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.UnitCount != 3 {
		t.Fatalf("unit count = %d, want 3", res.UnitCount)
	}
	// Numeric ordering: slide2 must come before slide10 (not lexical).
	if res.Segments[0].Label != "slide 1" || res.Segments[1].Label != "slide 2" || res.Segments[2].Label != "slide 3" {
		t.Fatalf("slide ordering wrong: %q %q %q", res.Segments[0].Label, res.Segments[1].Label, res.Segments[2].Label)
	}
	if !strings.Contains(res.Segments[2].Text, "Tenth slide") {
		t.Errorf("slide10 content missing; got:\n%s", res.Segments[2].Text)
	}
}

func TestExtractPptxPageFilter(t *testing.T) {
	slide := func(s string) string {
		return `<p:sld xmlns:a="a"><a:p><a:t>` + s + `</a:t></a:p></p:sld>`
	}
	data := buildZip(t, map[string]string{
		"ppt/slides/slide1.xml": slide("one"),
		"ppt/slides/slide2.xml": slide("two"),
		"ppt/slides/slide3.xml": slide("three"),
	})
	res, err := extractDocument("/tmp/deck.pptx", data, []int{2})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(res.Segments) != 1 || res.Segments[0].Label != "slide 2" {
		t.Fatalf("page filter failed: %+v", res.Segments)
	}
	if !strings.Contains(res.Segments[0].Text, "two") {
		t.Errorf("filtered slide content wrong: %q", res.Segments[0].Text)
	}
}

func TestExtractUnsupportedFormat(t *testing.T) {
	_, err := extractDocument("/tmp/file.rtf", []byte("whatever"), nil)
	fb := asFallback(t, err)
	if fb.Reason != "unsupported_format" {
		t.Fatalf("reason = %q, want unsupported_format", fb.Reason)
	}
}

func TestExtractCorruptZip(t *testing.T) {
	// .docx extension but the bytes are not a zip at all.
	_, err := extractDocument("/tmp/file.docx", []byte("not a zip"), nil)
	fb := asFallback(t, err)
	if fb.Reason != "parse_error" {
		t.Fatalf("reason = %q, want parse_error", fb.Reason)
	}
}

func TestExtractPDFCorrupt(t *testing.T) {
	// Garbage with a .pdf extension must yield a clean fallback, never a panic.
	_, err := extractDocument("/tmp/file.pdf", []byte("%PDF-1.4\nnot really a pdf"), nil)
	fb := asFallback(t, err)
	if fb.Format != "pdf" {
		t.Fatalf("format = %q, want pdf", fb.Format)
	}
	if fb.Reason != "parse_error" && fb.Reason != "empty_result" {
		t.Fatalf("reason = %q, want parse_error or empty_result", fb.Reason)
	}
}

func TestExtractPDFRealFile(t *testing.T) {
	// A minimal single-page PDF with an uncompressed text stream. The point is
	// robustness: extraction must not panic and must return either text or a
	// structured fallback (some standard-14-font PDFs yield no width tables).
	pdfBytes := minimalPDF()
	res, err := extractDocument("/tmp/real.pdf", pdfBytes, nil)
	if err != nil {
		fb := asFallback(t, err)
		if fb.Format != "pdf" {
			t.Fatalf("fallback format = %q, want pdf", fb.Format)
		}
		return // acceptable: clean fallback, no crash
	}
	if res.Format != "pdf" || res.UnitCount < 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// minimalPDF builds a tiny but structurally valid one-page PDF with a text
// object drawing "Hello PDF".
func minimalPDF() []byte {
	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>",
		"", // object 4 is the stream, built below
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
	}
	stream := "BT /F1 24 Tf 72 720 Td (Hello PDF) Tj ET"
	objs[3] = "<</Length " + itoa(len(stream)) + ">>\nstream\n" + stream + "\nendstream"

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, body := range objs {
		offsets[i+1] = buf.Len()
		buf.WriteString(itoa(i+1) + " 0 obj\n" + body + "\nendobj\n")
	}
	xrefPos := buf.Len()
	buf.WriteString("xref\n0 " + itoa(len(objs)+1) + "\n")
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		buf.WriteString(pad10(offsets[i]) + " 00000 n \n")
	}
	buf.WriteString("trailer\n<</Size " + itoa(len(objs)+1) + "/Root 1 0 R>>\n")
	buf.WriteString("startxref\n" + itoa(xrefPos) + "\n%%EOF")
	return buf.Bytes()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func pad10(n int) string {
	s := itoa(n)
	for len(s) < 10 {
		s = "0" + s
	}
	return s
}
