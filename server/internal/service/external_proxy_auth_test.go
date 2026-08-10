// Lock the Authorization header composition logic across the credential
// shapes the SPA Save flow emits. Each provider can declare multiple
// auth modes (e.g. TAPD = basic + bearer/PAT); the credential stores an
// explicit `auth_mode` so buildAuthHeader doesn't have to guess.
package service

import (
	"encoding/base64"
	"testing"
)

func TestBuildAuthHeader_BearerExplicitMode(t *testing.T) {
	prov := &ProviderDef{AuthType: "bearer", AuthHeader: "Authorization", AuthPrefix: "Bearer"}
	got := buildAuthHeader(prov, map[string]any{
		"auth_mode": "bearer",
		"token":     "ghp_abc",
	})
	if got != "Bearer ghp_abc" {
		t.Fatalf("got %q want %q", got, "Bearer ghp_abc")
	}
}

func TestBuildAuthHeader_BasicExplicitMode(t *testing.T) {
	prov := &ProviderDef{AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"}
	got := buildAuthHeader(prov, map[string]any{
		"auth_mode": "basic",
		"user":      "alice",
		"password":  "s3cret",
	})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TAPD lone token with NO explicit auth_mode (e.g. a credential saved as
// `{token: ...}` before the mode existed). Under a basic-typed provider the
// old code mis-sent this as `Basic <token>` -> TAPD 422. Shape inference must
// resolve it to Bearer.
func TestBuildAuthHeader_TAPDLoneTokenNoMode(t *testing.T) {
	prov := &ProviderDef{Name: "tapd", AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"}
	got := buildAuthHeader(prov, map[string]any{
		"token": "1eadbaa879f225cc73f32762b3501100ea55c747",
	})
	if got != "Bearer 1eadbaa879f225cc73f32762b3501100ea55c747" {
		t.Fatalf("got %q want Bearer <token>", got)
	}
}

// TAPD lone token with a WRONG explicit auth_mode=basic. Basic is impossible
// without a user:password pair, so the override must fall through to Bearer
// instead of emitting a guaranteed-bad `Basic <token>`.
func TestBuildAuthHeader_TAPDLoneTokenWrongBasicMode(t *testing.T) {
	prov := &ProviderDef{Name: "tapd", AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"}
	got := buildAuthHeader(prov, map[string]any{
		"auth_mode": "basic",
		"token":     "1eadbaa879f225cc73f32762b3501100ea55c747",
	})
	if got != "Bearer 1eadbaa879f225cc73f32762b3501100ea55c747" {
		t.Fatalf("got %q want Bearer <token> (lone token cannot do Basic)", got)
	}
}

// A genuine basic pair must STILL go out as Basic -- the override only fires
// when there is no usable user:password pair.
func TestBuildAuthHeader_BasicPairUnaffected(t *testing.T) {
	prov := &ProviderDef{Name: "tapd", AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"}
	got := buildAuthHeader(prov, map[string]any{
		"auth_mode":    "basic",
		"api_user":     "alice",
		"api_password": "s3cret",
	})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TAPD basic credential saved with TAPD's legacy field names.
func TestBuildAuthHeader_BasicTAPDLegacyFields(t *testing.T) {
	prov := &ProviderDef{Name: "tapd", AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"}
	got := buildAuthHeader(prov, map[string]any{
		"auth_mode":    "basic",
		"api_user":     "alice",
		"api_password": "s3cret",
	})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TAPD PAT: provider.AuthType=basic, provider.AuthPrefix=Basic, but the
// credential carries an explicit bearer mode. Bearer mode MUST override
// provider.AuthPrefix — otherwise TAPD PAT credentials get emitted as
// malformed "Basic <raw_pat>" and TAPD rejects with 401.
func TestBuildAuthHeader_BearerModeOverridesProviderPrefix(t *testing.T) {
	prov := &ProviderDef{Name: "tapd", AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"}
	got := buildAuthHeader(prov, map[string]any{
		"auth_mode": "bearer",
		"token":     "pat_xyz",
	})
	if got != "Bearer pat_xyz" {
		t.Fatalf("got %q want Bearer pat_xyz — bearer mode must override provider AuthPrefix", got)
	}
}

// Same shape but with access_token (the historical TAPD PAT key) —
// should still produce "Bearer <pat>".
func TestBuildAuthHeader_BearerModeAccessTokenKey(t *testing.T) {
	prov := &ProviderDef{Name: "tapd", AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"}
	got := buildAuthHeader(prov, map[string]any{
		"auth_mode":    "bearer",
		"access_token": "pat_xyz",
	})
	if got != "Bearer pat_xyz" {
		t.Fatalf("got %q want Bearer pat_xyz", got)
	}
}

func TestBuildAuthHeader_CustomHeaderNoPrefix(t *testing.T) {
	prov := &ProviderDef{AuthType: "custom_header", AuthHeader: "X-Api-Key", AuthPrefix: ""}
	got := buildAuthHeader(prov, map[string]any{
		"auth_mode": "custom_header",
		"token":     "raw-key",
	})
	if got != "raw-key" {
		t.Fatalf("got %q want %q", got, "raw-key")
	}
}

// Legacy path: credential pre-dates the multi-mode refactor and has no
// auth_mode in its config. We fall back to provider.AuthType + the
// access_token / token / user+password detection.
func TestBuildAuthHeader_LegacyBearer(t *testing.T) {
	prov := &ProviderDef{AuthType: "bearer", AuthHeader: "Authorization", AuthPrefix: "Bearer"}
	got := buildAuthHeader(prov, map[string]any{"token": "ghp_abc"})
	if got != "Bearer ghp_abc" {
		t.Fatalf("got %q want %q", got, "Bearer ghp_abc")
	}
}

func TestBuildAuthHeader_LegacyBasic(t *testing.T) {
	prov := &ProviderDef{AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"}
	got := buildAuthHeader(prov, map[string]any{
		"api_user":     "alice",
		"api_password": "s3cret",
	})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// Legacy PAT: credential has access_token (no auth_mode). Should be
// emitted as Bearer regardless of provider.AuthType=basic.
func TestBuildAuthHeader_LegacyTAPDPAT(t *testing.T) {
	prov := &ProviderDef{Name: "tapd", AuthType: "basic", AuthHeader: "Authorization", AuthPrefix: "Basic"}
	got := buildAuthHeader(prov, map[string]any{"access_token": "pat_xyz"})
	if got != "Bearer pat_xyz" {
		t.Fatalf("got %q want Bearer pat_xyz", got)
	}
}

func TestBuildAuthHeader_EmptyConfig(t *testing.T) {
	prov := &ProviderDef{AuthType: "bearer", AuthHeader: "Authorization", AuthPrefix: "Bearer"}
	got := buildAuthHeader(prov, map[string]any{})
	if got != "" {
		t.Fatalf("got %q want empty", got)
	}
}

// SerpAPI-shape: query_param providers carry the secret in the URL query, so
// buildAuthHeader must produce NO header (otherwise the api_key would leak into
// a stray Authorization header) and buildAuthQuery must return the api_key pair.
func TestQueryParamAuth_SerpAPI(t *testing.T) {
	prov := &ProviderDef{Name: "serp-api", AuthType: "query_param", AuthHeader: "api_key"}
	cfg := map[string]any{"auth_mode": "query_param", "token": "serp_secret_xyz"}

	if h := buildAuthHeader(prov, cfg); h != "" {
		t.Fatalf("query_param provider must not emit an auth header, got %q", h)
	}
	name, val := buildAuthQuery(prov, cfg)
	if name != "api_key" || val != "serp_secret_xyz" {
		t.Fatalf("got (%q,%q) want (api_key, serp_secret_xyz)", name, val)
	}
}

// The api_key value is read from any of the token/access_token/api_key aliases
// (credToken), so a credential saved under "api_key" works too.
func TestQueryParamAuth_ApiKeyAlias(t *testing.T) {
	prov := &ProviderDef{Name: "serp-api", AuthType: "query_param", AuthHeader: "api_key"}
	name, val := buildAuthQuery(prov, map[string]any{"auth_mode": "query_param", "api_key": "k123"})
	if name != "api_key" || val != "k123" {
		t.Fatalf("got (%q,%q) want (api_key, k123)", name, val)
	}
}

// Legacy credential (no auth_mode) still routes to query-param auth off the
// provider's AuthType, and the param name defaults to api_key when AuthHeader
// is blank.
func TestQueryParamAuth_LegacyAndDefaultName(t *testing.T) {
	prov := &ProviderDef{Name: "serp-api", AuthType: "query_param", AuthHeader: ""}
	name, val := buildAuthQuery(prov, map[string]any{"token": "legacy"})
	if name != "api_key" || val != "legacy" {
		t.Fatalf("got (%q,%q) want (api_key, legacy)", name, val)
	}
}

// Header-auth providers must NOT produce a query-param pair — the two paths are
// mutually exclusive.
func TestQueryParamAuth_HeaderProviderUnaffected(t *testing.T) {
	prov := &ProviderDef{Name: "github", AuthType: "bearer", AuthHeader: "Authorization", AuthPrefix: "Bearer"}
	if name, val := buildAuthQuery(prov, map[string]any{"auth_mode": "bearer", "token": "ghp_abc"}); name != "" || val != "" {
		t.Fatalf("bearer provider must not use query-param auth, got (%q,%q)", name, val)
	}
	if h := buildAuthHeader(prov, map[string]any{"auth_mode": "bearer", "token": "ghp_abc"}); h != "Bearer ghp_abc" {
		t.Fatalf("bearer header still expected, got %q", h)
	}
}

// Empty credential → no query-param pair (nothing to inject).
func TestQueryParamAuth_EmptyConfig(t *testing.T) {
	prov := &ProviderDef{Name: "serp-api", AuthType: "query_param", AuthHeader: "api_key"}
	if name, val := buildAuthQuery(prov, map[string]any{"auth_mode": "query_param"}); name != "" || val != "" {
		t.Fatalf("empty credential must not inject, got (%q,%q)", name, val)
	}
}

func TestProviderDef_AuthModes(t *testing.T) {
	cases := []struct {
		p    ProviderDef
		want []string
	}{
		{ProviderDef{Name: "github", AuthType: "bearer"}, []string{"bearer"}},
		// tapd is single-mode now (driven by auth_type): basic and bearer are
		// modeled as two separate user providers, not one dual-mode provider.
		{ProviderDef{Name: "tapd", AuthType: "basic"}, []string{"basic"}},
		{ProviderDef{Name: "tapd", AuthType: "bearer"}, []string{"bearer"}},
		{ProviderDef{Name: "jira", AuthType: "basic"}, []string{"basic"}},
		{ProviderDef{Name: "linear", AuthType: "bearer"}, []string{"bearer"}},
		{ProviderDef{Name: "custom", AuthType: "custom_header"}, []string{"custom_header"}},
		{ProviderDef{Name: "noauth", AuthType: ""}, []string{"bearer"}},
		{ProviderDef{Name: "serp-api", AuthType: "query_param"}, []string{"query_param"}},
		{ProviderDef{Name: "gsc", AuthType: "bearer"}, []string{"bearer"}},
	}
	for _, c := range cases {
		got := c.p.AuthModes()
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %v want %v", c.p.Name, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: got %v want %v", c.p.Name, got, c.want)
			}
		}
	}
}
