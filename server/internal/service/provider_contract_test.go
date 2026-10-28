package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sampleContract is a structurally complete contract exercising every field
// (params of each location, a request example, a write operation, a read-only
// verify target).
func sampleContract() *ProviderContract {
	return &ProviderContract{
		SchemaVersion:   ContractSchemaVersion,
		Provider:        "Demo",
		BaseURL:         "https://api.demo.dev",
		Auth:            &ContractAuth{Strategy: "bearer", Header: "Authorization", Prefix: "Bearer", Note: "injected by proxy"},
		Capabilities:    []string{"orders", "inventory"},
		VerifyOperation: "list_orders",
		Operations: []ContractOperation{
			{
				Name: "list_orders", Method: "GET", Path: "/orders", ReadOnly: true,
				Summary:        "List orders",
				Parameters:     []ContractParam{{Name: "status", In: "query", Type: "string", Description: "open | closed"}},
				RequestExample: json.RawMessage(`{"status":"open"}`),
				ResponseShape:  "orders[] each {id, status, total}",
			},
			{
				Name: "create_order", Method: "POST", Path: "/orders", ReadOnly: false,
				Summary:        "Create an order",
				Parameters:     []ContractParam{{Name: "sku", In: "body", Type: "string", Required: true}},
				RequestExample: json.RawMessage(`{"sku":"abc"}`),
			},
		},
	}
}

// TestContract_RoundTrip: a contract survives Marshal → Parse unchanged, and
// re-marshalling is byte-stable (the digest would otherwise drift between
// reads).
func TestContract_RoundTrip(t *testing.T) {
	c := sampleContract()
	require.NoError(t, ValidateContract(c))

	blob, err := MarshalContract(c)
	require.NoError(t, err)
	require.True(t, IsStructuredContract(blob))

	back := ParseContract(blob)
	require.Equal(t, c.Provider, back.Provider)
	require.Equal(t, c.BaseURL, back.BaseURL)
	require.Equal(t, c.Auth, back.Auth)
	require.Equal(t, c.Capabilities, back.Capabilities)
	require.Equal(t, c.VerifyOperation, back.VerifyOperation)
	require.Equal(t, c.Operations, back.Operations)
	require.Equal(t, c.ComputeDigest(), back.Digest)
	require.Empty(t, back.LegacyProfile)

	again, err := MarshalContract(back)
	require.NoError(t, err)
	require.Equal(t, blob, again, "re-serializing a parsed contract must be byte-stable")
}

// TestContract_DigestStabilityAndSensitivity pins the digest contract: stable
// for identical content, independent of the digest field itself, and flipped
// by any content change.
func TestContract_DigestStabilityAndSensitivity(t *testing.T) {
	c := sampleContract()
	d1 := c.ComputeDigest()
	require.Equal(t, d1, c.ComputeDigest(), "digest must be deterministic")
	require.True(t, strings.HasPrefix(d1, digestPrefix))

	// The digest must not depend on a previously-stamped value.
	withDigest := *c
	withDigest.Digest = "sha256:deadbeef"
	require.Equal(t, d1, withDigest.ComputeDigest())

	changed := *c
	changed.Operations = []ContractOperation{{Name: "list_orders", Method: "GET", Path: "/orders/v2", ReadOnly: true}}
	require.NotEqual(t, d1, changed.ComputeDigest(), "a content change must change the digest")
}

// TestParseContract_LegacyFreeText: an old free-text profile is preserved
// verbatim, yields no operations, and never errors.
func TestParseContract_LegacyFreeText(t *testing.T) {
	const txt = "Just some free text describing the API."
	require.False(t, IsStructuredContract(txt))

	c := ParseContract(txt)
	require.Empty(t, c.Operations)
	require.Equal(t, txt, c.LegacyProfile)
	require.Empty(t, c.Digest)

	// A contract derived from it must still serialize (no crash on old rows).
	p := &ProviderDef{Name: "old", Label: "Old", APIBaseURL: "https://old.dev", AuthType: "bearer", AuthHeader: "Authorization", AuthPrefix: "Bearer", Profile: txt}
	derived := p.Contract()
	require.Equal(t, txt, derived.LegacyProfile)
	require.NotEmpty(t, derived.Digest)
	_, err := json.Marshal(derived)
	require.NoError(t, err)
}

