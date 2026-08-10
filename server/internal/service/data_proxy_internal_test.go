// Internal (white-box) tests for data_proxy helpers that are unexported:
// summarizeStatement (value redaction, I4) and splitDBTable (3-part name
// handling, M1).
package service

import (
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
)

func TestSummarizeStatementRedactsLiterals(t *testing.T) {
	got := summarizeStatement("SELECT * FROM users WHERE ssn='123-45-6789' AND age=42")
	// The literal values must not survive redaction.
	if strings.Contains(got, "123-45-6789") {
		t.Fatalf("string literal leaked into summary: %q", got)
	}
	if strings.Contains(got, "42") {
		t.Fatalf("numeric literal leaked into summary: %q", got)
	}
	// Redaction placeholders must be present.
	if !strings.Contains(got, "'?'") {
		t.Fatalf("expected redacted string literal '?' in summary: %q", got)
	}
	want := "SELECT * FROM users WHERE ssn='?' AND age=?"
	if got != want {
		t.Fatalf("summary: got %q want %q", got, want)
	}
}

func TestSummarizeStatementCaps(t *testing.T) {
	long := ""
	for i := 0; i < 500; i++ {
		long += "a"
	}
	got := summarizeStatement(long)
	// 200 runes + "..." suffix.
	if len([]rune(got)) != 203 {
		t.Fatalf("expected 203 runes (200 cap + ...), got %d", len([]rune(got)))
	}
}

func TestSummarizeOperationSQLUnchanged(t *testing.T) {
	op := dataconn.Operation{Statement: "SELECT * FROM users WHERE ssn='123-45-6789'"}
	got := summarizeOperation(dataconn.KindPostgres, op)
	want := "SELECT * FROM users WHERE ssn='?'"
	if got != want {
		t.Fatalf("SQL summary: got %q want %q", got, want)
	}
}

func TestSummarizeMongoRedactsValues(t *testing.T) {
	op := dataconn.Operation{
		Collection: "users",
		MongoOp:    "find",
		Filter: map[string]any{
			"ssn":  "123-45-6789",
			"age":  map[string]any{"$gt": 42},
			"tags": []any{"a", "b", "c"},
		},
	}
	got := summarizeOperation(dataconn.KindMongo, op)
	for _, leaked := range []string{"123-45-6789", "42", `"a"`} {
		if strings.Contains(got, leaked) {
			t.Fatalf("value %s leaked into mongo summary: %q", leaked, got)
		}
	}
	// Structural skeleton must survive: op, collection, keys, $ operators.
	for _, kept := range []string{"find", "users", "ssn", "$gt", "tags"} {
		if !strings.Contains(got, kept) {
			t.Fatalf("expected %q in mongo summary skeleton: %q", kept, got)
		}
	}
	// Scalar arrays collapse to a single placeholder (no value-count leak).
	if !strings.Contains(got, `["?"]`) {
		t.Fatalf("expected scalar array collapsed to [\"?\"]: %q", got)
	}
}

func TestSummarizeMongoPipelineAndDocument(t *testing.T) {
	op := dataconn.Operation{
		Collection: "orders",
		MongoOp:    "aggregate",
		Pipeline: []map[string]any{
			{"$match": map[string]any{"status": "paid"}},
			{"$out": "report"},
		},
	}
	got := summarizeOperation(dataconn.KindMongo, op)
	if strings.Contains(got, "paid") {
		t.Fatalf("pipeline value leaked: %q", got)
	}
	for _, kept := range []string{"aggregate", "orders", "$match", "status", "$out"} {
		if !strings.Contains(got, kept) {
			t.Fatalf("expected %q in pipeline summary: %q", kept, got)
		}
	}

	// Insert document: field names only, never values.
	op = dataconn.Operation{
		Collection: "users",
		MongoOp:    "insertOne",
		Document:   map[string]any{"email": "a@b.c", "name": "Zoe"},
	}
	got = summarizeOperation(dataconn.KindMongo, op)
	if strings.Contains(got, "a@b.c") || strings.Contains(got, "Zoe") {
		t.Fatalf("document value leaked: %q", got)
	}
	if !strings.Contains(got, "email") || !strings.Contains(got, "name") {
		t.Fatalf("expected document field names in summary: %q", got)
	}
}

