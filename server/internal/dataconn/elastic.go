package dataconn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

// ESConnector implements Connector for Elasticsearch over HTTP/JSON DSL
// (issue #358, Epic #345 Wave3). The agent-facing surface is the structured
// Operation ES field group (Index + ESMethod + ESPath + Query); Classify
// resolves (method, endpoint) against the W1 PoC conclusions §2.3 whitelist
// (docs/superpowers/specs/2026-06-10-nosql-w1-poc-conclusions.md) — anything
// off the table (incl. _msearch, _reindex, _cat/cluster management, index DDL)
// is rejected with ErrUnsupported. Execute re-runs the same resolution as
// defense in depth, then performs the HTTP call via the official
// go-elasticsearch v8 client and normalizes hits/docs into ResultSet with the
// raw response preserved in Raw.
type ESConnector struct{}

func NewElasticsearchConnector() *ESConnector { return &ESConnector{} }

func (c *ESConnector) Kind() SourceKind { return KindElasticsearch }

type esBodyPolicy int

const (
	esBodyNone     esBodyPolicy = iota // endpoint takes no body; a body is rejected
	esBodyOptional                     // body allowed but not required
	esBodyRequired                     // endpoint is meaningless/dangerous without a body
)

// esEndpoints is the (method, endpoint) -> read/write whitelist of W1 §2.3.
// Template segments are matched literally except "{id}" which accepts one
// validated document-id segment. _search bodies are not inspected: query, knn
// and aggs are all reads (kNN vector search rides the free tier, checklist
// item "放行 knn 检索"). _msearch stays rejected in phase 1 (NDJSON header
// lines can re-target other indices; W1 supersedes the older issue summary).
//
// Known residual risk (accepted by W1 §2.3's no-body-inspection decision):
// query clauses with indexed lookups — terms lookup, percolate, geo_shape
// indexed-shape — can pull FIELD VALUES from a document in another index into
// the query. This applies to _search and equally to the "query" bodies of
// _update_by_query/_delete_by_query (the write itself still only touches the
// path index). They leak filter terms, not documents; rejecting "index" keys
// inside query clauses is a candidate hardening follow-up.
var esEndpoints = []struct {
	methods  string // space-separated allowed methods
	template string
	mode     AccessMode
	body     esBodyPolicy
}{
	{"GET POST", "_search", ModeRead, esBodyOptional},
	{"GET POST", "_count", ModeRead, esBodyOptional},
	{"POST", "_mget", ModeRead, esBodyRequired},
	{"GET", "_doc/{id}", ModeRead, esBodyNone},
	{"GET", "_source/{id}", ModeRead, esBodyNone},
	{"GET", "_mapping", ModeRead, esBodyNone},

	{"PUT POST", "_doc", ModeWrite, esBodyRequired},
	{"PUT POST", "_doc/{id}", ModeWrite, esBodyRequired},
	{"PUT", "_create/{id}", ModeWrite, esBodyRequired},
	{"POST", "_update/{id}", ModeWrite, esBodyRequired},
	{"POST", "_bulk", ModeWrite, esBodyRequired},
	{"POST", "_update_by_query", ModeWrite, esBodyRequired},
	{"POST", "_delete_by_query", ModeWrite, esBodyRequired},
	{"DELETE", "_doc/{id}", ModeWrite, esBodyNone},
}

type esResolved struct {
	method   string // normalized (uppercase)
	template string
	segments []string // concrete path segments (validated)
	mode     AccessMode
	body     esBodyPolicy
}

func (c *ESConnector) Classify(op Operation) (AccessMode, ResourceRef, error) {
	r, objects, err := classifyES(op)
	if err != nil {
		return "", ResourceRef{}, err
	}
	return r.mode, ResourceRef{
		Database:   op.Database,
		Objects:    objects,
		CommandCls: r.method + " " + r.template,
		// An ES operation always addresses an index; keep the scope gate's
		// fail-closed guard armed.
		ReferencesTables: true,
	}, nil
}