// TestParseContract_LegacyJSONEndpoints: the pre-contract JSON profile
// ({"provider","base_url","auth","endpoints":[…]}) is converted to operations
// with derived names, query params, and a coarse read-only guess.
func TestParseContract_LegacyJSONEndpoints(t *testing.T) {
	legacy := `{
	  "provider": "Old",
	  "base_url": "https://old.dev",
	  "auth": "token sent in the Authorization header",
	  "endpoints": [
	    {"method": "GET", "path": "/things", "summary": "list things",
	     "query_params": {"limit": "max rows"}},
	    {"method": "POST", "path": "/things/{id}/actions", "summary": "do a thing"}
	  ]
	}`

	c := ParseContract(legacy)
	require.Equal(t, "Old", c.Provider)
	require.Equal(t, "https://old.dev", c.BaseURL)
	require.NotNil(t, c.Auth)
	require.Equal(t, "token sent in the Authorization header", c.Auth.Note)
	require.Empty(t, c.LegacyProfile)
	require.Len(t, c.Operations, 2)

	require.Equal(t, "get-things", c.Operations[0].Name)
	require.True(t, c.Operations[0].ReadOnly)
	require.Equal(t, []ContractParam{{Name: "limit", In: "query", Type: "string", Description: "max rows"}}, c.Operations[0].Parameters)

	require.Equal(t, "post-things-id-actions", c.Operations[1].Name)
	require.False(t, c.Operations[1].ReadOnly)
}

// TestParseContract_Empty covers the blank profile.
func TestParseContract_Empty(t *testing.T) {
	c := ParseContract("   \n ")
	require.Empty(t, c.Operations)
	require.Empty(t, c.LegacyProfile)
	require.False(t, IsStructuredContract(""))
}

// TestProviderDef_Contract_FillsFromRow: when the profile is empty the
// contract is synthesized from the provider row, and query_param auth maps
// the row's auth_header column to the query-parameter name.
func TestProviderDef_Contract_FillsFromRow(t *testing.T) {
	bearer := &ProviderDef{
		Name: "github", Label: "GitHub", APIBaseURL: "https://api.github.com",
		AuthType: "bearer", AuthHeader: "Authorization", AuthPrefix: "Bearer",
	}
	c := bearer.Contract()
	require.Equal(t, "GitHub", c.Provider)
	require.Equal(t, "https://api.github.com", c.BaseURL)
	require.Equal(t, ContractSchemaVersion, c.SchemaVersion)
	require.Equal(t, "bearer", c.Auth.Strategy)
	require.Equal(t, "Authorization", c.Auth.Header)
	require.Empty(t, c.Operations)
	require.NotEmpty(t, c.Digest)

	qp := &ProviderDef{
		Name: "serp-api", APIBaseURL: "https://serpapi.com",
		AuthType: "query_param", AuthHeader: "api_key",
	}
	cq := qp.Contract()
	require.Equal(t, "serp-api", cq.Provider, "falls back to Name when Label is empty")
	require.Equal(t, "query_param", cq.Auth.Strategy)
	require.Equal(t, "api_key", cq.Auth.Param)
	require.Empty(t, cq.Auth.Header)
}

