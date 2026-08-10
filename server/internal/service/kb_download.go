package service

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// kbAllowLocalSources gates the loopback/private-network and local-file source
// paths. It defaults to false so the multi-tenant hosted edition is SSRF-safe:
// a tenant-supplied url source cannot be used to reach cloud metadata endpoints
// (169.254.169.254), localhost, or RFC1918 services, file:// / local git clones
// (arbitrary server-file reads) are refused, and a source_kind=local path must
// stay inside the owner's datasets dir (see KBService.ensureLocalSourceAllowed).
// The single-tenant personal/local edition — where the owner is the machine's
// user — opts in. It is wired to edition config at startup via
// SetKBAllowLocalSources (storage.kb.allow_local_sources → Config.KBAllowLocalSources,
// defaulting true on personal / false on hosted); tests toggle it directly.
var kbAllowLocalSources = false

// SetKBAllowLocalSources wires the edition/config decision into the package-level
// guard read by the download path (dial hook, git/file handlers) and the local
// source-root gate. Call once at server startup, before serving. The download
// HTTP client is a process-wide singleton whose dial hook reads this live, so a
// single process-wide value (not a per-service field) is the right shape.
func SetKBAllowLocalSources(allow bool) { kbAllowLocalSources = allow }

// ipBlocked reports whether an IP is in a range a tenant must not be able to
// reach via a server-side fetch (loopback, RFC1918/ULA private, link-local incl.
// the cloud-metadata 169.254.0.0/16, CGNAT 100.64/10, unspecified, multicast).
func ipBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true // 100.64.0.0/10 carrier-grade NAT
	}
	return false
}

// validateRemoteHost resolves host and rejects it if any resolved address is
// blocked. Used for the git path, which does its own dialing (best-effort
// pre-check; the HTTP path additionally pins the dialed IP to defeat rebinding).
func validateRemoteHost(ctx context.Context, host string) error {
	if kbAllowLocalSources {
		return nil
	}
	if host == "" {
		return fmt.Errorf("missing host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ipBlocked(ip) {
			return fmt.Errorf("blocked address %s", ip)
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if ipBlocked(ip) {
			return fmt.Errorf("host %s resolves to blocked address %s", host, ip)
		}
	}
	return nil
}

var kbHTTPClientOnce struct {
	sync.Once
	client *http.Client
}

// kbHTTPClient returns a process-wide HTTP client whose dialer resolves +
// validates every address it connects to (including redirect hops, since each
// new connection dials through the same hook) and pins the dialed IP to the one
// it validated, closing the DNS-rebinding window. The hook reads
// kbAllowLocalSources live at dial time, so the single shared client still
// honors the guard (and lets tests toggle it). Sharing one client avoids leaking
// a fresh Transport (and its idle connections) on every download.
func kbHTTPClient() *http.Client {
	kbHTTPClientOnce.Do(func() {
		// clientTimeout=0: no overall deadline, since repo/archive downloads can
		// legitimately run longer than any single request timeout. The dialer
		// still bounds connect time.
		kbHTTPClientOnce.client = NewSSRFGuardedClient(30*time.Second, 0)
	})
	return kbHTTPClientOnce.client
}

// NewSSRFGuardedClient builds an *http.Client whose dialer rejects connections
// to private/loopback/link-local/cloud-metadata addresses (see ipBlocked) and
// pins the validated IP to defeat DNS rebinding — every redirect hop re-dials
// through the same hook. It honors the process-wide kbAllowLocalSources gate, so
// the personal edition keeps local access while the hosted edition is locked
// down. Shared by the KB downloader and the external-API proxy so both fetch
// paths get the same SSRF guarantees.
//
// dialTimeout bounds connect time; clientTimeout is the overall request deadline
// (pass 0 for none, e.g. long downloads).
func NewSSRFGuardedClient(dialTimeout, clientTimeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: dialTimeout}
	return &http.Client{
		Timeout: clientTimeout,
		Transport: &http.Transport{
			// Proxy SSRF guard: with a proxy configured, the DialContext hook
			// below only ever sees (and validates/pins) the *proxy's* address —
			// the real target host is resolved and reached by the proxy, sailing
			// past the private-network gate. So in the hosted edition we refuse to
			// use a proxy at all and dial the target directly through the
			// validating dialer; the local edition keeps env-proxy support. Read
			// live so the shared client honors a runtime toggle.
			Proxy: func(req *http.Request) (*url.URL, error) {
				if kbAllowLocalSources {
					return http.ProxyFromEnvironment(req)
				}
				return nil, nil
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: time.Second,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if kbAllowLocalSources {
					return dialer.DialContext(ctx, network, addr)
				}
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if ipBlocked(ip) {
						return nil, fmt.Errorf("blocked address %s (private/loopback)", ip)
					}
				}
				if len(ips) == 0 {
					return nil, fmt.Errorf("no address for %s", host)
				}
				// Dial the exact IP we validated (not the hostname again) so
				// DNS can't be rebound to an internal address between check and
				// dial.
				return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
			},
		},
	}
}

