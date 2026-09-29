package service

// In-app ffmpeg download — the "install" path behind Settings → 系统依赖 for
// the ffmpeg row (design v3.2 §7.3: 系统依赖按需下载，不内嵌).
//
// Contract with the Install/Subscribe machinery in system_deps.go: the whole
// download runs inside the single install job's goroutine, publishes every
// user-visible line as an InstallEvent (the same SSE stream the package-manager
// installs use) and returns a process-style exit code. Failures are never
// silent: the error text is published verbatim together with an actionable
// hint, and the generic "请手动运行" trailer of the PM path is skipped.
//
// Resumability: each archive is downloaded to
// <dataDir>/cache/downloads/<name>.part. When a .part file exists the request
// carries `Range: bytes=<size>-`; a 206 appends, a 200 (server ignored Range)
// restarts from zero. Interruptions (network drop, cancellation) keep the
// .part and the next click continues from there. Content failures (HTTP error,
// error-page size gate, unpack failure) delete it — a prefix of an error page
// must never be resumed.
//
// Proxies: the default http.Client transport is http.DefaultTransport, whose
// Proxy is http.ProxyFromEnvironment, so HTTP_PROXY / HTTPS_PROXY (and their
// lowercase forms) work with no extra code.

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/ffmpegbin"
)

const (
	// ffmpegArchiveMinBytes gates a COMPLETED download: a static ffmpeg build
	// is tens of MB, so anything under 1 MiB is an error page, a captive
	// proxy's interception page or a truncated transfer — never an archive.
	ffmpegArchiveMinBytes = 1 << 20

	// ffmpegPartSuffix marks an in-progress download. The .part file IS the
	// archive (nothing is renamed on completion): it is unpacked in place and
	// deleted once the install has succeeded, so the downloads dir only ever
	// holds unfinished work.
	ffmpegPartSuffix = ".part"

	// Progress-line throttling: emit at most one line per second, and at most
	// one per 1% of the total, so a fast link cannot flood the SSE stream.
	ffmpegProgressMinInterval = time.Second
	ffmpegProgressMinPercent  = 1.0

	// downloadAttempts bounds the internal retry of a single archive request
	// (currently only used to recover from a stale .part answering 416).
	downloadAttempts = 3
)

// BtbN/FFmpeg-Builds — the de-facto community distribution of FFmpeg git
// master (gpl builds). The `latest` release tag is rolling but its asset NAMES
// are stable, so the URLs do not rot; the CONTENT drifts with upstream master.
// win64-gpl.zip holds bin/ffmpeg.exe + bin/ffprobe.exe; the linux tarballs
// hold bin/ffmpeg + bin/ffprobe.
const (
	btbnBaseURL      = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest"
	btbnWindowsAMD64 = btbnBaseURL + "/ffmpeg-master-latest-win64-gpl.zip"
	btbnLinuxAMD64   = btbnBaseURL + "/ffmpeg-master-latest-linux64-gpl.tar.xz"
	btbnLinuxARM64   = btbnBaseURL + "/ffmpeg-master-latest-linuxarm64-gpl.tar.xz"
)

// osxexperts.net — the darwin source (one zip per tool; the binary sits at the
// archive root).
//
// RISKS, all documented at the point of use rather than discovered later:
//   - Community, unofficial builds. The FILENAMES EMBED the FFmpeg release
//     ("71" == FFmpeg 7.1): when upstream publishes 7.2 these URLs start
//     returning 404 and the constants below must be bumped.
//   - evermeet.cx (the previous darwin source) is NOT used: its ffprobe
//     endpoint is broken — /ffprobe/getrelease/zip 302s to the *ffmpeg*
//     archive (measured 2026-09-27), so downloading "ffprobe" silently
//     yielded ffmpeg twice.
//   - The zips are unsigned/unnotarised: macOS may require an explicit user
//     approval (or `xattr -d com.apple.quarantine`) before executing them.
const (
	osxexpertsFFmpegARM64  = "https://www.osxexperts.net/ffmpeg71arm.zip"
	osxexpertsFFprobeARM64 = "https://www.osxexperts.net/ffprobe71arm.zip"
	osxexpertsFFmpegAMD64  = "https://www.osxexperts.net/ffmpeg71intel.zip"
	osxexpertsFFprobeAMD64 = "https://www.osxexperts.net/ffprobe71intel.zip"
)

