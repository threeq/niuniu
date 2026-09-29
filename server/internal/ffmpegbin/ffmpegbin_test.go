package ffmpegbin

// These tests are fully offline: they never download or execute ffmpeg. They
// build fake binaries under temporary data dirs / PATH entries and assert the
// resolution order (env override → installed copy → PATH).
//
// Test names start with "Ffmpeg" on purpose: the repository's acceptance
// command filters this package with -run 'FFmpeg|Ffmpeg|SystemDeps'.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeBin is a tiny stand-in for a static ffmpeg binary. It is never executed
// — these tests only check that resolution picks the file up.
func fakeBin(tag string) []byte { return []byte("fake-bin:" + tag + "\n") }

// writeExec writes an executable file (0755; the mode is meaningless on
// Windows but harmless) creating parent dirs as needed.
func writeExec(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// isolateEnv clears the env overrides and empties PATH so the host's real
// ffmpeg can never leak into an assertion.
func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envFFmpeg, "")
	t.Setenv(envFFprobe, "")
	t.Setenv("PATH", t.TempDir())
}

// fakeOnPath creates a temp dir holding fake executables named like tools and
// prepends it to PATH.
func fakeOnPath(t *testing.T, tools ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		writeExec(t, filepath.Join(dir, BinName(runtime.GOOS, tool)), "fake-path-"+tool)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func sameFile(t *testing.T, got, want string) {
	t.Helper()
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat %s: %v", got, err)
	}
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat %s: %v", want, err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("resolved %s, want %s", got, want)
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s content = %q, want %q", path, got, want)
	}
}

// installFake places a fake tool under <dataDir>/bin/ffmpeg/<subdir>/ — the
// layout the in-app downloader produces — and returns the file path.
func installFake(t *testing.T, dataDir, subdir, tool, content string) string {
	t.Helper()
	return writeExec(t, filepath.Join(dataDir, "bin", "ffmpeg", subdir, BinName(runtime.GOOS, tool)), content)
}

func TestFfmpegBinName(t *testing.T) {
	for _, c := range []struct{ goos, tool, want string }{
		{"windows", "ffmpeg", "ffmpeg.exe"},
		{"windows", "ffprobe", "ffprobe.exe"},
		{"linux", "ffmpeg", "ffmpeg"},
		{"darwin", "ffprobe", "ffprobe"},
	} {
		if got := BinName(c.goos, c.tool); got != c.want {
			t.Errorf("BinName(%q, %q) = %q, want %q", c.goos, c.tool, got, c.want)
		}
	}
}

func TestFfmpegInstallRoot(t *testing.T) {
	got := InstallRoot(filepath.Join("base"), "windows", "amd64")
	want := filepath.Join("base", "bin", "ffmpeg", "windows-amd64")
	if got != want {
		t.Errorf("InstallRoot = %q, want %q", got, want)
	}
}

func TestFfmpegResolveEnvOverrideWins(t *testing.T) {
	isolateEnv(t)
	dataDir := t.TempDir()
	customFFmpeg := writeExec(t, filepath.Join(t.TempDir(), "custom-ffmpeg"), "custom-ffmpeg")
	customFFprobe := writeExec(t, filepath.Join(t.TempDir(), "custom-ffprobe"), "custom-ffprobe")
	// Lower-priority sources that must lose:
	installFake(t, dataDir, "linux-amd64", "ffmpeg", "installed-ffmpeg")
	installFake(t, dataDir, "linux-amd64", "ffprobe", "installed-ffprobe")
	fakeOnPath(t, "ffmpeg", "ffprobe")

	t.Setenv(envFFmpeg, customFFmpeg)
	t.Setenv(envFFprobe, customFFprobe)

	got, err := Resolve(dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != customFFmpeg {
		t.Fatalf("Resolve = %q, want the %s override %q", got, envFFmpeg, customFFmpeg)
	}
	probe, err := ResolveFFprobe(dataDir)
	if err != nil {
		t.Fatalf("ResolveFFprobe: %v", err)
	}
	if probe != customFFprobe {
		t.Fatalf("ResolveFFprobe = %q, want the %s override %q", probe, envFFprobe, customFFprobe)
	}
}

func TestFfmpegResolveBrokenEnvOverrideFailsLoudly(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		isolateEnv(t)
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		t.Setenv(envFFmpeg, missing)
		// An installed copy exists but the broken override must not fall back.
		dataDir := t.TempDir()
		installFake(t, dataDir, "linux-amd64", "ffmpeg", "installed")

		_, err := Resolve(dataDir)
		if err == nil {
			t.Fatal("Resolve accepted a missing NIUNIU_FFMPEG override")
		}
		if !errors.Is(err, ErrNotAvailable) {
			t.Errorf("error %v does not wrap ErrNotAvailable", err)
		}
		if !strings.Contains(err.Error(), envFFmpeg) {
			t.Errorf("error %q does not name %s", err, envFFmpeg)
		}
	})
	t.Run("not executable", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows has no exec bit")
		}
		isolateEnv(t)
		path := filepath.Join(t.TempDir(), "ffmpeg")
		if err := os.WriteFile(path, fakeBin("no-exec"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envFFmpeg, path)
		_, err := Resolve(t.TempDir())
		if err == nil {
			t.Fatal("Resolve accepted a non-executable NIUNIU_FFMPEG override")
		}
		if !errors.Is(err, ErrNotAvailable) {
			t.Errorf("error %v does not wrap ErrNotAvailable", err)
		}
	})
}

