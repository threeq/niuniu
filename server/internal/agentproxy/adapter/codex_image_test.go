package adapter

import (
	"strings"
	"testing"
)

// Codex 0.164 imagegen thread items must surface as image events — before
// this they fell into codexItemEvents' default case and were silently
// dropped, so generated images never reached the chat.

const testPNGBase64 = "iVBORw0KGgoAAAANSUhEUg==" // \x89PNG\r\n\x1a\n magic

func TestCodexItemEvents_ImageGenerationCompleted(t *testing.T) {
	events, err := ParseCodexJSONLine(
		`{"jsonrpc":"2.0","method":"item/completed","params":{"item":{"id":"ig-1","type":"imageGeneration","status":"completed","revised_prompt":"a cat, flat design","result":"` + testPNGBase64 + `"}}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 || len(events[0].Images) != 1 {
		t.Fatalf("events=%+v want one image event", events)
	}
	img := events[0].Images[0]
	if img.Data != testPNGBase64 {
		t.Fatalf("data=%q want the raw base64", img.Data)
	}
	if img.MimeType != "image/png" {
		t.Fatalf("mime=%q want sniffed image/png", img.MimeType)
	}
	if img.Caption != "a cat, flat design" {
		t.Fatalf("caption=%q want revised_prompt", img.Caption)
	}
}

func TestCodexItemEvents_ImageGenerationDataURI(t *testing.T) {
	events, err := ParseCodexJSONLine(
		`{"method":"item/completed","params":{"item":{"type":"imageGeneration","status":"completed","result":"data:image/jpeg;base64,/9j/4AAQ"}}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%+v", events)
	}
	img := events[0].Images[0]
	if img.Data != "/9j/4AAQ" || img.MimeType != "image/jpeg" {
		t.Fatalf("data=%q mime=%q want stripped data-URI + image/jpeg", img.Data, img.MimeType)
	}
}

func TestCodexItemEvents_ImageGenerationFailedAndInProgress(t *testing.T) {
	events, err := ParseCodexJSONLine(
		`{"method":"item/completed","params":{"item":{"type":"imageGeneration","status":"failed","revised_prompt":"a cat"}}}`)
	if err != nil {
		t.Fatalf("parse failed item: %v", err)
	}
	if len(events) != 1 || events[0].Type != "system" || events[0].Subtype != "image_generation_failed" {
		t.Fatalf("failed item → %+v want system/image_generation_failed", events)
	}

	events, err = ParseCodexJSONLine(
		`{"method":"item/started","params":{"item":{"type":"imageGeneration","status":"in_progress"}}}`)
	if err != nil {
		t.Fatalf("parse started item: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("in-progress item must render nothing, got %+v", events)
	}
}

func TestCodexItemEvents_ImageViewReferencesWorkspaceFile(t *testing.T) {
	events, err := ParseCodexJSONLine(
		`{"method":"item/completed","params":{"item":{"type":"imageView","id":"iv-1","path":".worktrees/main/out.png"}}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 || len(events[0].Images) != 1 {
		t.Fatalf("events=%+v want one image event", events)
	}
	if events[0].Images[0].Path != ".worktrees/main/out.png" {
		t.Fatalf("path=%q", events[0].Images[0].Path)
	}
}

func TestSniffImageMime(t *testing.T) {
	cases := map[string]string{
		testPNGBase64:          "image/png",
		"/9j/4AAQ":             "image/jpeg",
		"R0lGODlh":             "image/gif",
		"UklGRg==":             "", // RIFF but not enough bytes for WEBP check → png fallback
		"aGVsbG8=":             "image/png",
		"!!!not base64!!!":     "image/png",
	}
	for in, want := range cases {
		got := sniffImageMime(in, "")
		if want != "" && got != want {
			t.Fatalf("sniff(%q)=%q want %q", in, got, want)
		}
	}
	if got := sniffImageMime(testPNGBase64, "image/webp"); got != "image/webp" {
		t.Fatalf("declared mime must win, got %q", got)
	}
	if !strings.HasPrefix(testPNGBase64, "iVBOR") {
		t.Fatal("sanity")
	}
}
