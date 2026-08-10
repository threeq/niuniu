package main

// Go-native document text extraction for the read-accel feature (issue #281).
//
// All extractors are pure functions over the file bytes so they are trivially
// testable and have no process-level side effects. They return a structured
// *extractResult on success, or a *extractFallback when the document cannot be
// parsed natively — the caller turns the latter into the {fallback:"read",...}
// envelope that steers the agent back to the built-in Read tool (see #282
// loop-prevention). Nothing here ever panics out to the MCP layer: the PDF
// path in particular is wrapped in recover() because ledongthuc/pdf can panic
// on malformed font tables.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
)

// docSegment is one addressable chunk of a document: a PDF page, a pptx slide,
// an xlsx sheet, or (for docx) a single unlabeled body. Label is human-facing
// ("page 1", "slide 3", "sheet Data"); empty for docx.
type docSegment struct {
	Label string
	Text  string
}

// extractResult is the success shape returned by every format extractor.
type extractResult struct {
	Format    string       // pdf | docx | xlsx | pptx
	Unit      string       // page | slide | sheet | paragraph
	UnitCount int          // total units in the document (before any page filter)
	Segments  []docSegment // extracted (and possibly page-filtered) units
}

// extractFallback signals that native extraction did not produce usable text
// and the caller should fall back to the built-in Read tool. reason is a
// machine code: unsupported_format | parse_error | empty_result.
type extractFallback struct {
	Reason string
	Format string
	Detail string // human-readable specifics (joined into the envelope message)
}

func (e *extractFallback) Error() string { return e.Reason + ": " + e.Detail }

// detectFormat maps a file extension to a supported format key, or "" if the
// format is not handled natively.
func detectFormat(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "pdf"
	case ".docx":
		return "docx"
	case ".xlsx":
		return "xlsx"
	case ".pptx":
		return "pptx"
	default:
		return ""
	}
}

// extractDocument dispatches on the file extension and returns either a result
// or a *extractFallback (as error). `pages` filters PDF pages / pptx slides
// (1-indexed); nil means all. It is ignored for docx and xlsx.
func extractDocument(path string, data []byte, pages []int) (*extractResult, error) {
	format := detectFormat(path)
	switch format {
	case "pdf":
		return extractPDF(data, pages)
	case "docx":
		return extractDocx(data)
	case "xlsx":
		return extractXlsx(data)
	case "pptx":
		return extractPptx(data, pages)
	default:
		return nil, &extractFallback{
			Reason: "unsupported_format",
			Format: "unknown",
			Detail: fmt.Sprintf("extension %q is not handled by the Go-native extractor", strings.ToLower(filepath.Ext(path))),
		}
	}
}

// --- PDF ---

// extractPDF pulls plain text per page via ledongthuc/pdf. The library can
// panic on malformed input, so every page extraction is recover-guarded and a
// panic is downgraded to a parse_error fallback rather than crashing the MCP
// server.
func extractPDF(data []byte, pages []int) (result *extractResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = &extractFallback{Reason: "parse_error", Format: "pdf", Detail: fmt.Sprintf("PDF parser panicked: %v", r)}
		}
	}()

	reader, perr := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if perr != nil {
		return nil, &extractFallback{Reason: "parse_error", Format: "pdf", Detail: perr.Error()}
	}
	total := reader.NumPage()
	if total <= 0 {
		return nil, &extractFallback{Reason: "parse_error", Format: "pdf", Detail: "PDF reports zero pages"}
	}

	want := pageSet(pages, total)
	segs := make([]docSegment, 0, len(want))
	var nonEmpty int
	for _, n := range want {
		page := reader.Page(n)
		if page.V.IsNull() {
			continue
		}
		text, terr := page.GetPlainText(nil)
		if terr != nil {
			// A single bad page shouldn't sink the whole document; keep going.
			continue
		}
		text = strings.TrimRight(text, "\n")
		if strings.TrimSpace(text) != "" {
			nonEmpty++
		}
		segs = append(segs, docSegment{Label: fmt.Sprintf("page %d", n), Text: text})
	}

	if nonEmpty == 0 {
		// No extractable text on any requested page — almost always a scanned /
		// image-only PDF. Hand off to Read (or OCR, #283) instead of returning
		// blank pages.
		return nil, &extractFallback{Reason: "empty_result", Format: "pdf", Detail: "no extractable text (likely a scanned/image PDF)"}
	}
	return &extractResult{Format: "pdf", Unit: "page", UnitCount: total, Segments: segs}, nil
}

