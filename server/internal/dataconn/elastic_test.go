package dataconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func esOp(method, path string, query map[string]any) Operation {
	return Operation{
		Index:    "orders",
		ESMethod: method,
		ESPath:   path,
		Query:    query,
	}
}

func TestESNormalizeAggregations(t *testing.T) {
	// A size:0 date_histogram with a metric sub-agg -> one row per bucket,
	// columns key/doc_count/<sub-agg>.
	raw := []byte(`{
		"hits": {"hits": []},
		"aggregations": {
			"per_min": {
				"buckets": [
					{"key_as_string": "2026-06-23T19:38:00", "key": 1750000680000, "doc_count": 12, "errs": {"value": 3}},
					{"key_as_string": "2026-06-23T19:39:00", "key": 1750000740000, "doc_count": 7,  "errs": {"value": 1}}
				]
			}
		}
	}`)
	rs, err := esNormalize(esResolved{template: "_search", method: "POST"}, raw, 1000)
	if err != nil {
		t.Fatalf("esNormalize: %v", err)
	}
	if cols := columnNames(rs); strings.Join(cols, ",") != "key,doc_count,errs" {
		t.Fatalf("columns=%v want [key doc_count errs]", cols)
	}
	if len(rs.Rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rs.Rows))
	}
	if rs.Rows[0][0] != "2026-06-23T19:38:00" || rs.Rows[0][1] != float64(12) || rs.Rows[0][2] != float64(3) {
		t.Fatalf("row0=%v want [time 12 3]", rs.Rows[0])
	}

	// Row limit truncates buckets.
	rs, err = esNormalize(esResolved{template: "_search", method: "POST"}, raw, 1)
	if err != nil {
		t.Fatalf("esNormalize(limit=1): %v", err)
	}
	if len(rs.Rows) != 1 || !rs.Truncated {
		t.Fatalf("rows=%d truncated=%v want 1/true", len(rs.Rows), rs.Truncated)
	}
}