// classifyES is the shared validation path of Classify and Execute: index
// validation, whitelist resolution, body policy, and (for _bulk) per-action
// index collection.
func classifyES(op Operation) (esResolved, []string, error) {
	if err := validateESIndex(op.Index); err != nil {
		return esResolved{}, nil, err
	}
	r, err := resolveESEndpoint(op.ESMethod, op.ESPath)
	if err != nil {
		return esResolved{}, nil, err
	}
	switch r.body {
	case esBodyNone:
		if op.Query != nil {
			return esResolved{}, nil, fmt.Errorf("elasticsearch: %s %s takes no request body", r.method, r.template)
		}
	case esBodyRequired:
		if op.Query == nil {
			return esResolved{}, nil, fmt.Errorf("elasticsearch: %s %s requires a request body", r.method, r.template)
		}
	}
	objects := []string{op.Index}
	var extra []string
	switch r.template {
	case "_bulk":
		extra, err = esBulkIndices(op.Query)
	case "_mget":
		extra, err = esMgetIndices(op.Query)
	}
	if err != nil {
		return esResolved{}, nil, err
	}
	seen := map[string]bool{op.Index: true}
	for _, idx := range extra {
		if !seen[idx] {
			seen[idx] = true
			objects = append(objects, idx)
		}
	}
	return r, objects, nil
}

// resolveESEndpoint matches ESMethod+ESPath against the whitelist. Any miss —
// unknown endpoint, wrong method, malformed/hostile path segments — wraps
// ErrUnsupported (W1 §2.3: off-table is rejected as unsupported).
func resolveESEndpoint(method, path string) (esResolved, error) {
	m := strings.ToUpper(strings.TrimSpace(method))
	segs := strings.Split(path, "/")
	for _, s := range segs {
		if s == "" {
			return esResolved{}, fmt.Errorf("%w: elasticsearch endpoint %q %q is not whitelisted", ErrUnsupported, method, path)
		}
	}
	for _, e := range esEndpoints {
		tsegs := strings.Split(e.template, "/")
		if len(tsegs) != len(segs) || !methodAllowed(e.methods, m) {
			continue
		}
		match := true
		for i, ts := range tsegs {
			if ts == "{id}" {
				if validateESID(segs[i]) != nil {
					match = false
					break
				}
				continue
			}
			if ts != segs[i] {
				match = false
				break
			}
		}
		if match {
			return esResolved{method: m, template: e.template, segments: segs, mode: e.mode, body: e.body}, nil
		}
	}
	return esResolved{}, fmt.Errorf("%w: elasticsearch endpoint %q %q is not whitelisted", ErrUnsupported, method, path)
}

func methodAllowed(methods, m string) bool {
	for _, allowed := range strings.Fields(methods) {
		if allowed == m {
			return true
		}
	}
	return false
}

// validateESIndex enforces "one concrete index" (W1 §2.3): no wildcards,
// commas, exclusions, _all or remote-cluster syntax — rolling patterns live in
// scope entries, never in the request — no dot-prefixed system/hidden names,
// plus the names ES itself forbids (uppercase, illegal characters, leading
// -/_/+, >255 bytes).
func validateESIndex(name string) error {
	if name == "" {
		return fmt.Errorf("elasticsearch: index is required")
	}
	if len(name) > 255 {
		return fmt.Errorf("elasticsearch: index name exceeds 255 bytes")
	}
	if strings.HasPrefix(name, ".") {
		// Dot-prefixed names are ES system/hidden indices (.security-7,
		// .kibana, data-stream backing indices) — never a legitimate target.
		return fmt.Errorf("elasticsearch: index name %q is invalid (dot-prefixed names are reserved for system/hidden indices)", name)
	}
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, "_") || strings.HasPrefix(name, "+") {
		return fmt.Errorf("elasticsearch: index name %q is invalid (wildcards, _all, exclusions and names starting with -/_/+ are not allowed; rolling patterns belong in the source scope)", name)
	}
	for _, r := range name {
		if r >= 'A' && r <= 'Z' {
			return fmt.Errorf("elasticsearch: index name %q is invalid (must be lowercase)", name)
		}
		if r <= ' ' || strings.ContainsRune(`\/*?"<>|,#:%`, r) {
			return fmt.Errorf("elasticsearch: index name %q contains an invalid character", name)
		}
	}
	return nil
}

// validateESID vets a document-id path segment: no traversal, no URL-encoded
// characters, no leading underscore (which would alias an unknown _ API), no
// wildcards/commas.
func validateESID(id string) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("elasticsearch: invalid document id")
	}
	if len(id) > 512 {
		return fmt.Errorf("elasticsearch: document id too long")
	}
	if strings.HasPrefix(id, "_") {
		return fmt.Errorf("elasticsearch: invalid document id %q", id)
	}
	for _, r := range id {
		if r <= ' ' || r == 0x7f || strings.ContainsRune(`/\?#%*,"<>|`, r) {
			return fmt.Errorf("elasticsearch: document id contains an invalid character")
		}
	}
	return nil
}

