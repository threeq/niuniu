package ffmpegbin

// These tests are fully offline: they never download or execute ffmpeg. Where
// the real payload would be embedded, withPayload() installs a map of fake
// binary bytes instead, so extraction, fingerprinting, idempotence, env
// overrides and PATH fallback are all exercised without a real ffmpeg present.
//
// Tests that swap the payload must not call t.Parallel (the payload and its
// caches are package-level).

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeBin is a tiny stand-in for a static ffmpeg binary. It is never executed
// — these tests only check that the bytes are copied and the file is marked
// executable.
func fakeBin(tag string) []byte { return []byte("fake-bin:" + tag + "\n") }

// resetCaches clears the memoised payload and fingerprint so a fake payload
// installed by a test takes effect.
func resetCaches() {
	payloadOnce = sync.Once{}
	payloadCache = nil
	fpOnce = sync.Once{}
	fpValue = ""
}

// withPayload installs a fake embedded payload (nil = none) for the duration of
// fn and clears the caches before and after. Tests using it must not call
// t.Parallel.
func withPayload(t *testing.T, files map[string][]byte, fn func()) {
	t.Helper()
	original := embeddedPayload
	embeddedPayload = func() map[string][]byte { return files }
	resetCaches()
	defer func() {
		embeddedPayload = original
		resetCaches()
	}()
	fn()
}

// fpOf computes the fingerprint a fresh build with the given payload reports.
func fpOf(t *testing.T, files map[string][]byte) string {
	t.Helper()
	var fp string
	withPayload(t, files, func() { fp = Fingerprint() })
	return fp
}

// fakeOnPath creates a temp dir holding fake executables named like tools and
// prepends it to PATH.
func fakeOnPath(t *testing.T, tools ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		if err := os.WriteFile(filepath.Join(dir, binName(tool)), fakeBin("path-"+tool), 0o755); err != nil {
			t.Fatal(err)
		}
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

func assertContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s content = %q, want %q", path, got, want)
	}
}

func TestBinNameFor(t *testing.T) {
	for _, c := range []struct{ goos, tool, want string }{
		{"windows", "ffmpeg", "ffmpeg.exe"},
		{"windows", "ffprobe", "ffprobe.exe"},
		{"linux", "ffmpeg", "ffmpeg"},
		{"darwin", "ffprobe", "ffprobe"},
	} {
		if got := binNameFor(c.goos, c.tool); got != c.want {
			t.Errorf("binNameFor(%q, %q) = %q, want %q", c.goos, c.tool, got, c.want)
		}
	}
	if got, want := binName("ffmpeg"), binNameFor(runtime.GOOS, "ffmpeg"); got != want {
		t.Errorf("binName(ffmpeg) = %q, running platform wants %q", got, want)
	}
}

func TestPlatformDir(t *testing.T) {
	for _, c := range []struct{ goos, goarch, want string }{
		{"windows", "amd64", "windows-amd64"},
		{"linux", "arm64", "linux-arm64"},
		{"darwin", "arm64", "darwin-arm64"},
	} {
		if got := platformDir(c.goos, c.goarch); got != c.want {
			t.Errorf("platformDir(%q, %q) = %q, want %q", c.goos, c.goarch, got, c.want)
		}
	}
}

func TestFingerprintEmptyWithoutPayload(t *testing.T) {
	if len(payload()) != 0 {
		t.Skip("build embeds a real payload; stub-only assertion")
	}
	withPayload(t, nil, func() {
		if fp := Fingerprint(); fp != "" {
			t.Fatalf("Fingerprint() = %q with no payload, want \"\"", fp)
		}
	})
}

// TestEmbeddedPayloadWhenStaged runs for real only in a build made with the
// ffmpeg_bundled tag over a staged dist/ tree (the release pipeline's shape);
// plain dev builds and `go test` without the tag skip it. It exercises the
// real embed -> payload() -> Fingerprint -> extract chain.
func TestEmbeddedPayloadWhenStaged(t *testing.T) {
	files := payload()
	if len(files) == 0 {
		t.Skip("no staged payload in this build")
	}
	if Fingerprint() == "" {
		t.Error("Fingerprint() empty despite a staged payload")
	}
	if _, ok := files[binName("ffmpeg")]; !ok {
		t.Errorf("staged payload has no %s (keys: %v)", binName("ffmpeg"), sortedNames(files))
	}
	for _, name := range sortedNames(files) {
		if len(files[name]) == 0 {
			t.Errorf("staged payload entry %s is empty", name)
		}
	}

	t.Setenv(envFFmpeg, "")
	dataDir := t.TempDir()
	got, err := Resolve(dataDir)
	if err != nil {
		t.Fatalf("Resolve against the staged payload: %v", err)
	}
	if want := filepath.Join(dataDir, "bin", "ffmpeg", Fingerprint(), binName("ffmpeg")); got != want {
		t.Fatalf("Resolve = %q, want %q", got, want)
	}
	assertContent(t, got, files[binName("ffmpeg")])
}

