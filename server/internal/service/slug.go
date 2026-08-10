package service

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// deriveSlug builds a URL-safe slug from name. ASCII letters and digits are
// kept (lowercased); any other run of characters collapses to a single '-'.
// When the result is empty (e.g. a pure-Chinese name), falls back to
// "org-<6 hex>". Caller must ensure uniqueness against the organizations
// table.
func deriveSlug(name string) string {
	var b strings.Builder
	var prevDash bool
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		var buf [3]byte
		_, _ = rand.Read(buf[:])
		return "org-" + hex.EncodeToString(buf[:])
	}
	if len(s) > 48 {
		s = strings.TrimRight(s[:48], "-")
		if s == "" {
			s = "org"
		}
	}
	return s
}

// isSlugUniqueViolation matches the unique-constraint error on the slug
// column across SQLite (modernc.org/sqlite) and PostgreSQL (pgx). Loose
// string matching is intentional — we only need to distinguish "duplicate
// slug" from other DB errors, and avoiding pgconn imports keeps the
// service package driver-agnostic.
func isSlugUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "slug") {
		return false
	}
	// SQLite (modernc.org/sqlite): "UNIQUE constraint failed: organizations.slug"
	// PG/pgx: "ERROR: duplicate key value violates unique constraint
	//          \"organizations_slug_key\" (SQLSTATE 23505)"
	return strings.Contains(msg, "unique") || strings.Contains(msg, "23505")
}
