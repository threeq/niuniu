package kbindex

import (
	"strings"
	"unicode/utf8"
)

// DefaultChunkChars is the default per-chunk byte budget. It mirrors the proven
// value used by the project-learning ingest path (~1200 bytes per slice).
const DefaultChunkChars = 1200

// Chunk is a single retrieval unit produced from a document's text. Content is
// always an exact substring of the original text starting at ByteOffset, so the
// offset is a usable pointer for seeking back into the source file.
type Chunk struct {
	Index      int    // 0-based ordinal within the document
	Content    string // chunk text (an exact substring of the source at ByteOffset)
	ByteOffset int    // byte offset of Content within the original text
}

// ChunkText slices text into retrieval-sized chunks. The budget is measured in
// bytes; splits prefer newline boundaries and never cut a multi-byte rune, so
// every chunk is valid UTF-8. Leading/trailing whitespace on each chunk is
// trimmed, and the reported ByteOffset points at the first retained byte so the
// invariant text[ByteOffset:ByteOffset+len(Content)] == Content always holds.
func ChunkText(text string, maxChars int) []Chunk {
	if maxChars <= 0 {
		maxChars = DefaultChunkChars
	}
	var chunks []Chunk
	n := len(text)
	start := 0
	for start < n {
		end := start + maxChars
		if end >= n {
			end = n
		} else {
			end = breakPoint(text, start, end)
		}
		raw := text[start:end]
		// Trim leading whitespace, advancing the recorded offset to match.
		trimmedLeft := strings.TrimLeft(raw, " \t\r\n")
		lead := len(raw) - len(trimmedLeft)
		content := strings.TrimRight(trimmedLeft, " \t\r\n")
		if content != "" {
			chunks = append(chunks, Chunk{
				Index:      len(chunks),
				Content:    content,
				ByteOffset: start + lead,
			})
		}
		start = end
	}
	return chunks
}

// breakPoint returns a split index in (start, hardEnd] that lands on a rune
// boundary, preferring the last newline in the back half of the range so chunks
// align to natural text boundaries instead of arbitrary byte positions.
func breakPoint(text string, start, hardEnd int) int {
	// Pull hardEnd back to a valid rune boundary first.
	for hardEnd > start && !utf8.RuneStart(text[hardEnd]) {
		hardEnd--
	}
	// Prefer a newline in the back half of [start, hardEnd) to keep chunks whole.
	half := start + (hardEnd-start)/2
	if i := strings.LastIndexByte(text[half:hardEnd], '\n'); i >= 0 {
		return half + i + 1 // split just after the newline
	}
	return hardEnd
}