// TestESClassifyWhitelist drives the (method, endpoint) -> read/write table of
// W1 PoC conclusions §2.3: reads and writes resolve to their mode with the
// touched indices in ResourceRef.Objects; anything off the table — including
// _msearch, management APIs, _reindex and DELETE {index} — is rejected with
// ErrUnsupported.
func TestESClassifyWhitelist(t *testing.T) {
	c := NewElasticsearchConnector()
	searchBody := map[string]any{"query": map[string]any{"match_all": map[string]any{}}}
	doc := map[string]any{"name": "alpha"}

	cases := []struct {
		name string
		op   Operation
		mode AccessMode
		objs []string
	}{
		{"get search", esOp("GET", "_search", searchBody), ModeRead, []string{"orders"}},
		{"post search", esOp("POST", "_search", searchBody), ModeRead, []string{"orders"}},
		{"get search no body", esOp("GET", "_search", nil), ModeRead, []string{"orders"}},
		// checklist #141: kNN vector search rides _search and stays a read.
		{"knn search", esOp("POST", "_search", map[string]any{
			"knn": map[string]any{"field": "vec", "query_vector": []any{0.1, 0.2}, "k": 5},
		}), ModeRead, []string{"orders"}},
		{"lowercase method", esOp("get", "_search", nil), ModeRead, []string{"orders"}},
		{"get count", esOp("GET", "_count", nil), ModeRead, []string{"orders"}},
		{"post count", esOp("POST", "_count", searchBody), ModeRead, []string{"orders"}},
		{"mget", esOp("POST", "_mget", map[string]any{"ids": []any{"1", "2"}}), ModeRead, []string{"orders"}},
		{"get doc", esOp("GET", "_doc/42", nil), ModeRead, []string{"orders"}},
		{"get source", esOp("GET", "_source/42", nil), ModeRead, []string{"orders"}},
		{"get mapping", esOp("GET", "_mapping", nil), ModeRead, []string{"orders"}},

		{"index doc put", esOp("PUT", "_doc/42", doc), ModeWrite, []string{"orders"}},
		{"index doc post autoid", esOp("POST", "_doc", doc), ModeWrite, []string{"orders"}},
		{"create doc", esOp("PUT", "_create/42", doc), ModeWrite, []string{"orders"}},
		{"update doc", esOp("POST", "_update/42", map[string]any{"doc": doc}), ModeWrite, []string{"orders"}},
		{"update by query", esOp("POST", "_update_by_query", map[string]any{
			"query": map[string]any{"term": map[string]any{"x": 1}}, "script": map[string]any{"source": "ctx._source.x=2"},
		}), ModeWrite, []string{"orders"}},
		{"delete by query", esOp("POST", "_delete_by_query", searchBody), ModeWrite, []string{"orders"}},
		{"delete doc", esOp("DELETE", "_doc/42", nil), ModeWrite, []string{"orders"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, ref, err := c.Classify(tc.op)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode != tc.mode {
				t.Fatalf("mode: got %s want %s", mode, tc.mode)
			}
			if !equalStringSet(ref.Objects, tc.objs) {
				t.Fatalf("objects: got %v want %v", ref.Objects, tc.objs)
			}
			if !ref.ReferencesTables {
				t.Fatal("ES ops always reference an index; ReferencesTables must be true")
			}
		})
	}

	denied := []struct {
		name string
		op   Operation
	}{
		// W1 §2.3 explicit phase-1 rejections.
		{"msearch", esOp("POST", "_msearch", nil)},
		{"reindex", esOp("POST", "_reindex", searchBody)},
		{"delete index ddl", esOp("DELETE", "", nil)},
		{"settings", esOp("GET", "_settings", nil)},
		{"cat", esOp("GET", "_cat/indices", nil)},
		{"scripts", esOp("POST", "_scripts/foo", doc)},
		// Wrong method for a whitelisted endpoint.
		{"delete search", esOp("DELETE", "_search", nil)},
		{"get bulk", esOp("GET", "_bulk", nil)},
		{"get update_by_query", esOp("GET", "_update_by_query", nil)},
		{"post create", esOp("POST", "_create/42", doc)},
		{"get mget", esOp("GET", "_mget", map[string]any{"ids": []any{"1"}})},
		// Malformed / hostile path shapes.
		{"doc missing id", esOp("GET", "_doc", nil)},
		{"doc trailing slash", esOp("GET", "_doc/", nil)},
		{"path traversal", esOp("GET", "_doc/../_cluster/settings", nil)},
		{"id with slash", esOp("GET", "_doc/a/b", nil)},
		{"id url-encoded", esOp("GET", "_doc/%2e%2e", nil)},
		{"id leading underscore", esOp("GET", "_doc/_async", nil)},
		{"leading slash path", esOp("GET", "/_search", nil)},
		{"unknown api", esOp("GET", "_explain/1", nil)},
	}
	for _, tc := range denied {
		t.Run("deny "+tc.name, func(t *testing.T) {
			_, _, err := c.Classify(tc.op)
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("want ErrUnsupported, got %v", err)
			}
		})
	}
}