// DownloadProgress reports a coarse stage + percent (0..100) so the caller can
// surface a live loading bar. stage is one of "downloading" / "extracting".
type DownloadProgress func(stage string, percent int)

// kbDownloadAttempts is how many times each candidate URL is retried before the
// downloader falls through to the next mirror.
const kbDownloadAttempts = 3

// kbDefaultMaxBytes caps a single HTTP fetch so a runaway or wrong URL can't
// fill the disk. source_config.max_bytes overrides it (0 in config => default).
const kbDefaultMaxBytes int64 = 2 << 30 // 2 GiB

// kbGitCloneTimeout bounds a whole shallow clone (connect + fetch + checkout).
// runIngest hands the download path context.Background() (no deadline), so a
// stalled or malicious remote would otherwise wedge the ingest goroutine in
// "downloading" forever. A var (not const) so tests can shorten it.
var kbGitCloneTimeout = 10 * time.Minute

// kbGitCloneSizePollInterval is how often the clone's on-disk footprint is
// checked against the size cap while git runs. A var so tests can speed it up.
var kbGitCloneSizePollInterval = 2 * time.Second

// Decompression-bomb guards: max_bytes bounds only the *compressed* download, so
// extraction tracks the running uncompressed total and entry count separately.
const (
	kbMaxExtractedBytes int64 = 8 << 30 // 8 GiB uncompressed across an archive
	kbMaxArchiveEntries int64 = 1_000_000
)

// extractGuard bounds the aggregate size and entry count of an archive
// extraction to defend against zip/gzip bombs (a small archive that expands to
// fill the disk or exhausts inodes).
type extractGuard struct {
	maxBytes, maxEntries int64
	bytes, entries       int64
}

func newExtractGuard() *extractGuard {
	return &extractGuard{maxBytes: kbMaxExtractedBytes, maxEntries: kbMaxArchiveEntries}
}

func (g *extractGuard) entry() error {
	g.entries++
	if g.entries > g.maxEntries {
		return fmt.Errorf("archive exceeds %d entries", g.maxEntries)
	}
	return nil
}

func (g *extractGuard) add(n int64) error {
	g.bytes += n
	if g.bytes > g.maxBytes {
		return fmt.Errorf("extracted size exceeds %d bytes", g.maxBytes)
	}
	return nil
}

// limitedCopy copies src to dst, charging the bytes against the guard so a
// streamed entry can't blow past the aggregate cap.
func limitedCopy(dst io.Writer, src io.Reader, g *extractGuard) error {
	remaining := g.maxBytes - g.bytes + 1 // +1 so an exact-fit copy isn't falsely tripped
	n, err := io.Copy(dst, io.LimitReader(src, remaining))
	if err != nil {
		return err
	}
	return g.add(n)
}

