package kbindex

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkTextEmpty(t *testing.T) {
	if got := ChunkText("", 100); len(got) != 0 {
		t.Fatalf("empty text should yield 0 chunks, got %d", len(got))
	}
	if got := ChunkText("   \n\n  ", 100); len(got) != 0 {
		t.Fatalf("whitespace-only text should yield 0 chunks, got %d", len(got))
	}
}

func TestChunkTextShortSingleChunk(t *testing.T) {
	text := "  hello world  "
	got := ChunkText(text, 100)
	if len(got) != 1 {
		t.Fatalf("short text should be one chunk, got %d", len(got))
	}
	if got[0].Content != "hello world" {
		t.Fatalf("content should be trimmed, got %q", got[0].Content)
	}
	if got[0].Index != 0 {
		t.Fatalf("first chunk index should be 0, got %d", got[0].Index)
	}
}

func TestChunkTextByteOffsetInvariant(t *testing.T) {
	// Each chunk's content must be the exact substring of the original text at
	// its reported byte offset, so the offset is a usable pointer into the file.
	text := strings.Repeat("alpha beta gamma delta.\n", 50)
	got := ChunkText(text, 80)
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(got))
	}
	for i, c := range got {
		if c.Index != i {
			t.Errorf("chunk %d has Index %d", i, c.Index)
		}
		if c.ByteOffset < 0 || c.ByteOffset+len(c.Content) > len(text) {
			t.Fatalf("chunk %d offset %d out of range", i, c.ByteOffset)
		}
		if text[c.ByteOffset:c.ByteOffset+len(c.Content)] != c.Content {
			t.Fatalf("chunk %d content does not match text at offset %d", i, c.ByteOffset)
		}
		if len(c.Content) > 80 {
			t.Fatalf("chunk %d exceeds budget: %d bytes", i, len(c.Content))
		}
	}
}

func TestChunkTextChineseValidUTF8(t *testing.T) {
	// Chinese chars are 3 bytes; a byte budget must never cut mid-rune.
	text := strings.Repeat("知识库全文检索中文测试段落。", 40)
	got := ChunkText(text, 50)
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(got))
	}
	for i, c := range got {
		if !utf8.ValidString(c.Content) {
			t.Fatalf("chunk %d is not valid UTF-8 (cut mid-rune)", i)
		}
	}
}
