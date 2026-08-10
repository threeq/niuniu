package main

// MCP tools for the read-accel document fast path (issue #281).
//
// One tool, always registered (no project/workspace context needed):
//   - read_document: extract text from a PDF/docx/xlsx/pptx, with optional
//     page range, keyword search, and a byte cap so a huge file never floods
//     the context window. With meta_only=true it returns just the cheap
//     metadata (format, page/slide/sheet count) so the agent can pick a page
//     range before pulling text.
//
// When the Go-native extractor can't produce usable text it returns a
// structured {fallback:"read",...} envelope (NOT an MCP error) that tells the
// agent to use the built-in Read tool instead — and explicitly not to retry
// this tool, which is the cooperative half of #282's loop-prevention.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// defaultMaxBytes caps read_document output so a large document degrades to a
// truncated-but-useful slice instead of blowing the context budget. Callers
// override via max_bytes; pages=/search= narrow further.
const defaultMaxBytes = 100_000

// maxInputFileBytes guards against loading a pathologically large file fully
// into memory. Above this we steer the agent to Read (which streams/offsets).
const maxInputFileBytes = 100 << 20 // 100 MiB

func registerDocumentTools(s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("read_document",
			mcp.WithDescription("Extract text from PDF / Word (docx) / Excel (xlsx) / PowerPoint (pptx) in pure Go, avoiding loading the whole binary into context. "+
				"Supports page ranges, keyword search, and byte-capped chunked reads. Prefer this over the built-in Read for large documents. "+
				"Pass meta_only=true for a cheap metadata probe (format + unit count/labels: pages/slides/sheets) — do this first to decide which pages/sheets to read instead of extracting everything. "+
				"If native extraction fails/is unsupported/yields no text, it returns a {\"fallback\":\"read\",...} signal — then use the built-in Read on the file and do not call this tool again."),
			mcp.WithString("path", mcp.Description("Absolute path to the document"), mcp.Required()),
			mcp.WithBoolean("meta_only", mcp.Description("Return only lightweight metadata (format, unit count, per-unit labels) instead of extracted text. Cheap probe to plan a targeted read.")),
			mcp.WithString("pages", mcp.Description("Page/slide range, e.g. \"1-5,10,12-15\" (PDF pages, pptx slides). Omit = all. Ignored for docx/xlsx.")),
			mcp.WithNumber("max_bytes", mcp.Description("Byte cap on returned text (default 100000). Over the cap it truncates and suggests narrowing with pages/search.")),
			mcp.WithString("search", mcp.Description("Keyword search (case-insensitive): returns only matching lines tagged with page/slide/sheet — good for locating content in a large document.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			path, errRes := requireString(args, "path")
			if errRes != nil {
				return errRes, nil
			}
			search := ""
			if v, ok := args["search"].(string); ok {
				search = v
			}
			maxBytes := defaultMaxBytes
			if v, ok := args["max_bytes"].(float64); ok && int(v) > 0 {
				maxBytes = int(v)
			}

			data, fileFmt, errRes := readDocFile(path)
			if errRes != nil {
				return errRes, nil
			}
			if data == nil { // too large -> fallback envelope already chosen
				return documentFallback(path, fileFmt, "too_large", "file exceeds the in-memory extraction limit"), nil
			}

			// meta_only: lightweight metadata probe (the former document_info tool).
			if metaOnly, _ := args["meta_only"].(bool); metaOnly {
				info, err := documentInfo(path, data)
				if err != nil {
					if fb, ok := err.(*extractFallback); ok {
						return documentFallback(path, fb.Format, fb.Reason, fb.Detail), nil
					}
					return mcp.NewToolResultError(err.Error()), nil
				}
				out, _ := json.Marshal(info)
				return mcp.NewToolResultText(string(out)), nil
			}

			var pages []int
			if spec, ok := args["pages"].(string); ok && strings.TrimSpace(spec) != "" {
				p, err := parsePageSpec(spec, 1<<30)
				if err != nil {
					return mcp.NewToolResultError(fmt.Sprintf("invalid pages %q: %v", spec, err)), nil
				}
				pages = p
			}

			res, err := extractDocument(path, data, pages)
			if err != nil {
				if fb, ok := err.(*extractFallback); ok {
					return documentFallback(path, fb.Format, fb.Reason, fb.Detail), nil
				}
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(renderExtract(path, res, search, maxBytes)), nil
		},
	)
}

// readDocFile resolves and reads the file. It returns an MCP error result for a
// genuinely missing/unreadable path (Read wouldn't help there either), or
// (nil, format, nil) when the file is too large to load — the caller turns that
// into a too_large fallback envelope.
func readDocFile(path string) ([]byte, string, *mcp.CallToolResult) {
	format := detectFormat(path)
	st, err := os.Stat(path)
	if err != nil {
		return nil, format, mcp.NewToolResultError(fmt.Sprintf("cannot stat %q: %v", path, err))
	}
	if st.IsDir() {
		return nil, format, mcp.NewToolResultError(fmt.Sprintf("%q is a directory, not a document", path))
	}
	if st.Size() > maxInputFileBytes {
		return nil, format, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, format, mcp.NewToolResultError(fmt.Sprintf("cannot read %q: %v", path, err))
	}
	return data, format, nil
}

// documentFallback builds the {fallback:"read",...} envelope as a successful
// (non-error) tool result so the agent reads the JSON and switches to Read.
func documentFallback(path, format, reason, detail string) *mcp.CallToolResult {
	if format == "" {
		format = "unknown"
	}
	payload := map[string]any{
		"fallback": "read",
		"reason":   reason,
		"path":     path,
		"format":   format,
		"message": "Go-native extraction unavailable (" + detail + "). " +
			"Use the built-in Read tool on this path instead. " +
			"Do NOT call this MCP tool again for this file.",
	}
	b, _ := json.Marshal(payload)
	return mcp.NewToolResultText(string(b))
}

// docInfo is the document_info response shape.
type docInfo struct {
	Path      string   `json:"path"`
	Format    string   `json:"format"`
	Unit      string   `json:"unit"`
	UnitCount int      `json:"unit_count"`
	Labels    []string `json:"labels,omitempty"`
	SizeBytes int64    `json:"size_bytes"`
}

// documentInfo reports counts without pulling full body text where it can be
// avoided (PDF page count and zip unit counts are cheap). A scanned/image PDF
// still reports its page count here rather than failing.
func documentInfo(path string, data []byte) (*docInfo, error) {
	format := detectFormat(path)
	info := &docInfo{Path: path, Format: format, SizeBytes: int64(len(data))}
	switch format {
	case "pdf":
		n, labels, err := pdfPageCount(data)
		if err != nil {
			return nil, err
		}
		info.Unit, info.UnitCount, info.Labels = "page", n, labels
	case "docx":
		res, err := extractDocx(data)
		if err != nil {
			return nil, err
		}
		info.Unit, info.UnitCount = "paragraph", res.UnitCount
	case "xlsx", "pptx":
		// Cheap unit enumeration via the zip table of contents + labels.
		unit, labels, err := ooxmlUnitLabels(format, data)
		if err != nil {
			return nil, err
		}
		info.Unit, info.UnitCount, info.Labels = unit, len(labels), labels
	default:
		return nil, &extractFallback{Reason: "unsupported_format", Format: "unknown", Detail: "extension not handled by the Go-native extractor"}
	}
	return info, nil
}

// pdfPageCount returns the page count, recover-guarded against parser panics.
func pdfPageCount(data []byte) (count int, labels []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			count, labels = 0, nil
			err = &extractFallback{Reason: "parse_error", Format: "pdf", Detail: fmt.Sprintf("PDF parser panicked: %v", r)}
		}
	}()
	res, e := extractPDFInfo(data)
	return res, nil, e
}