// TestESClassifyBodyPolicy: endpoints that take no request body reject a
// populated Query (fail-closed — nothing rides along an id-addressed GET or
// DELETE), and endpoints whose semantics require a body reject a missing one.
func TestESClassifyBodyPolicy(t *testing.T) {
	c := NewElasticsearchConnector()
	body := map[string]any{"k": "v"}
	rejected := []struct {
		name string
		op   Operation
	}{
		{"get doc with body", esOp("GET", "_doc/1", body)},
		{"get source with body", esOp("GET", "_source/1", body)},
		{"get mapping with body", esOp("GET", "_mapping", body)},
		{"delete doc with body", esOp("DELETE", "_doc/1", body)},
		{"mget without body", esOp("POST", "_mget", nil)},
		{"index doc without body", esOp("PUT", "_doc/1", nil)},
		{"create without body", esOp("PUT", "_create/1", nil)},
		{"update without body", esOp("POST", "_update/1", nil)},
		{"bulk without body", esOp("POST", "_bulk", nil)},
		{"update_by_query without body", esOp("POST", "_update_by_query", nil)},
		{"delete_by_query without body", esOp("POST", "_delete_by_query", nil)},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := c.Classify(tc.op); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// TestESClassifyIndexValidation: Operation.Index must be one concrete index —
// wildcards, commas, exclusions, _all, remote-cluster syntax and names ES
// itself forbids are rejected before any scope check (W1 §2.3 fail-closed).
func TestESClassifyIndexValidation(t *testing.T) {
	c := NewElasticsearchConnector()
	bad := []string{
		"", "logs-*", "log?s", "a,b", "-orders", "_all", "remote:orders",
		"+orders", "_internal", ".", "..", "Orders", "or ders", "a/b", "a\\b",
		"a#b", "a\"b", "a<b", strings.Repeat("x", 256),
		// Dot-prefixed names are ES system/hidden indices (.security-7,
		// .kibana, .ds-* backing indices) — never a legitimate agent target.
		".security-7", ".kibana",
	}
	for _, idx := range bad {
		op := esOp("GET", "_search", nil)
		op.Index = idx
		if _, _, err := c.Classify(op); err == nil {
			t.Errorf("index %q: expected error", idx)
		}
	}
	good := []string{"orders", "logs-2026.06.10", "my_index", "a"}
	for _, idx := range good {
		op := esOp("GET", "_search", nil)
		op.Index = idx
		if _, _, err := c.Classify(op); err != nil {
			t.Errorf("index %q: unexpected error %v", idx, err)
		}
	}
}

// TestESClassifyBulk: the _bulk body is Query={"operations": [...]} — the
// NDJSON lines as a JSON array. Action lines may address another index via
// _index; every addressed index joins ResourceRef.Objects so the scope gate
// sees all of them (W1 §2.3). Malformed sequences are rejected.
func TestESClassifyBulk(t *testing.T) {
	c := NewElasticsearchConnector()
	bulk := func(ops ...any) Operation {
		return esOp("POST", "_bulk", map[string]any{"operations": ops})
	}
	action := func(verb, index string) map[string]any {
		a := map[string]any{}
		if index != "" {
			a["_index"] = index
		}
		return map[string]any{verb: a}
	}
	doc := map[string]any{"name": "alpha"}

	t.Run("indices collected across action lines", func(t *testing.T) {
		op := bulk(
			action("index", "other"), doc,
			action("create", ""), doc, // defaults to the path index
			action("update", "third"), map[string]any{"doc": doc},
			action("delete", ""),
		)
		mode, ref, err := c.Classify(op)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mode != ModeWrite {
			t.Fatalf("mode: got %s want write", mode)
		}
		if !equalStringSet(ref.Objects, []string{"orders", "other", "third"}) {
			t.Fatalf("objects: got %v", ref.Objects)
		}
	})

	malformed := []struct {
		name string
		op   Operation
	}{
		{"operations missing", esOp("POST", "_bulk", map[string]any{"docs": []any{}})},
		{"operations empty", bulk()},
		{"operations not array", esOp("POST", "_bulk", map[string]any{"operations": "x"})},
		{"action two keys", bulk(map[string]any{"index": map[string]any{}, "create": map[string]any{}}, doc)},
		{"unknown verb", bulk(map[string]any{"upsert": map[string]any{}}, doc)},
		{"missing source line", bulk(action("index", ""))},
		{"wildcard action index", bulk(action("index", "logs-*"), doc)},
		{"action not object", bulk("index", doc)},
		{"action value not object", bulk(map[string]any{"delete": "x"})},
	}
	for _, tc := range malformed {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := c.Classify(tc.op); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// TestESClassifyMGet: like _bulk action lines, _mget docs[] entries can
// re-target another index via _index — every override must be validated and
// surfaced in ResourceRef.Objects for the scope gate, and the body must be
// exactly one of {docs} / {ids} (fail-closed).
func TestESClassifyMGet(t *testing.T) {
	c := NewElasticsearchConnector()

	t.Run("docs _index overrides collected", func(t *testing.T) {
		op := esOp("POST", "_mget", map[string]any{"docs": []any{
			map[string]any{"_index": "other", "_id": "1"},
			map[string]any{"_id": "2"}, // defaults to the path index
		}})
		mode, ref, err := c.Classify(op)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mode != ModeRead {
			t.Fatalf("mode: got %s want read", mode)
		}
		if !equalStringSet(ref.Objects, []string{"orders", "other"}) {
			t.Fatalf("objects: got %v", ref.Objects)
		}
	})

	t.Run("ids form keeps path index only", func(t *testing.T) {
		op := esOp("POST", "_mget", map[string]any{"ids": []any{"1", "2"}})
		_, ref, err := c.Classify(op)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStringSet(ref.Objects, []string{"orders"}) {
			t.Fatalf("objects: got %v", ref.Objects)
		}
	})

	malformed := []map[string]any{
		{"docs": []any{map[string]any{"_index": "logs-*", "_id": "1"}}}, // wildcard override
		{"docs": []any{map[string]any{"_index": 7, "_id": "1"}}},       // non-string override
		{"docs": []any{"x"}},                                           // entry not an object
		{"docs": []any{}},                                              // empty docs
		{"ids": []any{}},                                               // empty ids
		{"ids": "1"},                                                   // ids not an array
		{"docs": []any{map[string]any{"_id": "1"}}, "ids": []any{"2"}}, // both forms
		{"source": true},                                               // neither form
	}
	for i, q := range malformed {
		if _, _, err := c.Classify(esOp("POST", "_mget", q)); err == nil {
			t.Errorf("malformed mget body %d: expected error", i)
		}
	}
}

// fakeES wraps httptest with the X-Elastic-Product header the v8 client
// verifies, and returns a ConnConfig pointing at the fake.
func fakeES(t *testing.T, handler http.HandlerFunc) ConnConfig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return ConnConfig{
		Kind: KindElasticsearch,
		Host: host,
		Port: port,
		Options: map[string]any{
			"scheme": "http",
		},
	}
}

func TestESPing(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"name":"node-1","cluster_name":"poc","version":{"number":"8.18.0"}}`)
	})
	if err := c.Ping(context.Background(), cc); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestESPingError(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
	})
	if err := c.Ping(context.Background(), cc); err == nil {
		t.Fatal("expected error from 401 info response")
	}
}

// TestESExecuteSearch: hits.hits[]._source are flattened into columns (meta
// columns first, then sorted source keys), the raw response is preserved in
// Raw, and the server-injected RowLimit clamps the request "size".
func TestESExecuteSearch(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/orders/_search" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		// RowLimit+1 is requested so the normalizer can detect truncation
		// (the extra hit is dropped and flips Truncated).
		if size, ok := body["size"].(float64); !ok || size != 6 {
			t.Errorf("request size: got %v want 6 (RowLimit+1 injected)", body["size"])
		}
		fmt.Fprint(w, `{
			"took": 3,
			"hits": {
				"total": {"value": 2},
				"hits": [
					{"_index":"orders","_id":"1","_score":1.5,"_source":{"name":"alpha","n":1,"tags":["a","b"]}},
					{"_index":"orders","_id":"2","_score":1.1,"_source":{"name":"beta","n":2,"tags":null}}
				]
			}
		}`)
	})
	op := esOp("POST", "_search", map[string]any{"query": map[string]any{"match_all": map[string]any{}}})
	op.RowLimit = 5
	rs, err := c.Execute(context.Background(), cc, op)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	wantCols := []string{"_index", "_id", "_score", "n", "name", "tags"}
	if len(rs.Columns) != len(wantCols) {
		t.Fatalf("columns: got %v want %v", rs.Columns, wantCols)
	}
	for i, w := range wantCols {
		if rs.Columns[i].Name != w {
			t.Fatalf("column %d: got %s want %s", i, rs.Columns[i].Name, w)
		}
	}
	if len(rs.Rows) != 2 {
		t.Fatalf("rows: got %d want 2", len(rs.Rows))
	}
	if rs.Rows[0][4] != "alpha" {
		t.Errorf("row0 name: got %v", rs.Rows[0][4])
	}
	if rs.Rows[0][3] != float64(1) {
		t.Errorf("row0 n: got %v", rs.Rows[0][3])
	}
	// Arrays/objects are JSON-encoded into a string cell.
	if s, ok := rs.Rows[0][5].(string); !ok || s != `["a","b"]` {
		t.Errorf("row0 tags: got %v", rs.Rows[0][5])
	}
	if rs.Truncated {
		t.Error("not truncated")
	}
	if rs.Engine != string(KindElasticsearch) {
		t.Errorf("engine: got %s", rs.Engine)
	}
	if len(rs.Raw) == 0 || !strings.Contains(string(rs.Raw), `"took"`) {
		t.Errorf("raw response not preserved: %s", rs.Raw)
	}
}

// TestESExecuteSearchKeepsSmallerSize: an agent-requested size below the
// RowLimit is forwarded unchanged (no truncation probing needed — the agent
// asked for less, mirroring a SQL LIMIT below the row cap).
func TestESExecuteSearchKeepsSmallerSize(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if size, ok := body["size"].(float64); !ok || size != 3 {
			t.Errorf("request size: got %v want 3 (agent value kept)", body["size"])
		}
		fmt.Fprint(w, `{"hits":{"hits":[]}}`)
	})
	op := esOp("POST", "_search", map[string]any{"size": float64(3)})
	op.RowLimit = 5
	if _, err := c.Execute(context.Background(), cc, op); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

// TestESExecuteSearchClampsHugeSize: an absurd agent "size" must still be
// clamped to RowLimit+1 — float64→int conversion of huge values is
// implementation-defined and must not be allowed to skip the clamp.
func TestESExecuteSearchClampsHugeSize(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if size, ok := body["size"].(float64); !ok || size != 3 {
			t.Errorf("request size: got %v want 3 (RowLimit+1)", body["size"])
		}
		fmt.Fprint(w, `{"hits":{"hits":[]}}`)
	})
	op := esOp("POST", "_search", map[string]any{"size": float64(1e19)})
	op.RowLimit = 2
	if _, err := c.Execute(context.Background(), cc, op); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

// TestESExecuteSearchExactLimit: a result of exactly RowLimit hits is NOT
// truncated — the probe hit (RowLimit+1) is what flips the flag.
func TestESExecuteSearchExactLimit(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"hits":{"hits":[
			{"_id":"1","_source":{"n":1}},
			{"_id":"2","_source":{"n":2}}
		]}}`)
	})
	op := esOp("GET", "_search", nil)
	op.RowLimit = 2
	rs, err := c.Execute(context.Background(), cc, op)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rs.Rows) != 2 || rs.Truncated {
		t.Fatalf("rows=%d truncated=%v, want 2/false", len(rs.Rows), rs.Truncated)
	}
}

