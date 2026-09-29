package service

// In-app ffmpeg download tests (design v3.2 §7.3). Fully offline: every
// archive is served by an httptest server and the platform matrix is repointed
// at it, so CI never downloads from GitHub/osxexperts (hundreds of MB) and an
// upstream outage can never turn these red.
//
// Test names carry the "FFmpeg" spelling on purpose: the repository's
// acceptance command filters this package with -run 'FFmpeg|Ffmpeg|SystemDeps'.

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/ffmpegbin"
)

// --- fixtures ---

// incompressibleBytes returns n pseudo-random bytes. Randomness matters: a
// patterned payload would deflate to nothing and the archive would fall under
// the 1 MiB size gate, so the fixtures would never exercise the happy path.
// The seed keeps each tool's payload distinct and the test deterministic.
func incompressibleBytes(t *testing.T, n int, seed int64) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.New(rand.NewSource(seed)).Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// zipWithEntries builds an in-memory zip (paths use forward slashes, like the
// real BtbN/osxexperts archives).
func zipWithEntries(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bothToolZip shapes a BtbN archive: <root>/bin/ffmpeg + <root>/bin/ffprobe.
func bothToolZip(t *testing.T, goos string, payload int, includeFFprobe bool) (archive, ffmpegBin, ffprobeBin []byte) {
	t.Helper()
	ffmpegBin = incompressibleBytes(t, payload, 1)
	ffprobeBin = incompressibleBytes(t, payload, 2)
	root := "ffmpeg-master-latest-" + goos + "-gpl"
	entries := map[string][]byte{
		root + "/bin/" + ffmpegbin.BinName(goos, "ffmpeg"): ffmpegBin,
		root + "/LICENSE.txt":                              []byte("GPLv3\n"),
	}
	if includeFFprobe {
		entries[root+"/bin/"+ffmpegbin.BinName(goos, "ffprobe")] = ffprobeBin
	}
	return zipWithEntries(t, entries), ffmpegBin, ffprobeBin
}

// --- HTTP fixtures ---

// rangeAwareHandler serves body as a static file WITH Range support (206 +
// Content-Range), recording every Range header it sees — the shape of a
// well-behaved CDN/GitHub release host.
func rangeAwareHandler(body []byte, seen *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rh := r.Header.Get("Range"); rh != "" {
			*seen = append(*seen, rh)
			var start int64
			fmt.Sscanf(strings.TrimPrefix(rh, "bytes="), "%d-", &start)
			if start > 0 && start < int64(len(body)) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
				w.Header().Set("Content-Length", strconv.Itoa(len(body)-int(start)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(body[start:])
				return
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	}
}

// staticBytesHandler always answers 200 with the full body — the "server
// ignores Range" case — while still recording Range headers.
func staticBytesHandler(body []byte, seen *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rh := r.Header.Get("Range"); rh != "" && seen != nil {
			*seen = append(*seen, rh)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	}
}

// --- service fixture ---

// newFFmpegTestService returns a service pinned to a temp data dir, with the
// (goos, goarch) platform's download source repointed at urls (nil removes the
// platform entirely — the unsupported-platform case). lookPath fails for
// everything so no probe can reach the host's real tools, and the HTTP client
// is proxy-less so a test host with HTTP_PROXY set cannot route the httptest
// URL through it and hang.
func newFFmpegTestService(t *testing.T, goos, goarch string, urls []string, kind ffmpegArchiveKind, layout ffmpegLayout) *SystemDepsService {
	t.Helper()
	svc := NewSystemDepsService()
	svc.goos, svc.goarch = goos, goarch
	svc.dataDir = t.TempDir()
	svc.httpClient = &http.Client{Transport: &http.Transport{}}
	svc.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	key := goos + "-" + goarch
	prev, had := ffmpegSources[key]
	if urls == nil {
		delete(ffmpegSources, key)
	} else {
		ffmpegSources[key] = ffmpegSource{urls: urls, layout: layout, kind: kind, label: "test-build", approxMB: 2}
	}
	t.Cleanup(func() {
		if had {
			ffmpegSources[key] = prev
		} else {
			delete(ffmpegSources, key)
		}
	})
	return svc
}

func newTestInstallJob() *installJob {
	return &installJob{id: "ffmpeg-test-job", tool: "ffmpeg", subs: map[chan InstallEvent]struct{}{}}
}

// jobLines is the job's published history, newline-joined (safe to call after
// runFFmpegDownload returned; it locks regardless, so a late call cannot race).
func jobLines(job *installJob) string {
	job.mu.Lock()
	defer job.mu.Unlock()
	out := make([]string, 0, len(job.history))
	for _, evt := range job.history {
		if evt.Line != "" {
			out = append(out, evt.Line)
		}
	}
	return strings.Join(out, "\n")
}

// collectInstallLines subscribes to an Install job and drains it, returning the
// streamed lines and the final exit code.
func collectInstallLines(t *testing.T, svc *SystemDepsService, jobID string) (string, int) {
	t.Helper()
	ch, unsub, err := svc.Subscribe(jobID)
	if err != nil {
		t.Fatalf("Subscribe(%q): %v", jobID, err)
	}
	defer unsub()
	var lines []string
	exit := -1
	deadline := time.After(30 * time.Second)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return strings.Join(lines, "\n"), exit
			}
			if evt.Line != "" {
				lines = append(lines, evt.Line)
			}
			if evt.Done {
				exit = evt.ExitCode
			}
		case <-deadline:
			t.Fatalf("timeout waiting for install to finish; lines so far:\n%s", strings.Join(lines, "\n"))
		}
	}
}

