package dataconn

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	readLeadRe = regexp.MustCompile(`^(?i)(select|show|explain|describe|desc|with)\b`)
	// writeAnyRe matches a write keyword anywhere (whole-word) in the
	// comment-stripped statement. A CTE/WITH-led statement that hides a DELETE
	// (e.g. "WITH x AS (SELECT 1) DELETE FROM users") must classify as a write,
	// so we scan the whole text rather than only the leading keyword. Note: SET
	// is intentionally NOT in this set — it appears in every UPDATE ... SET and
	// would mis-fire; a bare "SET ..." session command leads with neither a read
	// nor a write keyword and is rejected as unrecognized.
	writeAnyRe = regexp.MustCompile(`(?i)\b(insert|update|delete|replace|merge|create|alter|drop|truncate|grant|revoke|call)\b`)
	// tableRefRe flags a statement that references tables (FROM/JOIN/INTO/UPDATE/TABLE).
	tableRefRe = regexp.MustCompile(`(?i)\b(from|join|into|update|table)\b`)
	// identRe captures the object name after a FROM/JOIN/INTO/UPDATE/TABLE
	// keyword. Quoted forms (`x`, "x", [x]) are normalized away by
	// normalizeQuotedIdents before this runs, so a plain ident capture suffices.
	// The dotted segment repeats so multi-part names are captured whole:
	// "db.table", a Postgres "db.schema.table", and a Trino federated
	// "catalog.schema.table" — truncating to the first two segments would make
	// splitDBTable report the schema as the table and break the scope gate.
	identRe = regexp.MustCompile(`(?i)\b(?:from|join|into|update|table)\s+([a-zA-Z_][\w$]*(?:\.[a-zA-Z_][\w$]*)*)`)
	// cteNameRe captures CTE alias names from "WITH a AS (...), b AS (...)".
	// It matches each "<name> AS (" occurrence; the names are aliases (not base
	// tables) and are excluded from the extracted object set.
	cteNameRe = regexp.MustCompile(`(?i)([a-zA-Z_][\w$]*)\s+AS\s*\(`)
	// quoteStripper removes identifier-quoting characters (backtick, double
	// quote, square brackets) so quoted idents extract their bare name.
	quoteStripper = strings.NewReplacer("`", "", `"`, "", "[", "", "]", "")
)

// normalizeQuotedIdents strips identifier-quoting characters so quoted
// identifiers extract their bare name: FROM `db`.`tbl` -> FROM db.tbl,
// FROM "schema"."events" -> FROM schema.events, FROM [db].[tbl] -> FROM db.tbl.
// This errs toward classifying ambiguous text as a write (fail-safe) and only
// affects object extraction; double-quoted string literals collapsing to bare
// text is acceptable here because the read/write classification already scans
// for write keywords independently.
func normalizeQuotedIdents(s string) string {
	return quoteStripper.Replace(s)
}

// stripSQLComments removes -- line and /* */ block comments so a comment
// can't hide a stacked statement or a write keyword.
func stripSQLComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '-' && s[i+1] == '-' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i += 2
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// classifySQL parses a single SQL statement into read/write + touched objects.
// Rejects empty input and stacked statements (defense against injection).
func classifySQL(raw string) (AccessMode, ResourceRef, error) {
	clean := strings.TrimSpace(stripSQLComments(raw))
	if clean == "" {
		return "", ResourceRef{}, fmt.Errorf("empty statement")
	}
	// Reject stacked statements: a ';' followed by more non-space content.
	if idx := strings.IndexByte(clean, ';'); idx >= 0 {
		if strings.TrimSpace(clean[idx+1:]) != "" {
			return "", ResourceRef{}, fmt.Errorf("multiple statements are not allowed")
		}
		clean = strings.TrimSpace(clean[:idx])
	}
	// Classify WRITE if ANY whole-word write keyword appears anywhere in the
	// statement — a CTE/WITH-led statement that hides a DELETE/UPDATE/INSERT must
	// not slip through as a read just because it leads with WITH. Only classify
	// READ when no write keyword is present AND the statement leads with a read
	// keyword. (False positives from keywords-as-identifiers are acceptable: a
	// misclassified read merely triggers a confirmation card, never data risk.)
	var mode AccessMode
	switch {
	case writeAnyRe.MatchString(clean):
		mode = ModeWrite
	case readLeadRe.MatchString(clean):
		mode = ModeRead
	default:
		return "", ResourceRef{}, fmt.Errorf("unrecognized statement")
	}

	// Object extraction runs on a quote-normalized copy so quoted identifiers
	// (`db`.`tbl`, "schema"."events", [db].[tbl]) extract their bare names.
	normalized := normalizeQuotedIdents(clean)

	// Collect CTE alias names ("WITH a AS (...), b AS (...)") so they can be
	// excluded from the object set — they are aliases, not base tables. The real
	// base tables inside CTE bodies are still captured by the FROM/JOIN scan.
	cteAliases := map[string]bool{}
	for _, m := range cteNameRe.FindAllStringSubmatch(normalized, -1) {
		cteAliases[strings.ToLower(m[1])] = true
	}

	var objs []string
	seen := map[string]bool{}
	for _, m := range identRe.FindAllStringSubmatch(normalized, -1) {
		name := strings.ToLower(m[1])
		if cteAliases[name] {
			continue
		}
		if !seen[name] {
			seen[name] = true
			objs = append(objs, m[1])
		}
	}
	return mode, ResourceRef{Objects: objs, ReferencesTables: tableRefRe.MatchString(normalized)}, nil
}

func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[strings.ToLower(x)]++
	}
	for _, x := range b {
		m[strings.ToLower(x)]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}
