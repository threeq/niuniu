package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/imageopt"
)

// readHookInput is the subset of the Claude Code PreToolUse hook stdin
// payload (https://code.claude.com/docs/en/hooks) the Read router cares
// about. Unknown fields (transcript_path, permission_mode, ...) are
// discarded by the JSON decoder.
type readHookInput struct {
	SessionID string          `json:"session_id"`
	CWD       string          `json:"cwd"`
	ToolName  string          `json:"tool_name"`
	ToolInput readHookToolArg `json:"tool_input"`
}

// readHookToolArg is the Read tool's argument shape. offset/limit arrive as
// JSON numbers; we treat any non-zero value as "the agent is doing a
// targeted read" and never reroute those (see runReadHook).
type readHookToolArg struct {
	FilePath string  `json:"file_path"`
	Offset   float64 `json:"offset"`
	Limit    float64 `json:"limit"`
}

// preToolUseDecision is the PreToolUse hook stdout contract. Emitting it
// with permissionDecision="deny" blocks the Read and feeds the reason back
// to the model; emitting nothing (and exiting 0) lets the built-in Read
// proceed under the normal permission flow.
type preToolUseDecision struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// Routing thresholds. Overridable via env so they can be tuned without a
// rebuild. Defaults chosen so small reads stay on the fast built-in path
// and only genuinely heavy reads get rerouted.
//
//   - Images at or above imageRouteBytes go to mcp__niuniu__read_image
//     (built-in Read base64-inlines the whole image into the vision
//     context — slow and token-expensive for large images).
//   - Binary office/PDF documents are ALWAYS rerouted regardless of size:
//     built-in Read cannot parse them into meaningful text at all.
//   - Plain text / code at or above textRouteBytes is denied with a nudge
//     to use Read offset/limit or Grep instead of loading it whole.
const (
	defaultImageRouteBytes int64 = 512 * 1024       // 512 KiB
	defaultTextRouteBytes  int64 = 2 * 1024 * 1024  // 2 MiB
	defaultMarkerTTL             = 180 * time.Second // loop-prevention window

	// defaultImageRouteLongEdge is the pixel long-edge trigger (issue #490
	// follow-up). Matches imageopt.DefaultVisionMaxEdge: an image whose long
	// edge reaches Claude's vision cap (~1568px) is rerouted even when its
	// disk size is below defaultImageRouteBytes. Closes the "small bytes, big
	// pixels" gap — a well-compressed screenshot (e.g. 1920×1080 at ~300KB)
	// used to slip past the byte-only check and base64-flood the context,
	// because vision cost scales with pixel count, not disk bytes.
	defaultImageRouteLongEdge = 1568
)

var (
	imageExts = map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".webp": true, ".bmp": true, ".tif": true, ".tiff": true,
	}
	docExts = map[string]bool{
		".pdf": true, ".docx": true, ".xlsx": true, ".pptx": true,
		".doc": true, ".xls": true, ".ppt": true,
	}
)

// runReadHook is the PreToolUse hook handler for the built-in Read tool. It
// reads the hook JSON from stdin and decides whether to let the Read run
// (the fast built-in path for small files) or deny it and redirect the
// agent to a niuniu MCP fast-path tool (read_image / read_document) for
// large images and binary documents.
//
// FAIL-OPEN: any parse/stat/IO error results in "allow" (exit 0, empty
// stdout). A misbehaving hook must never wedge the agent — the worst
// acceptable outcome is the built-in Read running when we could have
// rerouted, never the reverse.
//
// LOOP-PREVENTION (#282 / #281 fallback chain): when a fast-path tool fails
// it returns {"fallback":"read", "path": ...} steering the agent back to
// the built-in Read. That second Read hits this hook again on the same
// path and would be rerouted forever. We defend with a short-lived
// per-(session,path) marker: the FIRST reroute drops a marker; any Read of
// the same path within the TTL window is allowed straight through (and the
// marker consumed), so the fallback Read reaches the built-in tool exactly
// once and the cycle cannot form.
func runReadHook(stdin io.Reader, stdout, stderr io.Writer) int {
	var input readHookInput
	if err := json.NewDecoder(stdin).Decode(&input); err != nil {
		// Can't understand the payload — never block on it.
		return 0
	}

	// Matcher is "Read" in settings.json, but guard anyway: anything that
	// isn't the built-in Read tool is none of our business.
	if input.ToolName != "Read" {
		return 0
	}

	raw := strings.TrimSpace(input.ToolInput.FilePath)
	if raw == "" {
		return 0
	}

	// Targeted reads (offset/limit) are exactly what we ask the agent to do
	// for large text — never reroute them. This also lets the agent escape a
	// reroute deterministically by re-reading with a window.
	if input.ToolInput.Offset > 0 || input.ToolInput.Limit > 0 {
		return 0
	}

	abs := raw
	if !filepath.IsAbs(abs) && input.CWD != "" {
		abs = filepath.Join(input.CWD, abs)
	}
	abs = filepath.Clean(abs)

	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		// Missing path or a directory: let built-in Read surface its own
		// error / behavior. Not our job to second-guess.
		return 0
	}

	// Loop-prevention: a recent marker means this is a retry (most likely a
	// fast-path fallback Read). Consume it and let the built-in Read run.
	if consumeMarkerIfRecent(input.SessionID, abs) {
		return 0
	}

	ext := strings.ToLower(filepath.Ext(abs))
	size := info.Size()

	var reason string
	switch {
	case imageExts[ext] && imageNeedsReroute(abs, size):
		reason = fmt.Sprintf(
			"图片 %s（%s）较大，内置 Read 会把整图 base64 塞进视觉上下文——慢且耗 token。"+
				"请改用 mcp__niuniu__read_image 读取该路径（装了 Tesseract 则 OCR 出文本，否则自动降采样供视觉）。"+
				"若该工具失败并返回 fallback:read，再次 Read 同一路径会被直接放行、不再改道。",
			abs, humanBytes(size))
	case docExts[ext]:
		reason = fmt.Sprintf(
			"二进制文档 %s 无法被内置 Read 正确解析为文本。"+
				"请改用 mcp__niuniu__read_document 抽取其文本/表格内容。"+
				"若抽取失败并返回 fallback:read，再次 Read 同一路径会被直接放行、不再改道。",
			abs)
	case size >= envBytes("NIUNIU_READHOOK_TEXT_BYTES", defaultTextRouteBytes):
		reason = fmt.Sprintf(
			"纯文本/代码文件 %s（%s）较大，一次性载入会浪费上下文。"+
				"请用内置 Read 的 offset/limit 分段读取，或先用 Grep 定点检索关键词，再按行号窗口读。",
			abs, humanBytes(size))
	default:
		// Small file / unknown binary type — fast built-in path.
		return 0
	}

	// We are rerouting: record the marker BEFORE denying so the fallback
	// Read (if any) finds it.
	writeMarker(input.SessionID, abs)

	var dec preToolUseDecision
	dec.HookSpecificOutput.HookEventName = "PreToolUse"
	dec.HookSpecificOutput.PermissionDecision = "deny"
	dec.HookSpecificOutput.PermissionDecisionReason = reason
	if err := json.NewEncoder(stdout).Encode(&dec); err != nil {
		fmt.Fprintf(stderr, "read-hook: encode decision: %v\n", err)
		return 0 // still fail-open
	}
	return 0
}

