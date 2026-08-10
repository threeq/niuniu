// Package browserhistory reads the local browser history databases (Chromium
// family + Firefox) directly off disk for the "info-radar" scene (路子 A).
//
// It is privacy-sensitive by construction: it reads ONLY history (plaintext
// URL/title/visit-time), never cookies/passwords/DPAPI-encrypted stores. The
// caller (the browser-history MCP tool, gated OFF by default and enabled only
// for the radar) is responsible for opt-in, scoping, and never persisting the
// raw entries — this package just returns them.
//
// Lock handling: Chromium keeps an exclusive lock on the `History` SQLite file
// while the browser runs, so we copy the DB (plus any -wal/-shm sidecars) to a
// temp file and read the copy read-only. Timestamps are normalized to Go time:
// Chromium stores microseconds since 1601-01-01 UTC, Firefox microseconds since
// the Unix epoch.
package browserhistory

import (
	"database/sql"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo), driver name "sqlite"
)

// chromiumEpochOffsetMicros is the microsecond gap between the Chromium/WebKit
// epoch (1601-01-01 UTC) and the Unix epoch (1970-01-01 UTC).
const chromiumEpochOffsetMicros int64 = 11644473600000000

// defaultLimit caps how many entries a query returns when Limit is unset.
const defaultLimit = 200

// Entry is one visited page.
type Entry struct {
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	VisitTime  time.Time `json:"visit_time"`
	VisitCount int       `json:"visit_count"`
	Browser    string    `json:"browser"` // e.g. "chrome", "edge", "firefox"
	Profile    string    `json:"profile"` // profile dir name, e.g. "Default"
}

// Query scopes a read. All fields are optional.
type Query struct {
	// Since bounds visits to those at or after this instant (zero = no lower
	// bound). This is the primary privacy/scoping knob (e.g. "last 7 days").
	Since time.Time
	// Limit caps total returned entries after merge+sort (0 = defaultLimit).
	Limit int
	// Domains, when non-empty, keeps only entries whose URL host equals or is a
	// subdomain of one of these hostnames (suffix match on labels).
	Domains []string
}

// source describes one discovered history database on disk.
type source struct {
	browser string
	profile string
	path    string
	firefox bool // true => Firefox places.sqlite schema; false => Chromium
}

// Read discovers the current user's installed Chromium/Firefox history
// databases, reads each lock-safely, and returns the merged entries matching q,
// newest first. Databases that cannot be read (browser holding an un-copyable
// lock, malformed file) are skipped rather than failing the whole read; the
// first genuinely fatal error (none currently) would be returned.
func Read(q Query) ([]Entry, error) {
	return readSources(discoverSources(), q)
}

// readSources reads every source and merges the results. Exported-for-test via
// the discoverSources seam so unit tests can point at fixture DBs.
func readSources(sources []source, q Query) ([]Entry, error) {
	var all []Entry
	for _, s := range sources {
		entries, err := readOne(s, q)
		if err != nil {
			// Skip unreadable DBs (locked-and-uncopyable, corrupt); the radar
			// degrades gracefully rather than erroring the whole run.
			continue
		}
		all = append(all, entries...)
	}
	all = filterDomains(all, q.Domains)
	sort.Slice(all, func(i, j int) bool { return all[i].VisitTime.After(all[j].VisitTime) })
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// readOne copies the source DB to a temp file (defeating the browser's lock),
// opens the copy read-only, and reads it with the schema-appropriate query.
func readOne(s source, q Query) ([]Entry, error) {
	tmp, cleanup, err := copyForRead(s.path)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(tmp)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if s.firefox {
		return readFirefox(db, s, q)
	}
	return readChromium(db, s, q)
}

// readChromium reads the `urls` table (microseconds since 1601-01-01 UTC).
func readChromium(db *sql.DB, s source, q Query) ([]Entry, error) {
	var sinceDB int64
	if !q.Since.IsZero() {
		sinceDB = q.Since.UnixMicro() + chromiumEpochOffsetMicros
	}
	rows, err := db.Query(
		`SELECT url, title, visit_count, last_visit_time FROM urls
		 WHERE last_visit_time >= ? ORDER BY last_visit_time DESC`, sinceDB)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var u, title string
		var count int
		var ts int64
		if err := rows.Scan(&u, &title, &count, &ts); err != nil {
			return nil, err
		}
		out = append(out, Entry{
			Title:      title,
			URL:        u,
			VisitTime:  time.UnixMicro(ts - chromiumEpochOffsetMicros).UTC(),
			VisitCount: count,
			Browser:    s.browser,
			Profile:    s.profile,
		})
	}
	return out, rows.Err()
}

// readFirefox reads moz_places (last_visit_date is microseconds since the Unix
// epoch and may be NULL for never-visited bookmarks).
func readFirefox(db *sql.DB, s source, q Query) ([]Entry, error) {
	var sinceDB int64
	if !q.Since.IsZero() {
		sinceDB = q.Since.UnixMicro()
	}
	rows, err := db.Query(
		`SELECT url, title, visit_count, last_visit_date FROM moz_places
		 WHERE last_visit_date IS NOT NULL AND last_visit_date >= ?
		 ORDER BY last_visit_date DESC`, sinceDB)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var u string
		var title sql.NullString
		var count int
		var ts int64
		if err := rows.Scan(&u, &title, &count, &ts); err != nil {
			return nil, err
		}
		out = append(out, Entry{
			Title:      title.String,
			URL:        u,
			VisitTime:  time.UnixMicro(ts).UTC(),
			VisitCount: count,
			Browser:    s.browser,
			Profile:    s.profile,
		})
	}
	return out, rows.Err()
}

