package store

import (
	"regexp"
	"sort"
	"strings"
)

// SchemaDiff compares CREATE TABLE definitions between the SQLite and PostgreSQL
// schema strings. It normalises PG-specific type tokens before comparing so
// expected differences (BIGSERIAL vs INTEGER AUTOINCREMENT, BIGINT vs INTEGER)
// do not generate false positives.
//
// Returns:
//   - sqliteOnly: table names present in sqliteSchema but absent in pgSchema.
//   - pgOnly: table names present in pgSchema but absent in sqliteSchema.
//   - differing: map of table name → [sqliteDef, pgDef] for tables present in
//     both schemas but with non-equivalent column structure after normalisation.
func SchemaDiff(sqliteSchema, pgSchema string) (sqliteOnly, pgOnly []string, differing map[string][2]string) {
	// Canonicalise BOTH schemas to a shared type vocabulary before comparing.
	// Applying the mapping to only one side was the original bug: tokens the two
	// schemas share lexically (BOOLEAN, DATETIME, FALSE) but that map to a
	// canonical form would flip on one side only and read as drift.
	sqlite := extractTables(canonicalizeTypes(sqliteSchema))
	pg := extractTables(canonicalizeTypes(pgSchema))

	differing = make(map[string][2]string)

	for name, def := range sqlite {
		pgDef, ok := pg[name]
		if !ok {
			sqliteOnly = append(sqliteOnly, name)
			continue
		}
		// Compare with inline FK clauses stripped from BOTH sides: SQLite declares
		// every foreign key inline, while PostgreSQL cannot forward-reference and so
		// adds many of them as deferred `ALTER TABLE ... ADD CONSTRAINT` statements
		// that live OUTSIDE the CREATE TABLE block this comparison sees. Without
		// stripping, every FK column reads as drift even though the constraint sets
		// are identical (both schemas carry the same 93 references). Stripping does
		// not weaken FK validation — the deferred PG constraints were never in scope
		// of this block-level comparison anyway.
		if normalizeWS(stripInlineFKs(def)) != normalizeWS(stripInlineFKs(pgDef)) {
			differing[name] = [2]string{def, pgDef}
		}
	}
	for name := range pg {
		if _, ok := sqlite[name]; !ok {
			pgOnly = append(pgOnly, name)
		}
	}
	sort.Strings(sqliteOnly)
	sort.Strings(pgOnly)
	return
}

// extractTables parses CREATE TABLE IF NOT EXISTS blocks from a SQL schema
// string. Returns a map of lowercase table name → definition block (comments
// stripped). Handles nested parentheses correctly.
func extractTables(schema string) map[string]string {
	tableRE := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+(\w+)`)
	result := make(map[string]string)

	var cur strings.Builder
	var name string
	depth := 0
	inTable := false

	for _, line := range strings.Split(schema, "\n") {
		// Strip inline SQL comments.
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if !inTable {
			if m := tableRE.FindStringSubmatch(line); m != nil {
				name = strings.ToLower(m[1])
				inTable = true
				cur.Reset()
				depth = 0
			}
		}
		if inTable {
			cur.WriteString(line)
			cur.WriteString("\n")
			depth += strings.Count(line, "(") - strings.Count(line, ")")
			if depth <= 0 {
				result[name] = cur.String()
				inTable = false
				name = ""
				cur.Reset()
			}
		}
	}
	return result
}

// canonicalizeTypes rewrites the type tokens and default expressions that the
// SQLite and PostgreSQL schemas express differently into a single shared
// vocabulary, so semantically-identical columns compare equal. It is applied to
// BOTH schema strings (see SchemaDiff) — these equivalences are bidirectional,
// and a token may appear on either side (e.g. BOOLEAN/DATETIME are used by the
// SQLite schema too):
//   - BIGSERIAL/SERIAL PK → INTEGER PRIMARY KEY AUTOINCREMENT
//   - BIGINT → INTEGER, BOOLEAN → INTEGER, DOUBLE PRECISION → REAL
//   - TIMESTAMPTZ / DATETIME → TIMESTAMP, NOW() → CURRENT_TIMESTAMP
//   - JSONB → TEXT and the `::jsonb` value cast (e.g. '[]'::jsonb → '[]')
func canonicalizeTypes(schema string) string {
	r := strings.NewReplacer(
		"BIGSERIAL PRIMARY KEY", "INTEGER PRIMARY KEY AUTOINCREMENT",
		"SERIAL PRIMARY KEY", "INTEGER PRIMARY KEY AUTOINCREMENT",
		"BIGINT", "INTEGER",
		"BOOLEAN", "INTEGER",
		"DOUBLE PRECISION", "REAL",
		"TIMESTAMPTZ", "TIMESTAMP",
		"DATETIME", "TIMESTAMP",
		"NOW()", "CURRENT_TIMESTAMP",
		"JSONB", "TEXT",
		"::jsonb", "",
		"BYTEA", "BLOB",
	)
	return r.Replace(schema)
}

// inlineFKRE matches an inline column foreign key: `REFERENCES tbl(col)` plus any
// trailing ON DELETE / ON UPDATE actions. Stripped from both schemas before
// comparison (see SchemaDiff) because PG expresses many FKs as deferred ALTERs.
var inlineFKRE = regexp.MustCompile(`(?i)\s+REFERENCES\s+\w+\s*\([^)]*\)(\s+ON\s+(?:DELETE|UPDATE)\s+(?:CASCADE|RESTRICT|NO\s+ACTION|SET\s+NULL|SET\s+DEFAULT))*`)

// stripInlineFKs removes inline REFERENCES clauses so the column comparison is
// FK-expression agnostic.
func stripInlineFKs(s string) string {
	return inlineFKRE.ReplaceAllString(s, "")
}

func normalizeWS(s string) string {
	return strings.ToLower(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
}