// extractPDFInfo returns just the page count (recover handled by the caller).
func extractPDFInfo(data []byte) (int, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, &extractFallback{Reason: "parse_error", Format: "pdf", Detail: err.Error()}
	}
	n := reader.NumPage()
	if n <= 0 {
		return 0, &extractFallback{Reason: "parse_error", Format: "pdf", Detail: "PDF reports zero pages"}
	}
	return n, nil
}

// --- shared zip helpers (docx/xlsx/pptx are all OOXML zips) ---

func openZip(data []byte) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, &extractFallback{Reason: "parse_error", Format: "ooxml", Detail: "not a valid OOXML (zip) container: " + err.Error()}
	}
	return zr, nil
}

// maxZipEntryBytes bounds a single decompressed OOXML entry. A docx/xlsx/pptx
// is a zip, so a few-KB file can declare an entry that inflates to gigabytes
// (a classic zip bomb); without a cap io.ReadAll would allocate the whole thing
// and OOM the MCP process, defeating maxInputFileBytes. 200 MiB is far above
// any real document part.
const maxZipEntryBytes = 200 << 20

func zipFileBytes(zr *zip.Reader, name string) ([]byte, bool) {
	for _, f := range zr.File {
		if f.Name == name {
			// Reject before decompressing a byte when the header's declared
			// size is already implausible.
			if f.UncompressedSize64 > maxZipEntryBytes {
				return nil, false
			}
			rc, err := f.Open()
			if err != nil {
				return nil, false
			}
			defer rc.Close()
			// Cap the actual stream too: a lying/zero header must not let the
			// decompressed size exceed the bound.
			b, err := io.ReadAll(io.LimitReader(rc, maxZipEntryBytes+1))
			if err != nil || int64(len(b)) > maxZipEntryBytes {
				return nil, false
			}
			return b, true
		}
	}
	return nil, false
}

// localName returns the namespace-stripped element name (e.g. "t" for "w:t").
func localName(t xml.StartElement) string { return t.Name.Local }

// --- docx ---

// extractDocx reads word/document.xml and concatenates <w:t> runs, breaking
// paragraphs on </w:p>, tabs on <w:tab/>, and line breaks on <w:br/>.
func extractDocx(data []byte) (*extractResult, error) {
	zr, err := openZip(data)
	if err != nil {
		return nil, err
	}
	xmlBytes, ok := zipFileBytes(zr, "word/document.xml")
	if !ok {
		return nil, &extractFallback{Reason: "parse_error", Format: "docx", Detail: "missing word/document.xml"}
	}
	text := collectOOXMLText(xmlBytes, "t", "p", "tab", "br")
	if strings.TrimSpace(text) == "" {
		return nil, &extractFallback{Reason: "empty_result", Format: "docx", Detail: "document body has no text"}
	}
	return &extractResult{
		Format:    "docx",
		Unit:      "paragraph",
		UnitCount: strings.Count(text, "\n") + 1,
		Segments:  []docSegment{{Label: "", Text: strings.TrimRight(text, "\n")}},
	}, nil
}