func TestESExecuteSearchTruncates(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		// Reply with more hits than the limit regardless of requested size.
		fmt.Fprint(w, `{"hits":{"hits":[
			{"_id":"1","_source":{"n":1}},
			{"_id":"2","_source":{"n":2}},
			{"_id":"3","_source":{"n":3}}
		]}}`)
	})
	op := esOp("GET", "_search", map[string]any{"size": 100})
	op.RowLimit = 2
	rs, err := c.Execute(context.Background(), cc, op)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rs.Rows) != 2 || !rs.Truncated {
		t.Fatalf("rows=%d truncated=%v, want 2/true", len(rs.Rows), rs.Truncated)
	}
}

func TestESExecuteCount(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"count":42,"_shards":{"total":1}}`)
	})
	rs, err := c.Execute(context.Background(), cc, esOp("GET", "_count", nil))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rs.Columns) != 1 || rs.Columns[0].Name != "count" {
		t.Fatalf("columns: %v", rs.Columns)
	}
	if len(rs.Rows) != 1 || rs.Rows[0][0] != float64(42) {
		t.Fatalf("rows: %v", rs.Rows)
	}
}

func TestESExecuteGetDoc(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders/_doc/42" {
			t.Errorf("path: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"_index":"orders","_id":"42","found":true,"_source":{"name":"alpha"}}`)
	})
	rs, err := c.Execute(context.Background(), cc, esOp("GET", "_doc/42", nil))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rs.Rows) != 1 {
		t.Fatalf("rows: %v", rs.Rows)
	}
}

