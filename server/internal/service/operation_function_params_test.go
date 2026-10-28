package service

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/sandbox"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// A function that supplies its arguments via http_request({operation, params})
// must have them reach the proxy. The old inline ProxyInput literal forwarded
// only Operation/Query/Body, so `params` vanished before the proxy ever saw the
// call — silent data loss for the function author. This pins the mapping.
func TestFunctionEgressProxyInputCarriesParams(t *testing.T) {
	row := store.IntegrationFunction{ProviderName: "acme"}
	in := FunctionInvokeInput{UserID: 7, WorkspaceID: 3, SourceKey: "sk"}
	req := &sandbox.HTTPRequestMessage{
		Operation: "listOrders",
		Params:    map[string]any{"id": "42", "filter": "open"},
		Query:     map[string]any{"tag": "x"},
		Body:      map[string]any{"note": "n"},
	}

	got := proxyInputFor(row, req, in)

	if got.Params == nil || got.Params["id"] != "42" || got.Params["filter"] != "open" {
		t.Fatalf("the function's params did not reach ProxyInput: %#v", got.Params)
	}
	// The pre-existing forwarding must be untouched.
	if got.Operation != "listOrders" || got.Query["tag"] != "x" || got.Body["note"] != "n" {
		t.Fatalf("operation/query/body forwarding regressed: %#v", got)
	}
	if got.UserID != 7 || got.WorkspaceID != 3 || got.SourceKey != "sk" || got.Provider != "acme" {
		t.Fatalf("call context was not threaded through: %#v", got)
	}
}

// A request that names no params must produce a nil Params, not an empty map
// that a downstream `!= nil` check would read as "the caller sent something".
func TestFunctionEgressProxyInputNilParams(t *testing.T) {
	got := proxyInputFor(store.IntegrationFunction{ProviderName: "acme"},
		&sandbox.HTTPRequestMessage{Operation: "op"}, FunctionInvokeInput{})
	if got.Params != nil {
		t.Fatalf("an absent params bag became %#v", got.Params)
	}
}