// kbSourceConfig is the optional tuning carried in knowledge_bases.source_config
// (JSON). All fields are optional; absent ones fall back to defaults. This is
// how presets express mirrors / subset selection generically — no per-dataset
// code lives in the downloader.
type kbSourceConfig struct {
	MirrorURLs []string `json:"mirror_urls"`
	SHA256     string   `json:"sha256"`
	MaxBytes   int64    `json:"max_bytes"`
	Subdir     string   `json:"subdir"`
}

func parseKBSourceConfig(raw string) kbSourceConfig {
	var c kbSourceConfig
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return c
	}
	_ = json.Unmarshal([]byte(raw), &c) // best-effort; bad JSON => zero config
	return c
}

// kbSourceSubdir extracts the cleaned, traversal-safe subdir from a KB's
// source_config (used by resolveSourceRoot to narrow ingest to a subset).
func kbSourceSubdir(raw string) string {
	return safeRelSubdir(parseKBSourceConfig(raw).Subdir)
}

// safeRelSubdir normalizes a configured subdir to a forward-slash relative path
// that can never escape its root. A leading "/" is tolerated (stripped), but any
// ".." that would climb out yields "" so a misconfigured subdir is ignored
// rather than silently pointing at the wrong subtree.
func safeRelSubdir(sub string) string {
	sub = strings.TrimLeft(strings.TrimSpace(filepath.ToSlash(sub)), "/")
	if sub == "" {
		return ""
	}
	clean := path.Clean(sub)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return clean
}

// DownloadURLSource fetches a url-kind KB's content into its per-owner dataset
// directory (~/.niuniu/{owner}/datasets/<kbID>/). It tries the primary
// source_addr first, then each source_config.mirror_url in order (按序兜底), so a
// blocked GitHub origin can fall through to a domestic mirror. The download is
// staged in a sibling temp dir and atomically swapped into place, making repeat
// runs idempotent. Returns the first mirror's success or a combined error.
func (s *KBService) DownloadURLSource(ctx context.Context, owner OwnerRef, kb store.KnowledgeBase, progress DownloadProgress) error {
	if progress == nil {
		progress = func(string, int) {}
	}
	cfg := parseKBSourceConfig(kb.SourceConfig)

	candidates := dedupeNonEmpty(append([]string{kb.SourceAddr}, cfg.MirrorURLs...))
	if len(candidates) == 0 {
		return fmt.Errorf("kb %d: url source has no address", kb.ID)
	}

	target := owner.DatasetsPath(s.dataDir, kb.ID)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("kb %d: prepare datasets dir: %w", kb.ID, err)
	}

	var errs []string
	for i, raw := range candidates {
		progress("downloading", 10+i) // nudge so the bar moves between mirrors
		staging := target + ".downloading"
		_ = os.RemoveAll(staging)
		if err := os.MkdirAll(staging, 0o755); err != nil {
			return fmt.Errorf("kb %d: stage dir: %w", kb.ID, err)
		}

		err := fetchWithRetry(ctx, raw, staging, cfg, progress)
		if err != nil {
			_ = os.RemoveAll(staging)
			errs = append(errs, fmt.Sprintf("%s: %v", raw, err))
			continue
		}

		// Commit crash-safely: move any prior good copy aside first, swap the new
		// one in, and only then drop the old copy. If the rename fails we restore
		// the previous dataset rather than leaving the KB empty.
		backup := target + ".old"
		_ = os.RemoveAll(backup)
		hadPrev := false
		if _, statErr := os.Stat(target); statErr == nil {
			if err := os.Rename(target, backup); err != nil {
				_ = os.RemoveAll(staging)
				return fmt.Errorf("kb %d: stage prior copy: %w", kb.ID, err)
			}
			hadPrev = true
		}
		if err := os.Rename(staging, target); err != nil {
			if hadPrev {
				_ = os.Rename(backup, target) // restore previous good copy
			}
			_ = os.RemoveAll(staging)
			return fmt.Errorf("kb %d: commit download: %w", kb.ID, err)
		}
		_ = os.RemoveAll(backup)
		progress("extracting", 50)
		return nil
	}
	return fmt.Errorf("kb %d: all sources failed: %s", kb.ID, strings.Join(errs, "; "))
}