// ffmpegArchiveKind is how an archive is unpacked.
type ffmpegArchiveKind int

const (
	ffmpegArchiveZip ffmpegArchiveKind = iota
	// ffmpegArchiveTarXz is delegated to the system `tar -xJf` — the Go
	// stdlib has no xz decoder, and vendoring one for a single archive format
	// is not worth the dependency. Linux boxes have tar (busybox tar included);
	// a box without it gets an explicit error.
	ffmpegArchiveTarXz
)

// ffmpegLayout is where the tools sit inside the archive(s).
type ffmpegLayout int

const (
	// ffmpegLayoutBoth: ONE archive carrying both tools under bin/ (BtbN).
	ffmpegLayoutBoth ffmpegLayout = iota
	// ffmpegLayoutPerTool: one archive PER TOOL, the binary at the root;
	// urls[0] is ffmpeg and urls[1] is ffprobe (osxexperts).
	ffmpegLayoutPerTool
)

// ffmpegSource is the download plan for one GOOS/GOARCH.
type ffmpegSource struct {
	urls   []string
	layout ffmpegLayout
	kind   ffmpegArchiveKind
	// label and approxMB shape the user-facing opening line ("download size
	// ~N MB"); the size is a rough order of magnitude for the banner only —
	// never a gate (the gate is ffmpegArchiveMinBytes).
	label    string
	approxMB int
}

// ffmpegSources is the platform → archives matrix. It is a variable, not a
// const map, so tests can point a platform at an httptest server (no test ever
// touches the network).
var ffmpegSources = map[string]ffmpegSource{
	"windows-amd64": {urls: []string{btbnWindowsAMD64}, layout: ffmpegLayoutBoth, kind: ffmpegArchiveZip, label: "win64-gpl", approxMB: 160},
	"linux-amd64":   {urls: []string{btbnLinuxAMD64}, layout: ffmpegLayoutBoth, kind: ffmpegArchiveTarXz, label: "linux64-gpl", approxMB: 110},
	"linux-arm64":   {urls: []string{btbnLinuxARM64}, layout: ffmpegLayoutBoth, kind: ffmpegArchiveTarXz, label: "linuxarm64-gpl", approxMB: 105},
	"darwin-arm64": {
		urls:     []string{osxexpertsFFmpegARM64, osxexpertsFFprobeARM64},
		layout:   ffmpegLayoutPerTool,
		kind:     ffmpegArchiveZip,
		label:    "macOS arm64 (osxexperts)",
		approxMB: 80,
	},
	"darwin-amd64": {
		urls:     []string{osxexpertsFFmpegAMD64, osxexpertsFFprobeAMD64},
		layout:   ffmpegLayoutPerTool,
		kind:     ffmpegArchiveZip,
		label:    "macOS intel (osxexperts)",
		approxMB: 80,
	},
}

// ffmpegSourceFor returns the archive plan for goos/goarch.
func ffmpegSourceFor(goos, goarch string) (ffmpegSource, bool) {
	src, ok := ffmpegSources[goos+"-"+goarch]
	return src, ok
}

// ffmpegSource is the plan for this service's platform (see targetPlatform).
func (s *SystemDepsService) ffmpegSource() (ffmpegSource, bool) {
	goos, goarch := s.targetPlatform()
	return ffmpegSourceFor(goos, goarch)
}

// resumableError marks a failure whose on-disk state is still a valid prefix
// of the archive (network drop, cancellation, deadline): the .part is kept and
// the next attempt resumes it. Every other failure invalidates the prefix.
type resumableError struct{ err error }

func (e *resumableError) Error() string { return e.err.Error() }
func (e *resumableError) Unwrap() error { return e.err }

func resumable(err error) error { return &resumableError{err: err} }

// --- install job body ---

