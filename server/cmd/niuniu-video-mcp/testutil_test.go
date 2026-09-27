package main

// Shared test fixtures. Everything here is local-only: HTTP providers are
// simulated with httptest, ffmpeg-dependent tests skip when the binary cannot
// be resolved (so the suite passes on a bare machine) and no test touches the
// network or the user's real ~/.niuniu.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// testWS returns a fresh workspace directory.
func testWS(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// envMap adapts a map into the envLookup seam BuildRegistry expects.
func envMap(m map[string]string) envLookup {
	return func(k string) string { return m[k] }
}

// testShot describes one fixture shot.
type testShot struct {
	ID       string
	Dur      float64
	Type     string // image | video
	Asset    string // visual.asset (workspace-relative or bare)
	TTSAsset string
	Subtitle string
}

// storyboardJSON renders a structurally valid storyboard with the given
// review_status (the validator contract lives in storyboard.go).
func storyboardJSON(status string, shots []testShot) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"title":"测试短片","aspect_ratio":"16:9","fps":30,"resolution":"640x360","review_status":%q,"shots":[`, status)
	for i, sh := range shots {
		if i > 0 {
			b.WriteString(",")
		}
		typ := sh.Type
		if typ == "" {
			typ = "image"
		}
		fmt.Fprintf(&b, `{"id":%q,"duration_sec":%v,"narration":"旁白%d","subtitle":%q,`+
			`"visual":{"type":%q,"tier":"L2","prompt":"prompt %d","asset":%q},`+
			`"tts":{"voice":"alloy","asset":%q},"transition":"cut"}`,
			sh.ID, sh.Dur, i, sh.Subtitle, typ, i, sh.Asset, sh.TTSAsset)
	}
	b.WriteString("]}")
	return b.String()
}

// writeStoryboard writes <ws>/video-project/storyboard.json.
func writeStoryboard(t *testing.T, ws, content string) {
	t.Helper()
	if err := ensureDirs(ws); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	path := projectPath(ws, "storyboard.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write storyboard: %v", err)
	}
}

// resultText extracts the text payload of a tool result.
func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("工具返回 nil result")
	}
	if len(res.Content) == 0 {
		return ""
	}
	switch c := res.Content[0].(type) {
	case mcp.TextContent:
		return c.Text
	case *mcp.TextContent:
		return c.Text
	default:
		return fmt.Sprintf("%v", c)
	}
}

// callArgs builds a CallToolRequest with arguments (mcp-go's call shape).
func callArgs(args map[string]any) mcp.CallToolRequest {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	return req
}

// jsonField pulls a top-level string field out of a JSON tool result.
func jsonField(t *testing.T, text, field string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("解析工具结果失败: %v；原文：%s", err, text)
	}
	s, _ := m[field].(string)
	return s
}

// resolveTestFFmpeg resolves ffmpeg or skips the test.
func resolveTestFFmpeg(t *testing.T) string {
	t.Helper()
	bin, err := DefaultDeps().ffmpegPath()
	if err != nil {
		t.Skipf("跳过（本机无 ffmpeg）：%v", err)
	}
	return bin
}

// ffmpegHasFilter reports whether the binary exposes a filter (e.g. subtitles
// needs libass) — keeps the compose test honest on trimmed builds. `-filters`
// prints to stdout, which runFFmpeg (stderr-only) does not capture, so this
// runs the process itself.
func ffmpegHasFilter(t *testing.T, bin, filter string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-hide_banner", "-filters").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == filter {
			return true
		}
	}
	return false
}

// ffmpegRun runs ffmpeg for fixture generation and fails the test on error.
func ffmpegRun(t *testing.T, bin string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if out, err := runFFmpeg(ctx, bin, args); err != nil {
		t.Fatalf("fixture ffmpeg 失败: %v\n%s", err, out)
	}
}

// assetPath builds a workspace-relative asset path string.
func assetPath(parts ...string) string {
	return filepath.ToSlash(filepath.Join(append([]string{"video-project", "assets"}, parts...)...))
}
