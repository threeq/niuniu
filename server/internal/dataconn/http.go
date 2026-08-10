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
)

// HTTPConnector implements Connector for a generic HTTP/JSON REST data source.
// The agent-facing surface is the structured Operation HTTP field group
// (HTTPMethod + HTTPPath + HTTPQuery + HTTPBody); the source config supplies the
// base URL (host/port/options.scheme/options.base_path), optional HTTP basic
// auth (user/password) and static request headers (options.headers, e.g. an
// Authorization bearer token — secret). Classify maps the verb to read/write
// (GET/HEAD/POST read, PUT/PATCH/DELETE write) and exposes the request path as
// the single scope object so the data-proxy gate can authorize it against
// path-prefix allow/deny lists. POST is a read by default because JSON APIs
// overwhelmingly use it for queries (search / GraphQL / JSON-RPC) — the same
// reasoning the elasticsearch connector applies to POST _search — so a
// read-only http source supports GET and POST out of the box; only the
// unambiguous mutating verbs (PUT/PATCH/DELETE) need a read-write source.
// Execute performs the request (redirects disabled — no SSRF
// via 3xx to an internal host) and normalizes the JSON response into a
// ResultSet: a top-level array becomes rows, an object becomes one row, any
// other JSON / non-JSON body becomes a single "value" cell.
type HTTPConnector struct{ client *http.Client }

func NewHTTPConnector() *HTTPConnector {
	return &HTTPConnector{client: &http.Client{
		// Never follow redirects: the host is admin-configured, but a redirect
		// could re-target an internal address (SSRF). Return the 3xx response
		// as-is instead.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *HTTPConnector) Kind() SourceKind { return KindHTTP }

// httpReadMethods / httpWriteMethods are the verb whitelist. POST is a read
// (JSON APIs use it for queries — search / GraphQL / JSON-RPC), so GET and POST
// both work on a read-only source; only PUT/PATCH/DELETE are writes.
var httpReadMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true}
var httpWriteMethods = map[string]bool{"PUT": true, "PATCH": true, "DELETE": true}

// normalizeHTTPMethod uppercases and validates the verb. An unknown verb is a
// malformed operation (plain error -> invalid_input), not a policy refusal.
func normalizeHTTPMethod(method string) (string, error) {
	m := strings.ToUpper(strings.TrimSpace(method))
	if httpReadMethods[m] || httpWriteMethods[m] {
		return m, nil
	}
	return "", fmt.Errorf("http: method %q is not supported (use GET/HEAD/POST/PUT/PATCH/DELETE)", method)
}

// cleanHTTPPath validates and normalizes the agent-supplied path: it must be
// relative (no scheme/host), carry a single leading slash, contain no ".."
// traversal segment, and embed no query string (query params go in HTTPQuery so
// the scope object stays a clean path). The returned value is both the request
// path and the scope object.
func cleanHTTPPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", fmt.Errorf("http: path is required")
	}
	if strings.Contains(p, "://") {
		return "", fmt.Errorf("http: path must be relative (no scheme/host): %q", raw)
	}
	if strings.ContainsAny(p, "?#") {
		return "", fmt.Errorf("http: path must not contain a query string or fragment; pass query params via http_query: %q", raw)
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if strings.HasPrefix(p, "//") {
		return "", fmt.Errorf("http: path must not start with // (protocol-relative): %q", raw)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("http: path must not contain a '..' segment: %q", raw)
		}
	}
	return p, nil
}

func (c *HTTPConnector) Classify(op Operation) (AccessMode, ResourceRef, error) {
	method, err := normalizeHTTPMethod(op.HTTPMethod)
	if err != nil {
		return "", ResourceRef{}, err
	}
	p, err := cleanHTTPPath(op.HTTPPath)
	if err != nil {
		return "", ResourceRef{}, err
	}
	mode := ModeWrite
	if httpReadMethods[method] {
		mode = ModeRead
	}
	return mode, ResourceRef{
		Objects:    []string{p},
		CommandCls: method,
		// An HTTP operation always addresses a path; keep the scope gate's
		// fail-closed guard armed.
		ReferencesTables: true,
	}, nil
}