// renderExtract turns extracted segments into the final text payload, applying
// the optional keyword search and the byte cap. Segment labels become section
// headers so the agent keeps page/slide/sheet provenance.
func renderExtract(path string, res *extractResult, search string, maxBytes int) string {
	var sb strings.Builder
	header := fmt.Sprintf("[%s · %d %s(s) · %s]\n", res.Format, res.UnitCount, res.Unit, path)
	sb.WriteString(header)

	matched := 0
	for _, seg := range res.Segments {
		text := seg.Text
		if search != "" {
			text = filterLines(text, search)
			if strings.TrimSpace(text) == "" {
				continue
			}
		}
		matched++
		if seg.Label != "" {
			sb.WriteString("\n===== " + seg.Label + " =====\n")
		} else {
			sb.WriteString("\n")
		}
		sb.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			sb.WriteByte('\n')
		}
	}

	if search != "" && matched == 0 {
		return header + fmt.Sprintf("\nNo lines matching %q in %d %s(s).", search, res.UnitCount, res.Unit)
	}
	return capBytes(sb.String(), maxBytes)
}

// filterLines keeps only the lines containing term (case-insensitive).
func filterLines(text, term string) string {
	lt := strings.ToLower(term)
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(strings.ToLower(line), lt) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// capBytes truncates on a line boundary near the limit and appends a hint.
func capBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	if nl := strings.LastIndexByte(cut, '\n'); nl > maxBytes/2 {
		cut = cut[:nl]
	} else {
		// No newline to cut on in the back half: trim any partial trailing
		// multibyte rune so we never emit invalid UTF-8 (e.g. a half a CJK
		// char on a long single-line segment).
		for len(cut) > 0 {
			// A genuine U+FFFD decodes as (RuneError, 3); only an incomplete
			// trailing sequence yields (RuneError, 0|1) and must be trimmed.
			if r, size := utf8.DecodeLastRuneInString(cut); r != utf8.RuneError || size > 1 {
				break
			}
			cut = cut[:len(cut)-1]
		}
	}
	omitted := len(s) - len(cut)
	return cut + fmt.Sprintf("\n...[truncated: %d more bytes. Narrow with pages= or search=, or raise max_bytes.]", omitted)
}