// collectOOXMLText is a tiny streaming text collector shared by docx and pptx.
// textElem is the run-text element local name ("t" for docx <w:t> and pptx
// <a:t>), paraElem the paragraph element ("p"), and tab/br the optional inline
// tab and break elements (pass "" to disable).
func collectOOXMLText(xmlBytes []byte, textElem, paraElem, tabElem, brElem string) string {
	dec := xml.NewDecoder(bytes.NewReader(xmlBytes))
	var sb strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch localName(t) {
			case textElem:
				inText = true
			case tabElem:
				if tabElem != "" {
					sb.WriteByte('\t')
				}
			case brElem:
				if brElem != "" {
					sb.WriteByte('\n')
				}
			}
		case xml.CharData:
			if inText {
				sb.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case textElem:
				inText = false
			case paraElem:
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

// --- pptx ---

// extractPptx reads ppt/slides/slideN.xml in numeric order, one segment per
// slide, with text drawn from <a:t> runs.
func extractPptx(data []byte, pages []int) (*extractResult, error) {
	zr, err := openZip(data)
	if err != nil {
		return nil, err
	}
	// Collect slide files and sort by their numeric suffix.
	type slideFile struct {
		num  int
		name string
	}
	var slides []slideFile
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			base := strings.TrimSuffix(strings.TrimPrefix(f.Name, "ppt/slides/slide"), ".xml")
			if n, perr := strconv.Atoi(base); perr == nil {
				slides = append(slides, slideFile{num: n, name: f.Name})
			}
		}
	}
	if len(slides) == 0 {
		return nil, &extractFallback{Reason: "parse_error", Format: "pptx", Detail: "no ppt/slides/slideN.xml entries"}
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].num < slides[j].num })

	total := len(slides)
	want := pageSet(pages, total) // slide indices are 1-based positions
	wantSet := make(map[int]bool, len(want))
	for _, n := range want {
		wantSet[n] = true
	}

	segs := make([]docSegment, 0, len(want))
	var nonEmpty int
	for idx, sf := range slides {
		pos := idx + 1
		if !wantSet[pos] {
			continue
		}
		xmlBytes, ok := zipFileBytes(zr, sf.name)
		if !ok {
			continue
		}
		text := strings.TrimRight(collectOOXMLText(xmlBytes, "t", "p", "", ""), "\n")
		if strings.TrimSpace(text) != "" {
			nonEmpty++
		}
		segs = append(segs, docSegment{Label: fmt.Sprintf("slide %d", pos), Text: text})
	}
	if nonEmpty == 0 {
		return nil, &extractFallback{Reason: "empty_result", Format: "pptx", Detail: "slides contain no extractable text"}
	}
	return &extractResult{Format: "pptx", Unit: "slide", UnitCount: total, Segments: segs}, nil
}

// --- xlsx ---

// xlsxSharedStrings parses xl/sharedStrings.xml into an ordered slice. Each
// <si> may hold a single <t> or several <r><t> rich-text runs; both collapse
// to one string.
func xlsxSharedStrings(zr *zip.Reader) []string {
	xmlBytes, ok := zipFileBytes(zr, "xl/sharedStrings.xml")
	if !ok {
		return nil
	}
	dec := xml.NewDecoder(bytes.NewReader(xmlBytes))
	var out []string
	var cur strings.Builder
	inSI := false
	inT := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch localName(t) {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = true
			}
		case xml.CharData:
			if inSI && inT {
				cur.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				out = append(out, cur.String())
				inSI = false
			}
		}
	}
	return out
}