// esBulkIndices parses the _bulk body — Query = {"operations": [...]}, the
// NDJSON lines as a JSON array — strictly: action lines must carry exactly one
// of index/create/update/delete, index/create/update must be followed by one
// document line, and any _index override is validated as a concrete index and
// returned for the scope gate (W1 §2.3: every action line's index passes
// scope, the path index is the default). Malformed sequences are rejected.
func esBulkIndices(query map[string]any) ([]string, error) {
	raw, ok := query["operations"]
	if !ok {
		return nil, fmt.Errorf(`elasticsearch: _bulk body must be {"operations": [...]} (the NDJSON lines as a JSON array)`)
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil, fmt.Errorf("elasticsearch: _bulk operations must be a non-empty array")
	}
	var indices []string
	for i := 0; i < len(arr); {
		line, ok := arr[i].(map[string]any)
		if !ok || len(line) != 1 {
			return nil, fmt.Errorf("elasticsearch: _bulk action line %d must be an object with exactly one action key", i)
		}
		var verb string
		var meta any
		for k, v := range line {
			verb, meta = k, v
		}
		switch verb {
		case "index", "create", "update", "delete":
		default:
			return nil, fmt.Errorf("elasticsearch: _bulk action %q at line %d is not allowed", verb, i)
		}
		m, ok := meta.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("elasticsearch: _bulk action %q metadata at line %d must be an object", verb, i)
		}
		if idxRaw, present := m["_index"]; present {
			idx, ok := idxRaw.(string)
			if !ok {
				return nil, fmt.Errorf("elasticsearch: _bulk _index at line %d must be a string", i)
			}
			if err := validateESIndex(idx); err != nil {
				return nil, err
			}
			indices = append(indices, idx)
		}
		i++
		if verb != "delete" {
			if i >= len(arr) {
				return nil, fmt.Errorf("elasticsearch: _bulk action %q is missing its document line", verb)
			}
			if _, ok := arr[i].(map[string]any); !ok {
				return nil, fmt.Errorf("elasticsearch: _bulk document line %d must be an object", i)
			}
			i++
		}
	}
	return indices, nil
}

// esMgetIndices parses the _mget body fail-closed: exactly one of "docs" /
// "ids" must be present and non-empty, and — like _bulk action lines — every
// docs[] entry that re-targets another index via _index is validated as one
// concrete index and returned for the scope gate.
func esMgetIndices(query map[string]any) ([]string, error) {
	docsRaw, hasDocs := query["docs"]
	idsRaw, hasIDs := query["ids"]
	if hasDocs == hasIDs {
		return nil, fmt.Errorf(`elasticsearch: _mget body must contain exactly one of "docs" or "ids"`)
	}
	if hasIDs {
		ids, ok := idsRaw.([]any)
		if !ok || len(ids) == 0 {
			return nil, fmt.Errorf("elasticsearch: _mget ids must be a non-empty array")
		}
		return nil, nil
	}
	docs, ok := docsRaw.([]any)
	if !ok || len(docs) == 0 {
		return nil, fmt.Errorf("elasticsearch: _mget docs must be a non-empty array")
	}
	var indices []string
	for i, d := range docs {
		m, ok := d.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("elasticsearch: _mget docs entry %d must be an object", i)
		}
		if idxRaw, present := m["_index"]; present {
			idx, ok := idxRaw.(string)
			if !ok {
				return nil, fmt.Errorf("elasticsearch: _mget docs entry %d _index must be a string", i)
			}
			if err := validateESIndex(idx); err != nil {
				return nil, err
			}
			indices = append(indices, idx)
		}
	}
	return indices, nil
}

// client builds a go-elasticsearch client from the decrypted ConnConfig.
// Supported Options keys: "scheme" ("http", default, or "https") and
// "api_key" (sent as an ApiKey Authorization header; otherwise User/Password
// become HTTP basic auth).
func (c *ESConnector) client(conn ConnConfig) (*elasticsearch.Client, error) {
	port := conn.Port
	if port == 0 {
		port = 9200
	}
	scheme := "http"
	if s, ok := conn.Options["scheme"].(string); ok && s != "" {
		scheme = s
	}
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("elasticsearch: unsupported scheme %q", scheme)
	}
	cfg := elasticsearch.Config{
		Addresses: []string{fmt.Sprintf("%s://%s:%d", scheme, conn.Host, port)},
		Username:  conn.User,
		Password:  conn.Password,
		// The transport retries 502/503/504 by default, which would re-send
		// non-idempotent writes (auto-id POST _doc, _bulk) whose first attempt
		// may already have landed — same reasoning as the redis connector's
		// MaxRetries:-1. The op timeout bounds the single attempt instead.
		DisableRetry: true,
	}
	if ak, ok := conn.Options["api_key"].(string); ok && ak != "" {
		cfg.APIKey = ak
	}
	return elasticsearch.NewClient(cfg)
}