// fetchWithRetry retries a single candidate before the caller moves on. Git/file
// failures and transient HTTP errors all get the same bounded retry.
func fetchWithRetry(ctx context.Context, raw, dest string, cfg kbSourceConfig, progress DownloadProgress) error {
	var last error
	for attempt := 1; attempt <= kbDownloadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 1 {
			// Clear any partial output before retrying into the same dest.
			clearDir(dest)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt-1) * 500 * time.Millisecond):
			}
		}
		if err := fetchOne(ctx, raw, dest, cfg, progress); err != nil {
			last = err
			continue
		}
		return nil
	}
	return last
}

// fetchOne dispatches one URL to the right protocol handler, landing files
// directly under dest.
func fetchOne(ctx context.Context, raw, dest string, cfg kbSourceConfig, progress DownloadProgress) error {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	// Detect git on the raw address first: a repo URL keeps its http(s) scheme but
	// ends in .git, an ssh remote is git@host:repo, and a local bare repo is a
	// plain filesystem path ending in .git (which url.Parse would misread on
	// Windows as scheme "c").
	if strings.HasPrefix(lower, "git://") || strings.HasPrefix(raw, "git@") || strings.HasSuffix(lower, ".git") {
		return gitClone(ctx, raw, dest, cfg, progress)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	switch u.Scheme {
	case "file":
		// Reading arbitrary server-local files is gated to the local edition; in
		// the hosted edition file:// is refused (use the 'local' source kind on a
		// trusted path instead).
		if !kbAllowLocalSources {
			return fmt.Errorf("file:// sources are disabled")
		}
		return copyFromFileURL(u, dest, progress)
	case "http", "https":
		return httpFetch(ctx, raw, dest, cfg, progress)
	default:
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
}

// gitClone shallow-clones a repository into dest. The agent reads the working
// tree directly; the .git dir is left in place but ignored by gatherTextFiles
// (it skips dotfiles), so we don't pay an extra walk to strip it. The clone is
// bounded three ways so a huge or stalled repo can't wedge ingest or fill the
// disk: an overall deadline, a live on-disk size cap, and --depth 1 (final
// version only, no history).
func gitClone(ctx context.Context, raw, dest string, cfg kbSourceConfig, progress DownloadProgress) error {
	progress("downloading", 30)

	// SSRF / local-read guard: validate the remote host before invoking git. A
	// network remote (http(s)/git/ssh) must resolve to a public address; a local
	// path / file remote (no network host) is only allowed in the local edition.
	// Residual (accepted): git re-resolves the host itself, leaving a narrow
	// DNS-rebinding window between this check and git's connect that the HTTP path
	// closes by dialing the validated IP. Pinning the IP for git would break TLS
	// SNI/cert validation (the cert is issued to the hostname), so we keep the
	// best-effort pre-check here and instead shut the proxy-SSRF door below.
	host, networked := gitRemoteHost(raw)
	if networked {
		if err := validateRemoteHost(ctx, host); err != nil {
			return fmt.Errorf("git remote: %w", err)
		}
	} else if !kbAllowLocalSources {
		return fmt.Errorf("local git sources are disabled")
	}

	// Pin the allowed transports: exclude ext::/remote-helpers (arbitrary command
	// execution) always, and the file/local transport unless the local edition is
	// enabled (it would otherwise read server-local repos).
	allowProto := "http:https:git:ssh"
	if kbAllowLocalSources {
		allowProto += ":file"
	}

	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = kbDefaultMaxBytes
	}

	// Bound the whole clone with a deadline so a stalled remote can't pin the
	// ingest goroutine in "downloading" indefinitely.
	ctx, cancel := context.WithTimeout(ctx, kbGitCloneTimeout)
	defer cancel()

	// dest is always a freshly-emptied staging dir (DownloadURLSource stages a new
	// dir per attempt and fetchWithRetry clears it before a retry), and git
	// permits cloning into an existing empty directory — so clone straight in, no
	// subdir-then-flatten dance (which would collide with a repo whose root
	// contains an entry named "_clone"). "--" stops option injection.
	//
	// --depth 1 --single-branch: fetch only the final version of the default
	// branch, no history and no other branch tips — the smallest fetch that still
	// yields a usable working tree.
	args := []string{"clone", "--depth", "1", "--single-branch"}
	if !kbAllowLocalSources {
		// Proxy SSRF (git path): git honors http(s)_proxy env + http.proxy config,
		// which would let a proxy resolve+reach the target on our behalf, past the
		// host validation above. In the hosted edition force the proxy empty (belt
		// with stripProxyEnv below) so git dials the validated remote directly.
		args = append(args, "-c", "http.proxy=")
	}
	args = append(args, "--", raw, dest)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitCloneEnv(allowProto)

	// Enforce the size cap *during* the clone: exec writes straight to disk, so the
	// only way to honor a cap is to watch the growing tree and kill git once it
	// blows past it (bounding usage to cap + one poll interval).
	var sizeExceeded atomic.Bool
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	go func() {
		ticker := time.NewTicker(kbGitCloneSizePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				if dirSize(dest) > maxBytes {
					sizeExceeded.Store(true)
					cancel() // kill the clone
					return
				}
			}
		}
	}()

	out, err := cmd.CombinedOutput()
	stopMonitor()
	if sizeExceeded.Load() {
		return fmt.Errorf("git clone exceeds max_bytes (%d)", maxBytes)
	}
	if err != nil {
		return fmt.Errorf("git clone: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// Final guard: a repo that fit under the cap at every poll but crossed it by the
	// last write still gets rejected here.
	if n := dirSize(dest); n > maxBytes {
		return fmt.Errorf("git clone exceeds max_bytes (%d)", maxBytes)
	}
	return nil
}

// gitCloneEnv builds the environment for the clone command: the transport
// allowlist + no terminal prompt, and — in the hosted edition — strips proxy
// env vars so git can't route the fetch through a proxy that would bypass the
// remote-host validation (proxy SSRF).
func gitCloneEnv(allowProto string) []string {
	env := os.Environ()
	if !kbAllowLocalSources {
		env = stripProxyEnv(env)
	}
	return append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL="+allowProto,
	)
}

// stripProxyEnv drops the standard proxy-selecting env vars (http_proxy etc.)
// from a KEY=VALUE environment slice. no_proxy is left intact (it only exempts
// hosts from proxying, which is harmless once the proxies are gone).
func stripProxyEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		switch strings.ToLower(key) {
		case "http_proxy", "https_proxy", "all_proxy", "ftp_proxy":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// dirSize sums the sizes of all regular files under dir. Errors (including races
// with a tree git is still writing) are ignored so a transient stat failure
// doesn't abort the size check — the next poll re-reads.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// gitRemoteHost extracts the network host from a git remote address and reports
// whether the remote is networked (true) or local/file (false, no host to
// validate). Handles scheme URLs (https/git/ssh/file) and scp-like git@host:path.
func gitRemoteHost(raw string) (host string, networked bool) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	// scp-like syntax: [user@]host:path (no "://"), e.g. git@github.com:o/r.git
	if !strings.Contains(raw, "://") {
		if at := strings.Index(raw, "@"); at >= 0 {
			rest := raw[at+1:]
			if colon := strings.Index(rest, ":"); colon >= 0 {
				return rest[:colon], true
			}
		}
		return "", false // plain local path
	}
	if strings.HasPrefix(lower, "file://") {
		return "", false
	}
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname(), u.Hostname() != ""
	}
	return "", false
}

