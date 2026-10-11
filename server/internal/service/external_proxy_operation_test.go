// Tests for operation-level authorization (issue #737): the contract's named
// operations are the authorization object, enforced by the proxy at egress,
// with write/sensitive operations distinguished from read-only ones. The
// legacy method+path whitelist stays as the fallback so nothing regresses.
package service

import (
	"errors"
	"strings"
	"testing"
)

// opAuthzContract is a contract with one read-only and one write operation,
// including a path template.
const opAuthzContract = `{
  "schema_version": "1",
  "provider": "OrderShop",
  "base_url": "https://orders.example.com",
  "auth": {"strategy": "bearer", "header": "Authorization", "prefix": "Bearer"},
  "operations": [
    {"name": "checkOrder", "method": "GET", "path": "/orders/{orderId}", "read_only": true},
    {"name": "listOrders", "method": "GET", "path": "/orders", "read_only": true},
    {"name": "cancelOrder", "method": "POST", "path": "/orders/{orderId}/cancel", "read_only": false},
    {"name": "refundOrder", "method": "POST", "path": "/orders/{orderId}/refund", "read_only": false}
  ]
}`

func opAuthzEnv(t *testing.T) *ExternalProxyService {
	t.Helper()
	// gateAllows -> providerSvc.IsAllowed is a pure function of the row, so an
	// empty provider service is enough to exercise the fallback path.
	return &ExternalProxyService{providerSvc: &ExternalProviderService{}}
}

func TestAuthorizeCall_UnknownOperationRejectedWithReason(t *testing.T) {
	svc := opAuthzEnv(t)
	prov := &ProviderDef{Name: "ordershop", Whitelist: `[{"method":"GET","path":"*"}]`}
	contract := ParseContract(opAuthzContract)

	_, _, _, _, err := svc.authorizeCall(prov, contract, ProxyInput{
		Provider:  "ordershop",
		Operation: "deleteEverything",
	}, false)
	if err == nil {
		t.Fatal("an unknown operation must be rejected")
	}
	var pe *ProxyError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProxyError, got %T", err)
	}
	if pe.Code != "unknown_operation" {
		t.Fatalf("code = %q, want unknown_operation", pe.Code)
	}
	if !strings.Contains(pe.Message, "deleteEverything") {
		t.Fatalf("reason must name the offending operation, got %q", pe.Message)
	}
}

func TestAuthorizeCall_WriteOperationRejectedWithoutWriteToggle(t *testing.T) {
	svc := opAuthzEnv(t)
	prov := &ProviderDef{Name: "ordershop", Whitelist: `[{"method":"POST","path":"*"}]`}
	contract := ParseContract(opAuthzContract)

	// Even though the whitelist would allow POST *, the resolved operation is a
	// write, so operation-level authorization denies it.
	_, _, _, _, err := svc.authorizeCall(prov, contract, ProxyInput{
		Provider:  "ordershop",
		Operation: "cancelOrder",
		Query:     map[string]any{"orderId": "42"},
	}, false)
	var pe *ProxyError
	if !errors.As(err, &pe) || pe.Code != "operation_requires_write" {
		t.Fatalf("expected operation_requires_write, got %v", err)
	}
	if !strings.Contains(pe.Message, "cancelOrder") {
		t.Fatalf("reason must name the operation, got %q", pe.Message)
	}
}

func TestAuthorizeCall_ReadOnlyOperationAllowedWithoutWriteToggle(t *testing.T) {
	svc := opAuthzEnv(t)
	prov := &ProviderDef{Name: "ordershop"}
	contract := ParseContract(opAuthzContract)

	method, path, query, _, err := svc.authorizeCall(prov, contract, ProxyInput{
		Provider:  "ordershop",
		Operation: "checkOrder",
		Query:     map[string]any{"orderId": "42", "expand": "items"},
	}, false)
	if err != nil {
		t.Fatalf("read-only operation must be allowed: %v", err)
	}
	if method != "GET" || path != "/orders/42" {
		t.Fatalf("resolved %s %s, want GET /orders/42", method, path)
	}
	// The path parameter must be folded into the path, not re-sent as a query.
	if _, still := query["orderId"]; still {
		t.Fatalf("orderId should have been consumed into the path, query=%v", query)
	}
	if query["expand"] != "items" {
		t.Fatalf("non-path query param must survive, got %v", query)
	}
}