// runFFmpegDownload is the install job body for tool == "ffmpeg": download the
// platform's archive(s), unpack them, and atomically install ffmpeg+ffprobe
// into <dataDir>/bin/ffmpeg/<goos>-<goarch>/. Returns the exit code the job
// reports (0 ok, 1 any failure); all user-visible lines are published here.
func (s *SystemDepsService) runFFmpegDownload(ctx context.Context, job *installJob) int {
	goos, goarch := s.targetPlatform()
	src, ok := ffmpegSourceFor(goos, goarch)
	if !ok {
		job.publish(InstallEvent{Line: fmt.Sprintf(
			"[niuniu] 当前平台 %s/%s 没有内置下载源。请手动安装 ffmpeg 并加入 PATH，或参见 %s",
			goos, goarch, fallbackURLs["ffmpeg"])})
		return 1
	}
	base, err := s.baseDir()
	if err != nil {
		job.publish(InstallEvent{Line: fmt.Sprintf("[niuniu] 无法确定数据目录（%v）；无法下载 ffmpeg。", err)})
		return 1
	}

	// Opening line: ffmpeg is downloaded, not shelled out to a package manager,
	// so the package-manager banner ("$ winget install ...") that Install emits
	// for every other tool is replaced by this friendly one. The SPA renders
	// stream lines verbatim, no branching needed.
	job.publish(InstallEvent{Line: fmt.Sprintf(
		"[niuniu] 应用内下载 ffmpeg（%s，约 %d MB，支持断点续传）", src.label, src.approxMB)})

	// 1. Download every archive with resume support.
	parts := make([]string, len(src.urls))
	for i, url := range src.urls {
		part := filepath.Join(base, "cache", "downloads", ffmpegArchiveName(url)+ffmpegPartSuffix)
		if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
			job.publish(InstallEvent{Line: fmt.Sprintf("[niuniu] 无法创建下载目录（%v）。", err)})
			return 1
		}
		parts[i] = part

		job.publish(InstallEvent{Line: fmt.Sprintf("[niuniu] 下载源：%s", url)})
		if have := fileSize(part); have > 0 {
			job.publish(InstallEvent{Line: fmt.Sprintf(
				"[niuniu] 检测到未完成的下载（已保留 %.1f MB），从断点继续", ffmpegMB(have))})
		}
		if err := s.downloadArchive(ctx, job, url, part); err != nil {
			if isResumable(err) {
				job.publish(InstallEvent{Line: fmt.Sprintf(
					"[niuniu] 下载中断：%v。已保留 %.1f MB，再次点击“安装”将续传。",
					unwrapResumable(err), ffmpegMB(fileSize(part)))})
			} else {
				// A corrupt or non-archive body must not be resumed.
				os.Remove(part)
				job.publish(InstallEvent{Line: fmt.Sprintf("[niuniu] 下载失败：%v。已清理残留，可重试。", err)})
			}
			publishFFmpegNetworkHint(job, base, goos, goarch)
			return 1
		}

		switch size := fileSize(part); {
		case size < ffmpegArchiveMinBytes:
			os.Remove(part)
			job.publish(InstallEvent{Line: fmt.Sprintf(
				"[niuniu] 下载内容异常：%s 只有 %d 字节（小于 1 MiB 下限），可能是错误页或被代理拦截；已清理残留，请重试。",
				filepath.Base(part), size)})
			publishFFmpegNetworkHint(job, base, goos, goarch)
			return 1
		default:
			job.publish(InstallEvent{Line: fmt.Sprintf(
				"[niuniu] 下载完成：%s（%.1f MB）", filepath.Base(part), ffmpegMB(size))})
		}
	}

	// 2. Unpack into a work dir next to the final install root, stage exactly
	// the two binaries into workDir/install, then rename that into place (a
	// reader either sees the previous install or the new one; the extraction
	// trees — doc/, LICENSE, the archive's own directory layout — never reach
	// the install dir).
	installParent := filepath.Join(base, "bin", "ffmpeg")
	if err := os.MkdirAll(installParent, 0o755); err != nil {
		job.publish(InstallEvent{Line: fmt.Sprintf("[niuniu] 无法创建安装目录（%v）。", err)})
		return 1
	}
	workDir, err := os.MkdirTemp(installParent, ".staging-")
	if err != nil {
		job.publish(InstallEvent{Line: fmt.Sprintf("[niuniu] 无法创建临时目录（%v）。", err)})
		return 1
	}
	defer os.RemoveAll(workDir) // leftover extraction trees; no-op after success
	stageDir := filepath.Join(workDir, "install")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		job.publish(InstallEvent{Line: fmt.Sprintf("[niuniu] 无法创建临时目录（%v）。", err)})
		return 1
	}

	for i, part := range parts {
		job.publish(InstallEvent{Line: fmt.Sprintf("[niuniu] 正在解包：%s", filepath.Base(part))})
		extractDir := filepath.Join(workDir, fmt.Sprintf("x-%d", i))
		if err := extractArchive(part, extractDir, src.kind); err != nil {
			os.Remove(part)
			job.publish(InstallEvent{Line: fmt.Sprintf(
				"[niuniu] 解包失败：%v。已清理残留，请重试；连续失败可手动安装 ffmpeg 到 %s",
				err, ffmpegbin.InstallRoot(base, goos, goarch))})
			return 1
		}
		for _, tool := range ffmpegTools(src, i) {
			staged, err := findStagedTool(extractDir, goos, tool, src.layout)
			if err != nil {
				os.Remove(part)
				job.publish(InstallEvent{Line: fmt.Sprintf(
					"[niuniu] 解包后缺少 %s 组件：%v。该下载源可能已变更或内容损坏；"+
						"已清理残留，请重试或手动放置 ffmpeg/ffprobe 到 %s",
					tool, err, ffmpegbin.InstallRoot(base, goos, goarch))})
				return 1
			}
			if err := stageBinary(staged, filepath.Join(stageDir, ffmpegbin.BinName(goos, tool))); err != nil {
				os.Remove(part)
				job.publish(InstallEvent{Line: fmt.Sprintf("[niuniu] 安装 %s 失败：%v。", tool, err)})
				return 1
			}
		}
	}

	// 3. Atomically swap the staged payload into place.
	finalDir := ffmpegbin.InstallRoot(base, goos, goarch)
	if err := replaceDir(stageDir, finalDir); err != nil {
		job.publish(InstallEvent{Line: fmt.Sprintf(
			"[niuniu] 安装失败（%v）：无法将 %s 就位。", err, finalDir)})
		return 1
	}
	// The archives served their purpose; keeping them would waste hundreds of
	// MB per platform.
	for _, part := range parts {
		os.Remove(part)
	}
	job.publish(InstallEvent{Line: fmt.Sprintf(
		"[niuniu] ffmpeg 安装完成（%s，含 ffmpeg 与 ffprobe）。", finalDir)})
	return 0
}

