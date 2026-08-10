package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestCapBytes_TrimsPartialTrailingRune guards the byte-cap against slicing a
// multibyte rune in half on a long newline-free segment (e.g. a wide CJK row).
func TestCapBytes_TrimsPartialTrailingRune(t *testing.T) {
	s := strings.Repeat("中", 200) // 600 bytes, 3 bytes/rune, no newline
	out := capBytes(s, 100)        // 100 is not a multiple of 3 -> cut lands mid-rune
	if !utf8.ValidString(out) {
		t.Fatalf("capBytes produced invalid UTF-8 at the cut boundary: %q", out)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation hint, got %q", out)
	}
}

// toolResultText concatenates the text content of an MCP tool result.
func toolResultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := mcp.AsTextContent(c); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func toolResultIsError(res *mcp.CallToolResult) bool { return res.IsError }

func TestRenderExtractWithLabels(t *testing.T) {
	res := &extractResult{
		Format: "pptx", Unit: "slide", UnitCount: 2,
		Segments: []docSegment{
			{Label: "slide 1", Text: "alpha\nbeta"},
			{Label: "slide 2", Text: "gamma"},
		},
	}
	out := renderExtract("/tmp/d.pptx", res, "", defaultMaxBytes)
	if !strings.Contains(out, "[pptx · 2 slide(s) · /tmp/d.pptx]") {
		t.Errorf("missing header; got:\n%s", out)
	}
	if !strings.Contains(out, "===== slide 1 =====") || !strings.Contains(out, "===== slide 2 =====") {
		t.Errorf("missing slide headers; got:\n%s", out)
	}
	if !strings.Contains(out, "gamma") {
		t.Errorf("missing slide 2 text; got:\n%s", out)
	}
}

func TestRenderExtractSearchFilter(t *testing.T) {
	res := &extractResult{
		Format: "docx", Unit: "paragraph", UnitCount: 3,
		Segments: []docSegment{{Label: "", Text: "apple pie\nbanana split\napple tart"}},
	}
	out := renderExtract("/tmp/d.docx", res, "apple", defaultMaxBytes)
	if strings.Contains(out, "banana") {
		t.Errorf("search should have dropped non-matching line; got:\n%s", out)
	}
	if !strings.Contains(out, "apple pie") || !strings.Contains(out, "apple tart") {
		t.Errorf("search dropped matching lines; got:\n%s", out)
	}
}

func TestRenderExtractSearchNoMatch(t *testing.T) {
	res := &extractResult{
		Format: "docx", Unit: "paragraph", UnitCount: 1,
		Segments: []docSegment{{Label: "", Text: "nothing here"}},
	}
	out := renderExtract("/tmp/d.docx", res, "zzz", defaultMaxBytes)
	if !strings.Contains(out, "No lines matching") {
		t.Errorf("expected no-match note; got:\n%s", out)
	}
}

func TestCapBytes(t *testing.T) {
	long := strings.Repeat("line of text\n", 100) // ~1300 bytes
	out := capBytes(long, 200)
	if len(out) > 400 {
		t.Errorf("cap not applied, len=%d", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("missing truncation hint; got:\n%s", out)
	}

	short := "tiny"
	if capBytes(short, 200) != short {
		t.Errorf("short text should be unchanged")
	}
}

func TestDocumentFallbackEnvelope(t *testing.T) {
	res := documentFallback("/tmp/x.rtf", "", "unsupported_format", "no handler")
	text := toolResultText(t, res)
	var env map[string]any
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, text)
	}
	if env["fallback"] != "read" {
		t.Errorf("fallback = %v, want read", env["fallback"])
	}
	if env["reason"] != "unsupported_format" {
		t.Errorf("reason = %v", env["reason"])
	}
	if env["format"] != "unknown" {
		t.Errorf("empty format should normalize to 'unknown', got %v", env["format"])
	}
	msg, _ := env["message"].(string)
	if !strings.Contains(msg, "Read tool") || !strings.Contains(msg, "Do NOT call this MCP tool again") {
		t.Errorf("message must steer to Read and warn against retry (loop-prevention); got: %q", msg)
	}
}

func TestReadDocFileMissing(t *testing.T) {
	_, _, errRes := readDocFile(filepath.Join(t.TempDir(), "nope.pdf"))
	if errRes == nil {
		t.Fatal("expected MCP error result for missing file")
	}
	if !toolResultIsError(errRes) {
		t.Fatal("missing file should be an MCP error, not a fallback envelope")
	}
}

func TestReadDocFileDirectory(t *testing.T) {
	_, _, errRes := readDocFile(t.TempDir())
	if errRes == nil || !toolResultIsError(errRes) {
		t.Fatal("directory should be an MCP error")
	}
}

func TestDocumentInfoXlsx(t *testing.T) {
	workbook := `<workbook xmlns="x" xmlns:r="rns"><sheets>` +
		`<sheet name="Alpha" sheetId="1" r:id="rId1"/>` +
		`<sheet name="Beta" sheetId="2" r:id="rId2"/></sheets></workbook>`
	rels := `<Relationships xmlns="rels">` +
		`<Relationship Id="rId1" Target="worksheets/sheet1.xml"/>` +
		`<Relationship Id="rId2" Target="worksheets/sheet2.xml"/></Relationships>`
	data := buildZip(t, map[string]string{
		"xl/workbook.xml":            workbook,
		"xl/_rels/workbook.xml.rels": rels,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="x"><sheetData/></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet xmlns="x"><sheetData/></worksheet>`,
	})
	info, err := documentInfo("/tmp/wb.xlsx", data)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.Unit != "sheet" || info.UnitCount != 2 {
		t.Fatalf("unit=%q count=%d, want sheet/2", info.Unit, info.UnitCount)
	}
	if len(info.Labels) != 2 || info.Labels[0] != "Alpha" || info.Labels[1] != "Beta" {
		t.Fatalf("labels = %v, want [Alpha Beta]", info.Labels)
	}
}

func TestDocumentInfoPptx(t *testing.T) {
	slide := `<p:sld xmlns:a="a"><a:p><a:t>x</a:t></a:p></p:sld>`
	data := buildZip(t, map[string]string{
		"ppt/slides/slide1.xml": slide,
		"ppt/slides/slide2.xml": slide,
	})
	info, err := documentInfo("/tmp/d.pptx", data)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.Unit != "slide" || info.UnitCount != 2 {
		t.Fatalf("unit=%q count=%d, want slide/2", info.Unit, info.UnitCount)
	}
}

// TestReadDocFileRoundTrip writes a real .docx to disk and runs the full
// read path (stat -> read -> extract -> render).
func TestReadDocFileRoundTrip(t *testing.T) {
	doc := `<w:document xmlns:w="x"><w:body><w:p><w:r><w:t>Roundtrip OK</w:t></w:r></w:p></w:body></w:document>`
	data := buildZip(t, map[string]string{"word/document.xml": doc})
	p := filepath.Join(t.TempDir(), "sample.docx")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, format, errRes := readDocFile(p)
	if errRes != nil {
		t.Fatalf("readDocFile error result: %s", toolResultText(t, errRes))
	}
	if format != "docx" {
		t.Fatalf("format = %q", format)
	}
	res, err := extractDocument(p, got, nil)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	out := renderExtract(p, res, "", defaultMaxBytes)
	if !strings.Contains(out, "Roundtrip OK") {
		t.Errorf("missing extracted text; got:\n%s", out)
	}
}
