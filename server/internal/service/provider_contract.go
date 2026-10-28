// Package service — provider integration contract.
//
// A ProviderContract is the machine-readable "integration pack" for an
// external API provider: everything an agent needs to drive the provider
// without re-reading upstream docs — named operations (semantic name,
// method, path, parameters + location, read-only vs write, a request
// example, a response-shape hint), the auth strategy the proxy injects, a
// cheap read-only verify probe, capability tags for discovery, and a
// content digest for drift detection.
//
// Storage: the contract is a JSON document kept in the existing
// external_providers.profile TEXT column (no new column, no DDL). The column
// historically held either a free-text blob or a loose JSON profile
// ({"provider","base_url","auth","endpoints":[...]}); ParseContract reads all
// three shapes so old rows keep working (backward compatible).
//
// Versioning: the wire shape is versioned by schema_version (currently "1").
// Consumers must tolerate unknown fields. Renaming/removing a field requires
// a schema_version bump. digest is sha256 over the contract with the digest
// field itself zeroed, so it changes iff the contract content changes.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ContractSchemaVersion is the current contract wire-format version. Bump it
// when a field is renamed or its meaning changes (additive fields do not).
const ContractSchemaVersion = "1"

// digestPrefix namespaces the digest algorithm so a future switch (e.g. to
// sha512) is distinguishable in stored values.
const digestPrefix = "sha256:"

// ProviderContract is the structured integration contract for a provider.
// See the package doc for the JSON wire shape.
type ProviderContract struct {
	// SchemaVersion versions the wire format. Defaults to ContractSchemaVersion.
	SchemaVersion string `json:"schema_version"`
	// Provider is a human-readable display name (e.g. "SerpAPI").
	Provider string `json:"provider,omitempty"`
	// BaseURL is the API root the proxy prefixes onto operation paths.
	BaseURL string `json:"base_url,omitempty"`
	// Auth describes how the proxy injects credentials at egress. The agent
	// never sees or passes secrets — this is a requirement description only.
	Auth *ContractAuth `json:"auth,omitempty"`
	// AuthStrategy is the flat, forward-compatible spelling of the auth
	// strategy name (issue #737: `auth_strategy: oauth2_refresh | sigv4 | …`).
	// It is a convenience alias: ParseContract folds it into Auth.Strategy, so
	// consumers only ever read Auth.Strategy. Kept on the wire so a contract may
	// declare the strategy without the nested `auth` object.
	AuthStrategy string `json:"auth_strategy,omitempty"`
	// Capabilities are coarse tags ("serp", "seo", "search-analytics") used
	// for discovery and matching a task to a candidate provider.
	Capabilities []string `json:"capabilities,omitempty"`
	// VerifyOperation names (by Operation.Name) the cheapest, idempotent,
	// read-only probe usable to verify auth + connectivity after publish. The
	// verification-probe feature consumes this; lint requires it to resolve to
	// a read-only operation.
	VerifyOperation string `json:"verify_operation,omitempty"`
	// Operations are the named, semantic operations this provider exposes.
	Operations []ContractOperation `json:"operations"`
	// Notes is optional free-text guidance for the agent.
	Notes string `json:"notes,omitempty"`
	// Digest is sha256 of the contract with this field zeroed. Computed on
	// read (ProviderDef.Contract); any digest stored in the blob is replaced.
	Digest string `json:"digest,omitempty"`
	// LegacyProfile preserves an old free-text profile verbatim so the agent
	// can still read it when no structured contract exists. Empty for
	// structured contracts.
	LegacyProfile string `json:"legacy_profile,omitempty"`
}

// ContractAuth describes the credential requirement for a provider. Strategy
// names are open-ended: existing static strategies (bearer, basic,
// custom_header, query_param, imap) plus future ones (oauth2_refresh, sigv4,
// mtls) declared by the auth-strategy work.
type ContractAuth struct {
	Strategy string `json:"strategy,omitempty"`
	// Header is the request header the credential is injected into
	// (header-based strategies).
	Header string `json:"header,omitempty"`
	// Prefix precedes the credential value, e.g. "Bearer ".
	Prefix string `json:"prefix,omitempty"`
	// Param is the query-parameter name for strategy=query_param.
	Param string `json:"param,omitempty"`
	// Note is an agent-facing hint, e.g. "injected by the proxy — do NOT
	// include it yourself".
	Note string `json:"note,omitempty"`
}

