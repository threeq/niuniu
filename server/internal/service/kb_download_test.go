package service

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tarGzBytes builds an in-memory .tar.gz with the given files (rel path ->
// content), mirroring how GitHub's archive tarballs nest under a top dir.
func tarGzBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// allowLocal opts the package-level SSRF guard into permitting loopback/private
// hosts and local file/git sources for the duration of a test (httptest servers
// listen on 127.0.0.1, which is blocked by default). Reset on cleanup.
func allowLocal(t *testing.T) {
	t.Helper()
	prev := kbAllowLocalSources
	kbAllowLocalSources = true
	t.Cleanup(func() { kbAllowLocalSources = prev })
}

func TestDownloadTarGzAndIngest(t *testing.T) {
	allowLocal(t)
	svc, owner := newKBTest(t)
	ctx := context.Background()

	archive := tarGzBytes(t, map[string]string{
		"chinese-poetry-master/poem.json": `{"title":"静夜思","content":"床前明月光"}`,
		"chinese-poetry-master/README.md": "# corpus",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "poetry", SourceKind: KBSourceURL, SourceAddr: srv.URL + "/master.tar.gz",
	})
	if err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	stages := []string{}
	if err := svc.DownloadURLSource(ctx, owner, kb, func(stage string, _ int) {
		stages = append(stages, stage)
	}); err != nil {
		t.Fatalf("DownloadURLSource: %v", err)
	}
	if len(stages) == 0 {
		t.Fatal("expected progress callbacks")
	}

	// Files landed where resolveSourceRoot will look (datasets/<id>).
	root := owner.DatasetsPath(svc.dataDir, kb.ID)
	if _, err := os.Stat(filepath.Join(root, "chinese-poetry-master", "poem.json")); err != nil {
		t.Fatalf("expected extracted poem.json: %v", err)
	}

	// End to end: ingest the downloaded corpus and search it.
	res, err := svc.Ingest(ctx, owner, kb.ID, IngestOptions{Force: true})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.FilesIngested < 1 {
		t.Fatalf("expected >=1 file ingested, got %d", res.FilesIngested)
	}
	hits, err := svc.Search(ctx, owner, kb.ID, "明月光", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected a search hit for downloaded content")
	}
}

func TestDownloadZip(t *testing.T) {
	allowLocal(t)
	svc, owner := newKBTest(t)
	ctx := context.Background()
	archive := zipBytes(t, map[string]string{"docs/a.md": "alpha", "docs/b.md": "beta"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{Name: "z", SourceKind: KBSourceURL, SourceAddr: srv.URL + "/x.zip"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DownloadURLSource(ctx, owner, kb, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	root := owner.DatasetsPath(svc.dataDir, kb.ID)
	for _, rel := range []string{"docs/a.md", "docs/b.md"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
}

func TestDownloadSingleFile(t *testing.T) {
	allowLocal(t)
	svc, owner := newKBTest(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("plain body"))
	}))
	defer srv.Close()

	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{Name: "f", SourceKind: KBSourceURL, SourceAddr: srv.URL + "/notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DownloadURLSource(ctx, owner, kb, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(owner.DatasetsPath(svc.dataDir, kb.ID), "notes.txt"))
	if err != nil || string(got) != "plain body" {
		t.Fatalf("single file: got %q err %v", got, err)
	}
}

func TestDownloadMirrorFallback(t *testing.T) {
	allowLocal(t)
	svc, owner := newKBTest(t)
	ctx := context.Background()

	// Primary always 404s; the mirror serves a valid tarball. The downloader must
	// fall through in order and still succeed.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer dead.Close()
	archive := tarGzBytes(t, map[string]string{"data/x.md": "from mirror"})
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer live.Close()

	cfg := fmt.Sprintf(`{"mirror_urls":[%q]}`, live.URL+"/m.tar.gz")
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "fb", SourceKind: KBSourceURL, SourceAddr: dead.URL + "/p.tar.gz", SourceConfig: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DownloadURLSource(ctx, owner, kb, nil); err != nil {
		t.Fatalf("expected mirror fallback to succeed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(owner.DatasetsPath(svc.dataDir, kb.ID), "data", "x.md")); err != nil {
		t.Fatalf("mirror content missing: %v", err)
	}
}

func TestDownloadAllSourcesFail(t *testing.T) {
	allowLocal(t)
	svc, owner := newKBTest(t)
	ctx := context.Background()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{Name: "x", SourceKind: KBSourceURL, SourceAddr: dead.URL + "/a.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DownloadURLSource(ctx, owner, kb, nil); err == nil {
		t.Fatal("expected error when every source fails")
	}
}

func TestDownloadIdempotent(t *testing.T) {
	allowLocal(t)
	svc, owner := newKBTest(t)
	ctx := context.Background()
	archive := tarGzBytes(t, map[string]string{"a.md": "v1"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{Name: "idem", SourceKind: KBSourceURL, SourceAddr: srv.URL + "/a.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := svc.DownloadURLSource(ctx, owner, kb, nil); err != nil {
			t.Fatalf("download #%d: %v", i, err)
		}
	}
	// A clean single copy remains (no nested .downloading dir leaked).
	root := owner.DatasetsPath(svc.dataDir, kb.ID)
	if _, err := os.Stat(filepath.Join(root, "a.md")); err != nil {
		t.Fatalf("content missing after repeat: %v", err)
	}
	if _, err := os.Stat(root + ".downloading"); !os.IsNotExist(err) {
		t.Fatalf("staging dir should not persist")
	}
}

func TestDownloadMaxBytesExceeded(t *testing.T) {
	allowLocal(t)
	svc, owner := newKBTest(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
	}))
	defer srv.Close()
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "big", SourceKind: KBSourceURL, SourceAddr: srv.URL + "/big.txt",
		SourceConfig: `{"max_bytes": 16}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DownloadURLSource(ctx, owner, kb, nil); err == nil {
		t.Fatal("expected max_bytes guard to fail the download")
	}
}

func TestDownloadFileURL(t *testing.T) {
	allowLocal(t) // file:// is gated to the local edition
	svc, owner := newKBTest(t)
	ctx := context.Background()
	src := t.TempDir()
	writeKBFile(t, src, "nested/doc.md", "local corpus")

	fileURL := "file://" + filepath.ToSlash(src)
	if filepath.IsAbs(src) && src[0] != '/' {
		fileURL = "file:///" + filepath.ToSlash(src) // windows drive path
	}
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{Name: "fileu", SourceKind: KBSourceURL, SourceAddr: fileURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DownloadURLSource(ctx, owner, kb, nil); err != nil {
		t.Fatalf("file:// download: %v", err)
	}
	if _, err := os.Stat(filepath.Join(owner.DatasetsPath(svc.dataDir, kb.ID), "nested", "doc.md")); err != nil {
		t.Fatalf("file:// content missing: %v", err)
	}
}

func TestDownloadGitClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	allowLocal(t) // local git sources are gated to the local edition
	svc, owner := newKBTest(t)
	ctx := context.Background()

	// Build a tiny repo and a bare .git clone source the downloader can clone.
	work := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	writeKBFile(t, work, "verse.md", "git corpus body")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	bare := filepath.Join(t.TempDir(), "corpus.git")
	if out, err := exec.Command("git", "clone", "--bare", "-q", work, bare).CombinedOutput(); err != nil {
		t.Fatalf("bare clone: %v: %s", err, out)
	}

	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{Name: "gitkb", SourceKind: KBSourceURL, SourceAddr: bare})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DownloadURLSource(ctx, owner, kb, nil); err != nil {
		t.Fatalf("git clone download: %v", err)
	}
	if _, err := os.Stat(filepath.Join(owner.DatasetsPath(svc.dataDir, kb.ID), "verse.md")); err != nil {
		t.Fatalf("cloned working tree missing verse.md: %v", err)
	}
}

func TestDownloadGitCloneEnforcesSizeCap(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	allowLocal(t)
	// Poll fast so the monitor trips mid-clone rather than only at the final guard.
	old := kbGitCloneSizePollInterval
	kbGitCloneSizePollInterval = 5 * time.Millisecond
	defer func() { kbGitCloneSizePollInterval = old }()

	svc, owner := newKBTest(t)
	ctx := context.Background()

	work := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	// A payload comfortably larger than the 1 KiB cap we set below.
	writeKBFile(t, work, "big.md", strings.Repeat("x", 64<<10))
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	bare := filepath.Join(t.TempDir(), "big.git")
	if out, err := exec.Command("git", "clone", "--bare", "-q", work, bare).CombinedOutput(); err != nil {
		t.Fatalf("bare clone: %v: %s", err, out)
	}

	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "bigkb", SourceKind: KBSourceURL, SourceAddr: bare,
		SourceConfig: `{"max_bytes": 1024}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DownloadURLSource(ctx, owner, kb, nil); err == nil {
		t.Fatal("expected clone to be rejected for exceeding max_bytes")
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	dest := t.TempDir()
	for _, bad := range []string{"../escape", "../../etc/passwd", "a/../../b", "/abs/evil"} {
		if _, err := safeJoin(dest, bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
	for _, ok := range []string{"a.md", "sub/dir/file.json", "./x"} {
		if _, err := safeJoin(dest, ok); err != nil {
			t.Fatalf("expected %q to be allowed: %v", ok, err)
		}
	}
}

func TestExtractZipRejectsZipSlip(t *testing.T) {
	dest := t.TempDir()
	// Hand-build a zip whose entry name climbs out of dest.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../evil.txt")
	_, _ = w.Write([]byte("pwn"))
	_ = zw.Close()
	archive := filepath.Join(t.TempDir(), "slip.zip")
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(archive, dest, newExtractGuard()); err == nil {
		t.Fatal("expected zip-slip entry to be rejected")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); err == nil {
		t.Fatal("zip-slip wrote outside dest")
	}
}

func TestExtractGuardCapsAggregateSize(t *testing.T) {
	dest := t.TempDir()
	archive := filepath.Join(t.TempDir(), "many.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 5; i++ {
		w, _ := zw.Create(fmt.Sprintf("f%d.txt", i))
		_, _ = w.Write(bytes.Repeat([]byte("a"), 100))
	}
	_ = zw.Close()
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// A tiny guard (120 bytes / unlimited entries) must abort partway.
	g := &extractGuard{maxBytes: 120, maxEntries: kbMaxArchiveEntries}
	if err := extractZip(archive, dest, g); err == nil {
		t.Fatal("expected aggregate-size guard to trip")
	}
}

func TestExtractGuardCapsEntryCount(t *testing.T) {
	dest := t.TempDir()
	archive := filepath.Join(t.TempDir(), "entries.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 5; i++ {
		w, _ := zw.Create(fmt.Sprintf("f%d.txt", i))
		_, _ = w.Write([]byte("x"))
	}
	_ = zw.Close()
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &extractGuard{maxBytes: kbMaxExtractedBytes, maxEntries: 3}
	if err := extractZip(archive, dest, g); err == nil {
		t.Fatal("expected entry-count guard to trip")
	}
}

func TestFetchOneRejectsGitExtTransport(t *testing.T) {
	// Even with local sources permitted (the most lenient mode), an ext::-style
	// address ending in .git must not execute: GIT_ALLOW_PROTOCOL pins transports
	// to exclude ext, so the clone fails rather than running a command.
	allowLocal(t)
	dest := t.TempDir()
	marker := filepath.Join(t.TempDir(), "pwned")
	evil := fmt.Sprintf(`ext::sh -c "touch %s".git`, filepath.ToSlash(marker))
	err := fetchOne(context.Background(), evil, dest, kbSourceConfig{}, func(string, int) {})
	if err == nil {
		t.Fatal("expected ext:: clone to fail")
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("ext:: transport executed a command — RCE not blocked")
	}
}

func TestDownloadBlocksLoopbackByDefault(t *testing.T) {
	// Without allowLocal, an http(s) source pointing at loopback (here httptest)
	// must be refused by the SSRF guard — this is the cloud-metadata / internal
	// service protection for the hosted edition.
	svc, owner := newKBTest(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer srv.Close()
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{Name: "ssrf", SourceKind: KBSourceURL, SourceAddr: srv.URL + "/x.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DownloadURLSource(ctx, owner, kb, nil); err == nil {
		t.Fatal("expected loopback fetch to be blocked by default")
	}
}

func TestDownloadBlocksFileURLByDefault(t *testing.T) {
	svc, owner := newKBTest(t)
	ctx := context.Background()
	src := t.TempDir()
	writeKBFile(t, src, "doc.md", "local")
	kb, err := svc.CreateKB(ctx, owner, CreateKBParams{
		Name: "fblock", SourceKind: KBSourceURL, SourceAddr: "file://" + filepath.ToSlash(src),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DownloadURLSource(ctx, owner, kb, nil); err == nil {
		t.Fatal("expected file:// to be disabled by default")
	}
}

func TestIPBlocked(t *testing.T) {
	blocked := []string{"127.0.0.1", "::1", "10.0.0.5", "172.16.3.4", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0"}
	for _, s := range blocked {
		if !ipBlocked(net.ParseIP(s)) {
			t.Errorf("expected %s to be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34"} {
		if ipBlocked(net.ParseIP(s)) {
			t.Errorf("expected %s to be allowed", s)
		}
	}
}

func TestGitRemoteHost(t *testing.T) {
	cases := []struct {
		raw       string
		host      string
		networked bool
	}{
		{"https://github.com/o/r.git", "github.com", true},
		{"git://gitee.com/o/r.git", "gitee.com", true},
		{"git@github.com:o/r.git", "github.com", true},
		{"file:///srv/repo.git", "", false},
		{"/local/path/repo.git", "", false},
		{"C:/local/repo.git", "", false},
	}
	for _, c := range cases {
		h, n := gitRemoteHost(c.raw)
		if h != c.host || n != c.networked {
			t.Errorf("gitRemoteHost(%q) = (%q,%v), want (%q,%v)", c.raw, h, n, c.host, c.networked)
		}
	}
}

func TestSafeRelSubdir(t *testing.T) {
	cases := map[string]string{
		"":            "",
		".":           "",
		"sub":         "sub",
		"a/b":         "a/b",
		"../escape":   "",
		"/abs":        "abs",
		"a/../../bad": "",
	}
	for in, want := range cases {
		if got := safeRelSubdir(in); got != want {
			t.Errorf("safeRelSubdir(%q) = %q, want %q", in, got, want)
		}
	}
}