func ffmpegPartPathFor(dataDir, rawURL string) string {
	return filepath.Join(dataDir, "cache", "downloads", ffmpegArchiveName(rawURL)+ffmpegPartSuffix)
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: content mismatch (%d bytes, want %d)", path, len(got), len(want))
	}
}

func assertNoStagingLeft(t *testing.T, dataDir string) {
	t.Helper()
	parent := filepath.Join(dataDir, "bin", "ffmpeg")
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Errorf("leftover staging dir after install: %s", filepath.Join(parent, e.Name()))
		}
	}
}

// --- tests ---

// ① fresh download: archive → unpack → install, no resume, no leftovers.
func TestFFmpegDownloadFreshInstall(t *testing.T) {
	body, ffmpegBin, ffprobeBin := bothToolZip(t, "linux", 700<<10, true)
	var ranges []string
	srv := httptest.NewServer(rangeAwareHandler(body, &ranges))
	defer srv.Close()
	url := srv.URL + "/ffmpeg-master-latest-linux64-gpl.tar.xz"

	svc := newFFmpegTestService(t, "linux", "amd64", []string{url}, ffmpegArchiveZip, ffmpegLayoutBoth)
	job := newTestInstallJob()
	if code := svc.runFFmpegDownload(context.Background(), job); code != 0 {
		t.Fatalf("runFFmpegDownload exit=%d, want 0; lines:\n%s", code, jobLines(job))
	}

	if len(ranges) != 0 {
		t.Errorf("a fresh download must not send a Range header, got %v", ranges)
	}
	root := ffmpegbin.InstallRoot(svc.dataDir, "linux", "amd64")
	assertFileBytes(t, filepath.Join(root, "ffmpeg"), ffmpegBin)
	assertFileBytes(t, filepath.Join(root, "ffprobe"), ffprobeBin)
	// The install dir holds exactly the two binaries (no doc trees, no staging).
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("install dir should hold exactly ffmpeg+ffprobe, got %d entries", len(entries))
	}
	if runtime.GOOS != "windows" {
		for _, tool := range []string{"ffmpeg", "ffprobe"} {
			st, err := os.Stat(filepath.Join(root, tool))
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm()&0o111 == 0 {
				t.Errorf("%s installed without an exec bit (mode %04o)", tool, st.Mode().Perm())
			}
		}
	}
	if _, err := os.Stat(ffmpegPartPathFor(svc.dataDir, url)); !os.IsNotExist(err) {
		t.Errorf(".part file still present after a successful install (%v)", err)
	}
	assertNoStagingLeft(t, svc.dataDir)
	lines := jobLines(job)
	for _, want := range []string{"应用内下载 ffmpeg", "下载完成", "ffmpeg 安装完成"} {
		if !strings.Contains(lines, want) {
			t.Errorf("missing %q in stream:\n%s", want, lines)
		}
	}
}