func TestSummarizeRedisRedactsValues(t *testing.T) {
	cases := []struct {
		cmd  string
		args []string
		want string
	}{
		// Value-bearing args redacted; key kept (keys are the audit identity).
		{"set", []string{"cache:user:1", "secret-value"}, "SET cache:user:1 ?"},
		// Keep-all commands: args are keys/fields/cursors, no values.
		{"MGET", []string{"k1", "k2"}, "MGET k1 k2"},
		{"SCAN", []string{"0", "MATCH", "cache:*", "COUNT", "100"}, "SCAN 0 MATCH cache:* COUNT 100"},
		// MSET: keys kept (even positions), values redacted.
		{"MSET", []string{"k1", "v1", "k2", "v2"}, "MSET k1 ? k2 ?"},
		// HSET: key + field names kept, field values redacted.
		{"HSET", []string{"h", "f1", "v1", "f2", "v2"}, "HSET h f1 ? f2 ?"},
		// Default rule: first arg (key) kept, rest redacted.
		{"ZADD", []string{"z", "1.5", "member-payload"}, "ZADD z ? ?"},
	}
	for _, c := range cases {
		got := summarizeOperation(dataconn.KindRedis, dataconn.Operation{Command: c.cmd, Args: c.args})
		if got != c.want {
			t.Fatalf("redis summary for %s: got %q want %q", c.cmd, got, c.want)
		}
	}
}

func TestSummarizeESRedactsValues(t *testing.T) {
	op := dataconn.Operation{
		Index:    "orders",
		ESMethod: "post",
		ESPath:   "_search",
		Query: map[string]any{
			"query": map[string]any{"match": map[string]any{"customer": "Alice Chen"}},
			"size":  10,
		},
	}
	got := summarizeOperation(dataconn.KindElasticsearch, op)
	if strings.Contains(got, "Alice") || strings.Contains(got, "10") {
		t.Fatalf("ES query value leaked: %q", got)
	}
	for _, kept := range []string{"POST", "orders", "_search", "query", "match", "customer", "size"} {
		if !strings.Contains(got, kept) {
			t.Fatalf("expected %q in ES summary skeleton: %q", kept, got)
		}
	}
}

func TestRedactValueDepthCap(t *testing.T) {
	// Hostile nesting beyond the cap collapses to "?" instead of recursing.
	deep := map[string]any{}
	cur := deep
	for i := 0; i < 30; i++ {
		next := map[string]any{}
		cur["n"] = next
		cur = next
	}
	cur["leaf"] = "value"
	got := summarizeOperation(dataconn.KindMongo, dataconn.Operation{
		Collection: "c", MongoOp: "find", Filter: deep,
	})
	if strings.Contains(got, "leaf") || strings.Contains(got, "value") {
		t.Fatalf("depth cap failed, deep content leaked: %q", got)
	}
}

func TestCheckScopeSysSchemaBypassesDBAllowlist(t *testing.T) {
	scope := scopeConfig{databases: []string{"analytics"}}

	// information_schema query must pass even though it is not in databases allowlist.
	ref := dataconn.ResourceRef{
		Objects:          []string{"information_schema.tables"},
		ReferencesTables: true,
	}
	if reason := checkScope(dataconn.KindMySQL, scope, ref); reason != "" {
		t.Fatalf("information_schema should bypass db allowlist, got: %q", reason)
	}

	// pg_catalog similarly exempt.
	ref.Objects = []string{"pg_catalog.pg_tables"}
	if reason := checkScope(dataconn.KindMySQL, scope, ref); reason != "" {
		t.Fatalf("pg_catalog should bypass db allowlist, got: %q", reason)
	}

	// A non-system database not in the allowlist must still be denied.
	ref.Objects = []string{"secret.payments"}
	if reason := checkScope(dataconn.KindMySQL, scope, ref); reason == "" {
		t.Fatal("expected scope_denied for non-allowed database 'secret'")
	}
}