// ffmpegTools lists the tools carried by archive i of src: both tools for the
// single-archive layout, one per archive for the per-tool layout.
func ffmpegTools(src ffmpegSource, i int) []string {
	if src.layout == ffmpegLayoutPerTool {
		if i == 0 {
			return []string{"ffmpeg"}
		}
		return []string{"ffprobe"}
	}
	return []string{"ffmpeg", "ffprobe"}
}

// publishFFmpegNetworkHint appends the actionable follow-up line shown after
// any download failure: the two escapes the user actually has (a proxy, or a
// manual binary drop into the install root).
func publishFFmpegNetworkHint(job *installJob, base, goos, goarch string) {
	job.publish(InstallEvent{Line: "[niuniu] 网络不可达时可先配置代理（HTTP_PROXY/HTTPS_PROXY）后重试，" +
		"或手动放置 ffmpeg/ffprobe 到 " + ffmpegbin.InstallRoot(base, goos, goarch) + "/。"})
}

// --- download with resume ---

// downloadArchive fetches url into part, resuming a partial file when one
// exists. Returned errors are resumableError-wrapped when the bytes already on
// disk are still a valid prefix of the archive.
func (s *SystemDepsService) downloadArchive(ctx context.Context, job *installJob, url, part string) error {
	client := s.ffmpegHTTPClient()
	for attempt := 0; attempt < downloadAttempts; attempt++ {
		have := fileSize(part)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
		resp, err := client.Do(req)
		if err != nil {
			// Connection error / cancellation: the file on disk is untouched.
			return resumable(err)
		}

		retry, err := s.consumeResponse(ctx, job, resp, part, have)
		if err != nil {
			return err
		}
		if retry {
			// Stale .part rejected by the server (416) or resumed at the wrong
			// offset: drop it and re-request from scratch.
			os.Remove(part)
			continue
		}
		return nil
	}
	return fmt.Errorf("重试 %d 次后仍无法下载 %s", downloadAttempts, url)
}