// copyFromFileURL materializes a file:// source (dir or single file) into dest.
// Useful for air-gapped installs that pre-stage a corpus on a local volume.
func copyFromFileURL(u *url.URL, dest string, progress DownloadProgress) error {
	progress("downloading", 30)
	src := u.Path
	if src == "" {
		src = u.Opaque
	}
	// On Windows a file URL is /C:/path; trim the leading slash before a drive.
	if len(src) >= 3 && src[0] == '/' && src[2] == ':' {
		src = src[1:]
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyTree(src, dest)
	}
	return copyFile(src, filepath.Join(dest, filepath.Base(src)))
}

// httpFetch downloads raw over HTTP(S) and, by extension, either extracts an
// archive (zip / tar.gz / tar) into dest or saves the response as a single file.
func httpFetch(ctx context.Context, raw, dest string, cfg kbSourceConfig, progress DownloadProgress) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "niuniu-kb-downloader")
	resp, err := kbHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}

	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = kbDefaultMaxBytes
	}
	// Buffer to a temp file: archives need random access (zip) or a clean second
	// pass, and we want to verify the checksum over the whole payload first.
	tmpFile, err := os.CreateTemp("", "niuniu-kb-*.download")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	n, err := io.Copy(tmpFile, io.LimitReader(resp.Body, maxBytes+1))
	tmpFile.Close()
	if err != nil {
		return err
	}
	if n > maxBytes {
		return fmt.Errorf("download exceeds max_bytes (%d)", maxBytes)
	}
	progress("downloading", 40)

	if cfg.SHA256 != "" {
		if err := verifySHA256(tmpPath, cfg.SHA256); err != nil {
			return err
		}
	}

	lower := strings.ToLower(urlBase(raw))
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(tmpPath, dest, newExtractGuard())
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return extractTarGz(tmpPath, dest, true, newExtractGuard())
	case strings.HasSuffix(lower, ".tar"):
		return extractTarGz(tmpPath, dest, false, newExtractGuard())
	default:
		name := urlBase(raw)
		if name == "" || name == "/" || name == "." || name == ".." {
			name = "download.bin"
		}
		// Run the single-file destination through the same traversal guard the
		// archive paths use (path.Base can still yield "..").
		out, err := safeJoin(dest, name)
		if err != nil {
			return err
		}
		return copyFile(tmpPath, out)
	}
}

