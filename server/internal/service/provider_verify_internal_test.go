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

// A verify_operation that resolves to a read-via-POST is flagged (warning), not
// silently trusted — the coarse read-only check cannot prove it is safe.
func TestLintContractFlagsNonGetProbe(t *testing.T) {
	c := (&ProviderContract{
		VerifyOperation: "search",
		Operations: []ContractOperation{
			{Name: "search", Method: "POST", Path: "/search", ReadOnly: true},
		},
	}).normalized()
	issues := LintContract(c)
	var sawWarning bool
	for _, iss := range issues {
		require.NotEqual(t, "error", iss.Severity, "a read-only POST probe is a warning, not a blocking error")
		if iss.Severity == "warning" && iss.Path == "verify_operation" {
			sawWarning = true
		}
	}
	require.True(t, sawWarning, "non-GET verify_operation must be flagged")
	require.NoError(t, ValidateContract(c))
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