// consumeResponse streams resp.Body into part and reports whether the caller
// should retry from scratch (stale .part answered 416, or a 206 that resumed
// at an offset other than the one requested).
func (s *SystemDepsService) consumeResponse(ctx context.Context, job *installJob, resp *http.Response, part string, have int64) (retry bool, err error) {
	defer resp.Body.Close()

	var total int64
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// The server resumed at our offset — verify it resumed at OUR offset:
		// a mismatched start would append to the wrong place.
		start, size := parseContentRange(resp.Header.Get("Content-Range"))
		if start != have {
			return true, nil
		}
		if size > 0 {
			total = size
		} else {
			total = have + resp.ContentLength
		}
	case http.StatusOK:
		// Fresh download, or the server ignored our Range header: rewrite the
		// file from zero so nothing stale survives.
		have = 0
		total = resp.ContentLength
	case http.StatusRequestedRangeNotSatisfiable:
		if fileSize(part) > 0 {
			return true, nil // stale .part; retry without Range
		}
		return false, fmt.Errorf("服务器拒绝下载请求（HTTP 416）")
	default:
		return false, fmt.Errorf("下载 %s 失败：HTTP %s%s", resp.Request.URL, resp.Status, bodySnippet(resp.Body))
	}

	flags := os.O_CREATE | os.O_WRONLY
	if have > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return false, err
	}
	progress := &ffmpegProgress{job: job, total: total, resumeFrom: have, start: time.Now()}
	_, copyErr := io.Copy(io.MultiWriter(f, progress), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		// Network drop mid-transfer: the bytes written so far are a prefix of
		// the archive, so keep the .part and resume later.
		return false, resumable(copyErr)
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := ctx.Err(); err != nil {
		return false, resumable(err)
	}
	job.publish(InstallEvent{Line: fmt.Sprintf(
		"[niuniu] 进度 100%% (%.1f/%.1f MB)", ffmpegMB(fileSize(part)), ffmpegMB(fileSize(part)))})
	return false, nil
}

// ffmpegHTTPClient is the client used for downloads: the injected one in
// tests, otherwise a default client. A zero-value http.Client uses
// http.DefaultTransport, whose Proxy is http.ProxyFromEnvironment — the system
// proxy env vars are honoured without any extra code; the comment exists so
// that stays a decision, not an accident.
func (s *SystemDepsService) ffmpegHTTPClient() *http.Client {
	if s.httpClient != nil {
		return s.httpClient
	}
	return &http.Client{}
}

// ffmpegProgress counts transferred bytes (as an io.Writer for io.Copy) and
// publishes a throttled progress line — at most one per second and per 1%
// (design §7.3: 下载显示进度, without flooding the SSE stream).
type ffmpegProgress struct {
	job        *installJob
	total      int64 // 0 when the server did not disclose it
	resumeFrom int64 // bytes already on disk when this transfer started
	written    int64 // bytes transferred by THIS transfer
	start      time.Time
	lastAt     time.Time
	lastBytes  int64
	lastPct    float64
}

// Write implements io.Writer.
func (p *ffmpegProgress) Write(b []byte) (int, error) {
	n := len(b)
	p.written += int64(n)
	done := p.resumeFrom + p.written
	now := time.Now()
	pct := 0.0
	if p.total > 0 {
		pct = float64(done) / float64(p.total) * 100
	}
	if p.lastAt.IsZero() {
		p.lastAt = p.start
	}
	if now.Sub(p.lastAt) < ffmpegProgressMinInterval && pct-p.lastPct < ffmpegProgressMinPercent {
		return n, nil
	}
	rate := ffmpegMB(done-p.lastBytes) / now.Sub(p.lastAt).Seconds()
	switch {
	case p.total > 0:
		p.job.publish(InstallEvent{Line: fmt.Sprintf(
			"[niuniu] 进度 %.0f%% (%.1f/%.1f MB, %.1f MB/s)", pct, ffmpegMB(done), ffmpegMB(p.total), rate)})
	default:
		p.job.publish(InstallEvent{Line: fmt.Sprintf(
			"[niuniu] 进度 %.1f MB (%.1f MB/s)", ffmpegMB(done), rate)})
	}
	p.lastAt, p.lastBytes, p.lastPct = now, done, pct
	return n, nil
}

// parseContentRange parses "bytes <start>-<end>/<total>". A malformed or
// absent header, or a "*" size, yields zeros — the caller then falls back to
// the request offset and Content-Length.
func parseContentRange(h string) (start, total int64) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(h, "bytes ") {
		return 0, 0
	}
	span, size, found := strings.Cut(strings.TrimPrefix(h, "bytes "), "/")
	if !found {
		return 0, 0
	}
	startStr, _, found := strings.Cut(span, "-")
	if !found {
		return 0, 0
	}
	start, _ = strconv.ParseInt(strings.TrimSpace(startStr), 10, 64)
	total, _ = strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	return start, total
}