// urlBase returns the last path segment of a URL (its filename), ignoring query
// and fragment, for extension sniffing and single-file naming.
func urlBase(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return path.Base(u.Path)
	}
	return path.Base(raw)
}

func verifySHA256(file, want string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, strings.TrimSpace(want)) {
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, want)
	}
	return nil
}

// --- archive extraction (stdlib only; zip-slip / traversal guarded) ----------

// safeJoin joins dest+name and rejects any entry that would escape dest
// (zip-slip / tar traversal). It returns the cleaned absolute path or an error.
func safeJoin(dest, name string) (string, error) {
	slash := filepath.ToSlash(name)
	// Reject unix-absolute ("/etc/x") and Windows-absolute ("C:\x") entries
	// regardless of host OS, plus any ".." that climbs out.
	if path.IsAbs(slash) || filepath.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	target := filepath.Join(dest, clean)
	// Final defense: ensure the resolved path stays within dest.
	rel, err := filepath.Rel(dest, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return target, nil
}

func extractZip(archive, dest string, g *extractGuard) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if err := g.entry(); err != nil {
			return err
		}
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			continue // skip symlinks / devices / irregular entries
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		cerr := limitedCopy(out, rc, g)
		out.Close()
		rc.Close()
		if cerr != nil {
			return cerr
		}
	}
	return nil
}

func extractTarGz(archive, dest string, gzipped bool, g *extractGuard) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	var src io.Reader = f
	if gzipped {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		src = gz
	}
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := g.entry(); err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			// Bound the per-file copy and charge it against the aggregate guard so
			// a crafted tar can't stream unbounded data nor bomb the disk.
			if err := limitedCopy(out, io.LimitReader(tr, hdr.Size), g); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			// skip symlinks, hardlinks, fifos, devices
		}
	}
}

// --- small fs helpers --------------------------------------------------------

func dedupeNonEmpty(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// clearDir removes every entry inside dir but keeps dir itself.
func clearDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks etc.
		}
		return copyFile(p, target)
	})
}
