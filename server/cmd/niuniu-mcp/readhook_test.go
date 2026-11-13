package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePNG writes a w×h PNG. A blank (all-zero) RGBA compresses to a few KB
// regardless of dimensions, so this produces the "small bytes, big pixels"
// shape the pixel-edge trigger must catch.
func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
}

// runReadHookWith drives runReadHook with a tool_input payload and returns
// (exitCode, decision, rawStdout). decision is nil when stdout is empty
// (the "allow" case).
func runReadHookWith(t *testing.T, sessionID, cwd, filePath string, offset, limit int) (int, *preToolUseDecision, string) {
	t.Helper()
	payload := map[string]any{
		"session_id": sessionID,
		"cwd":        cwd,
		"tool_name":  "Read",
		"tool_input": map[string]any{
			"file_path": filePath,
			"offset":    offset,
			"limit":     limit,
		},
	}
	in := bytes.Buffer{}
	out := bytes.Buffer{}
	errb := bytes.Buffer{}
	if err := json.NewEncoder(&in).Encode(payload); err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	rc := runReadHook(&in, &out, &errb)
	raw := strings.TrimSpace(out.String())
	if raw == "" {
		return rc, nil, raw
	}
	var dec preToolUseDecision
	if err := json.Unmarshal([]byte(raw), &dec); err != nil {
		t.Fatalf("unmarshal decision from %q: %v", raw, err)
	}
	return rc, &dec, raw
}

func writeSizedFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustDeny(t *testing.T, dec *preToolUseDecision, wantFragment string) {
	t.Helper()
	if dec == nil {
		t.Fatalf("expected a deny decision, got allow (empty stdout)")
	}
	if dec.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName want PreToolUse, got %q", dec.HookSpecificOutput.HookEventName)
	}
	if dec.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("permissionDecision want deny, got %q", dec.HookSpecificOutput.PermissionDecision)
	}
	if !strings.Contains(dec.HookSpecificOutput.PermissionDecisionReason, wantFragment) {
		t.Errorf("reason missing %q:\n%s", wantFragment, dec.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestReadHook_SmallFileAllowed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.txt")
	writeSizedFile(t, p, 100)

	rc, dec, raw := runReadHookWith(t, "sess-small", dir, p, 0, 0)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if dec != nil {
		t.Fatalf("small file should be allowed (empty stdout), got %q", raw)
	}
}

func TestReadHook_LargeImageReroutedToReadImage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.png")
	writeSizedFile(t, p, int(defaultImageRouteBytes)+1)

	_, dec, _ := runReadHookWith(t, "sess-img", dir, p, 0, 0)
	mustDeny(t, dec, "mcp__niuniu__read_image")
}

func TestReadHook_SmallImageAllowed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tiny.png")
	writeSizedFile(t, p, 1024) // well under the image threshold

	_, dec, raw := runReadHookWith(t, "sess-img2", dir, p, 0, 0)
	if dec != nil {
		t.Fatalf("small image should be allowed, got deny: %q", raw)
	}
}

// TestReadHook_SmallBytesBigPixelsRerouted is the issue #490 follow-up: a
// well-compressed PNG can sit far under the byte threshold yet have a long
// edge at/above Claude's vision cap. The byte-only check used to let it slip
// through and base64-flood the context; the pixel-edge trigger must reroute it.
func TestReadHook_SmallBytesBigPixelsRerouted(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "screenshot.png")
	writePNG(t, p, 1920, 1080) // blank → a few KB on disk, 1920px long edge

	if st, err := os.Stat(p); err != nil {
		t.Fatalf("stat: %v", err)
	} else if st.Size() >= defaultImageRouteBytes {
		t.Fatalf("test premise broken: blank PNG is %d bytes, not under the byte threshold", st.Size())
	}

	_, dec, _ := runReadHookWith(t, "sess-bigpixels", dir, p, 0, 0)
	mustDeny(t, dec, "mcp__niuniu__read_image")
}

// TestReadHook_SmallBytesSmallPixelsAllowed guards the other side: an image
// under BOTH the byte and pixel thresholds stays on the fast built-in path.
func TestReadHook_SmallBytesSmallPixelsAllowed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "icon.png")
	writePNG(t, p, 64, 64)

	_, dec, raw := runReadHookWith(t, "sess-smallpixels", dir, p, 0, 0)
	if dec != nil {
		t.Fatalf("small-bytes small-pixels image must be allowed, got deny: %q", raw)
	}
}