// TestESExecuteGetSource: GET {index}/_source/{id} returns the bare document,
// rendered as one row over its sorted top-level keys.
func TestESExecuteGetSource(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orders/_source/42" {
			t.Errorf("path: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"name":"alpha","n":1}`)
	})
	rs, err := c.Execute(context.Background(), cc, esOp("GET", "_source/42", nil))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rs.Columns) != 2 || rs.Columns[0].Name != "n" || rs.Columns[1].Name != "name" {
		t.Fatalf("columns: %v", rs.Columns)
	}
	if len(rs.Rows) != 1 || rs.Rows[0][1] != "alpha" {
		t.Fatalf("rows: %v", rs.Rows)
	}
}

// TestESExecuteMGetMissingDoc: an _mget response mixing found and not-found
// entries normalizes without error (missing docs keep null source cells).
func TestESExecuteMGetMissingDoc(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"docs":[
			{"_index":"orders","_id":"1","found":false},
			{"_index":"orders","_id":"2","found":true,"_source":{"name":"beta"}}
		]}`)
	})
	rs, err := c.Execute(context.Background(), cc, esOp("POST", "_mget", map[string]any{"ids": []any{"1", "2"}}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rs.Rows) != 2 {
		t.Fatalf("rows: %v", rs.Rows)
	}
}

func TestESExecuteWriteRowsAffected(t *testing.T) {
	c := NewElasticsearchConnector()
	cases := []struct {
		name     string
		op       Operation
		response string
		want     int64
	}{
		{"index doc", esOp("PUT", "_doc/1", map[string]any{"a": 1}), `{"result":"created"}`, 1},
		{"update noop", esOp("POST", "_update/1", map[string]any{"doc": map[string]any{}}), `{"result":"noop"}`, 0},
		{"update by query", esOp("POST", "_update_by_query", map[string]any{"query": map[string]any{}}), `{"updated":5}`, 5},
		{"delete by query", esOp("POST", "_delete_by_query", map[string]any{"query": map[string]any{}}), `{"deleted":3}`, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.response)
			})
			rs, err := c.Execute(context.Background(), cc, tc.op)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if rs.RowsAffected != tc.want {
				t.Fatalf("rows affected: got %d want %d", rs.RowsAffected, tc.want)
			}
		})
	}
}

// TestESExecuteBulkNDJSON: Execute turns Query.operations into NDJSON with the
// x-ndjson content type, and reports one affected row per response item.
func TestESExecuteBulkNDJSON(t *testing.T) {
	c := NewElasticsearchConnector()
	var gotBody string
	var gotCT string
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotBody = string(b)
		gotCT = r.Header.Get("Content-Type")
		// One item failed (409) and one update was a noop: only the two
		// items that actually mutated a document count as affected.
		fmt.Fprint(w, `{"errors":true,"items":[{"index":{"status":201}},{"create":{"status":409,"error":{"type":"version_conflict_engine_exception"}}},{"update":{"status":200,"result":"noop"}},{"delete":{"status":200,"result":"deleted"}}]}`)
	})
	op := esOp("POST", "_bulk", map[string]any{"operations": []any{
		map[string]any{"index": map[string]any{"_id": "1"}},
		map[string]any{"name": "alpha"},
		map[string]any{"delete": map[string]any{"_id": "2"}},
	}})
	rs, err := c.Execute(context.Background(), cc, op)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if rs.RowsAffected != 2 {
		t.Fatalf("rows affected: got %d want 2", rs.RowsAffected)
	}
	if !strings.HasPrefix(gotCT, "application/x-ndjson") {
		t.Errorf("content type: %s", gotCT)
	}
	lines := strings.Split(strings.TrimRight(gotBody, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("ndjson lines: got %d (%q)", len(lines), gotBody)
	}
}

// TestESExecuteErrorStatus: a 4xx/5xx ES response surfaces as an error (the
// v8 client does not turn HTTP errors into Go errors by itself — W1 §1).
func TestESExecuteErrorStatus(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"type":"parsing_exception","reason":"bad query"}}`)
	})
	_, err := c.Execute(context.Background(), cc, esOp("POST", "_search", map[string]any{"query": map[string]any{}}))
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want HTTP 400 error, got %v", err)
	}
}

// TestESExecuteRevalidates: Execute re-runs the whitelist (defense in depth) —
// an off-table op must fail before any HTTP request is sent.
func TestESExecuteRevalidates(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no HTTP request should be issued for a rejected op")
	})
	if _, err := c.Execute(context.Background(), cc, esOp("POST", "_msearch", nil)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}

// TestESExecuteBasicAuth: ConnConfig.User/Password become HTTP basic auth.
func TestESExecuteBasicAuth(t *testing.T) {
	c := NewElasticsearchConnector()
	cc := fakeES(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "elastic" || pass != "s3cret" {
			t.Errorf("basic auth: ok=%v user=%s", ok, user)
		}
		fmt.Fprint(w, `{"count":0}`)
	})
	cc.User, cc.Password = "elastic", "s3cret"
	if _, err := c.Execute(context.Background(), cc, esOp("GET", "_count", nil)); err != nil {
		t.Fatalf("execute: %v", err)
	}
}