func TestCheckScopeRedisPrefix(t *testing.T) {
	scope := scopeConfig{
		databases:   []string{"ignored"}, // not applicable to redis; must be ignored
		tablesAllow: []string{"cache:", "sess:"},
		tablesDeny:  []string{"cache:secret:"},
	}
	ok := func(keys ...string) dataconn.ResourceRef {
		return dataconn.ResourceRef{Objects: keys, ReferencesTables: true}
	}

	if reason := checkScope(dataconn.KindRedis, scope, ok("cache:user:1", "sess:abc")); reason != "" {
		t.Fatalf("keys under allow prefixes should pass, got: %q", reason)
	}
	// Out-of-prefix key -> denied.
	if reason := checkScope(dataconn.KindRedis, scope, ok("jobs:1")); reason == "" {
		t.Fatal("key outside allow prefixes must be denied")
	}
	// Multi-key op: every key must be in scope; one stray key rejects the op.
	if reason := checkScope(dataconn.KindRedis, scope, ok("cache:a", "jobs:1")); reason == "" {
		t.Fatal("any out-of-prefix key must reject the whole operation")
	}
	// Deny prefix wins over allow prefix.
	if reason := checkScope(dataconn.KindRedis, scope, ok("cache:secret:k")); reason == "" {
		t.Fatal("deny prefix must win over allow prefix")
	}
	// Redis keys are case-SENSITIVE: a case-mismatched prefix is out of scope.
	if reason := checkScope(dataconn.KindRedis, scope, ok("Cache:user:1")); reason == "" {
		t.Fatal("redis prefix match must be case-sensitive")
	}
	// Unresolvable keys (e.g. EVAL) stay fail-closed.
	if reason := checkScope(dataconn.KindRedis, scope, dataconn.ResourceRef{ReferencesTables: true}); reason == "" {
		t.Fatal("empty objects with ReferencesTables must be denied (fail-closed)")
	}
	// Empty allow list = no prefix restriction (deny still applies).
	open := scopeConfig{tablesDeny: []string{"cache:secret:"}}
	if reason := checkScope(dataconn.KindRedis, open, ok("anything")); reason != "" {
		t.Fatalf("empty allow list should not restrict, got: %q", reason)
	}
	if reason := checkScope(dataconn.KindRedis, open, ok("cache:secret:x")); reason == "" {
		t.Fatal("deny must apply even with empty allow list")
	}

	// A SCAN object is enumerative: a scan prefix that COVERS a deny subtree
	// (deny is an extension of the scan prefix) would enumerate denied key
	// names and must be rejected, even though no single denied key is named.
	scan := func(prefix string) dataconn.ResourceRef {
		return dataconn.ResourceRef{Objects: []string{prefix}, CommandCls: "SCAN", ReferencesTables: true}
	}
	if reason := checkScope(dataconn.KindRedis, scope, scan("cache:")); reason == "" {
		t.Fatal("SCAN prefix covering a deny subtree must be rejected")
	}
	if reason := checkScope(dataconn.KindRedis, open, scan("")); reason == "" {
		t.Fatal("SCAN MATCH * covers every deny subtree and must be rejected when a deny exists")
	}
	if reason := checkScope(dataconn.KindRedis, scope, scan("cache:user:")); reason != "" {
		t.Fatalf("SCAN prefix disjoint from deny subtrees should pass, got: %q", reason)
	}
	// Single-key commands keep plain forward matching: GET cache: (a literal
	// key equal to the prefix) does not cover the deny subtree.
	if reason := checkScope(dataconn.KindRedis, scope, ok("cache:")); reason != "" {
		t.Fatalf("non-SCAN key equal to an allow prefix should pass, got: %q", reason)
	}
}

