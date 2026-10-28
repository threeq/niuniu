package sandbox

import "testing"

// A spec keyed on an operation name must keep authorizing exactly by that name.
// The operation name is the authorization object; nothing else widens it.
func TestAllowsOperationSpecByName(t *testing.T) {
	spec := Spec{Name: "ops", Egress: []EgressOp{{Operation: "cancelOrder"}}}
	if !spec.Allows(&HTTPRequestMessage{Operation: "cancelOrder"}) {
		t.Fatal("a declared operation was not authorized")
	}
	if spec.Allows(&HTTPRequestMessage{Operation: "other"}) {
		t.Fatal("an undeclared operation was authorized")
	}
	// A legacy method+path request must not be matched by an operation spec.
	if spec.Allows(&HTTPRequestMessage{Method: "GET", Path: "/cancel"}) {
		t.Fatal("a method+path request was wrongly authorized by an operation spec")
	}
}

// The legacy form used to authorize NOTHING (fail-closed, but a silent
// migration trap). It now matches a request that carries the same method+path.
func TestAllowsLegacyMethodPathSpec(t *testing.T) {
	spec := Spec{Name: "legacy", Egress: []EgressOp{{Method: "GET", Path: "/orders"}}}

	if !spec.Allows(&HTTPRequestMessage{Method: "GET", Path: "/orders"}) {
		t.Fatal("a legacy method+path spec did not authorize its matching request")
	}
	// The method match is case-insensitive, like the proxy's own comparison.
	if !spec.Allows(&HTTPRequestMessage{Method: "get", Path: "/orders"}) {
		t.Fatal("legacy method matching is case-sensitive")
	}
	// A different path is still denied.
	if spec.Allows(&HTTPRequestMessage{Method: "GET", Path: "/orders/9"}) {
		t.Fatal("a non-matching path was authorized")
	}
	// A different method is still denied.
	if spec.Allows(&HTTPRequestMessage{Method: "POST", Path: "/orders"}) {
		t.Fatal("a non-matching method was authorized")
	}
}

// Anything that names neither an operation nor a method+path matches nothing and
// must fail closed.
func TestAllowsDeniesWhenNothingMatches(t *testing.T) {
	legacy := Spec{Name: "legacy", Egress: []EgressOp{{Method: "GET", Path: "/orders"}}}
	if legacy.Allows(&HTTPRequestMessage{}) {
		t.Fatal("an empty request was authorized by a legacy spec")
	}
	opSpec := Spec{Name: "ops", Egress: []EgressOp{{Operation: "cancelOrder"}}}
	if opSpec.Allows(&HTTPRequestMessage{}) {
		t.Fatal("an empty request was authorized by an operation spec")
	}
}

// A spec may declare both forms; each must match its own request shape and never
// the other's.
func TestAllowsHandlesBothFormsInOneSpec(t *testing.T) {
	spec := Spec{Name: "mixed", Egress: []EgressOp{
		{Operation: "cancelOrder"},
		{Method: "GET", Path: "/orders"},
	}}
	if !spec.Allows(&HTTPRequestMessage{Operation: "cancelOrder"}) {
		t.Fatal("operation form was not matched in a mixed spec")
	}
	if !spec.Allows(&HTTPRequestMessage{Method: "GET", Path: "/orders"}) {
		t.Fatal("legacy form was not matched in a mixed spec")
	}
	if spec.Allows(&HTTPRequestMessage{Method: "DELETE", Path: "/orders"}) {
		t.Fatal("an unmatched legacy request was authorized")
	}
}
