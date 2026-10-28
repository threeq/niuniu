package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// The declared verify_operation always wins over the auto-selection.
func TestSelectVerifyOperationPrefersDeclared(t *testing.T) {
	c := &ProviderContract{
		VerifyOperation: "ping",
		Operations: []ContractOperation{
			{Name: "ping", Method: "HEAD", Path: "/ping", ReadOnly: true},
			{Name: "list", Method: "GET", Path: "/items", ReadOnly: true},
		},
	}
	op, src := SelectVerifyOperation(c)
	require.NotNil(t, op)
	require.Equal(t, "ping", op.Name)
	require.Equal(t, "verify_operation", src)
}

// With no declared probe, the least-harmful read-only operation is chosen:
// GET beats HEAD, fewer path placeholders beat more, fewer params beat more.
func TestSelectVerifyOperationDefaultsLeastHarmful(t *testing.T) {
	c := &ProviderContract{
		Operations: []ContractOperation{
			{Name: "create", Method: "POST", Path: "/items", ReadOnly: false},
			{Name: "head", Method: "HEAD", Path: "/h", ReadOnly: true},
			{Name: "list_sites", Method: "GET", Path: "/sites/{siteUrl}", ReadOnly: true},
			{Name: "ping", Method: "GET", Path: "/ping", ReadOnly: true},
		},
	}
	op, src := SelectVerifyOperation(c)
	require.NotNil(t, op)
	require.Equal(t, "ping", op.Name, "GET with no path placeholders is the least harmful")
	require.Equal(t, "default_least_harmful", src)
}

// A write-only contract has no usable probe.
func TestSelectVerifyOperationNoneReadOnly(t *testing.T) {
	c := &ProviderContract{
		Operations: []ContractOperation{{Name: "create", Method: "POST", Path: "/items"}},
	}
	op, src := SelectVerifyOperation(c)
	require.Nil(t, op)
	require.Empty(t, src)
}

func TestBuildProbeManifestWarnsNonCoarse(t *testing.T) {
	get := BuildProbeManifest(&ContractOperation{Name: "list", Method: "GET", Path: "/items", ReadOnly: true}, "verify_operation")
	require.True(t, get.CoarseReadOnly)
	require.Empty(t, get.Warnings)

	post := BuildProbeManifest(&ContractOperation{Name: "search", Method: "POST", Path: "/search", ReadOnly: true}, "verify_operation")
	require.False(t, post.CoarseReadOnly)
	require.NotEmpty(t, post.Warnings, "a non-GET probe must warn")
}

func TestBuildProbeRequest(t *testing.T) {
	// GET: the example keys become query params; path placeholders substituted.
	op := &ContractOperation{
		Name: "list_sites", Method: "GET", Path: "/sites/{siteUrl}/rows",
		RequestExample: raw(map[string]any{"siteUrl": "https://ex.com", "limit": 5}),
	}
	method, path, query, body, err := buildProbeRequest(op)
	require.NoError(t, err)
	require.Equal(t, "GET", method)
	require.Equal(t, "/sites/https:%2F%2Fex.com/rows", path)
	require.Nil(t, body)
	// json.Unmarshal decodes numbers as float64.
	require.EqualValues(t, 5, query["limit"])

	// POST: the example becomes the body.
	op = &ContractOperation{Name: "search", Method: "POST", Path: "/search",
		RequestExample: raw(map[string]any{"q": "x"})}
	_, _, query, body, err = buildProbeRequest(op)
	require.NoError(t, err)
	require.Nil(t, query)
	require.Equal(t, "x", body["q"])

	// Missing placeholder value -> error (never probe with a half-built path).
	op = &ContractOperation{Name: "op", Method: "GET", Path: "/a/{id}"}
	_, _, _, _, err = buildProbeRequest(op)
	require.Error(t, err)
}

// A verify_operation that resolves to a read-via-POST is a BLOCKING error: the
// coarse read-only check cannot prove it is side-effect-free, and a probe must
// never be able to mutate upstream state.
func TestLintContractRejectsNonGetProbe(t *testing.T) {
	c := (&ProviderContract{
		VerifyOperation: "search",
		Operations: []ContractOperation{
			{Name: "search", Method: "POST", Path: "/search", ReadOnly: true},
		},
	}).normalized()
	issues := LintContract(c)
	var sawErr bool
	for _, iss := range issues {
		if iss.Severity == "error" && (iss.Path == "verify_operation" || iss.Path == "operations[0].read_only") {
			sawErr = true
		}
	}
	require.True(t, sawErr, "a non-GET verify_operation / read_only POST must be a blocking error")
	require.Error(t, ValidateContract(c), "the contract must be rejected, not merely warned about")
}

// A non-GET/HEAD operation flagged read_only warns but does NOT block publish —
// the contract stays expressible; the safety is enforced at egress instead
// (the operation is kept out of the default safe set, see external_proxy.go).
func TestLintContractReadOnlyWriteMethodWarns(t *testing.T) {
	c := (&ProviderContract{
		Operations: []ContractOperation{
			{Name: "list", Method: "GET", Path: "/items", ReadOnly: true},
			{Name: "transfer", Method: "POST", Path: "/transfer", ReadOnly: true},
		},
	}).normalized()
	require.NoError(t, ValidateContract(c), "a read_only POST must not block publish")

	var warned bool
	for _, iss := range LintContract(c) {
		if iss.Path == "operations[1].read_only" {
			warned = true
			require.Equal(t, "warning", iss.Severity)
		}
	}
	require.True(t, warned, "expected a warning for the read_only POST")
}

// SelectVerifyOperation must never return a non-GET/HEAD operation, whatever the
// contract declares.
func TestSelectVerifyOperationNeverReturnsNonGet(t *testing.T) {
	// Declared verify_operation is a read-via-POST -> refused (nil), not returned.
	c := &ProviderContract{
		VerifyOperation: "search",
		Operations: []ContractOperation{
			{Name: "search", Method: "POST", Path: "/search", ReadOnly: true},
			{Name: "list", Method: "GET", Path: "/items", ReadOnly: true},
		},
	}
	op, src := SelectVerifyOperation(c)
	require.Nil(t, op, "the declared POST probe must not be returned")
	require.Empty(t, src)

	// Only a read-only POST exists (no declared probe) -> nil ("no read-only op").
	c = &ProviderContract{
		Operations: []ContractOperation{{Name: "search", Method: "POST", Path: "/search", ReadOnly: true}},
	}
	op, src = SelectVerifyOperation(c)
	require.Nil(t, op)
	require.Empty(t, src)

	// A read-only POST alongside a read-only GET: the GET is chosen.
	c = &ProviderContract{
		Operations: []ContractOperation{
			{Name: "search", Method: "POST", Path: "/search", ReadOnly: true},
			{Name: "list", Method: "GET", Path: "/items", ReadOnly: true},
		},
	}
	op, src = SelectVerifyOperation(c)
	require.NotNil(t, op)
	require.Equal(t, "list", op.Name)
	require.Equal(t, "GET", op.Method)
	require.Equal(t, "default_least_harmful", src)
}

// A verify_operation naming a genuine write is a blocking error.
func TestLintContractBlocksWriteProbe(t *testing.T) {
	c := (&ProviderContract{
		VerifyOperation: "create",
		Operations: []ContractOperation{
			{Name: "create", Method: "POST", Path: "/items", ReadOnly: false},
		},
	}).normalized()
	require.Error(t, ValidateContract(c))
}
