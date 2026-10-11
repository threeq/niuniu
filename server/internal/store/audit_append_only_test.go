package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// auditTables are the append-only record. Nothing in the application may
// rewrite or remove a row here: an audit trail that can be edited after the
// fact is not evidence (#743, "防篡改：审计记录 append-only").
//
// integration_approval_requests is deliberately NOT in this list. It is a work
// queue whose status must change as requests are decided, escalated or
// invalidated; the immutable record of those decisions lives in org_audit_log.
var auditTables = []string{
	"external_api_audit",
	"data_source_audit",
	"org_audit_log",
}

// TestAuditTablesAreNeverUpdatedOrDeleted is the other half of #743's
// append-only acceptance ("审计记录不可改删（API 无入口 + 测试）"). It scans both
// the sqlc sources and the generated store code, so a mutation added by hand
// fails just as loudly as one added to a query file.
func TestAuditTablesAreNeverUpdatedOrDeleted(t *testing.T) {
	files := []string{}
	for _, pattern := range []string{"queries/*.sql", "*.sql.go"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		files = append(files, matches...)
	}
	if len(files) < 10 {
		t.Fatalf("expected to scan the query sources and generated store, found only %d files", len(files))
	}

	report := func(path string, lineNo int, verb, table, line string) {
		t.Errorf("%s:%d mutates the append-only audit table %s with %s: %s",
			path, lineNo, table, verb, strings.TrimSpace(line))
	}

	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			verb, table, ok := mutationTarget(line)
			if !ok {
				continue
			}
			for _, audit := range auditTables {
				if table == audit {
					report(path, i+1, verb, audit, line)
				}
			}
		}
	}

	// The scan above only sees canonical statements: the sqlc sources and the
	// SQL they generate. Hand-written Go can mutate just as effectively through
	// a raw string handed to db.Exec, so every non-test Go file under
	// internal/ is scanned too -- otherwise the guard would be narrower than
	// the guarantee it claims.
	for _, path := range goFilesUnderInternal(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			lower := strings.ToLower(line)
			for _, audit := range auditTables {
				for _, verb := range []string{"update ", "delete from "} {
					if strings.Contains(lower, verb+audit) {
						report(path, i+1, strings.ToUpper(strings.TrimSpace(verb)), audit, line)
					}
				}
			}
		}
	}
}

// goFilesUnderInternal walks the server's Go sources, skipping the frontend
// tree and test files (a test may legitimately assert on such a statement).
func goFilesUnderInternal(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..") // server/internal/store -> server/
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "web" || d.Name() == "node_modules" || d.Name() == "dist" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		out = append(out, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(out) < 50 {
		t.Fatalf("expected to scan the Go sources under %s, found only %d", root, len(out))
	}
	return out
}

// TestAuditWritePathsAreInsertsOnly documents the sanctioned way to add to the
// audit trail: an INSERT, and only an INSERT. If a rename or a new writer
// shows up without an INSERT, this fails.
func TestAuditWritePathsAreInsertsOnly(t *testing.T) {
	assertQueryIsInsert := func(file, name string) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("queries", file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		body := queryBody(string(raw), name)
		if body == "" {
			t.Fatalf("query %s not found in %s", name, file)
		}
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(body)), "INSERT INTO") {
			t.Errorf("%s must be an INSERT, got: %s", name, firstLine(body))
		}
	}
	assertQueryIsInsert("external_providers.sql", "CreateAuditLog")
	assertQueryIsInsert("data_sources.sql", "CreateDataSourceAudit")
	assertQueryIsInsert("org_audit_log.sql", "AppendOrgAudit")
}

// mutationTarget reports the verb and table of an UPDATE / DELETE statement
// line. It tolerates the shapes both the query sources and the generated Go
// use (leading whitespace, backticks, trailing semicolons).
func mutationTarget(line string) (verb, table string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 {
		return "", "", false
	}
	verb = strings.ToUpper(fields[0])
	switch verb {
	case "UPDATE":
		return verb, normaliseIdent(fields[1]), true
	case "DELETE":
		if len(fields) < 3 || strings.ToUpper(fields[1]) != "FROM" {
			return "", "", false
		}
		return verb, normaliseIdent(fields[2]), true
	default:
		return "", "", false
	}
}

// normaliseIdent strips the punctuation that can trail an identifier in SQL or
// in a Go raw string.
func normaliseIdent(s string) string {
	return strings.ToLower(strings.Trim(s, "`\";(),"))
}

// queryBody returns the SQL body of the named `-- name: X :kind` block.
func queryBody(src, name string) string {
	marker := "-- name: " + name + " "
	i := strings.Index(src, marker)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	if nl := strings.Index(rest, "\n"); nl >= 0 {
		rest = rest[nl+1:]
	}
	if next := strings.Index(rest, "-- name: "); next >= 0 {
		rest = rest[:next]
	}
	return rest
}

func firstLine(s string) string {
	if nl := strings.Index(s, "\n"); nl >= 0 {
		return s[:nl]
	}
	return s
}