// xlsxSheetOrder returns the workbook's sheets as (name, worksheetFileName) in
// display order, resolving r:id through xl/_rels/workbook.xml.rels. On any
// parse miss it returns nil and the caller falls back to filename order.
func xlsxSheetOrder(zr *zip.Reader) []struct{ Name, File string } {
	wb, ok := zipFileBytes(zr, "xl/workbook.xml")
	if !ok {
		return nil
	}
	rels, ok := zipFileBytes(zr, "xl/_rels/workbook.xml.rels")
	if !ok {
		return nil
	}

	// rId -> target (e.g. "worksheets/sheet1.xml")
	relMap := map[string]string{}
	{
		dec := xml.NewDecoder(bytes.NewReader(rels))
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
				var id, target string
				for _, a := range se.Attr {
					switch a.Name.Local {
					case "Id":
						id = a.Value
					case "Target":
						target = a.Value
					}
				}
				if id != "" && target != "" {
					relMap[id] = target
				}
			}
		}
	}

	var out []struct{ Name, File string }
	dec := xml.NewDecoder(bytes.NewReader(wb))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
			var name, rid string
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "name":
					name = a.Value
				case "id": // r:id
					rid = a.Value
				}
			}
			target := relMap[rid]
			if target == "" {
				continue
			}
			target = strings.TrimPrefix(target, "/xl/")
			if !strings.HasPrefix(target, "xl/") {
				target = "xl/" + target
			}
			out = append(out, struct{ Name, File string }{Name: name, File: target})
		}
	}
	return out
}

// extractXlsx renders each worksheet as tab-separated rows, resolving shared
// strings and inline strings. One segment per sheet.
func extractXlsx(data []byte) (*extractResult, error) {
	zr, err := openZip(data)
	if err != nil {
		return nil, err
	}
	shared := xlsxSharedStrings(zr)

	sheets := xlsxSheetOrder(zr)
	if len(sheets) == 0 {
		// Fallback: every xl/worksheets/sheetN.xml in filename order.
		var files []string
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
				files = append(files, f.Name)
			}
		}
		sort.Strings(files)
		for i, fn := range files {
			sheets = append(sheets, struct{ Name, File string }{Name: fmt.Sprintf("Sheet%d", i+1), File: fn})
		}
	}
	if len(sheets) == 0 {
		return nil, &extractFallback{Reason: "parse_error", Format: "xlsx", Detail: "no worksheets found"}
	}

	segs := make([]docSegment, 0, len(sheets))
	var nonEmpty int
	for _, sh := range sheets {
		xmlBytes, ok := zipFileBytes(zr, sh.File)
		if !ok {
			continue
		}
		text := renderXlsxSheet(xmlBytes, shared)
		if strings.TrimSpace(text) != "" {
			nonEmpty++
		}
		segs = append(segs, docSegment{Label: "sheet " + sh.Name, Text: text})
	}
	if nonEmpty == 0 {
		return nil, &extractFallback{Reason: "empty_result", Format: "xlsx", Detail: "workbook has no cell values"}
	}
	return &extractResult{Format: "xlsx", Unit: "sheet", UnitCount: len(sheets), Segments: segs}, nil
}

// renderXlsxSheet streams one worksheet XML into tab-separated rows. Cell type
// t="s" indexes sharedStrings; t="inlineStr" uses the inline <is><t>; anything
// else (numbers, booleans) takes the raw <v>.
func renderXlsxSheet(xmlBytes []byte, shared []string) string {
	dec := xml.NewDecoder(bytes.NewReader(xmlBytes))
	var sb strings.Builder

	var rowCells []string
	inRow := false

	// per-cell state
	var cellType string
	var inV, inIS, inT bool
	var vBuf, isBuf strings.Builder

	flushCell := func() {
		var val string
		switch cellType {
		case "s":
			if idx, err := strconv.Atoi(strings.TrimSpace(vBuf.String())); err == nil && idx >= 0 && idx < len(shared) {
				val = shared[idx]
			}
		case "inlineStr":
			val = isBuf.String()
		default:
			val = vBuf.String()
		}
		rowCells = append(rowCells, val)
	}

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch localName(t) {
			case "row":
				inRow = true
				rowCells = rowCells[:0]
			case "c":
				cellType = ""
				vBuf.Reset()
				isBuf.Reset()
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						cellType = a.Value
					}
				}
			case "v":
				inV = true
			case "is":
				inIS = true
			case "t":
				inT = true
			}
		case xml.CharData:
			if inV {
				vBuf.Write(t)
			} else if inIS && inT {
				isBuf.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "is":
				inIS = false
			case "c":
				flushCell()
			case "row":
				// Trim trailing empties so sparse rows don't emit dangling tabs.
				end := len(rowCells)
				for end > 0 && rowCells[end-1] == "" {
					end--
				}
				if end > 0 {
					sb.WriteString(strings.Join(rowCells[:end], "\t"))
				}
				sb.WriteByte('\n')
				inRow = false
			}
		}
	}
	_ = inRow
	return strings.TrimRight(sb.String(), "\n")
}