// filterDomains keeps only entries whose URL host equals or is a subdomain of
// one of domains. An empty domains slice is a no-op (keep everything).
func filterDomains(entries []Entry, domains []string) []Entry {
	if len(domains) == 0 {
		return entries
	}
	norm := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			norm = append(norm, d)
		}
	}
	if len(norm) == 0 {
		return entries
	}
	out := entries[:0]
	for _, e := range entries {
		if hostMatches(e.URL, norm) {
			out = append(out, e)
		}
	}
	return out
}

// hostMatches reports whether rawURL's host equals or is a subdomain of any
// domain in norm (all lowercase). "mail.google.com" matches "google.com".
func hostMatches(rawURL string, norm []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	for _, d := range norm {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// copyForRead copies src (and its -wal/-shm sidecars, if present) into a fresh
// temp directory so the copy can be opened even while the browser holds a lock
// on the original. Returns the temp DB path and a cleanup func.
func copyForRead(src string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "niuniu-bh-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	base := filepath.Base(src)
	dst := filepath.Join(dir, base)
	if err := copyFile(src, dst); err != nil {
		cleanup()
		return "", func() {}, err
	}
	// Best-effort copy of WAL/SHM sidecars so recent (not-yet-checkpointed)
	// writes are visible; their absence is fine.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = copyFile(src+suffix, dst+suffix)
	}
	return dst, cleanup, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// discoverSources enumerates candidate history DBs for the current user across
// installed Chromium-family browsers and Firefox. Missing paths are skipped, so
// the returned slice only contains files that exist.
func discoverSources() []source {
	var out []source
	for _, c := range chromiumRoots() {
		for _, prof := range chromiumProfiles(c.root) {
			p := filepath.Join(c.root, prof, "History")
			if fileExists(p) {
				out = append(out, source{browser: c.browser, profile: prof, path: p})
			}
		}
	}
	for _, prof := range firefoxProfileDirs() {
		p := filepath.Join(prof, "places.sqlite")
		if fileExists(p) {
			out = append(out, source{browser: "firefox", profile: filepath.Base(prof), path: p, firefox: true})
		}
	}
	return out
}

type chromiumRoot struct {
	browser string
	root    string // "User Data" dir holding profile subdirs
}

// chromiumRoots returns the per-browser "User Data" roots for the current OS.
func chromiumRoots() []chromiumRoot {
	home, _ := os.UserHomeDir()
	var roots []chromiumRoot
	add := func(browser, root string) {
		if root != "" && dirExists(root) {
			roots = append(roots, chromiumRoot{browser: browser, root: root})
		}
	}
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" && home != "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		add("chrome", filepath.Join(local, "Google", "Chrome", "User Data"))
		add("edge", filepath.Join(local, "Microsoft", "Edge", "User Data"))
		add("brave", filepath.Join(local, "BraveSoftware", "Brave-Browser", "User Data"))
	case "darwin":
		as := filepath.Join(home, "Library", "Application Support")
		add("chrome", filepath.Join(as, "Google", "Chrome"))
		add("edge", filepath.Join(as, "Microsoft Edge"))
		add("brave", filepath.Join(as, "BraveSoftware", "Brave-Browser"))
	default: // linux
		cfg := filepath.Join(home, ".config")
		add("chrome", filepath.Join(cfg, "google-chrome"))
		add("chromium", filepath.Join(cfg, "chromium"))
		add("edge", filepath.Join(cfg, "microsoft-edge"))
		add("brave", filepath.Join(cfg, "BraveSoftware", "Brave-Browser"))
	}
	return roots
}

// chromiumProfiles returns the profile subdir names under a User Data root that
// hold a History DB: "Default" plus any "Profile N".
func chromiumProfiles(root string) []string {
	var profs []string
	entries, err := os.ReadDir(root)
	if err != nil {
		return profs
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "Default" || strings.HasPrefix(name, "Profile ") {
			profs = append(profs, name)
		}
	}
	return profs
}

// firefoxProfileDirs returns the Firefox profile directories for the current OS.
func firefoxProfileDirs() []string {
	home, _ := os.UserHomeDir()
	var base string
	switch runtime.GOOS {
	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" && home != "" {
			appdata = filepath.Join(home, "AppData", "Roaming")
		}
		base = filepath.Join(appdata, "Mozilla", "Firefox", "Profiles")
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles")
	default:
		base = filepath.Join(home, ".mozilla", "firefox")
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(base, e.Name()))
		}
	}
	return out
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// chromiumTimeToDB converts a Go time to the Chromium on-disk representation
// (microseconds since 1601-01-01 UTC). Exposed for tests that build fixtures.
func chromiumTimeToDB(t time.Time) int64 { return t.UnixMicro() + chromiumEpochOffsetMicros }