// httpBaseURL builds the base URL (scheme://host[:port]base_path) from the
// decrypted ConnConfig. options.scheme defaults to https; the port is omitted
// when it is the scheme default; options.base_path is an optional path prefix.
func httpBaseURL(conn ConnConfig) (string, error) {
	scheme := "https"
	if s, ok := conn.Options["scheme"].(string); ok && strings.TrimSpace(s) != "" {
		scheme = strings.ToLower(strings.TrimSpace(s))
	}
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("http: unsupported scheme %q", scheme)
	}
	host := strings.TrimSpace(conn.Host)
	if host == "" {
		return "", fmt.Errorf("http: host is required")
	}
	if strings.Contains(host, "://") {
		return "", fmt.Errorf("http: host must be a bare hostname (no scheme): %q", conn.Host)
	}
	hostPort := host
	if p := conn.Port; p != 0 && !((scheme == "http" && p == 80) || (scheme == "https" && p == 443)) {
		hostPort = fmt.Sprintf("%s:%d", host, p)
	}
	base := scheme + "://" + hostPort
	if bp, ok := conn.Options["base_path"].(string); ok {
		bp = strings.TrimRight(strings.TrimSpace(bp), "/")
		if bp != "" {
			if !strings.HasPrefix(bp, "/") {
				bp = "/" + bp
			}
			base += bp
		}
	}
	return base, nil
}

// httpApply sets the static request headers and optional HTTP basic auth from
// the ConnConfig onto a request (shared by Ping and Execute).
func httpApply(req *http.Request, conn ConnConfig) {
	if hdrs, ok := conn.Options["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if sv, ok := v.(string); ok && sv != "" {
				req.Header.Set(k, sv)
			}
		}
	}
	if strings.TrimSpace(conn.User) != "" {
		req.SetBasicAuth(conn.User, conn.Password)
	}
}

func (c *HTTPConnector) Ping(ctx context.Context, conn ConnConfig) error {
	base, err := httpBaseURL(conn)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
	if err != nil {
		return err
	}
	httpApply(req, conn)
	req.Header.Set("Accept", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		// A transport-level failure (DNS/TLS/connection refused/timeout) is a
		// real connectivity problem. Any received HTTP status — even 4xx/5xx —
		// proves the host is reachable, which is all Ping can assert for an
		// arbitrary REST base URL.
		return err
	}
	res.Body.Close()
	return nil
}

// maxHTTPResponseBytes caps how much of a response Execute will buffer — a
// backstop against hostile/misconfigured servers.
const maxHTTPResponseBytes = 32 << 20

