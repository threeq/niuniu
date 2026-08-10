package browserhistory

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// makeChromiumDB writes a minimal Chromium-shaped History DB (urls table) at
// path with the given rows. last_visit_time is stored in Chromium units.
func makeChromiumDB(t *testing.T, path string, rows []Entry) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE urls (url TEXT, title TEXT, visit_count INTEGER, last_visit_time INTEGER)`)
	require.NoError(t, err)
	for _, r := range rows {
		_, err = db.Exec(`INSERT INTO urls (url, title, visit_count, last_visit_time) VALUES (?, ?, ?, ?)`,
			r.URL, r.Title, r.VisitCount, chromiumTimeToDB(r.VisitTime))
		require.NoError(t, err)
	}
}

// makeFirefoxDB writes a minimal Firefox-shaped places DB (moz_places table).
// last_visit_date is microseconds since the Unix epoch.
func makeFirefoxDB(t *testing.T, path string, rows []Entry) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE moz_places (url TEXT, title TEXT, visit_count INTEGER, last_visit_date INTEGER)`)
	require.NoError(t, err)
	for _, r := range rows {
		_, err = db.Exec(`INSERT INTO moz_places (url, title, visit_count, last_visit_date) VALUES (?, ?, ?, ?)`,
			r.URL, r.Title, r.VisitCount, r.VisitTime.UnixMicro())
		require.NoError(t, err)
	}
	// A never-visited bookmark row: last_visit_date NULL must be skipped, not crash.
	_, err = db.Exec(`INSERT INTO moz_places (url, title, visit_count, last_visit_date) VALUES ('http://never.example/', 'nv', 0, NULL)`)
	require.NoError(t, err)
}

func TestReadSources_ChromiumTimestampAndSort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "History")
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	makeChromiumDB(t, path, []Entry{
		{URL: "https://old.example/", Title: "old", VisitCount: 1, VisitTime: base},
		{URL: "https://new.example/", Title: "new", VisitCount: 5, VisitTime: base.Add(48 * time.Hour)},
	})

	got, err := readSources([]source{{browser: "chrome", profile: "Default", path: path}}, Query{})
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Newest first.
	assert.Equal(t, "https://new.example/", got[0].URL)
	assert.Equal(t, "https://old.example/", got[1].URL)
	// Timestamp round-trips through the 1601-epoch conversion.
	assert.True(t, got[1].VisitTime.Equal(base), "chromium ts must convert back to the original instant")
	assert.Equal(t, 5, got[0].VisitCount)
	assert.Equal(t, "chrome", got[0].Browser)
}

func TestReadSources_SinceFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "History")
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	makeChromiumDB(t, path, []Entry{
		{URL: "https://a.example/", VisitTime: base},                     // old
		{URL: "https://b.example/", VisitTime: base.Add(10 * 24 * time.Hour)}, // recent
	})

	got, err := readSources([]source{{browser: "chrome", path: path}},
		Query{Since: base.Add(5 * 24 * time.Hour)})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "https://b.example/", got[0].URL)
}

func TestReadSources_DomainFilterAndSubdomain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "History")
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	makeChromiumDB(t, path, []Entry{
		{URL: "https://mail.google.com/inbox", VisitTime: base.Add(3 * time.Hour)},
		{URL: "https://www.bing.com/search", VisitTime: base.Add(2 * time.Hour)},
		{URL: "https://google.com/", VisitTime: base.Add(1 * time.Hour)},
	})

	got, err := readSources([]source{{browser: "chrome", path: path}},
		Query{Domains: []string{"google.com"}})
	require.NoError(t, err)
	require.Len(t, got, 2, "google.com and its subdomain mail.google.com match; bing.com does not")
	for _, e := range got {
		assert.Contains(t, e.URL, "google.com")
	}
}

func TestReadSources_LimitAfterMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "History")
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	rows := make([]Entry, 0, 5)
	for i := 0; i < 5; i++ {
		rows = append(rows, Entry{URL: "https://x.example/" + string(rune('a'+i)),
			VisitTime: base.Add(time.Duration(i) * time.Hour)})
	}
	makeChromiumDB(t, path, rows)

	got, err := readSources([]source{{browser: "chrome", path: path}}, Query{Limit: 2})
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Newest two by visit time.
	assert.Equal(t, "https://x.example/e", got[0].URL)
	assert.Equal(t, "https://x.example/d", got[1].URL)
}

func TestReadSources_MergeChromiumAndFirefox(t *testing.T) {
	dir := t.TempDir()
	cPath := filepath.Join(dir, "History")
	fPath := filepath.Join(dir, "places.sqlite")
	base := time.Date(2026, 7, 5, 8, 0, 0, 0, time.UTC)
	makeChromiumDB(t, cPath, []Entry{{URL: "https://chromium.example/", VisitTime: base.Add(1 * time.Hour)}})
	makeFirefoxDB(t, fPath, []Entry{{URL: "https://firefox.example/", Title: "ff", VisitCount: 2, VisitTime: base.Add(2 * time.Hour)}})

	got, err := readSources([]source{
		{browser: "chrome", path: cPath},
		{browser: "firefox", path: fPath, firefox: true},
	}, Query{})
	require.NoError(t, err)
	require.Len(t, got, 2, "NULL-visit Firefox bookmark row must be skipped, real rows merged")
	// Firefox row is newer -> first; its Unix-epoch timestamp must convert correctly.
	assert.Equal(t, "https://firefox.example/", got[0].URL)
	assert.True(t, got[0].VisitTime.Equal(base.Add(2*time.Hour)))
	assert.Equal(t, "firefox", got[0].Browser)
}

func TestReadSources_UnreadableSourceSkipped(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "History")
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	makeChromiumDB(t, good, []Entry{{URL: "https://ok.example/", VisitTime: base}})

	got, err := readSources([]source{
		{browser: "chrome", path: filepath.Join(dir, "does-not-exist")}, // skipped
		{browser: "chrome", path: good},
	}, Query{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "https://ok.example/", got[0].URL)
}