// markerDir is the per-host scratch dir holding loop-prevention markers.
func markerDir() string {
	return filepath.Join(os.TempDir(), "niuniu-readhook")
}

// markerPath maps a (session, absolute-path) pair to a stable scratch file.
// Session-scoping keeps concurrent agents from clearing each other's
// markers; the sha256 keeps the filename filesystem-safe and bounded. On
// Windows the path is lower-cased first: the filesystem is case-insensitive,
// so the reroute and the follow-up fallback Read must hash to the same key
// even if the model echoes the path back with different casing — otherwise
// loop-prevention misses and the fallback Read is denied again.
func markerPath(sessionID, abs string) string {
	key := abs
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	sum := sha256.Sum256([]byte(sessionID + "\x00" + key))
	return filepath.Join(markerDir(), hex.EncodeToString(sum[:]))
}

// consumeMarkerIfRecent reports whether a non-expired reroute marker exists
// for (session, abs); if so it deletes the marker (single-use) and returns
// true. A stale marker is also cleaned up but returns false so the path can
// be classified afresh.
func consumeMarkerIfRecent(sessionID, abs string) bool {
	p := markerPath(sessionID, abs)
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	fresh := time.Since(info.ModTime()) < markerTTL()
	_ = os.Remove(p)
	return fresh
}

// writeMarker drops/refreshes the reroute marker for (session, abs) and
// opportunistically prunes expired markers so the scratch dir stays bounded.
// All failures are swallowed — the marker is a best-effort optimization, not
// a correctness requirement (a missing marker at worst costs one extra
// reroute, never a wedged agent).
func writeMarker(sessionID, abs string) {
	dir := markerDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(markerPath(sessionID, abs), []byte(abs), 0o600)
	pruneStaleMarkers(dir)
}

func pruneStaleMarkers(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	ttl := markerTTL()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) >= ttl {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func markerTTL() time.Duration {
	if v := os.Getenv("NIUNIU_READHOOK_TTL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultMarkerTTL
}

// imageNeedsReroute decides whether a built-in image Read should be denied
// and rerouted to mcp__niuniu__read_image. Two independent triggers, EITHER
// suffices:
//
//   - file size >= NIUNIU_READHOOK_IMAGE_BYTES (default 512 KiB): a large
//     on-disk image is expensive to base64-inline regardless of pixel count.
//   - pixel long edge >= NIUNIU_READHOOK_IMAGE_EDGE (default 1568px): closes
//     the "small bytes, big pixels" gap — a well-compressed PNG can sit under
//     the byte threshold yet still flood the vision context once Claude tiles
//     it. DecodeConfig reads only the header (cheap). A header we can't parse
//     (corrupt / bmp / tiff) falls back to the byte trigger alone — fail-open,
//     never blocks the read.
func imageNeedsReroute(abs string, size int64) bool {
	if size >= envBytes("NIUNIU_READHOOK_IMAGE_BYTES", defaultImageRouteBytes) {
		return true
	}
	edge := envBytes("NIUNIU_READHOOK_IMAGE_EDGE", defaultImageRouteLongEdge)
	w, h, err := imageopt.DecodeDimensions(abs)
	if err != nil {
		return false
	}
	long := int64(w)
	if h > w {
		long = int64(h)
	}
	return long >= edge
}

func envBytes(name string, def int64) int64 {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// humanBytes renders a byte count as a compact human-readable string for
// the deny reason (e.g. "3.2 MB").
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