func TestReadHook_DocumentAlwaysRerouted(t *testing.T) {
	dir := t.TempDir()
	// Tiny PDF — built-in Read can't parse binary docs at any size, so it
	// must reroute regardless of the size thresholds.
	p := filepath.Join(dir, "spec.pdf")
	writeSizedFile(t, p, 200)

	_, dec, _ := runReadHookWith(t, "sess-doc", dir, p, 0, 0)
	mustDeny(t, dec, "mcp__niuniu__read_document")
}

func TestReadHook_LargeTextGuidesToOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.log")
	writeSizedFile(t, p, int(defaultTextRouteBytes)+1)

	_, dec, _ := runReadHookWith(t, "sess-text", dir, p, 0, 0)
	mustDeny(t, dec, "offset/limit")
}

func TestReadHook_OffsetLimitAlwaysAllowed(t *testing.T) {
	dir := t.TempDir()
	// A large image WITH a limit must still be allowed — a targeted read is
	// the agent saying it knows what it wants.
	p := filepath.Join(dir, "big.png")
	writeSizedFile(t, p, int(defaultImageRouteBytes)+1)

	_, dec, raw := runReadHookWith(t, "sess-targeted", dir, p, 0, 50)
	if dec != nil {
		t.Fatalf("targeted read (limit set) must be allowed, got deny: %q", raw)
	}
}

func TestReadHook_NonReadToolAllowed(t *testing.T) {
	in := bytes.Buffer{}
	out := bytes.Buffer{}
	errb := bytes.Buffer{}
	json.NewEncoder(&in).Encode(map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
	})
	rc := runReadHook(&in, &out, &errb)
	if rc != 0 || strings.TrimSpace(out.String()) != "" {
		t.Fatalf("non-Read tool must pass through: rc=%d out=%q", rc, out.String())
	}
}

func TestReadHook_MissingPathAllowed(t *testing.T) {
	dir := t.TempDir()
	_, dec, raw := runReadHookWith(t, "sess-missing", dir, filepath.Join(dir, "ghost.png"), 0, 0)
	if dec != nil {
		t.Fatalf("missing path must be allowed (built-in Read surfaces the error), got %q", raw)
	}
}

func TestReadHook_RelativePathResolvedViaCwd(t *testing.T) {
	dir := t.TempDir()
	writeSizedFile(t, filepath.Join(dir, "big.png"), int(defaultImageRouteBytes)+1)

	// file_path is relative; cwd must be joined in before stat/classify.
	_, dec, _ := runReadHookWith(t, "sess-rel", dir, "big.png", 0, 0)
	mustDeny(t, dec, "mcp__niuniu__read_image")
}

// TestReadHook_LoopPreventionAllowsRetry is the core #282 acceptance: the
// first Read of a large doc reroutes, but a SECOND Read of the same path
// within the TTL window (the fast-path fallback) is allowed straight
// through — so read_document's {"fallback":"read"} can't form a cycle.
func TestReadHook_LoopPreventionAllowsRetry(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "report.pdf")
	writeSizedFile(t, p, 4096)
	session := "sess-loop-unique-12345"

	// First read: rerouted.
	_, dec1, _ := runReadHookWith(t, session, dir, p, 0, 0)
	mustDeny(t, dec1, "mcp__niuniu__read_document")

	// Second read of the same path (the fallback retry): allowed.
	_, dec2, raw2 := runReadHookWith(t, session, dir, p, 0, 0)
	if dec2 != nil {
		t.Fatalf("fallback retry must be allowed (loop-prevention), got deny: %q", raw2)
	}

	// Third read: the marker was single-use/consumed, so a fresh classify
	// reroutes again (the prior fallback already happened; a new attempt is
	// a new cycle that again gets one fallback chance).
	_, dec3, _ := runReadHookWith(t, session, dir, p, 0, 0)
	mustDeny(t, dec3, "mcp__niuniu__read_document")
}

// TestReadHook_DifferentSessionsDontShareMarkers guards the session-scoping
// of loop-prevention markers — one agent's reroute must not silence
// another agent's first read of the same path.
func TestReadHook_DifferentSessionsDontShareMarkers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.xlsx")
	writeSizedFile(t, p, 4096)

	_, dec1, _ := runReadHookWith(t, "session-A", dir, p, 0, 0)
	mustDeny(t, dec1, "mcp__niuniu__read_document")

	// Different session, same path: must still reroute (no shared marker).
	_, dec2, _ := runReadHookWith(t, "session-B", dir, p, 0, 0)
	mustDeny(t, dec2, "mcp__niuniu__read_document")
}