func TestFfmpegResolveInstalledCopy(t *testing.T) {
	isolateEnv(t)
	dataDir := t.TempDir()
	want := installFake(t, dataDir, "windows-amd64", "ffmpeg", "installed-ffmpeg")

	got, err := Resolve(dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sameFile(t, got, want)
	assertContent(t, got, "installed-ffmpeg")
	if !Available(dataDir) {
		t.Error("Available = false with an installed copy present")
	}
}

// The probe/installer keep several platform dirs side by side after a
// re-install; resolution must pick the newest file, not an arbitrary entry.
func TestFfmpegResolveInstalledCopyNewestWins(t *testing.T) {
	isolateEnv(t)
	dataDir := t.TempDir()
	old := installFake(t, dataDir, "windows-amd64", "ffmpeg", "old")
	newer := installFake(t, dataDir, "windows-amd64-2", "ffmpeg", "newer")
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sameFile(t, got, newer)
	assertContent(t, got, "newer")

	// Flip the mtimes: now the other copy is the newest and must win.
	newTime := time.Now()
	if err := os.Chtimes(old, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(newer, older, older); err != nil {
		t.Fatal(err)
	}
	got, err = Resolve(dataDir)
	if err != nil {
		t.Fatalf("Resolve after mtime flip: %v", err)
	}
	sameFile(t, got, old)
	assertContent(t, got, "old")
}

func TestFfmpegResolveInstalledCopySkipsNonExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no exec bit")
	}
	isolateEnv(t)
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "bin", "ffmpeg", "linux-amd64", BinName(runtime.GOOS, "ffmpeg"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fakeBin("no-exec"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathDir := fakeOnPath(t, "ffmpeg")

	got, err := Resolve(dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sameFile(t, got, filepath.Join(pathDir, BinName(runtime.GOOS, "ffmpeg")))
}

func TestFfmpegResolveFallsBackToPath(t *testing.T) {
	isolateEnv(t)
	dir := fakeOnPath(t, "ffmpeg")
	got, err := Resolve(t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sameFile(t, got, filepath.Join(dir, BinName(runtime.GOOS, "ffmpeg")))
}

func TestFfmpegResolveEmptyDataDirSkipsInstalledScan(t *testing.T) {
	isolateEnv(t)
	pathDir := fakeOnPath(t, "ffmpeg")
	// Resolve("") must not consult any data dir — it can only answer from PATH.
	got, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sameFile(t, got, filepath.Join(pathDir, BinName(runtime.GOOS, "ffmpeg")))

	if p := InstalledPath("", runtime.GOOS, "ffmpeg"); p != "" {
		t.Errorf("InstalledPath(\"\") = %q, want empty", p)
	}
}

// ffmpeg and ffprobe resolve independently: a partially installed ffmpeg must
// not hide an ffprobe that lives on PATH (and vice versa).
func TestFfmpegResolveIndependentTools(t *testing.T) {
	isolateEnv(t)
	dataDir := t.TempDir()
	installed := installFake(t, dataDir, "windows-amd64", "ffmpeg", "installed-ffmpeg")
	pathDir := fakeOnPath(t, "ffprobe")

	got, err := Resolve(dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sameFile(t, got, installed)

	probe, err := ResolveFFprobe(dataDir)
	if err != nil {
		t.Fatalf("ResolveFFprobe: %v", err)
	}
	sameFile(t, probe, filepath.Join(pathDir, BinName(runtime.GOOS, "ffprobe")))
}

func TestFfmpegResolveUnavailable(t *testing.T) {
	isolateEnv(t)
	// PATH is an empty dir; the LookPath probe also covers the Windows
	// current-directory search, so a host that genuinely provides ffmpeg skips
	// instead of failing.
	if found, err := exec.LookPath("ffmpeg"); err == nil {
		t.Skipf("host provides ffmpeg at %s; nothing-available path not exercisable", found)
	}
	dataDir := t.TempDir()
	_, err := Resolve(dataDir)
	if !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("Resolve error = %v, want ErrNotAvailable", err)
	}
	// The not-found error must be actionable: point at the Settings page where
	// ffmpeg can be downloaded.
	if !strings.Contains(err.Error(), "系统依赖") {
		t.Errorf("not-found error does not point at 设置 → 系统依赖: %v", err)
	}
	if _, err := ResolveFFprobe(dataDir); !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("ResolveFFprobe error = %v, want ErrNotAvailable", err)
	}
	if Available(dataDir) {
		t.Error("Available = true with no override, no installed copy and an empty PATH")
	}
}
