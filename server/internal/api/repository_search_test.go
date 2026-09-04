package api

import (
	"testing"
)

// matchFileNames owns the ranking rules, which are easy to regress silently:
// a hit list that stops putting the obvious match first still "works".
func TestMatchFileNamesRanksSubstringFirst(t *testing.T) {
	entries := []fileEntry{
		// Fuzzy-only match for "app": a-p-p appear in order but not adjacent.
		{Path: "src/a/p/parser.go", Name: "parser.go"},
		{Path: "src/app.go", Name: "app.go"},
		{Path: "internal/very/deep/nested/app.go", Name: "app.go"},
	}

	hits, truncated := matchFileNames(entries, "app")
	if truncated {
		t.Error("3 entries should not truncate")
	}
	if len(hits) < 2 {
		t.Fatalf("want at least the two substring hits, got %d: %+v", len(hits), hits)
	}
	// Shortest substring match wins — that is nearly always the intended file.
	if hits[0].Path != "src/app.go" {
		t.Errorf("first hit = %q, want src/app.go", hits[0].Path)
	}
	// The deep substring match must still outrank the fuzzy-only one.
	if hits[1].Path != "internal/very/deep/nested/app.go" {
		t.Errorf("second hit = %q, want the deep substring match", hits[1].Path)
	}
}

func TestMatchFileNamesIsCaseInsensitive(t *testing.T) {
	entries := []fileEntry{{Path: "src/ReadMe.MD", Name: "ReadMe.MD"}}

	hits, _ := matchFileNames(entries, "readme")
	if len(hits) != 1 {
		t.Fatalf("case-insensitive match failed: %+v", hits)
	}
}

func TestMatchFileNamesTruncatesAtCap(t *testing.T) {
	entries := make([]fileEntry, repoSearchMaxFiles+25)
	for i := range entries {
		// Every path contains "x" so all of them match.
		entries[i] = fileEntry{Path: "x/file" + itoa(i) + ".go", Name: "file.go"}
	}

	hits, truncated := matchFileNames(entries, "x")
	if !truncated {
		t.Error("over-cap result set must report truncation, not silently drop")
	}
	if len(hits) != repoSearchMaxFiles {
		t.Errorf("returned %d hits, want cap of %d", len(hits), repoSearchMaxFiles)
	}
}

// itoa avoids importing strconv just for the loop above.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