// ② resume: a half-written .part must be continued with `Range: bytes=N-`,
// append the remainder and still verify as the complete archive.
func TestFFmpegDownloadResumesWithRange(t *testing.T) {
	body, ffmpegBin, ffprobeBin := bothToolZip(t, "linux", 700<<10, true)
	half := int64(len(body) / 2)
	var ranges []string
	srv := httptest.NewServer(rangeAwareHandler(body, &ranges))
	defer srv.Close()
	url := srv.URL + "/ffmpeg-master-latest-linux64-gpl.tar.xz"

	svc := newFFmpegTestService(t, "linux", "amd64", []string{url}, ffmpegArchiveZip, ffmpegLayoutBoth)
	part := ffmpegPartPathFor(svc.dataDir, url)
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, body[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	job := newTestInstallJob()
	if code := svc.runFFmpegDownload(context.Background(), job); code != 0 {
		t.Fatalf("runFFmpegDownload exit=%d, want 0; lines:\n%s", code, jobLines(job))
	}
	want := fmt.Sprintf("bytes=%d-", half)
	if len(ranges) != 1 || ranges[0] != want {
		t.Fatalf("Range headers = %v, want exactly [%s]", ranges, want)
	}
	lines := jobLines(job)
	if !strings.Contains(lines, "从断点继续") {
		t.Errorf("resume was not announced:\n%s", lines)
	}
	root := ffmpegbin.InstallRoot(svc.dataDir, "linux", "amd64")
	assertFileBytes(t, filepath.Join(root, "ffmpeg"), ffmpegBin)
	assertFileBytes(t, filepath.Join(root, "ffprobe"), ffprobeBin)
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf(".part file still present after a successful install (%v)", err)
	}
}