func TestFingerprintStableAndContentSensitive(t *testing.T) {
	base := map[string][]byte{binName("ffmpeg"): fakeBin("a"), binName("ffprobe"): fakeBin("b")}
	changed := map[string][]byte{binName("ffmpeg"): fakeBin("a"), binName("ffprobe"): fakeBin("B")}
	swapped := map[string][]byte{binName("ffmpeg"): fakeBin("b"), binName("ffprobe"): fakeBin("a")}

	fp1 := fpOf(t, base)
	if fp1 == "" {
		t.Fatal("Fingerprint() empty with a payload")
	}
	if fp2 := fpOf(t, base); fp2 != fp1 {
		t.Errorf("Fingerprint() not deterministic: %q vs %q", fp1, fp2)
	}
	if fp := fpOf(t, changed); fp == fp1 {
		t.Errorf("content change did not change fingerprint (%q)", fp)
	}
	if fp := fpOf(t, swapped); fp == fp1 {
		t.Errorf("swapping file names did not change fingerprint (%q)", fp)
	}
}

func TestResolveExtractsBundledPayload(t *testing.T) {
	files := map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg"), binName("ffprobe"): fakeBin("ffprobe")}
	dataDir := t.TempDir()
	t.Setenv(envFFmpeg, "")
	t.Setenv(envFFprobe, "")
	withPayload(t, files, func() {
		fp := Fingerprint()
		dir := filepath.Join(dataDir, "bin", "ffmpeg", fp)

		got, err := Resolve(dataDir)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if want := filepath.Join(dir, binName("ffmpeg")); got != want {
			t.Fatalf("Resolve = %q, want %q", got, want)
		}
		assertContent(t, got, files[binName("ffmpeg")])

		probe, err := ResolveFFprobe(dataDir)
		if err != nil {
			t.Fatalf("ResolveFFprobe: %v", err)
		}
		if want := filepath.Join(dir, binName("ffprobe")); probe != want {
			t.Fatalf("ResolveFFprobe = %q, want %q", probe, want)
		}
		assertContent(t, probe, files[binName("ffprobe")])

		marker, err := os.ReadFile(filepath.Join(dir, markerFile))
		if err != nil {
			t.Fatalf("read marker: %v", err)
		}
		if strings.TrimSpace(string(marker)) != fp {
			t.Fatalf("marker = %q, want fingerprint %q", marker, fp)
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		wantNames := append(sortedNames(files), markerFile)
		sort.Strings(wantNames) // os.ReadDir sorts by name; ".fp" sorts first
		if strings.Join(names, ",") != strings.Join(wantNames, ",") {
			t.Fatalf("extraction dir holds %v, want %v (temp files left behind?)", names, wantNames)
		}

		if runtime.GOOS != "windows" {
			st, err := os.Stat(got)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm()&0o111 == 0 {
				t.Errorf("%s mode %04o is not executable", got, st.Mode().Perm())
			}
		}

		if !Available(dataDir) {
			t.Error("Available = false with a fully extracted payload")
		}
	})
}

func TestResolveSkipsExtractionWhenMarkerMatches(t *testing.T) {
	files := map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg")}
	dataDir := t.TempDir()
	t.Setenv(envFFmpeg, "")
	withPayload(t, files, func() {
		first, err := Resolve(dataDir)
		if err != nil {
			t.Fatalf("first Resolve: %v", err)
		}
		// Tampering with the extracted file must survive a second Resolve:
		// that is the observable proof the marker made it skip extraction.
		tampered := []byte("tampered\n")
		if err := os.WriteFile(first, tampered, 0o755); err != nil {
			t.Fatal(err)
		}
		again, err := Resolve(dataDir)
		if err != nil {
			t.Fatalf("second Resolve: %v", err)
		}
		if again != first {
			t.Fatalf("second Resolve = %q, want %q", again, first)
		}
		assertContent(t, again, tampered)
	})
}

func TestResolveReExtractsWhenFileMissing(t *testing.T) {
	files := map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg"), binName("ffprobe"): fakeBin("ffprobe")}
	dataDir := t.TempDir()
	t.Setenv(envFFprobe, "")
	withPayload(t, files, func() {
		probe, err := ResolveFFprobe(dataDir)
		if err != nil {
			t.Fatalf("first ResolveFFprobe: %v", err)
		}
		if err := os.Remove(probe); err != nil {
			t.Fatal(err)
		}
		again, err := ResolveFFprobe(dataDir)
		if err != nil {
			t.Fatalf("second ResolveFFprobe: %v", err)
		}
		if again != probe {
			t.Fatalf("second ResolveFFprobe = %q, want %q", again, probe)
		}
		assertContent(t, again, files[binName("ffprobe")])
	})
}

func TestResolveReExtractsWhenMarkerMismatch(t *testing.T) {
	files := map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg")}
	dataDir := t.TempDir()
	t.Setenv(envFFmpeg, "")
	withPayload(t, files, func() {
		first, err := Resolve(dataDir)
		if err != nil {
			t.Fatalf("first Resolve: %v", err)
		}
		dir := filepath.Dir(first)
		if err := os.WriteFile(filepath.Join(dir, markerFile), []byte("stale-fingerprint"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(first, []byte("tampered\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		again, err := Resolve(dataDir)
		if err != nil {
			t.Fatalf("second Resolve: %v", err)
		}
		if again != first {
			t.Fatalf("second Resolve = %q, want %q", again, first)
		}
		assertContent(t, again, files[binName("ffmpeg")])
		marker, err := os.ReadFile(filepath.Join(dir, markerFile))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(marker)) != Fingerprint() {
			t.Fatalf("marker after re-extraction = %q, want %q", marker, Fingerprint())
		}
	})
}

func TestEnvOverrideWins(t *testing.T) {
	dir := t.TempDir()
	customFFmpeg := filepath.Join(dir, "custom-"+binName("ffmpeg"))
	customFFprobe := filepath.Join(dir, "custom-"+binName("ffprobe"))
	for _, p := range []string{customFFmpeg, customFFprobe} {
		if err := os.WriteFile(p, fakeBin("custom"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg"), binName("ffprobe"): fakeBin("ffprobe")}
	t.Setenv(envFFmpeg, customFFmpeg)
	t.Setenv(envFFprobe, customFFprobe)
	withPayload(t, files, func() {
		got, err := Resolve(t.TempDir())
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != customFFmpeg {
			t.Fatalf("Resolve = %q, want the %s override %q", got, envFFmpeg, customFFmpeg)
		}
		probe, err := ResolveFFprobe(t.TempDir())
		if err != nil {
			t.Fatalf("ResolveFFprobe: %v", err)
		}
		if probe != customFFprobe {
			t.Fatalf("ResolveFFprobe = %q, want the %s override %q", probe, envFFprobe, customFFprobe)
		}
	})
}

func TestBrokenEnvOverrideFailsLoudly(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		t.Setenv(envFFmpeg, missing)
		withPayload(t, map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg")}, func() {
			_, err := Resolve(t.TempDir())
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
	})
	t.Run("not executable", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows has no exec bit")
		}
		path := filepath.Join(t.TempDir(), "ffmpeg")
		if err := os.WriteFile(path, fakeBin("no-exec"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv(envFFmpeg, path)
		withPayload(t, map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg")}, func() {
			_, err := Resolve(t.TempDir())
			if err == nil {
				t.Fatal("Resolve accepted a non-executable NIUNIU_FFMPEG override")
			}
			if !errors.Is(err, ErrNotAvailable) {
				t.Errorf("error %v does not wrap ErrNotAvailable", err)
			}
		})
	})
}

func TestResolveFallsBackToPath(t *testing.T) {
	t.Setenv(envFFmpeg, "")
	dir := fakeOnPath(t, "ffmpeg")
	withPayload(t, nil, func() {
		got, err := Resolve(t.TempDir())
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		sameFile(t, got, filepath.Join(dir, binName("ffmpeg")))
	})
}

func TestResolveUnavailable(t *testing.T) {
	t.Setenv(envFFmpeg, "")
	t.Setenv(envFFprobe, "")
	// Replace PATH with an empty dir; the LookPath probe below also covers the
	// Windows current-directory search, so a host that genuinely provides
	// ffmpeg skips instead of failing.
	t.Setenv("PATH", t.TempDir())
	if found, err := exec.LookPath("ffmpeg"); err == nil {
		t.Skipf("host provides ffmpeg at %s; nothing-available path not exercisable", found)
	}
	withPayload(t, nil, func() {
		_, err := Resolve(t.TempDir())
		if !errors.Is(err, ErrNotAvailable) {
			t.Fatalf("Resolve error = %v, want ErrNotAvailable", err)
		}
		if _, err := ResolveFFprobe(t.TempDir()); !errors.Is(err, ErrNotAvailable) {
			t.Fatalf("ResolveFFprobe error = %v, want ErrNotAvailable", err)
		}
		if Available(t.TempDir()) {
			t.Error("Available = true with no payload, no override and an empty PATH")
		}
	})
}

func TestPartialPayloadFallsBackToPathForMissingTool(t *testing.T) {
	t.Setenv(envFFmpeg, "")
	t.Setenv(envFFprobe, "")
	pathDir := fakeOnPath(t, "ffprobe")
	files := map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg")} // no ffprobe bundled
	withPayload(t, files, func() {
		dataDir := t.TempDir()
		ffmpegPath, err := Resolve(dataDir)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if want := filepath.Join(dataDir, "bin", "ffmpeg", Fingerprint(), binName("ffmpeg")); ffmpegPath != want {
			t.Fatalf("Resolve = %q, want bundled %q", ffmpegPath, want)
		}
		probe, err := ResolveFFprobe(dataDir)
		if err != nil {
			t.Fatalf("ResolveFFprobe: %v", err)
		}
		sameFile(t, probe, filepath.Join(pathDir, binName("ffprobe")))

		// The unbundled tool must not have been materialised into the payload dir.
		if _, err := os.Stat(filepath.Join(dataDir, "bin", "ffmpeg", Fingerprint(), binName("ffprobe"))); err == nil {
			t.Error("ffprobe written to the extraction dir despite not being in the payload")
		}
	})
}

func TestExtractionFailureReturnsClearError(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "not-a-dir")
	if err := os.WriteFile(blocker, fakeBin("plain file"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envFFmpeg, "")
	withPayload(t, map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg")}, func() {
		_, err := Resolve(blocker) // dataDir is a file: extraction cannot proceed
		if err == nil {
			t.Fatal("Resolve succeeded with an unusable dataDir")
		}
		if !strings.Contains(err.Error(), "extract") {
			t.Errorf("error %q does not mention extraction", err)
		}
		if errors.Is(err, ErrNotAvailable) {
			t.Errorf("extraction failure %v masquerades as ErrNotAvailable", err)
		}
		if Available(blocker) {
			t.Error("Available = true while extraction fails")
		}
	})
}

func TestEmptyDataDirSkipsBundledCopy(t *testing.T) {
	t.Setenv(envFFmpeg, "")
	pathDir := fakeOnPath(t, "ffmpeg")
	withPayload(t, map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg")}, func() {
		got, err := Resolve("")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		sameFile(t, got, filepath.Join(pathDir, binName("ffmpeg")))
	})
}

func TestConcurrentResolveExtractsOnce(t *testing.T) {
	files := map[string][]byte{binName("ffmpeg"): fakeBin("ffmpeg")}
	dataDir := t.TempDir()
	t.Setenv(envFFmpeg, "")
	withPayload(t, files, func() {
		want := filepath.Join(dataDir, "bin", "ffmpeg", Fingerprint(), binName("ffmpeg"))
		const n = 16
		paths := make([]string, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range paths {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				paths[i], errs[i] = Resolve(dataDir)
			}(i)
		}
		wg.Wait()
		for i := range paths {
			if errs[i] != nil {
				t.Fatalf("goroutine %d: %v", i, errs[i])
			}
			if paths[i] != want {
				t.Fatalf("goroutine %d resolved %q, want %q", i, paths[i], want)
			}
		}
		assertContent(t, want, files[binName("ffmpeg")])
	})
}