func TestCheckScopeMongoCollections(t *testing.T) {
	scope := scopeConfig{
		databases:   []string{"analytics", "archive"},
		tablesAllow: []string{"orders", "archive.events"},
		tablesDeny:  []string{"users"},
	}
	ref := func(objs ...string) dataconn.ResourceRef {
		return dataconn.ResourceRef{Database: "analytics", Objects: objs, ReferencesTables: true}
	}

	if reason := checkScope(dataconn.KindMongo, scope, ref("orders")); reason != "" {
		t.Fatalf("allowed collection should pass, got: %q", reason)
	}
	// Exact, case-SENSITIVE collection match.
	if reason := checkScope(dataconn.KindMongo, scope, ref("Orders")); reason == "" {
		t.Fatal("mongo collection match must be case-sensitive")
	}
	if reason := checkScope(dataconn.KindMongo, scope, ref("payments")); reason == "" {
		t.Fatal("collection not in allow-list must be denied")
	}
	if reason := checkScope(dataconn.KindMongo, scope, ref("users")); reason == "" {
		t.Fatal("denied collection must be rejected")
	}
	// Qualified db.collection ($out/$merge cross-db targets): allow entry matches.
	if reason := checkScope(dataconn.KindMongo, scope, ref("orders", "archive.events")); reason != "" {
		t.Fatalf("qualified allow entry should pass, got: %q", reason)
	}
	// Qualified object whose db is outside the databases allowlist -> denied.
	if reason := checkScope(dataconn.KindMongo, scope, ref("secret.events")); reason == "" {
		t.Fatal("cross-db object outside databases allowlist must be denied")
	}
	// Unresolvable pipeline targets stay fail-closed.
	if reason := checkScope(dataconn.KindMongo, scope, dataconn.ResourceRef{ReferencesTables: true}); reason == "" {
		t.Fatal("empty objects with ReferencesTables must be denied (fail-closed)")
	}
}

func TestCheckScopeElasticIndex(t *testing.T) {
	scope := scopeConfig{
		tablesAllow: []string{"orders", "logs-*"},
		tablesDeny:  []string{"logs-secret-*"},
	}
	ref := func(objs ...string) dataconn.ResourceRef {
		return dataconn.ResourceRef{Objects: objs, ReferencesTables: true}
	}

	if reason := checkScope(dataconn.KindElasticsearch, scope, ref("orders")); reason != "" {
		t.Fatalf("exact allow entry should pass, got: %q", reason)
	}
	// Trailing-* allow entry covers rolling indices.
	if reason := checkScope(dataconn.KindElasticsearch, scope, ref("logs-2026.06.10")); reason != "" {
		t.Fatalf("rolling index under logs-* should pass, got: %q", reason)
	}
	// ES comparison is case-insensitive.
	if reason := checkScope(dataconn.KindElasticsearch, scope, ref("ORDERS")); reason != "" {
		t.Fatalf("ES index match should be case-insensitive, got: %q", reason)
	}
	// Deny pattern wins over allow pattern.
	if reason := checkScope(dataconn.KindElasticsearch, scope, ref("logs-secret-2026")); reason == "" {
		t.Fatal("deny pattern must win over allow pattern")
	}
	if reason := checkScope(dataconn.KindElasticsearch, scope, ref("metrics")); reason == "" {
		t.Fatal("index not in allow-list must be denied")
	}
	// Objects still carrying wildcards / commas / exclusions are unresolved -> denied.
	for _, bad := range []string{"logs-*", "a,b", "-orders", "ord?rs"} {
		if reason := checkScope(dataconn.KindElasticsearch, scope, ref(bad)); reason == "" {
			t.Fatalf("wildcard/multi index expression %q must be denied (fail-closed)", bad)
		}
	}
	if reason := checkScope(dataconn.KindElasticsearch, scope, dataconn.ResourceRef{ReferencesTables: true}); reason == "" {
		t.Fatal("empty objects with ReferencesTables must be denied (fail-closed)")
	}
}