func TestAuthorizeCall_WriteAllowedWhenToggleOn(t *testing.T) {
	svc := opAuthzEnv(t)
	prov := &ProviderDef{Name: "ordershop"}
	contract := ParseContract(opAuthzContract)

	method, path, _, _, err := svc.authorizeCall(prov, contract, ProxyInput{
		Provider:  "ordershop",
		Operation: "cancelOrder",
		Query:     map[string]any{"orderId": "42"},
	}, true)
	if err != nil {
		t.Fatalf("write toggle ON must authorize the write: %v", err)
	}
	if method != "POST" || path != "/orders/42/cancel" {
		t.Fatalf("resolved %s %s, want POST /orders/42/cancel", method, path)
	}
}

// TestAuthorizeCall_MethodPathResolvesToWriteOperation proves that a caller
// passing method+path (not an operation name) is still governed by the
// operation the path maps to — the "upgrade from URL glob" behaviour.
func TestAuthorizeCall_MethodPathResolvesToWriteOperation(t *testing.T) {
	svc := opAuthzEnv(t)
	prov := &ProviderDef{Name: "ordershop", Whitelist: `[{"method":"POST","path":"*"}]`}
	contract := ParseContract(opAuthzContract)

	_, _, _, _, err := svc.authorizeCall(prov, contract, ProxyInput{
		Provider: "ordershop",
		Method:   "POST",
		Path:     "/orders/42/refund",
	}, false)
	var pe *ProxyError
	if !errors.As(err, &pe) || pe.Code != "operation_requires_write" {
		t.Fatalf("expected operation_requires_write for a resolved write path, got %v", err)
	}
}

func TestAuthorizeCall_LegacyWhitelistFallback(t *testing.T) {
	svc := opAuthzEnv(t)
	// A legacy provider with no structured contract: authorization stays on the
	// curated method+path whitelist.
	legacy := &ProviderDef{Name: "github", Whitelist: `[{"method":"GET","path":"*"}]`}
	contract := ParseContract("just some free text profile")

	if _, _, _, _, err := svc.authorizeCall(legacy, contract, ProxyInput{
		Provider: "github", Method: "GET", Path: "/repos/o/r/issues",
	}, false); err != nil {
		t.Fatalf("whitelisted legacy GET must be allowed: %v", err)
	}

	_, _, _, _, err := svc.authorizeCall(legacy, contract, ProxyInput{
		Provider: "github", Method: "DELETE", Path: "/repos/o/r",
	}, false)
	var pe *ProxyError
	if !errors.As(err, &pe) || pe.Code != "write_disabled" {
		t.Fatalf("legacy non-whitelisted call must be denied with write_disabled, got %v", err)
	}
}

// TestAuthorizeCall_OffContractPathFallsBackToWhitelist locks the deliberate
// non-tightening: a contract-governed provider whose request matches no
// declared operation still defers to the curated whitelist rather than being
// denied outright — preserving pre-#737 behaviour for existing providers like
// GSC (whose whitelist is `GET *`).
func TestAuthorizeCall_OffContractPathFallsBackToWhitelist(t *testing.T) {
	svc := opAuthzEnv(t)
	prov := &ProviderDef{Name: "gsc", Whitelist: `[{"method":"GET","path":"*"}]`}
	contract := ParseContract(opAuthzContract)

	if _, _, _, _, err := svc.authorizeCall(prov, contract, ProxyInput{
		Provider: "gsc", Method: "GET", Path: "/some/other/endpoint",
	}, false); err != nil {
		t.Fatalf("off-contract whitelisted GET must still be allowed: %v", err)
	}
}