// ③ the server ignores Range and answers 200 with the full body: the download
// must restart from zero (never append a second copy onto the .part).
func TestFFmpegDownloadServerIgnoresRangeRestarts(t *testing.T) {
	body, ffmpegBin, ffprobeBin := bothToolZip(t, "linux", 700<<10, true)
	half := int64(len(body) / 2)
	var seen []string
	srv := httptest.NewServer(staticBytesHandler(body, &seen))
	defer srv.Close()
	url := srv.URL + "/ffmpeg-master-latest-linux64-gpl.tar.xz"

	svc := newFFmpegTestService(t, "linux", "amd64", []string{url}, ffmpegArchiveZip, ffmpegLayoutBoth)
	part := ffmpegPartPathFor(svc.dataDir, url)
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, body[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	job := newTestInstallJob()
	if code := svc.runFFmpegDownload(context.Background(), job); code != 0 {
		t.Fatalf("runFFmpegDownload exit=%d, want 0; lines:\n%s", code, jobLines(job))
	}
	// The resume attempt must have been made (that is what makes the 200 case
	// interesting), and the result must be the archive exactly once: a doubled
	// file would fail zip parsing or extraction content checks.
	if len(seen) == 0 {
		t.Error("expected a Range request before the server ignored it")
	}
	root := ffmpegbin.InstallRoot(svc.dataDir, "linux", "amd64")
	assertFileBytes(t, filepath.Join(root, "ffmpeg"), ffmpegBin)
	assertFileBytes(t, filepath.Join(root, "ffprobe"), ffprobeBin)
	if got := fileSize(part); got != 0 {
		t.Errorf(".part file must be gone after success, has %d bytes", got)
	}
}

// ④ the size gate: a completed transfer smaller than 1 MiB is an error page /
// captive proxy, not an archive — reject it, clean up, and say something the
// user can act on.
func TestFFmpegDownloadSizeGateRejectsErrorPage(t *testing.T) {
	page := []byte("<html><body><h1>403 Forbidden</h1><p>corporate proxy</p></body></html>")
	srv := httptest.NewServer(staticBytesHandler(page, nil))
	defer srv.Close()
	url := srv.URL + "/ffmpeg-master-latest-win64-gpl.zip"

	svc := newFFmpegTestService(t, "windows", "amd64", []string{url}, ffmpegArchiveZip, ffmpegLayoutBoth)
	job := newTestInstallJob()
	if code := svc.runFFmpegDownload(context.Background(), job); code == 0 {
		t.Fatalf("a %d-byte error page must not install; lines:\n%s", len(page), jobLines(job))
	}
	lines := jobLines(job)
	if !strings.Contains(lines, "下载内容异常") {
		t.Errorf("missing size-gate diagnosis:\n%s", lines)
	}
	if !strings.Contains(lines, "HTTP_PROXY") {
		t.Errorf("failure should carry the proxy/manual hint:\n%s", lines)
	}
	if got := fileSize(ffmpegPartPathFor(svc.dataDir, url)); got != 0 {
		t.Errorf("rejected body must not be kept for resume, .part has %d bytes", got)
	}
	if _, err := os.Stat(ffmpegbin.InstallRoot(svc.dataDir, "windows", "amd64")); !os.IsNotExist(err) {
		t.Errorf("nothing may be installed from a rejected body (%v)", err)
	}
}

// ⑤ archive unpacks but lacks a component (upstream layout changed / partial
// content): fail with a clear per-component error instead of installing a
// half-useful pair.
func TestFFmpegDownloadMissingComponentFailsClearly(t *testing.T) {
	// Single-tool zip, and the payload alone must clear the size gate.
	body, _, _ := bothToolZip(t, "linux", 1<<20+200<<10, false)
	srv := httptest.NewServer(staticBytesHandler(body, nil))
	defer srv.Close()
	url := srv.URL + "/ffmpeg-master-latest-linux64-gpl.tar.xz"

	svc := newFFmpegTestService(t, "linux", "amd64", []string{url}, ffmpegArchiveZip, ffmpegLayoutBoth)
	job := newTestInstallJob()
	if code := svc.runFFmpegDownload(context.Background(), job); code == 0 {
		t.Fatalf("an archive without ffprobe must not install; lines:\n%s", jobLines(job))
	}
	lines := jobLines(job)
	if !strings.Contains(lines, "缺少 ffprobe") {
		t.Errorf("missing per-component diagnosis:\n%s", lines)
	}
	if got := fileSize(ffmpegPartPathFor(svc.dataDir, url)); got != 0 {
		t.Errorf(".part should be cleaned after an unpack-level failure, has %d bytes", got)
	}
	if _, err := os.Stat(ffmpegbin.InstallRoot(svc.dataDir, "linux", "amd64")); !os.IsNotExist(err) {
		t.Errorf("a component-less archive must not create the install dir (%v)", err)
	}
}

// ⑥ the per-tool layout (darwin): two archives, one binary each at the archive
// root — both tools must land, and both .part files must be cleaned up.
func TestFFmpegDownloadPerToolLayout(t *testing.T) {
	ffmpegBin := incompressibleBytes(t, 1<<20+100<<10, 1)
	ffprobeBin := incompressibleBytes(t, 1<<20+100<<10, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("/ffmpeg71arm.zip", staticBytesHandler(zipWithEntries(t, map[string][]byte{"ffmpeg": ffmpegBin}), nil))
	mux.HandleFunc("/ffprobe71arm.zip", staticBytesHandler(zipWithEntries(t, map[string][]byte{"ffprobe": ffprobeBin}), nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	urls := []string{srv.URL + "/ffmpeg71arm.zip", srv.URL + "/ffprobe71arm.zip"}

	svc := newFFmpegTestService(t, "darwin", "arm64", urls, ffmpegArchiveZip, ffmpegLayoutPerTool)
	job := newTestInstallJob()
	if code := svc.runFFmpegDownload(context.Background(), job); code != 0 {
		t.Fatalf("runFFmpegDownload exit=%d, want 0; lines:\n%s", code, jobLines(job))
	}
	root := ffmpegbin.InstallRoot(svc.dataDir, "darwin", "arm64")
	assertFileBytes(t, filepath.Join(root, "ffmpeg"), ffmpegBin)
	assertFileBytes(t, filepath.Join(root, "ffprobe"), ffprobeBin)
	for _, u := range urls {
		if got := fileSize(ffmpegPartPathFor(svc.dataDir, u)); got != 0 {
			t.Errorf(".part for %s not cleaned (%d bytes)", u, got)
		}
	}
}

// ⑦ the Install carve-out: with no package manager at all, Install must still
// start a download job (not bounce to the docs URL) and the streamed job must
// finish 0 — the whole reason ffmpeg does not ride the PM gate.
func TestInstallFFmpegDownloadsWithoutPackageManager(t *testing.T) {
	t.Setenv("NIUNIU_EDITION", "")
	body, ffmpegBin, ffprobeBin := bothToolZip(t, "linux", 700<<10, true)
	srv := httptest.NewServer(staticBytesHandler(body, nil))
	defer srv.Close()

	svc := newFFmpegTestService(t, "linux", "amd64", []string{srv.URL + "/pkg.zip"}, ffmpegArchiveZip, ffmpegLayoutBoth)
	if pm := svc.detectPackageManager(); pm != "" {
		t.Fatalf("fixture must have no package manager, got %q", pm)
	}

	jobID, fallback, err := svc.Install(context.Background(), "ffmpeg")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if fallback != "" {
		t.Fatalf("ffmpeg must be installed in-app, not redirected to %q", fallback)
	}
	if jobID == "" {
		t.Fatal("Install returned no job id for ffmpeg")
	}
	lines, exit := collectInstallLines(t, svc, jobID)
	if exit != 0 {
		t.Fatalf("install exit=%d, want 0; lines:\n%s", exit, lines)
	}
	if !strings.Contains(lines, "应用内下载 ffmpeg") || !strings.Contains(lines, "支持断点续传") {
		t.Errorf("stream should open with the in-app download banner:\n%s", lines)
	}
	if strings.Contains(lines, "$ ") {
		t.Errorf("ffmpeg runs no shell command; no `$ cmd` banner expected:\n%s", lines)
	}
	root := ffmpegbin.InstallRoot(svc.dataDir, "linux", "amd64")
	assertFileBytes(t, filepath.Join(root, "ffmpeg"), ffmpegBin)
	assertFileBytes(t, filepath.Join(root, "ffprobe"), ffprobeBin)
	// The whole point of ffmpegbin.InstalledPath: the app's own install lands
	// OFF PATH, so the probe must still see it after the download.
	if p := ffmpegbin.InstalledPath(svc.dataDir, "linux", "ffmpeg"); p == "" {
		t.Error("ffmpegbin.InstalledPath does not see the freshly installed copy")
	}
}

// ⑧ no download source for this platform → the docs URL, no job. Covers the
// "exotic OS/arch" path and pins that fallbackURLs has an ffmpeg entry (a
// missing entry would hand the SPA an empty string and it would open an SSE
// stream with an undefined job id).
func TestInstallFFmpegUnsupportedPlatformFallsBack(t *testing.T) {
	t.Setenv("NIUNIU_EDITION", "")
	svc := newFFmpegTestService(t, "plan9", "amd64", nil, ffmpegArchiveZip, ffmpegLayoutBoth)

	jobID, fallback, err := svc.Install(context.Background(), "ffmpeg")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if jobID != "" {
		t.Fatalf("unsupported platform must not start a job, got %q", jobID)
	}
	if fallback != "https://ffmpeg.org/download.html" {
		t.Fatalf("fallback URL = %q, want the ffmpeg.org download page", fallback)
	}
	// Same platform through the probe: not found, and not installable — the
	// row carries no install affordance without a download source.
	tool := findTool(t, svc.Probe(context.Background()), "ffmpeg")
	if tool.Found || tool.Installable {
		t.Errorf("unsupported platform row = %+v, want found=false installable=false", tool)
	}
}

// ⑨ the probe sees an in-app install (off-PATH) and reports the version from
// `ffmpeg -version`'s first line, delivered through the standard ToolStatus
// shape (no new fields).
func TestFFmpegProbeFindsInAppInstall(t *testing.T) {
	// Empty (non-nil) urls: the platform is present so the row stays
	// installable, but nothing here ever runs a download.
	svc := newFFmpegTestService(t, "linux", "amd64", []string{}, ffmpegArchiveZip, ffmpegLayoutBoth)
	root := ffmpegbin.InstallRoot(svc.dataDir, "linux", "amd64")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(root, "ffmpeg")
	if err := os.WriteFile(installed, []byte("not a real binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc.runFFmpegVersion = func(ctx context.Context, name string) (string, error) {
		if name != installed {
			t.Errorf("version probe ran %q, want the installed copy %q", name, installed)
		}
		return "ffmpeg version 7.1-static Copyright (c) 2000-2024 the FFmpeg developers\nbuilt with gcc\n", nil
	}

	status := svc.probeOne(context.Background(), "ffmpeg")
	if !status.Found {
		t.Fatalf("probe did not find the in-app install: %+v", status)
	}
	if status.Path != installed {
		t.Errorf("probe path = %q, want %q", status.Path, installed)
	}
	if !strings.HasPrefix(status.Version, "ffmpeg version 7.1-static") {
		t.Errorf("probe version = %q, want the first -version line", status.Version)
	}

	info := svc.Probe(context.Background())
	tool := findTool(t, info, "ffmpeg")
	if !tool.Found || !tool.Installable {
		t.Errorf("probe row = %+v, want found+installable on linux/amd64", tool)
	}
}

// ⑩ the progress line is throttled (≥1 s or ≥1%): small early writes stay
// silent, a 1% step speaks, and the rate is reported in MB/s.
func TestFFmpegProgressThrottling(t *testing.T) {
	job := newTestInstallJob()
	now := time.Now()
	p := &ffmpegProgress{job: job, total: 1000, start: now, lastAt: now}

	write := func(n int) {
		t.Helper()
		if _, err := p.Write(make([]byte, n)); err != nil {
			t.Fatal(err)
		}
	}
	published := func() int {
		job.mu.Lock()
		defer job.mu.Unlock()
		return len(job.history)
	}

	write(1) // 0.1%, same second → dropped
	if got := published(); got != 0 {
		t.Fatalf("0.1%% in the same second must be throttled, got %d lines", got)
	}
	write(20) // 2.1% → emitted
	if got := published(); got != 1 {
		t.Fatalf("a 1%% step must publish, got %d lines", got)
	}
	write(8) // +0.8%, same second → dropped
	if got := published(); got != 1 {
		t.Fatalf("a sub-1%% same-second write must be throttled, got %d lines", got)
	}
	write(20) // +2% → emitted
	if got := published(); got != 2 {
		t.Fatalf("a further 1%% step must publish, got %d lines", got)
	}

	job.mu.Lock()
	line := job.history[1].Line
	job.mu.Unlock()
	for _, want := range []string{"进度", "MB/s", "0.0"} {
		if !strings.Contains(line, want) {
			t.Errorf("progress line %q missing %q", line, want)
		}
	}
}

// parseContentRange underpins the resume decision (206 at the wrong offset must
// restart, not append), so its parsing gets its own small table.
func TestFFmpegParseContentRange(t *testing.T) {
	for _, c := range []struct {
		header    string
		start, sz int64
	}{
		{"bytes 100-199/200", 100, 200},
		{"bytes 0-9/*", 0, 0},
		{"bytes 5-/10", 5, 10},
		{"", 0, 0},
		{"items 0-9/10", 0, 0},
		{"bytes garbage", 0, 0},
	} {
		start, size := parseContentRange(c.header)
		if start != c.start || size != c.sz {
			t.Errorf("parseContentRange(%q) = (%d, %d), want (%d, %d)", c.header, start, size, c.start, c.sz)
		}
	}
}
