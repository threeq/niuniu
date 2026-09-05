package docextract

// Exported surface of the extractor. The format-specific machinery in
// extract.go stays unexported (it was written as one file's worth of internals);
// this file is the stable contract its consumers depend on.
//
// Two shapes of consumer:
//   - cmd/niuniu-mcp needs the structured segments (page/slide/sheet labels,
//     page filtering, unit counts) to render read_document output.
//   - the knowledge-base ingest + browse path only needs "is this a binary
//     document?" and "give me its text", which PlainText answers.

import (
	"fmt"
	"os"
	"strings"
)

// Segment is one addressable chunk of a document: a PDF page, a pptx slide, an
// xlsx sheet, or (for docx) a single unlabeled body. Label is human-facing
// ("page 1", "slide 3", "sheet Data"); empty for docx.
type Segment = docSegment

// Result is the success shape returned by every format extractor.
type Result = extractResult

// Fallback signals that native extraction did not produce usable text and the
// caller should fall back to reading the raw file. Reason is a machine code:
// unsupported_format | parse_error | empty_result.
type Fallback = extractFallback

// DetectFormat maps a file extension to a supported format key ("pdf" | "docx"
// | "xlsx" | "pptx"), or "" if the format is not handled natively.
func DetectFormat(path string) string { return detectFormat(path) }

// IsSupported reports whether path names a binary document this package can
// extract text from. Callers use it to decide between "read the bytes verbatim"
// and "run the extractor".
func IsSupported(path string) bool { return detectFormat(path) != "" }

// Extract dispatches on the file extension and returns either a Result or a
// *Fallback (as error). pages filters PDF pages / pptx slides (1-indexed); nil
// means all. It is ignored for docx and xlsx.
func Extract(path string, data []byte, pages []int) (*Result, error) {
	return extractDocument(path, data, pages)
}

// ExtractDocx extracts a docx body. Exposed because the MCP metadata probe
// needs docx unit info without going through the extension dispatch.
func ExtractDocx(data []byte) (*Result, error) { return extractDocx(data) }

// PDFPageCount returns a PDF's page count without extracting its text.
func PDFPageCount(data []byte) (int, error) { return extractPDFInfo(data) }

// UnitLabels returns the unit name and per-unit labels for an OOXML document.
func UnitLabels(format string, data []byte) (string, []string, error) {
	return ooxmlUnitLabels(format, data)
}

// ParsePageSpec parses a "1-5,10,12-15" page range into 1-indexed page numbers.
func ParsePageSpec(spec string, max int) ([]int, error) { return parsePageSpec(spec, max) }

// PlainText reads the file at path and returns its full extracted text with the
// segments joined by blank lines. Segment labels are omitted: this feeds the KB
// index and the document reader, which want the prose, not a rendered report.
//
// A document the extractor cannot parse is an error (a *Fallback), not an empty
// string — the KB ingest path must be able to tell "no text" from "gave up", so
// it can skip the file instead of indexing a void.
func PlainText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	res, err := Extract(path, data, nil)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", &Fallback{Reason: "empty_result", Format: DetectFormat(path), Detail: "extractor returned no result"}
	}
	parts := make([]string, 0, len(res.Segments))
	for _, seg := range res.Segments {
		if s := strings.TrimSpace(seg.Text); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "", &Fallback{
			Reason: "empty_result",
			Format: res.Format,
			Detail: fmt.Sprintf("no text recovered from %d %s(s)", res.UnitCount, res.Unit),
		}
	}
	return strings.Join(parts, "\n\n"), nil
}