// --- unpack / install ---

// extractArchive unpacks archive into dir (created as needed for archives
// whose root is a directory: the caller creates it for the zip writers).
func extractArchive(archive, dir string, kind ffmpegArchiveKind) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	switch kind {
	case ffmpegArchiveZip:
		return extractFFmpegZip(archive, dir)
	case ffmpegArchiveTarXz:
		return extractTarXz(archive, dir)
	}
	return fmt.Errorf("未知的压缩格式 (%d)", kind)
}

// extractFFmpegZip carries the ffmpeg prefix on purpose: the service package
// already has a generic extractZip (kb_download.go) with a different guard
// model.
func extractFFmpegZip(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("打开 zip %s: %w", filepath.Base(archive), err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := f.Name
		if f.FileInfo().IsDir() {
			continue
		}
		// Guard against zip-slip: entries must stay under dir.
		target := filepath.Join(dir, filepath.FromSlash(name))
		if rel, err := filepath.Rel(dir, target); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("zip 条目越界: %s", name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		rc.Close()
		if closeErr := out.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			return fmt.Errorf("解包 %s: %w", name, copyErr)
		}
	}
	return nil
}

// extractTarXz unpacks a .tar.xz with the system tar. The Go stdlib has no xz
// decoder; requiring tar (present on every mainstream Linux, including busybox
// images) is cheaper than vendoring one. A box without tar gets a clear,
// actionable error instead of a confusing archive error.
func extractTarXz(archive, dir string) error {
	tarPath, err := exec.LookPath("tar")
	if err != nil {
		return fmt.Errorf("系统缺少 tar，无法解包 %s（请安装 tar 后重试，或手动安装 ffmpeg）", filepath.Base(archive))
	}
	cmd := exec.Command(tarPath, "-xJf", archive, "-C", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar -xJf %s 失败: %v (%s)", filepath.Base(archive), err, firstLine(string(out)))
	}
	return nil
}

// findStagedTool locates tool in an extracted tree: bin/<name> for the
// both-tools layout (BtbN), the base name at any depth for the per-tool layout
// (osxexperts zips hold the binary at the root).
func findStagedTool(root, goos, tool string, layout ffmpegLayout) (string, error) {
	name := ffmpegbin.BinName(goos, tool)
	var found string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if found != "" {
			return nil
		}
		slashPath := path.Clean(filepath.ToSlash(p))
		switch {
		case layout == ffmpegLayoutBoth && strings.HasSuffix(slashPath, "/bin/"+name):
			found = p
		case layout == ffmpegLayoutPerTool && path.Base(slashPath) == name:
			found = p
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("压缩包中未找到 %s", name)
	}
	return found, nil
}

// stageBinary copies src to dst (the staging dir's canonical name) and makes
// it executable on platforms with exec bits.
func stageBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// Windows has no exec bit (os.Chmod is a near no-op there); 0755 is what
	// Resolve's usableFile demands on Unix.
	return os.Chmod(dst, 0o755)
}

// replaceDir moves tmp onto final, replacing an existing directory.
func replaceDir(tmp, final string) error {
	if _, err := os.Stat(final); err == nil {
		// Windows cannot rename onto an existing directory. Removing first is
		// not strictly atomic, but the only readers are ffmpeg child processes
		// holding an open handle to the old binaries, which keep working.
		if err := os.RemoveAll(final); err != nil {
			return err
		}
	}
	return os.Rename(tmp, final)
}

// --- small helpers ---

// ffmpegArchiveName is the file name a URL's download is cached under
// (path.Base; the query string, when present, is dropped).
func ffmpegArchiveName(url string) string {
	u := url
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return path.Base(u)
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func ffmpegMB(n int64) float64 { return float64(n) / (1 << 20) }

func isResumable(err error) bool {
	var re *resumableError
	return errors.As(err, &re)
}

func unwrapResumable(err error) error {
	var re *resumableError
	if errors.As(err, &re) {
		return re.err
	}
	return err
}

// bodySnippet returns a short, printable excerpt of an error response body for
// the failure line (proxies love to return HTML there).
func bodySnippet(r io.Reader) string {
	buf := make([]byte, 200)
	n, _ := io.ReadFull(r, buf)
	s := strings.TrimSpace(string(buf[:n]))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	return "（" + s + "）"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
