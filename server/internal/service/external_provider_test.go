// Lock the whitelist matcher semantics. After the 2026-05-27 permission
// redesign, IsAllowed is only consulted when the per-user write toggle is
// OFF — write_enabled=ON in the proxy bypasses it entirely. These tests
// pin the matcher behaviour itself (empty/wildcard/glob/deny) so the
// fallback safe set continues to behave predictably.
package service

import "testing"

func TestIsAllowed_EmptyWhitelistAllowsGETOnly(t *testing.T) {
	s := &ExternalProviderService{}
	cases := []struct {
		method string
		want   bool
	}{
		{"GET", true},
		{"get", true},
		{"POST", false},
		{"DELETE", false},
	}
	for _, c := range cases {
		if got := s.IsAllowed("", c.method, "/anything"); got != c.want {
			t.Errorf("empty whitelist %s /anything: got %v want %v", c.method, got, c.want)
		}
	}
}

func TestIsAllowed_MalformedJSONDeniesAll(t *testing.T) {
	s := &ExternalProviderService{}
	if s.IsAllowed("not-json", "GET", "/x") {
		t.Fatal("malformed whitelist must deny all, including GET")
	}
}

func TestIsAllowed_WildcardPathAndMethod(t *testing.T) {
	s := &ExternalProviderService{}
	wl := `[{"method":"*","path":"*"}]`
	cases := []struct{ method, path string }{
		{"GET", "/foo"},
		{"POST", "/bar/baz"},
		{"DELETE", "/x/y/z"},
	}
	for _, c := range cases {
		if !s.IsAllowed(wl, c.method, c.path) {
			t.Errorf("wildcard rule should allow %s %s", c.method, c.path)
		}
	}
}

func TestIsAllowed_DefaultSafeSet_GETStar(t *testing.T) {
	// Built-in providers ship with this exact value as the curated safe set.
	s := &ExternalProviderService{}
	wl := `[{"method":"GET","path":"*"}]`
	if !s.IsAllowed(wl, "GET", "/tcases") {
		t.Fatal("GET should pass the default safe set")
	}
	if s.IsAllowed(wl, "POST", "/tcases") {
		t.Fatal("POST must NOT pass when write_enabled=OFF and whitelist is GET-only — this is the redesign's whole point")
	}
}

func TestIsAllowed_PathGlobSingleSegment(t *testing.T) {
	s := &ExternalProviderService{}
	wl := `[{"method":"POST","path":"/repos/*/*/issues"}]`
	if !s.IsAllowed(wl, "POST", "/repos/owner/repo/issues") {
		t.Fatal("two-star glob should match exactly one segment per star")
	}
	if s.IsAllowed(wl, "POST", "/repos/owner/issues") {
		t.Fatal("two-star glob requires two segments, not one")
	}
}

func TestIsAllowed_TrailingStarStaysInSegment(t *testing.T) {
	// "/repos/*" means /repos/<one-segment>, not /repos/anything/at/all.
	s := &ExternalProviderService{}
	wl := `[{"method":"GET","path":"/repos/*"}]`
	if !s.IsAllowed(wl, "GET", "/repos/foo") {
		t.Fatal("trailing star should match a single segment")
	}
	if s.IsAllowed(wl, "GET", "/repos/foo/bar") {
		t.Fatal("trailing star must NOT cross slash")
	}
}

func TestIsAllowed_MultipleRulesUnion(t *testing.T) {
	s := &ExternalProviderService{}
	wl := `[{"method":"GET","path":"*"},{"method":"POST","path":"/tcases"}]`
	if !s.IsAllowed(wl, "GET", "/anything") {
		t.Fatal("first rule should pass GET")
	}
	if !s.IsAllowed(wl, "POST", "/tcases") {
		t.Fatal("second rule should pass POST /tcases")
	}
	if s.IsAllowed(wl, "POST", "/tcase_categories") {
		t.Fatal("POST /tcase_categories should NOT pass — not in either rule")
	}
}

// gateAllows is the proxy's single permission predicate. These three
// tests are the structural backbone of the 2026-05-27 redesign — if any
// of them flip, the user-facing semantics of the "允许写入" toggle have
// silently changed.
func TestGateAllows_WriteEnabledBypassesWhitelist(t *testing.T) {
	s := &ExternalProxyService{providerSvc: &ExternalProviderService{}}
	// Default safe set is GET-only — but write_enabled=true must let
	// POST/DELETE/anything through. This is the whole point of the
	// redesign: one user-facing toggle, no JSON editing required.
	wl := `[{"method":"GET","path":"*"}]`
	cases := []struct{ method, path string }{
		{"POST", "/tcases"},
		{"PUT", "/tcases/123"},
		{"DELETE", "/anything/at/all"},
	}
	for _, c := range cases {
		if !s.gateAllows(true, wl, c.method, c.path) {
			t.Errorf("write_enabled=ON must bypass whitelist for %s %s — redesign regression", c.method, c.path)
		}
	}
}

func TestGateAllows_WriteDisabledFallsBackToSafeSet(t *testing.T) {
	s := &ExternalProxyService{providerSvc: &ExternalProviderService{}}
	wl := `[{"method":"GET","path":"*"}]`
	if !s.gateAllows(false, wl, "GET", "/tcases") {
		t.Fatal("write_enabled=OFF + GET in safe set must still pass — reads keep working with zero opt-in")
	}
	if s.gateAllows(false, wl, "POST", "/tcases") {
		t.Fatal("write_enabled=OFF + POST not in safe set must be blocked — fail-closed default")
	}
}

func TestGateAllows_WriteEnabledBypassesEvenMalformedWhitelist(t *testing.T) {
	// Defence in depth: a corrupt whitelist JSON normally denies everything,
	// but write_enabled=ON should still let the user through. Otherwise a
	// single bad row in external_providers locks the user out of their own
	// opted-in provider.
	s := &ExternalProxyService{providerSvc: &ExternalProviderService{}}
	if !s.gateAllows(true, "not-json", "POST", "/x") {
		t.Fatal("write_enabled=ON must bypass even a malformed whitelist")
	}
	if s.gateAllows(false, "not-json", "GET", "/x") {
		t.Fatal("write_enabled=OFF + malformed whitelist must still fail-closed")
	}
}