func (c *ESConnector) Ping(ctx context.Context, conn ConnConfig) error {
	es, err := c.client(conn)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	res, err := es.Info(es.Info.WithContext(ctx))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.IsError() {
		// 4xx/5xx does not surface as a Go error from the client (W1 §1);
		// check explicitly.
		return fmt.Errorf("elasticsearch info: %s", res.Status())
	}
	return nil
}

// maxESResponseBytes caps how much of an ES response Execute will buffer —
// a backstop against hostile/misconfigured servers and runaway aggregations,
// generous enough for any RowLimit-bounded result.
const maxESResponseBytes = 32 << 20

// Execute re-validates the operation (defense in depth — the service gate has
// already classified and scope-checked it), performs the HTTP request and
// normalizes the response into a ResultSet. The server-injected RowLimit caps
// the requested _search hit count and the number of normalized rows; the raw
// response body is preserved in Raw, capped only by maxESResponseBytes (NOT
// row-capped — aggregation payloads are returned whole, matching the SQL
// connectors' uncapped-result behavior, and a truncated _search response
// deliberately keeps the RowLimit+1 probe hit: scope, not RowLimit, is the
// access-control boundary).
func (c *ESConnector) Execute(ctx context.Context, conn ConnConfig, op Operation) (*ResultSet, error) {
	r, _, err := classifyES(op)
	if err != nil {
		return nil, err
	}
	es, err := c.client(conn)
	if err != nil {
		return nil, err
	}
	to := time.Duration(op.TimeoutMS) * time.Millisecond
	if to <= 0 {
		to = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	limit := op.RowLimit
	if limit <= 0 {
		limit = 1000
	}

	body, contentType, err := esRequestBody(r, op, limit)
	if err != nil {
		return nil, err
	}
	// classifyES has already rejected an empty Index; assert the invariant the
	// URL safety rests on (an empty first segment would make the path
	// protocol-relative: "//_search" parses as host "_search").
	if op.Index == "" {
		return nil, fmt.Errorf("elasticsearch: empty index")
	}
	parts := make([]string, 0, 1+len(r.segments))
	parts = append(parts, url.PathEscape(op.Index))
	for _, s := range r.segments {
		parts = append(parts, url.PathEscape(s))
	}
	req, err := http.NewRequestWithContext(ctx, r.method, "/"+strings.Join(parts, "/"), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")

	res, err := es.Perform(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxESResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxESResponseBytes {
		return nil, fmt.Errorf("elasticsearch: response exceeds %d bytes; narrow the query", maxESResponseBytes)
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("elasticsearch: HTTP %d: %s", res.StatusCode, excerpt(raw, 2048))
	}

	rs, err := esNormalize(r, raw, limit)
	if err != nil {
		return nil, err
	}
	rs.Engine = string(KindElasticsearch)
	rs.Raw = json.RawMessage(raw)
	return rs, nil
}

// esRequestBody renders the request body: NDJSON for _bulk, JSON otherwise.
// For _search the server-side RowLimit is injected as "size" (kept only when
// the agent asked for less).
func esRequestBody(r esResolved, op Operation, limit int) (io.Reader, string, error) {
	if op.Query == nil {
		return nil, "", nil
	}
	if r.template == "_bulk" {
		ops := op.Query["operations"].([]any) // shape validated by classifyES
		var buf bytes.Buffer
		for _, line := range ops {
			b, err := json.Marshal(line)
			if err != nil {
				return nil, "", err
			}
			buf.Write(b)
			buf.WriteByte('\n')
		}
		return &buf, "application/x-ndjson", nil
	}
	q := op.Query
	if r.template == "_search" {
		q = clampSearchSize(q, limit)
	}
	b, err := json.Marshal(q)
	if err != nil {
		return nil, "", err
	}
	return bytes.NewReader(b), "application/json", nil
}

// clampSearchSize copies the body keeping an agent-provided "size" at or
// below the limit (the agent asked for less — like a SQL LIMIT under the row
// cap, no truncation signal is owed). A missing or over-limit size becomes
// limit+1: the probe hit lets the normalizer detect truncation, and is
// dropped before the rows are returned.
func clampSearchSize(q map[string]any, limit int) map[string]any {
	body := make(map[string]any, len(q)+1)
	for k, v := range q {
		body[k] = v
	}
	// Compare in float space: converting a huge float64 to int is
	// implementation-defined and must not be allowed to skip the clamp.
	if s, ok := body["size"].(float64); ok && s >= 0 && s <= float64(limit) {
		return body
	}
	if s, ok := body["size"].(int); ok && s >= 0 && s <= limit {
		return body
	}
	body["size"] = limit + 1
	return body
}

// esNormalize maps the decoded response to a ResultSet per endpoint shape:
// _search hits.hits / _mget docs / single-doc GETs are flattened into rows
// (W1 §3: _source keys become columns); _count becomes a one-cell result;
// writes report RowsAffected; anything else (e.g. _mapping) is raw-only.
func esNormalize(r esResolved, raw []byte, limit int) (*ResultSet, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("elasticsearch: decode response: %w", err)
	}
	switch {
	case r.template == "_search":
		// Aggregations (the common size:0 + date_histogram/terms charting
		// pattern) live in body.aggregations, NOT body.hits — flatten the first
		// aggregation's buckets into one row per bucket so a time-series / group
		// chart can bind to them. Fall back to hits when there are no buckets.
		if aggs, ok := body["aggregations"].(map[string]any); ok && len(aggs) > 0 {
			if rs := esAggResultSet(aggs, limit); rs != nil {
				return rs, nil
			}
		}
		if hits, ok := body["hits"].(map[string]any); ok {
			if arr, ok := hits["hits"].([]any); ok {
				return esDocsResultSet(arr, limit)
			}
		}
		return &ResultSet{}, nil
	case r.template == "_count":
		if n, ok := body["count"].(float64); ok {
			return &ResultSet{
				Columns: []Column{{Name: "count", Type: "number"}},
				Rows:    [][]any{{n}},
			}, nil
		}
		return &ResultSet{}, nil
	case r.template == "_mget":
		if arr, ok := body["docs"].([]any); ok {
			return esDocsResultSet(arr, limit)
		}
		return &ResultSet{}, nil
	case r.template == "_doc/{id}" && r.method == http.MethodGet:
		if found, ok := body["found"].(bool); ok && !found {
			return &ResultSet{}, nil
		}
		return esDocsResultSet([]any{body}, limit)
	case r.template == "_source/{id}":
		return esSourceResultSet(body), nil
	case r.mode == ModeWrite:
		return &ResultSet{RowsAffected: esRowsAffected(body)}, nil
	default: // _mapping and other raw-only reads
		return &ResultSet{}, nil
	}
}

// esRowsAffected derives the affected-row count from a write response:
// _bulk items that actually succeeded (per-item failures keep the overall
// HTTP status at 200), _update_by_query/_delete_by_query counters, or the
// single-document "result" verb (noop counts as zero).
func esRowsAffected(body map[string]any) int64 {
	if items, ok := body["items"].([]any); ok {
		var n int64
		for _, it := range items {
			m, ok := it.(map[string]any)
			if !ok || len(m) != 1 {
				continue
			}
			for _, v := range m { // single key: the action verb
				res, ok := v.(map[string]any)
				if !ok {
					continue
				}
				status, ok := res["status"].(float64)
				if !ok || status >= 300 {
					continue
				}
				// Parity with the single-document path: a noop update
				// succeeded but mutated nothing.
				if result, ok := res["result"].(string); ok && result == "noop" {
					continue
				}
				n++
			}
		}
		return n
	}
	updated, hasUpdated := body["updated"].(float64)
	deleted, hasDeleted := body["deleted"].(float64)
	if hasUpdated || hasDeleted {
		return int64(updated) + int64(deleted)
	}
	if result, ok := body["result"].(string); ok {
		if result == "noop" {
			return 0
		}
		return 1
	}
	return 0
}

// esDocsResultSet flattens hit/doc objects (anything carrying _index/_id/
// _score/_source) into rows: three meta columns followed by the sorted union
// of top-level _source keys across the kept rows.
func esDocsResultSet(docs []any, limit int) (*ResultSet, error) {
	rs := &ResultSet{}
	kept := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		if len(kept) >= limit {
			rs.Truncated = true
			break
		}
		m, ok := d.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("elasticsearch: unexpected hit shape in response")
		}
		kept = append(kept, m)
	}
	keySet := map[string]bool{}
	for _, h := range kept {
		if src, ok := h["_source"].(map[string]any); ok {
			for k := range src {
				keySet[k] = true
			}
		}
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rs.Columns = append(rs.Columns,
		Column{Name: "_index", Type: "string"},
		Column{Name: "_id", Type: "string"},
		Column{Name: "_score", Type: "number"},
	)
	for _, k := range keys {
		rs.Columns = append(rs.Columns, Column{Name: k, Type: "string"})
	}
	for _, h := range kept {
		row := make([]any, 0, len(rs.Columns))
		row = append(row, h["_index"], h["_id"], h["_score"])
		src, _ := h["_source"].(map[string]any)
		for i, k := range keys {
			row = append(row, esCell(src[k], &rs.Columns[3+i]))
		}
		rs.Rows = append(rs.Rows, row)
	}
	return rs, nil
}

// esAggResultSet flattens the FIRST bucket-bearing aggregation (by sorted name,
// for determinism) into rows. It covers the common single date_histogram / terms
// aggregation used for charts; nested multi-aggregation trees fall back to nil
// (the caller then returns hits / raw). Returns nil when no aggregation has a
// "buckets" array.
func esAggResultSet(aggs map[string]any, limit int) *ResultSet {
	names := make([]string, 0, len(aggs))
	for k := range aggs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		agg, ok := aggs[name].(map[string]any)
		if !ok {
			continue
		}
		buckets, ok := agg["buckets"].([]any)
		if !ok {
			continue
		}
		return esBucketsResultSet(buckets, limit)
	}
	return nil
}