// --- page range parsing ---

// parsePageSpec turns "1-5,10,12-15" into a sorted, de-duplicated, 1-based int
// slice. Out-of-range values are clamped/skipped against max. An empty spec
// returns nil (meaning "all pages"). A spec that parses to nothing valid is an
// error so the caller can report it rather than silently returning everything.
func parsePageSpec(spec string, max int) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	seen := map[int]bool{}
	var out []int
	add := func(n int) {
		if n >= 1 && n <= max && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.IndexByte(part, '-'); i >= 0 {
			loStr := strings.TrimSpace(part[:i])
			hiStr := strings.TrimSpace(part[i+1:])
			lo, err1 := strconv.Atoi(loStr)
			hi, err2 := strconv.Atoi(hiStr)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid page range %q", part)
			}
			if lo > hi {
				lo, hi = hi, lo
			}
			for n := lo; n <= hi; n++ {
				add(n)
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid page number %q", part)
			}
			add(n)
		}
	}
	sort.Ints(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("page spec %q selects no pages in 1..%d", spec, max)
	}
	return out, nil
}

// ooxmlUnitLabels cheaply enumerates the addressable units of an xlsx/pptx via
// the zip table of contents (sheet names / slide positions) without rendering
// any cell or run text — used by document_info.
func ooxmlUnitLabels(format string, data []byte) (unit string, labels []string, err error) {
	zr, zerr := openZip(data)
	if zerr != nil {
		return "", nil, zerr
	}
	switch format {
	case "xlsx":
		sheets := xlsxSheetOrder(zr)
		if len(sheets) == 0 {
			var files []string
			for _, f := range zr.File {
				if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
					files = append(files, f.Name)
				}
			}
			sort.Strings(files)
			for i := range files {
				labels = append(labels, fmt.Sprintf("Sheet%d", i+1))
			}
		} else {
			for _, sh := range sheets {
				labels = append(labels, sh.Name)
			}
		}
		if len(labels) == 0 {
			return "", nil, &extractFallback{Reason: "parse_error", Format: "xlsx", Detail: "no worksheets found"}
		}
		return "sheet", labels, nil
	case "pptx":
		var nums []int
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
				base := strings.TrimSuffix(strings.TrimPrefix(f.Name, "ppt/slides/slide"), ".xml")
				if n, perr := strconv.Atoi(base); perr == nil {
					nums = append(nums, n)
				}
			}
		}
		if len(nums) == 0 {
			return "", nil, &extractFallback{Reason: "parse_error", Format: "pptx", Detail: "no slides found"}
		}
		sort.Ints(nums)
		for i := range nums {
			labels = append(labels, fmt.Sprintf("slide %d", i+1))
		}
		return "slide", labels, nil
	default:
		return "", nil, &extractFallback{Reason: "unsupported_format", Format: "unknown", Detail: "not an enumerable OOXML format"}
	}
}

// pageSet resolves the effective 1-based page list given an optional filter and
// the document's total. nil/empty filter means all pages.
func pageSet(pages []int, total int) []int {
	if len(pages) == 0 {
		out := make([]int, 0, total)
		for n := 1; n <= total; n++ {
			out = append(out, n)
		}
		return out
	}
	out := make([]int, 0, len(pages))
	for _, n := range pages {
		if n >= 1 && n <= total {
			out = append(out, n)
		}
	}
	return out
}