func TestAuthorizeCall_MissingPathParamRejected(t *testing.T) {
	svc := opAuthzEnv(t)
	prov := &ProviderDef{Name: "ordershop"}
	contract := ParseContract(opAuthzContract)

	_, _, _, _, err := svc.authorizeCall(prov, contract, ProxyInput{
		Provider:  "ordershop",
		Operation: "checkOrder",
	}, false)
	var pe *ProxyError
	if !errors.As(err, &pe) || pe.Code != "invalid_operation_params" {
		t.Fatalf("expected invalid_operation_params, got %v", err)
	}
	if !strings.Contains(pe.Message, "orderId") {
		t.Fatalf("reason must name the missing parameter, got %q", pe.Message)
	}
}

// --- contract operation helpers -------------------------------------------

func TestProviderContract_SplitOperations(t *testing.T) {
	c := ParseContract(opAuthzContract)
	read, write := c.SplitOperations()
	if strings.Join(read, ",") != "checkOrder,listOrders" {
		t.Fatalf("read ops = %v", read)
	}
	if strings.Join(write, ",") != "cancelOrder,refundOrder" {
		t.Fatalf("write ops = %v", write)
	}
}

func TestProviderContract_OperationByName_CaseInsensitive(t *testing.T) {
	c := ParseContract(opAuthzContract)
	if _, ok := c.OperationByName("CANCELORDER"); !ok {
		t.Fatal("operation lookup must be case-insensitive")
	}
	if _, ok := c.OperationByName("nope"); ok {
		t.Fatal("unknown operation must not resolve")
	}
}

func TestProviderContract_MatchOperation(t *testing.T) {
	c := ParseContract(opAuthzContract)
	cases := []struct {
		method, path string
		wantName     string
		wantOK       bool
	}{
		{"GET", "/orders/42", "checkOrder", true},
		{"get", "/orders/42", "checkOrder", true},
		{"GET", "/orders", "listOrders", true},
		{"POST", "/orders/42/cancel", "cancelOrder", true},
		{"GET", "/orders/42/cancel", "", false},       // method mismatch
		{"GET", "/orders/42/items", "", false},        // segment count mismatch
		{"DELETE", "/orders/42", "", false},           // no such operation
		{"GET", "/orders/42?x=1", "checkOrder", true}, // query stripped
	}
	for _, tc := range cases {
		op, ok := c.MatchOperation(tc.method, tc.path)
		if ok != tc.wantOK {
			t.Errorf("MatchOperation(%s %s) ok=%v want %v", tc.method, tc.path, ok, tc.wantOK)
			continue
		}
		if ok && op.Name != tc.wantName {
			t.Errorf("MatchOperation(%s %s) = %s, want %s", tc.method, tc.path, op.Name, tc.wantName)
		}
	}
}

func TestProviderContract_AuthStrategyAliasFolded(t *testing.T) {
	// The flat `auth_strategy` spelling is folded into Auth.Strategy.
	c := ParseContract(`{"schema_version":"1","auth_strategy":"oauth2_refresh","operations":[]}`)
	if c.Auth == nil || c.Auth.Strategy != "oauth2_refresh" {
		t.Fatalf("auth_strategy alias not folded: %+v", c.Auth)
	}
}

func TestProviderContract_ResolvePath_EscapesSegment(t *testing.T) {
	op := &ContractOperation{Name: "get", Path: "/v3/sites/{siteUrl}/data"}
	path, consumed, err := op.ResolvePath(map[string]any{"siteUrl": "https://example.com/a"}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// A value containing "/" must not escape its segment.
	if strings.Count(path, "/") != 4 {
		t.Fatalf("slash in a path param escaped its segment: %q", path)
	}
	if len(consumed) != 1 || consumed[0] != "query:siteUrl" {
		t.Fatalf("consumed = %v", consumed)
	}
}

func TestProviderContract_ResolvePath_FromBody(t *testing.T) {
	op := &ContractOperation{Name: "get", Path: "/orders/{orderId}"}
	path, consumed, err := op.ResolvePath(nil, map[string]any{"orderId": 7})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if path != "/orders/7" {
		t.Fatalf("path = %q", path)
	}
	if len(consumed) != 1 || consumed[0] != "body:orderId" {
		t.Fatalf("consumed = %v", consumed)
	}
}
