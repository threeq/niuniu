package relayclient

import (
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DefaultTrustedMobilesScope is used when auth is disabled (personal edition).
const DefaultTrustedMobilesScope = "default"

// TrustedMobile holds metadata for a paired mobile device.
type TrustedMobile struct {
	XpubHex    string
	Name       string
	Platform   string
	PairedAt   time.Time
	LastSeenAt time.Time // zero value when never seen
}

// trustedMobilesDBPath returns the path to the SQLite database used for
// trusted mobile metadata.
func trustedMobilesDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".niuniu", "relay", "trusted_mobiles.db"), nil
}

// legacyTrustedMobilesPath returns the old text-file path (pre-SQLite).
func legacyTrustedMobilesPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".niuniu", "relay", "trusted_mobiles.txt"), nil
}

// openTrustedMobilesDB opens (creating if needed) the SQLite database and
// ensures the schema exists, including the `scope` column used for per-user
// isolation in multi-user niuniu-server deployments.
func openTrustedMobilesDB() (*sql.DB, error) {
	dbPath, err := trustedMobilesDBPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	// New schema: primary key is (scope, xpub_hex) so different users can
	// trust the same mobile without colliding.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS trusted_mobiles (
		scope         TEXT NOT NULL DEFAULT '` + DefaultTrustedMobilesScope + `',
		xpub_hex      TEXT NOT NULL,
		name          TEXT NOT NULL DEFAULT '',
		platform      TEXT NOT NULL DEFAULT '',
		paired_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen_at  DATETIME,
		PRIMARY KEY (scope, xpub_hex)
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}

	// Back-compat migration: older DBs (created before this column existed)
	// had `xpub_hex` as the sole PK.  Detect the missing column and add it;
	// existing rows inherit DefaultTrustedMobilesScope via the column DEFAULT.
	if !hasColumn(db, "trusted_mobiles", "scope") {
		_, err = db.Exec(`ALTER TABLE trusted_mobiles
			ADD COLUMN scope TEXT NOT NULL DEFAULT '` + DefaultTrustedMobilesScope + `'`)
		if err != nil {
			db.Close()
			return nil, err
		}
		// SQLite can't alter PK in place; we leave the old PRIMARY KEY on
		// xpub_hex alone — it still prevents duplicates within the default
		// scope, which is sufficient for a single-user upgrade path.  Fresh
		// installs get the composite PK via the CREATE above.
	}
	return db, nil
}

// hasColumn reports whether the given column exists on the given table.
func hasColumn(db *sql.DB, table, column string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			continue
		}
		if name == column {
			return true
		}
	}
	return false
}

// migrateLegacyIfNeeded imports entries from the legacy .txt file (if it
// exists and hasn't already been migrated) into the SQLite DB under the
// DefaultTrustedMobilesScope.  On success the .txt file is renamed to .migrated.
func migrateLegacyIfNeeded(db *sql.DB) {
	legacyPath, err := legacyTrustedMobilesPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		return
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	lines := splitLines(string(data))
	for _, line := range lines {
		line = trimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if _, err := hex.DecodeString(line); err != nil {
			continue
		}
		_, _ = tx.Exec(
			`INSERT OR IGNORE INTO trusted_mobiles (scope, xpub_hex, name, platform, paired_at) VALUES (?, ?, '', '', ?)`,
			DefaultTrustedMobilesScope, line, now,
		)
	}
	if err := tx.Commit(); err != nil {
		return
	}
	_ = os.Rename(legacyPath, legacyPath+".migrated")
}

// LoadTrustedMobiles returns all trusted mobile entries for the given scope,
// migrating from the legacy .txt file first if needed.
func LoadTrustedMobiles(scope string) ([]TrustedMobile, error) {
	if scope == "" {
		scope = DefaultTrustedMobilesScope
	}
	db, err := openTrustedMobilesDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	migrateLegacyIfNeeded(db)

	rows, err := db.Query(
		`SELECT xpub_hex, name, platform, paired_at, last_seen_at FROM trusted_mobiles WHERE scope = ? ORDER BY paired_at`,
		scope,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TrustedMobile
	for rows.Next() {
		var m TrustedMobile
		var pairedAtStr string
		var lastSeenStr sql.NullString
		if err := rows.Scan(&m.XpubHex, &m.Name, &m.Platform, &pairedAtStr, &lastSeenStr); err != nil {
			continue
		}
		m.PairedAt = parseDateTime(pairedAtStr)
		if lastSeenStr.Valid && lastSeenStr.String != "" {
			m.LastSeenAt = parseDateTime(lastSeenStr.String)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LoadTrustedMobileBytes returns the trusted-mobile xpubs for the scope as
// raw byte slices (used by lanhost.Server).
func LoadTrustedMobileBytes(scope string) [][]byte {
	mobs, err := LoadTrustedMobiles(scope)
	if err != nil {
		return nil
	}
	out := make([][]byte, 0, len(mobs))
	for _, m := range mobs {
		b, err := hex.DecodeString(m.XpubHex)
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

// AppendTrustedMobile adds a trusted mobile to the database for the scope.
// name and platform may be empty strings when not yet known.
func AppendTrustedMobile(scope string, xpub []byte, name, platform string) error {
	if scope == "" {
		scope = DefaultTrustedMobilesScope
	}
	db, err := openTrustedMobilesDB()
	if err != nil {
		return err
	}
	defer db.Close()

	migrateLegacyIfNeeded(db)

	hexStr := hex.EncodeToString(xpub)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err = db.Exec(
		`INSERT OR REPLACE INTO trusted_mobiles (scope, xpub_hex, name, platform, paired_at) VALUES (?, ?, ?, ?, ?)`,
		scope, hexStr, name, platform, now,
	)
	return err
}

// RemoveTrustedMobile deletes a trusted mobile from the database under the given scope.
func RemoveTrustedMobile(scope, xpubHex string) error {
	if scope == "" {
		scope = DefaultTrustedMobilesScope
	}
	db, err := openTrustedMobilesDB()
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.Exec(`DELETE FROM trusted_mobiles WHERE scope = ? AND xpub_hex = ?`, scope, xpubHex)
	return err
}

// MarkSeen updates the last_seen_at timestamp for the given xpub hex in the scope.
func MarkSeen(scope, xpubHex string) error {
	if scope == "" {
		scope = DefaultTrustedMobilesScope
	}
	db, err := openTrustedMobilesDB()
	if err != nil {
		return err
	}
	defer db.Close()

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err = db.Exec(
		`UPDATE trusted_mobiles SET last_seen_at = ? WHERE scope = ? AND xpub_hex = ?`,
		now, scope, xpubHex,
	)
	return err
}

// parseDateTime parses a SQLite DATETIME string.
func parseDateTime(s string) time.Time {
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\r' || s[0] == '\n') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}
