package main

// Dependency injection for the module's external binary needs.
//
// FFmpeg is injected as a resolver func (plan §1.5) instead of an import, so
// this module never depends on the packaging infrastructure directly:
//
//	wave 1 (here):     exec.LookPath("ffmpeg")             — PATH only
//	wave 2 (wiring):   internal/ffmpegbin.Resolve(dataDir) — env override →
//	                   unpacked copy under <dataDir>/bin/ffmpeg/<fp>/ → PATH
//
// When ffmpeg cannot be resolved, media_compose degrades with an explicit
// Chinese error (design §9: 工具层降级，只出产物族+素材清单，不合成) while every
// other tool keeps working — nothing else in this module shells out to ffmpeg
// except the optional duration probe (which silently omits the duration).

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Deps carries the module's injectable dependencies.
type Deps struct {
	// ResolveFFmpeg returns an absolute path to a usable ffmpeg binary.
	ResolveFFmpeg func() (string, error)
}

// DefaultDeps returns the wave-1 resolver: PATH lookup only.
func DefaultDeps() Deps {
	return Deps{
		ResolveFFmpeg: func() (string, error) {
			p, err := exec.LookPath("ffmpeg")
			if err != nil {
				return "", fmt.Errorf("未找到 ffmpeg（PATH 中不存在）：媒体合成不可用。" +
					"请安装 ffmpeg 并加入 PATH，或使用带内嵌 ffmpeg 的发行版。")
			}
			return p, nil
		},
	}
}

// ffmpegPath resolves ffmpeg and normalizes the error into the user-facing
// degrade message used by media_compose.
func (d Deps) ffmpegPath() (string, error) {
	resolve := d.ResolveFFmpeg
	if resolve == nil {
		resolve = DefaultDeps().ResolveFFmpeg
	}
	p, err := resolve()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("未找到 ffmpeg：解析结果为空，媒体合成不可用。请安装 ffmpeg 并加入 PATH。")
	}
	return p, nil
}

// --- ffmpeg process helpers ---

// runFFmpeg executes ffmpeg with args, capturing stderr (ffmpeg logs there).
// A non-zero exit becomes an error carrying the last stderr lines — the agent
// needs ffmpeg's own diagnostic, not a generic "exit status 1".
func runFFmpeg(ctx context.Context, bin string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stderr.String()
	if err != nil {
		if ctx.Err() != nil {
			return out, fmt.Errorf("ffmpeg 被取消（%v）", ctx.Err())
		}
		return out, fmt.Errorf("ffmpeg 执行失败: %v\n--- ffmpeg 输出（末尾） ---\n%s", err, tailLines(out, 20))
	}
	return out, nil
}

// tailLines keeps the last n non-empty lines of a log blob.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

var durationRe = regexp.MustCompile(`Duration:\s*(\d+):(\d{2}):(\d{2})\.(\d{1,3})`)

// mediaInfo is a lightweight probe result.
type mediaInfo struct {
	DurationSec float64
	HasVideo    bool
	HasAudio    bool
	VideoCodec  string
	AudioCodec  string
	Width       int
	Height      int
	Valid       bool // a usable duration was parsed
}

// probeMedia inspects a media file by parsing ffmpeg's stderr banner. ffmpeg
// exits non-zero when no output file is given, so the exit code is ignored —
// what matters is whether the stream information parsed.
func probeMedia(ctx context.Context, bin, path string) (mediaInfo, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, _ := runFFmpeg(cctx, bin, []string{"-hide_banner", "-i", path})
	return parseProbeOutput(out), nil
}

// parseProbeOutput extracts duration/streams/codecs from ffmpeg stderr text.
func parseProbeOutput(out string) mediaInfo {
	info := mediaInfo{}
	if m := durationRe.FindStringSubmatch(out); m != nil {
		h, _ := strconv.Atoi(m[1])
		mnt, _ := strconv.Atoi(m[2])
		sec, _ := strconv.Atoi(m[3])
		frac := m[4]
		for len(frac) < 3 {
			frac += "0"
		}
		ms, _ := strconv.Atoi(frac)
		info.DurationSec = float64(h*3600+mnt*60+sec) + float64(ms)/1000
		info.Valid = true
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Stream #") {
			continue
		}
		if i := strings.Index(line, "Video: "); i >= 0 {
			info.HasVideo = true
			info.VideoCodec = firstToken(line[i+len("Video: "):])
			if dim := videoDimRe.FindStringSubmatch(line); dim != nil {
				info.Width, _ = strconv.Atoi(dim[1])
				info.Height, _ = strconv.Atoi(dim[2])
			}
		}
		if i := strings.Index(line, "Audio: "); i >= 0 {
			info.HasAudio = true
			info.AudioCodec = firstToken(line[i+len("Audio: "):])
		}
	}
	return info
}

var videoDimRe = regexp.MustCompile(`,\s*(\d{2,5})x(\d{2,5})`)

// firstToken returns the leading token of s (up to space/comma).
func firstToken(s string) string {
	if i := strings.IndexAny(s, " ,("); i >= 0 {
		return s[:i]
	}
	return s
}