// esBucketsResultSet renders aggregation buckets as rows: a "key" column (the
// bucket key, preferring key_as_string for date_histogram), "doc_count", then
// one column per metric sub-aggregation ({"value": n}), sorted for stable order.
func esBucketsResultSet(buckets []any, limit int) *ResultSet {
	rs := &ResultSet{}
	kept := make([]map[string]any, 0, len(buckets))
	subSet := map[string]bool{}
	for _, b := range buckets {
		if len(kept) >= limit {
			rs.Truncated = true
			break
		}
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		kept = append(kept, m)
		for k, v := range m {
			if k == "key" || k == "key_as_string" || k == "doc_count" {
				continue
			}
			if mm, ok := v.(map[string]any); ok {
				if _, hasVal := mm["value"]; hasVal {
					subSet[k] = true
				}
			}
		}
	}
	subs := make([]string, 0, len(subSet))
	for k := range subSet {
		subs = append(subs, k)
	}
	sort.Strings(subs)

	rs.Columns = []Column{{Name: "key", Type: "string"}, {Name: "doc_count", Type: "number"}}
	for _, s := range subs {
		rs.Columns = append(rs.Columns, Column{Name: s, Type: "number"})
	}
	for _, m := range kept {
		var key any = m["key"]
		if ks, ok := m["key_as_string"].(string); ok && ks != "" {
			key = ks
		}
		row := []any{key, m["doc_count"]}
		for _, s := range subs {
			var val any
			if mm, ok := m[s].(map[string]any); ok {
				val = mm["value"]
			}
			row = append(row, val)
		}
		rs.Rows = append(rs.Rows, row)
	}
	return rs
}

// esSourceResultSet renders a GET {index}/_source/{id} response — the bare
// document — as a single row over its sorted top-level keys.
func esSourceResultSet(src map[string]any) *ResultSet {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rs := &ResultSet{}
	row := make([]any, 0, len(keys))
	for _, k := range keys {
		rs.Columns = append(rs.Columns, Column{Name: k, Type: "string"})
		row = append(row, esCell(src[k], &rs.Columns[len(rs.Columns)-1]))
	}
	rs.Rows = [][]any{row}
	return rs
}

// esCell coerces a decoded JSON value into a JSON-friendly cell and refines
// the column type from the first non-null value (nested objects/arrays are
// re-encoded as JSON strings, type "json").
func esCell(v any, col *Column) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		col.Type = "string"
		return x
	case float64:
		col.Type = "number"
		return x
	case bool:
		col.Type = "bool"
		return x
	default: // map[string]any / []any
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		col.Type = "json"
		return string(b)
	}
}

// excerpt truncates an error body for the returned error message.
func excerpt(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