// Execute re-validates the operation (defense in depth), performs the HTTP
// request and normalizes the JSON response into a ResultSet. The server-injected
// RowLimit caps the number of normalized rows; the raw body is preserved in Raw
// (capped only by maxHTTPResponseBytes).
func (c *HTTPConnector) Execute(ctx context.Context, conn ConnConfig, op Operation) (*ResultSet, error) {
	method, err := normalizeHTTPMethod(op.HTTPMethod)
	if err != nil {
		return nil, err
	}
	p, err := cleanHTTPPath(op.HTTPPath)
	if err != nil {
		return nil, err
	}
	base, err := httpBaseURL(conn)
	if err != nil {
		return nil, err
	}

	target := base + p
	if len(op.HTTPQuery) > 0 {
		vals := url.Values{}
		// Sort keys for deterministic query strings (stable tests + audit).
		keys := make([]string, 0, len(op.HTTPQuery))
		for k := range op.HTTPQuery {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			vals.Set(k, fmt.Sprintf("%v", op.HTTPQuery[k]))
		}
		target += "?" + vals.Encode()
	}

	var body io.Reader
	if op.HTTPBody != nil {
		b, err := json.Marshal(op.HTTPBody)
		if err != nil {
			return nil, fmt.Errorf("http: marshal body: %w", err)
		}
		body = bytes.NewReader(b)
	}

	to := time.Duration(op.TimeoutMS) * time.Millisecond
	if to <= 0 {
		to = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	httpApply(req, conn)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxHTTPResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxHTTPResponseBytes {
		return nil, fmt.Errorf("http: response exceeds %d bytes; narrow the request", maxHTTPResponseBytes)
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("http: HTTP %d: %s", res.StatusCode, excerpt(raw, 2048))
	}

	limit := op.RowLimit
	if limit <= 0 {
		limit = 1000
	}
	// The unpack rule is a per-request parameter (op.HTTPListPath); it falls back
	// to the source default (options.list_path); empty means "return as-is".
	listPath := strings.TrimSpace(op.HTTPListPath)
	if listPath == "" {
		listPath, _ = conn.Options["list_path"].(string)
	}
	rs := httpNormalize(raw, limit, listPath)
	rs.Engine = string(KindHTTP)
	rs.Raw = json.RawMessage(raw)
	return rs, nil
}

// httpNormalize maps a response body to a ResultSet. When listPath is set it
// first "unpacks" the list out of the response (see httpExtractList) by drilling
// to that dotted location; when empty the response is rendered as-is. The
// (possibly unpacked) value is then rendered: a JSON array becomes rows, a JSON
// object becomes a single row, any other JSON scalar or a non-JSON body becomes
// a one-cell "value" result.
func httpNormalize(raw []byte, limit int, listPath string) *ResultSet {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return &ResultSet{
			Columns: []Column{{Name: "value", Type: "string"}},
			Rows:    [][]any{{string(raw)}},
		}
	}
	target, ok := httpExtractList(v, listPath)
	if !ok {
		// An explicit list_path that doesn't resolve -> empty result, a clear
		// "nothing matched the configured path" signal (not a single junk row).
		return &ResultSet{}
	}
	switch t := target.(type) {
	case []any:
		return httpRowsFromArray(t, limit)
	case map[string]any:
		return httpRowsFromArray([]any{t}, limit)
	default:
		rs := &ResultSet{Columns: []Column{{Name: "value", Type: "string"}}}
		rs.Rows = [][]any{{esCell(target, &rs.Columns[0])}}
		return rs
	}
}

// httpExtractList resolves the list/payload out of a decoded response. An empty
// listPath means "no unpack rule" — the response is returned as-is. A listPath
// ("a.b.c") drills through nested objects and returns false if any segment is
// missing (so a typo'd path surfaces as an empty result, not a silent fallback).
func httpExtractList(v any, listPath string) (any, bool) {
	if strings.TrimSpace(listPath) == "" {
		return v, true
	}
	cur := v
	for _, seg := range strings.Split(listPath, ".") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := m[seg]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// httpRowsFromArray flattens an array into rows. When every kept element is a
// JSON object the columns are the sorted union of their top-level keys;
// otherwise the elements are emitted under a single "value" column (re-encoding
// nested values as JSON).
func httpRowsFromArray(arr []any, limit int) *ResultSet {
	rs := &ResultSet{}
	kept := make([]any, 0, len(arr))
	for _, e := range arr {
		if len(kept) >= limit {
			rs.Truncated = true
			break
		}
		kept = append(kept, e)
	}

	allObj := len(kept) > 0
	for _, e := range kept {
		if _, ok := e.(map[string]any); !ok {
			allObj = false
			break
		}
	}
	if !allObj {
		rs.Columns = []Column{{Name: "value", Type: "string"}}
		for _, e := range kept {
			rs.Rows = append(rs.Rows, []any{esCell(e, &rs.Columns[0])})
		}
		return rs
	}

	keySet := map[string]bool{}
	for _, e := range kept {
		for k := range e.(map[string]any) {
			keySet[k] = true
		}
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rs.Columns = append(rs.Columns, Column{Name: k, Type: "string"})
	}
	for _, e := range kept {
		m := e.(map[string]any)
		row := make([]any, len(keys))
		for i, k := range keys {
			row[i] = esCell(m[k], &rs.Columns[i])
		}
		rs.Rows = append(rs.Rows, row)
	}
	return rs
}