// TestProviderDef_Contract_StaleDigestRecomputed: a digest stored in the blob
// is never served verbatim — it is recomputed from current content.
func TestProviderDef_Contract_StaleDigestRecomputed(t *testing.T) {
	raw := `{"schema_version":"1","operations":[{"name":"a","method":"GET","path":"/a","read_only":true}],"digest":"sha256:bogus"}`
	p := &ProviderDef{Name: "x", Label: "X", APIBaseURL: "https://x.dev", AuthType: "bearer", AuthHeader: "Authorization", AuthPrefix: "Bearer", Profile: raw}

	c := p.Contract()
	require.NotEqual(t, "sha256:bogus", c.Digest)
	require.True(t, strings.HasPrefix(c.Digest, digestPrefix))
	require.Equal(t, c.ComputeDigest(), c.Digest)
}

// TestLintContract_BuiltinSeedsValid: the contractized SerpAPI / GSC seeds are
// valid (no blocking errors; a read-only POST may warn).
func TestLintContract_BuiltinSeedsValid(t *testing.T) {
	for name, profile := range map[string]string{"serp-api": serpAPIProfile, "gsc": gscProfile} {
		t.Run(name, func(t *testing.T) {
			c := ParseContract(profile)
			require.NotEmpty(t, c.Operations)
			require.NotEmpty(t, c.VerifyOperation)
			require.True(t, IsStructuredContract(profile))
			for _, iss := range LintContract(c) {
				require.NotEqual(t, "error", iss.Severity, "seed %s: %s", name, iss)
			}
			require.NoError(t, ValidateContract(c))
		})
	}
}

// TestLintContract_Errors covers every blocking rule.
func TestLintContract_Errors(t *testing.T) {
	op := func(f func(*ContractOperation)) []ContractOperation {
		o := ContractOperation{Name: "a", Method: "GET", Path: "/a", ReadOnly: true}
		f(&o)
		return []ContractOperation{o}
	}
	cases := []struct {
		name string
		c    *ProviderContract
	}{
		{"nil", nil},
		{"no operations", &ProviderContract{}},
		{"missing name", &ProviderContract{Operations: op(func(o *ContractOperation) { o.Name = "" })}},
		{"duplicate name", &ProviderContract{Operations: append(op(func(*ContractOperation) {}), op(func(*ContractOperation) {})...)}},
		{"bad method", &ProviderContract{Operations: op(func(o *ContractOperation) { o.Method = "FETCH" })}},
		{"path without slash", &ProviderContract{Operations: op(func(o *ContractOperation) { o.Path = "orders" })}},
		{"bad param location", &ProviderContract{Operations: op(func(o *ContractOperation) {
			o.Parameters = []ContractParam{{Name: "q", In: "cookie"}}
		})}},
		{"verify op unknown", &ProviderContract{
			VerifyOperation: "nope",
			Operations:      op(func(*ContractOperation) {}),
		}},
		{"verify op is a write", &ProviderContract{
			VerifyOperation: "create",
			Operations: []ContractOperation{
				{Name: "create", Method: "POST", Path: "/a", ReadOnly: false},
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := LintContract(tc.c)
			require.NotEmpty(t, issues, "expected at least one finding")
			hasError := false
			for _, iss := range issues {
				if iss.Severity == "error" {
					hasError = true
				}
			}
			require.True(t, hasError, "expected a blocking error, got %v", issues)
			require.Error(t, ValidateContract(tc.c))
		})
	}
}

// TestLintContract_ReadOnlyPOSTIsWarning: a POST declared read-only is a
// WARNING, not a blocking error — the contract stays publishable so a genuine
// read-via-POST (e.g. a search endpoint) is still expressible. The safety comes
// from the egress gate instead: such an operation is kept out of the default
// safe set (external_proxy.go) and needs the per-user write toggle.
func TestLintContract_ReadOnlyPOSTIsWarning(t *testing.T) {
	c := &ProviderContract{
		Operations: []ContractOperation{
			{Name: "query", Method: "POST", Path: "/search", ReadOnly: true},
		},
	}
	issues := LintContract(c)
	require.Len(t, issues, 1)
	require.Equal(t, "warning", issues[0].Severity)
	require.Equal(t, "operations[0].read_only", issues[0].Path)
	require.NoError(t, ValidateContract(c), "a read_only POST must not block validation")
}