// ContractOperation is one named, semantic operation.
type ContractOperation struct {
	// Name is the stable semantic identifier (e.g. "search", "list_sites").
	Name string `json:"name"`
	// Method is the HTTP method (GET/POST/PUT/PATCH/DELETE/HEAD).
	Method string `json:"method"`
	// Path is relative to BaseURL; path placeholders use {name} (e.g.
	// "/webmasters/v3/sites/{siteUrl}/searchAnalytics/query").
	Path string `json:"path"`
	// Summary is a one-line description of what the operation does.
	Summary string `json:"summary,omitempty"`
	// ReadOnly marks an operation with no side effects. Advisory (a coarse
	// filter — some APIs read via POST), but it gates the verify probe and
	// informs the write toggle.
	ReadOnly bool `json:"read_only"`
	// Parameters lists the inputs and where they go.
	Parameters []ContractParam `json:"parameters,omitempty"`
	// RequestExample is a representative request (query object or body).
	RequestExample json.RawMessage `json:"request_example,omitempty"`
	// ResponseShape is a compact human/agent-readable hint of the response
	// (top-level keys and notable nested arrays).
	ResponseShape string `json:"response_shape,omitempty"`
	// Notes is optional operation-specific guidance.
	Notes string `json:"notes,omitempty"`
}