// TestCheckScopeSQLTrinoThreePart: Trino federation addresses tables as
// catalog.schema.table. The databases dimension gates the catalog (the only
// defense across federated catalogs — W1 PoC 结论 §4-3); below catalog level a
// full three-part tables_allow/tables_deny entry pins one exact object.
func TestCheckScopeSQLTrinoThreePart(t *testing.T) {
	scope := scopeConfig{
		databases:  []string{"hive"},
		tablesDeny: []string{"hive.pii.users"},
	}
	ref := func(objs ...string) dataconn.ResourceRef {
		return dataconn.ResourceRef{Objects: objs, ReferencesTables: true}
	}

	// Cross-catalog object outside the databases allowlist -> denied.
	if reason := checkScope(dataconn.KindTrino, scope, ref("tpch.tiny.nation")); reason == "" {
		t.Fatal("cross-catalog object must be denied by the databases allowlist")
	}
	// In-catalog object passes.
	if reason := checkScope(dataconn.KindTrino, scope, ref("hive.sales.orders")); reason != "" {
		t.Fatalf("in-catalog object should pass, got: %q", reason)
	}
	// Three-part deny entry matches the full dotted name.
	if reason := checkScope(dataconn.KindTrino, scope, ref("hive.pii.users")); reason == "" {
		t.Fatal("three-part deny entry must match the full name")
	}
	// The sysSchemas exemption applies to two-part schema.table introspection
	// only. In a three-part name the first segment is a CATALOG — a catalog
	// named like a system schema must not bypass the catalog allowlist.
	for _, obj := range []string{"sys.private.data", "information_schema.x.y"} {
		if reason := checkScope(dataconn.KindTrino, scope, ref(obj)); reason == "" {
			t.Fatalf("catalog %q must not get the sys-schema exemption", obj)
		}
	}
	// Two-part introspection exemption still holds for the same scope.
	if reason := checkScope(dataconn.KindTrino, scope, ref("information_schema.tables")); reason != "" {
		t.Fatalf("two-part information_schema introspection should pass, got: %q", reason)
	}

	// Three-part allow entry pins one exact object (case-insensitive).
	allow := scopeConfig{tablesAllow: []string{"hive.sales.orders"}}
	if reason := checkScope(dataconn.KindTrino, allow, ref("HIVE.SALES.ORDERS")); reason != "" {
		t.Fatalf("three-part allow entry should match case-insensitively, got: %q", reason)
	}
	if reason := checkScope(dataconn.KindTrino, allow, ref("hive.sales.payments")); reason == "" {
		t.Fatal("object outside the three-part allow entry must be denied")
	}
	// A different schema with the same table name is NOT covered: the full
	// name misses the entry, and the ("hive","orders") split forms
	// ("orders" / "hive.orders") miss it too -> denied.
	if reason := checkScope(dataconn.KindTrino, allow, ref("hive.staging.orders")); reason == "" {
		t.Fatal("same table in another schema must not match the three-part allow entry")
	}
}

func TestSplitDBTable(t *testing.T) {
	cases := []struct {
		obj, db, table string
	}{
		{"orders", "", "orders"},
		{"db.table", "db", "table"},
		// 3-part Postgres catalog.schema.object: db is the FIRST segment, table
		// is the LAST (the middle schema segment is dropped).
		{"analytics.public.events", "analytics", "events"},
	}
	for _, c := range cases {
		db, table := splitDBTable(c.obj)
		if db != c.db || table != c.table {
			t.Fatalf("splitDBTable(%q) = (%q,%q) want (%q,%q)", c.obj, db, table, c.db, c.table)
		}
	}
}