// ContractParam is a single operation input.
type ContractParam struct {
	Name        string `json:"name"`
	In          string `json:"in"` // query | path | body | header
	Type        string `json:"type,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}

// paramLocations is the set of valid ContractParam.In values.
var paramLocations = map[string]bool{
	"query": true, "path": true, "body": true, "header": true,
}

// httpMethods is the set of valid ContractOperation.Method values.
var httpMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true,
	"PATCH": true, "DELETE": true, "OPTIONS": true,
}

// readOnlyMethods are the methods treated as side-effect-free by the coarse
// read-only check. POST/DELETE etc. can still be declared ReadOnly (some APIs
// read via POST) but then earn a lint warning.
var readOnlyMethods = map[string]bool{"GET": true, "HEAD": true}

// --- parsing (backward compatible) ---------------------------------------

// ParseContract turns a stored profile blob into a ProviderContract. It never
// returns nil and never errors — an unreadable blob degrades to an empty
// contract carrying the raw text in LegacyProfile. Three shapes are handled,
// in order:
//
//  1. Structured contract JSON (has an "operations" or "schema_version" key)
//     → parsed as-is.
//  2. Legacy JSON profile ({"provider","base_url","auth","endpoints":[…]})
//     → endpoints are converted to operations (best effort), the free-text
//     auth note is preserved on Auth.Note.
//  3. Free text, empty, or unrecognized JSON → operations empty, raw text in
//     LegacyProfile.
func ParseContract(profile string) *ProviderContract {
	trimmed := strings.TrimSpace(profile)
	if trimmed == "" {
		return (&ProviderContract{}).normalized()
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &probe); err == nil {
		if _, ok := probe["operations"]; ok {
			var c ProviderContract
			if err := json.Unmarshal([]byte(trimmed), &c); err == nil {
				return c.normalized()
			}
		}
		if _, ok := probe["schema_version"]; ok {
			var c ProviderContract
			if err := json.Unmarshal([]byte(trimmed), &c); err == nil {
				return c.normalized()
			}
		}
		if _, ok := probe["endpoints"]; ok {
			if c, ok := legacyJSONToContract(trimmed); ok {
				return c
			}
		}
	}
	return (&ProviderContract{LegacyProfile: trimmed}).normalized()
}

// IsStructuredContract reports whether the blob is a structured contract
// (as opposed to legacy free text / a loose JSON profile).
func IsStructuredContract(profile string) bool {
	trimmed := strings.TrimSpace(profile)
	if trimmed == "" {
		return false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return false
	}
	_, hasOps := probe["operations"]
	_, hasVer := probe["schema_version"]
	return hasOps || hasVer
}

// normalized fills defaults that every stored contract should carry. In
// particular Operations is always a non-nil slice so the wire JSON is
// `"operations": []` rather than `null` — agents should never have to handle
// a null list.
func (c *ProviderContract) normalized() *ProviderContract {
	if c.SchemaVersion == "" {
		c.SchemaVersion = ContractSchemaVersion
	}
	if c.Operations == nil {
		c.Operations = []ContractOperation{}
	}
	// Fold the flat `auth_strategy` alias into the canonical Auth.Strategy so
	// every consumer reads the strategy from one place (issue #737).
	if c.AuthStrategy != "" {
		if c.Auth == nil {
			c.Auth = &ContractAuth{}
		}
		if c.Auth.Strategy == "" {
			c.Auth.Strategy = c.AuthStrategy
		}
	}
	return c
}

// legacyEndpoint mirrors one entry of the old profile's endpoints array.
type legacyEndpoint struct {
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Summary     string            `json:"summary"`
	QueryParams map[string]string `json:"query_params"`
	Notes       string            `json:"notes"`
}

type legacyProfileDoc struct {
	Provider  string           `json:"provider"`
	BaseURL   string           `json:"base_url"`
	Auth      string           `json:"auth"`
	Endpoints []legacyEndpoint `json:"endpoints"`
}

// legacyJSONToContract converts the pre-contract JSON profile into a
// contract. Names are derived from method+path (the legacy shape had no
// semantic names); read_only is the coarse method-based guess. Returns ok=false
// if the document is not a usable legacy profile.
func legacyJSONToContract(raw string) (*ProviderContract, bool) {
	var doc legacyProfileDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, false
	}
	c := &ProviderContract{
		SchemaVersion: ContractSchemaVersion,
		Provider:      doc.Provider,
		BaseURL:       doc.BaseURL,
		Operations:    make([]ContractOperation, 0, len(doc.Endpoints)),
	}
	if doc.Auth != "" {
		c.Auth = &ContractAuth{Note: doc.Auth}
	}
	seen := map[string]int{}
	for _, e := range doc.Endpoints {
		name := uniqueName(slugifyOperationName(e.Method, e.Path), seen)
		op := ContractOperation{
			Name:     name,
			Method:   strings.ToUpper(strings.TrimSpace(e.Method)),
			Path:     e.Path,
			Summary:  e.Summary,
			ReadOnly: readOnlyMethods[strings.ToUpper(strings.TrimSpace(e.Method))],
			Notes:    e.Notes,
		}
		for pname, pdesc := range e.QueryParams {
			op.Parameters = append(op.Parameters, ContractParam{
				Name: pname, In: "query", Type: "string", Description: pdesc,
			})
		}
		sortParams(op.Parameters)
		c.Operations = append(c.Operations, op)
	}
	return c, true
}

// uniqueName appends -2, -3, … when a derived name collides.
func uniqueName(name string, seen map[string]int) string {
	if name == "" {
		name = "operation"
	}
	seen[name]++
	if n := seen[name]; n > 1 {
		return fmt.Sprintf("%s-%d", name, n)
	}
	return name
}

// slugifyOperationName builds a stable kebab-case name from method+path, e.g.
// ("GET","/webmasters/v3/sites") → "get-webmasters-v3-sites".
func slugifyOperationName(method, path string) string {
	var b strings.Builder
	prevDash := true // suppress a leading dash
	for _, r := range strings.ToLower(method + "-" + path) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// sortParams orders parameters by name so contract output (and therefore its
// digest) is deterministic regardless of map iteration order.
func sortParams(ps []ContractParam) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && ps[j-1].Name > ps[j].Name; j-- {
			ps[j-1], ps[j] = ps[j], ps[j-1]
		}
	}
}

// --- digest ---------------------------------------------------------------

// ComputeDigest returns the digest of the contract with the Digest field
// zeroed, so the digest never depends on itself. Same content ⇒ same digest;
// any content change ⇒ different digest.
func (c *ProviderContract) ComputeDigest() string {
	clone := *c
	clone.Digest = ""
	b, err := json.Marshal(&clone)
	if err != nil {
		// Structs of these types always marshal; be defensive anyway.
		return ""
	}
	sum := sha256.Sum256(b)
	return digestPrefix + hex.EncodeToString(sum[:])
}

// Finalize ensures the version default and stamps the current digest. Called
// before a contract leaves the server so the digest always reflects the bytes
// being served (a stale stored digest can never be returned).
func (c *ProviderContract) Finalize() *ProviderContract {
	c.normalized()
	c.Digest = c.ComputeDigest()
	return c
}

// MarshalContract serializes a finalized contract to the JSON stored in the
// profile column. Used by the draft pipeline when persisting a new contract.
func MarshalContract(c *ProviderContract) (string, error) {
	c.Finalize()
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal contract: %w", err)
	}
	return string(b), nil
}

// Contract derives this provider's structured contract: the parsed profile
// layered over the row's own wiring. base_url / auth are filled from the row
// whenever the profile does not declare them, so the returned contract always
// reflects how the proxy actually authenticates. The digest is recomputed
// (any digest in the stored blob is ignored).
func (p *ProviderDef) Contract() *ProviderContract {
	c := ParseContract(p.Profile)
	if c.Provider == "" {
		if p.Label != "" {
			c.Provider = p.Label
		} else {
			c.Provider = p.Name
		}
	}
	if c.BaseURL == "" {
		c.BaseURL = p.APIBaseURL
	}
	if c.Auth == nil {
		c.Auth = authFromRow(p)
	} else {
		if c.Auth.Strategy == "" {
			c.Auth.Strategy = p.AuthType
		}
		if c.Auth.Header == "" && c.Auth.Param == "" {
			if p.AuthType == "query_param" {
				c.Auth.Param = p.AuthHeader
			} else {
				c.Auth.Header = p.AuthHeader
			}
		}
		if c.Auth.Prefix == "" {
			c.Auth.Prefix = p.AuthPrefix
		}
	}
	return c.Finalize()
}

// authFromRow synthesizes a ContractAuth from the provider row's own auth
// wiring (used when the profile does not declare auth).
func authFromRow(p *ProviderDef) *ContractAuth {
	a := &ContractAuth{
		Strategy: p.AuthType,
		Prefix:   p.AuthPrefix,
		Note:     "injected automatically by the niuniu proxy — do NOT include it yourself.",
	}
	if p.AuthType == "query_param" {
		// For query-param auth the row's auth_header column holds the
		// query-parameter name (see the SerpAPI seed).
		a.Param = p.AuthHeader
	} else {
		a.Header = p.AuthHeader
	}
	return a
}

// --- lint -----------------------------------------------------------------

// ContractIssue is a single lint finding.
type ContractIssue struct {
	Severity string `json:"severity"` // "error" (blocks publish) | "warning" (advisory)
	Path     string `json:"path"`     // locator, e.g. "operations[1].method"
	Message  string `json:"message"`
}

func (i ContractIssue) String() string {
	return fmt.Sprintf("%s: %s: %s", i.Severity, i.Path, i.Message)
}

// --- operation resolution (operation-level authorization, issue #737) ------

// OperationByName returns the operation with the given semantic name. Names
// are matched case-insensitively so an agent's casing never causes a spurious
// "unknown operation".
func (c *ProviderContract) OperationByName(name string) (*ContractOperation, bool) {
	if c == nil {
		return nil, false
	}
	want := strings.TrimSpace(name)
	for i := range c.Operations {
		if strings.EqualFold(strings.TrimSpace(c.Operations[i].Name), want) {
			return &c.Operations[i], true
		}
	}
	return nil, false
}

// MatchOperation resolves a concrete (method, path) request back to the
// contract operation it targets — the reference used when a caller passes
// method+path instead of an operation name. Matching is method
// (case-insensitive) + segment-wise path, where a `{param}` segment in the
// operation template matches any single non-empty path segment. Returns
// ok=false when the request targets no declared operation (the caller then
// falls back to the legacy whitelist).
func (c *ProviderContract) MatchOperation(method, path string) (*ContractOperation, bool) {
	if c == nil {
		return nil, false
	}
	wantMethod := strings.ToUpper(strings.TrimSpace(method))
	// Normalise away the query string if the caller passed one in the path.
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	reqSegs := splitPathSegments(path)
	for i := range c.Operations {
		op := &c.Operations[i]
		if strings.ToUpper(strings.TrimSpace(op.Method)) != wantMethod {
			continue
		}
		if pathTemplateMatches(op.Path, reqSegs) {
			return op, true
		}
	}
	return nil, false
}

// IsReadOnly reports whether the operation has no side effects. Nil-safe.
func (o *ContractOperation) IsReadOnly() bool {
	return o != nil && o.ReadOnly
}

// SplitOperations partitions the contract's operations into read-only and
// write/sensitive name lists (issue #737: 写/敏感操作单列). Order follows the
// contract so the listing is stable. The confirm card and the write switch
// consume this to control authorization at operation granularity.
func (c *ProviderContract) SplitOperations() (readOnly, write []string) {
	readOnly, write = []string{}, []string{}
	if c == nil {
		return readOnly, write
	}
	for i := range c.Operations {
		op := &c.Operations[i]
		if op.ReadOnly {
			readOnly = append(readOnly, op.Name)
		} else {
			write = append(write, op.Name)
		}
	}
	return readOnly, write
}

// ResolvePath fills an operation's path template (`{name}` placeholders) from
// the caller's query/body values and reports which keys were consumed. Path
// parameter values are searched in query first, then body, and URL-escaped per
// segment so a value containing "/" cannot escape its segment. A missing
// required placeholder yields an error naming the parameter, so an
// operation-level call can never silently address the wrong URL.
//
// Consumed keys are returned so the caller can drop them from the outgoing
// query/body (a path parameter must not also be sent as a query parameter).
func (o *ContractOperation) ResolvePath(query, body map[string]any) (string, []string, error) {
	path := o.Path
	if !strings.Contains(path, "{") {
		return path, nil, nil
	}
	var consumed []string
	var missing []string
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if len(seg) < 3 || seg[0] != '{' || seg[len(seg)-1] != '}' {
			continue
		}
		name := seg[1 : len(seg)-1]
		val, where, ok := lookupParam(name, query, body)
		if !ok {
			missing = append(missing, name)
			continue
		}
		segments[i] = url.PathEscape(fmt.Sprintf("%v", val))
		consumed = append(consumed, where)
	}
	if len(missing) > 0 {
		return "", nil, fmt.Errorf("operation %q is missing path parameter(s): %s", o.Name, strings.Join(missing, ", "))
	}
	return strings.Join(segments, "/"), consumed, nil
}

// lookupParam finds name in query then body, returning the value and a
// "<where>:<name>" token so the caller can drop the key from the right map.
func lookupParam(name string, query, body map[string]any) (any, string, bool) {
	if query != nil {
		if v, ok := query[name]; ok {
			return v, "query:" + name, true
		}
	}
	if body != nil {
		if v, ok := body[name]; ok {
			return v, "body:" + name, true
		}
	}
	return nil, "", false
}

// splitPathSegments splits a URL path into its non-empty segments, ignoring a
// leading slash. Query/fragment are the caller's responsibility (stripped in
// MatchOperation).
func splitPathSegments(path string) []string {
	raw := strings.Split(strings.Trim(path, "/"), "/")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		out = append(out, s)
	}
	return out
}

// pathTemplateMatches reports whether a concrete request path (already split
// into segments) matches an operation path template. A `{param}` segment
// matches any single non-empty segment; every other segment must match
// exactly.
func pathTemplateMatches(tmpl string, reqSegs []string) bool {
	tmplSegs := splitPathSegments(tmpl)
	if len(tmplSegs) != len(reqSegs) {
		return false
	}
	for i, ts := range tmplSegs {
		if len(ts) >= 3 && ts[0] == '{' && ts[len(ts)-1] == '}' {
			if reqSegs[i] == "" {
				return false
			}
			continue
		}
		if ts != reqSegs[i] {
			return false
		}
	}
	return true
}

// LintContract validates a contract and returns every finding. An empty slice
// means the contract is publishable. This is the lint entry point the
// integration draft pipeline reuses before a draft may be published.
//
// Severity "error" must block publish; "warning" is advisory. Rules:
//
//   - operations must be non-empty;
//   - each operation needs a unique non-empty name, a valid HTTP method, and
//     a path beginning with "/";
//   - each parameter needs a name and a valid location
//     (query|path|body|header);
//   - verify_operation, when set, must name an existing operation, and that
//     operation must be read-only (the probe must be side-effect-free) AND use a
//     provably read-only method (GET/HEAD). A verify_operation that is not
//     GET/HEAD is an error — the coarse check cannot prove a read-via-POST (e.g.
//     GSC searchAnalytics) is side-effect-free, and a probe must never be able to
//     mutate upstream state (defence in depth: this is one of three gates, the
//     others being SelectVerifyOperation and the publish-time CoarseReadOnly
//     check);
//   - a non-GET/HEAD operation declared read_only is a warning — the coarse
//     check cannot distinguish a read-by-POST from a write, so the declaration
//     is not trusted for gating: such an operation stays publishable but is kept
//     OUT of the default safe set (see external_proxy.go) and needs the per-user
//     write toggle.
func LintContract(c *ProviderContract) []ContractIssue {
	var issues []ContractIssue
	if c == nil {
		return []ContractIssue{{Severity: "error", Path: "", Message: "contract is nil"}}
	}
	if len(c.Operations) == 0 {
		issues = append(issues, ContractIssue{
			Severity: "error", Path: "operations",
			Message: "contract has no operations",
		})
	}

	byName := make(map[string]*ContractOperation, len(c.Operations))
	for i := range c.Operations {
		op := &c.Operations[i]
		loc := fmt.Sprintf("operations[%d]", i)
		if strings.TrimSpace(op.Name) == "" {
			issues = append(issues, ContractIssue{"error", loc + ".name", "operation name is required"})
		} else if _, dup := byName[op.Name]; dup {
			issues = append(issues, ContractIssue{"error", loc + ".name", "duplicate operation name " + op.Name})
		} else {
			byName[op.Name] = op
		}
		method := strings.ToUpper(strings.TrimSpace(op.Method))
		if !httpMethods[method] {
			issues = append(issues, ContractIssue{"error", loc + ".method", "invalid HTTP method " + op.Method})
		}
		if !strings.HasPrefix(op.Path, "/") {
			issues = append(issues, ContractIssue{"error", loc + ".path", "path must start with /"})
		}
		for j, p := range op.Parameters {
			ploc := fmt.Sprintf("%s.parameters[%d]", loc, j)
			if strings.TrimSpace(p.Name) == "" {
				issues = append(issues, ContractIssue{"error", ploc + ".name", "parameter name is required"})
			}
			if !paramLocations[p.In] {
				issues = append(issues, ContractIssue{"error", ploc + ".in", "invalid parameter location " + p.In})
			}
		}
		if op.ReadOnly && httpMethods[method] && !readOnlyMethods[method] {
			issues = append(issues, ContractIssue{"warning", loc + ".read_only",
				op.Method + " declared read_only — the coarse check cannot prove a non-GET/HEAD method has no side effects, so this operation is not in the default safe set and requires the \"Allow writes\" toggle"})
		}
	}

	if c.VerifyOperation != "" {
		op, ok := byName[c.VerifyOperation]
		switch {
		case !ok:
			issues = append(issues, ContractIssue{"error", "verify_operation",
				"verify_operation " + c.VerifyOperation + " does not name an existing operation"})
		case !op.ReadOnly:
			issues = append(issues, ContractIssue{"error", "verify_operation",
				"verify_operation " + c.VerifyOperation + " must be read-only (probe must have no side effects)"})
		case !coarseReadOnly(op.Method):
			// Declared read-only but not GET/HEAD (a read-via-POST). The coarse
			// check cannot prove it is side-effect-free (e.g. a GET that mutates,
			// or a POST that deletes), so the contract is rejected outright rather
			// than trusting the declaration + a human confirmation.
			issues = append(issues, ContractIssue{"error", "verify_operation",
				"verify_operation " + c.VerifyOperation + " uses " + op.Method +
					" — not a provably read-only method (GET/HEAD); the verification probe must be GET or HEAD"})
		}
	}
	return issues
}

// ValidateContract runs LintContract and returns an error if any finding has
// severity "error". The returned error lists every blocking issue. Warnings do
// not fail validation.
func ValidateContract(c *ProviderContract) error {
	var msgs []string
	for _, iss := range LintContract(c) {
		if iss.Severity == "error" {
			msgs = append(msgs, iss.String())
		}
	}
	if len(msgs) > 0 {
		return fmt.Errorf("invalid provider contract: %s", strings.Join(msgs, "; "))
	}
	return nil
}
