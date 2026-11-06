package main

// MCP tool contract for intake binding management (design §10.3). The tools are
// thin tunnels over the project API, so the tests pin the two properties that
// actually matter: the exact endpoint/method each tool drives, and the promise
// that no plaintext secret is ever part of a tool call or reply.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type intakeCall struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

type intakeFakeAPI struct {
	mu        sync.Mutex
	calls     []intakeCall
	status    int
	response  string
	responses map[string]string // optional per-path body overrides
}

func newIntakeFake(t *testing.T, response string) (*intakeFakeAPI, *server.MCPServer, *httptest.Server) {
	t.Helper()
	fake := &intakeFakeAPI{status: http.StatusOK, response: response}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		fake.mu.Lock()
		fake.calls = append(fake.calls, intakeCall{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Body:   body,
		})
		status, response := fake.status, fake.response
		if override, ok := fake.responses[r.URL.Path]; ok {
			response = override
		}
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(ts.Close)

	s := server.NewMCPServer("niuniu-test", "test")
	api := &apiClient{base: ts.URL, client: ts.Client()}
	registerIntakeSourceTools(s, api, "7")
	return fake, s, ts
}

func (f *intakeFakeAPI) last(t *testing.T) intakeCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no API call was made")
	}
	return f.calls[len(f.calls)-1]
}

func callIntakeTool(t *testing.T, s *server.MCPServer, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool := s.GetTool(name)
	if tool == nil {
		t.Fatalf("tool %q is not registered", name)
	}
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: name, Arguments: args},
	})
	if err != nil {
		t.Fatalf("%s handler returned a transport error: %v", name, err)
	}
	return res
}

func TestIntakeSourceTools_AreRegistered(t *testing.T) {
	_, s, _ := newIntakeFake(t, `{}`)
	for _, name := range []string{
		"intake_source_list",
		"intake_source_create",
		"intake_source_test",
		"intake_source_rotate",
		"intake_source_delete",
		"intake_source_poll",
		"intake_source_credential_link",
	} {
		if s.GetTool(name) == nil {
			t.Errorf("tool %q is not registered", name)
		}
	}
}

func TestIntakeSourceCreate_ForwardsBindingAndReturnsLinkOnly(t *testing.T) {
	fake, s, _ := newIntakeFake(t,
		`{"source":{"id":3,"source_kind":"ci","name":"jenkins","has_secret":true},"secret_link":"/intake/sources/secret/abc"}`)
	res := callIntakeTool(t, s, "intake_source_create", map[string]any{
		"source_kind": "ci",
		"name":        "jenkins",
		"mode":        "push",
		"provider":    "jenkins",
		"config":      map[string]any{"events": []any{"failed"}},
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodPost || call.Path != "/mcp/projects/7/intake/sources" {
		t.Fatalf("create hit %s %s", call.Method, call.Path)
	}
	if call.Body["source_kind"] != "ci" || call.Body["provider"] != "jenkins" {
		t.Fatalf("create body lost fields: %v", call.Body)
	}
	if _, ok := call.Body["secret"]; ok {
		t.Fatal("create must never send a secret")
	}
	// The reply is the API's {source, secret_link} — the plaintext secret is not
	// present, only the single-use link.
	out := toolResultText(t, res)
	if !strings.Contains(out, "/intake/sources/secret/abc") {
		t.Fatalf("secret_link missing from reply: %s", out)
	}
	if strings.Contains(out, `"secret"`) {
		t.Fatalf("reply leaked a plaintext secret: %s", out)
	}
}

func TestIntakeSourceCreate_EmailImapCarriesCredentialID(t *testing.T) {
	fake, s, _ := newIntakeFake(t, `{"source":{"id":4,"source_kind":"email","name":"support"}}`)
	res := callIntakeTool(t, s, "intake_source_create", map[string]any{
		"source_kind":   "email",
		"name":          "support",
		"mode":          "imap",
		"credential_id": float64(42),
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	if got := fake.last(t).Body["credential_id"]; got != float64(42) {
		t.Fatalf("credential_id = %v, want 42", got)
	}
}

func TestIntakeSourceCreate_ResolvesCredentialAlias(t *testing.T) {
	fake, s, _ := newIntakeFake(t, `{"source":{"id":4,"source_kind":"email","name":"support"}}`)
	fake.mu.Lock()
	fake.responses = map[string]string{
		"/mcp/external-credentials": `{"items":[{"id":12,"provider":"imap","alias":"support@example.com"}]}`,
	}
	fake.mu.Unlock()

	res := callIntakeTool(t, s, "intake_source_create", map[string]any{
		"source_kind":      "email",
		"name":             "support",
		"mode":             "imap",
		"credential_alias": "support@example.com",
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	fake.mu.Lock()
	calls := append([]intakeCall(nil), fake.calls...)
	fake.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("want lookup + create, got %d calls", len(calls))
	}
	if calls[0].Path != "/mcp/external-credentials" || calls[0].Query != "provider=imap" {
		t.Fatalf("lookup call = %v", calls[0])
	}
	if calls[1].Body["credential_id"] != float64(12) {
		t.Fatalf("credential_id = %v, want the resolved 12", calls[1].Body["credential_id"])
	}
}

func TestIntakeSourceCreate_UnknownCredentialAliasFails(t *testing.T) {
	fake, s, _ := newIntakeFake(t, `{"items":[]}`)
	res := callIntakeTool(t, s, "intake_source_create", map[string]any{
		"source_kind":      "email",
		"name":             "support",
		"mode":             "imap",
		"credential_alias": "missing@example.com",
	})
	if !toolResultIsError(res) {
		t.Fatal("an unknown alias must not create a binding")
	}
	if fake.last(t).Path != "/mcp/external-credentials" {
		t.Fatal("create must not be attempted when the credential cannot be resolved")
	}
	if !strings.Contains(toolResultText(t, res), "missing@example.com") {
		t.Fatalf("error should name the alias: %s", toolResultText(t, res))
	}
}

func TestIntakeSourceCredentialLink_UsesAliasNotTitle(t *testing.T) {
	fake, s, _ := newIntakeFake(t, `{"url":"/credential/tok"}`)
	callIntakeTool(t, s, "intake_source_credential_link", map[string]any{"name": "support@example.com"})
	target, _ := fake.last(t).Body["target"].(map[string]any)
	if target["alias"] != "support@example.com" {
		t.Fatalf("alias = %v, want the mailbox label", target["alias"])
	}
}

func TestIntakeSourceCreate_RequiresName(t *testing.T) {
	_, s, _ := newIntakeFake(t, `{}`)
	res := callIntakeTool(t, s, "intake_source_create", map[string]any{"source_kind": "ci"})
	if !toolResultIsError(res) {
		t.Fatal("missing name must be a tool error")
	}
	if !strings.Contains(toolResultText(t, res), "name") {
		t.Fatalf("error should name the missing field: %s", toolResultText(t, res))
	}
}

func TestIntakeSourceList_FiltersByKind(t *testing.T) {
	fake, s, _ := newIntakeFake(t, `{"sources":[]}`)
	res := callIntakeTool(t, s, "intake_source_list", map[string]any{"source_kind": "email"})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodGet || call.Path != "/mcp/projects/7/intake/sources" {
		t.Fatalf("list hit %s %s", call.Method, call.Path)
	}
	if call.Query != "source_kind=email" {
		t.Fatalf("query = %q", call.Query)
	}
}

func TestIntakeSourceMutations_HitTheRightEndpoints(t *testing.T) {
	cases := []struct {
		tool   string
		method string
		path   string
	}{
		{"intake_source_test", http.MethodPost, "/mcp/projects/7/intake/sources/9/test"},
		{"intake_source_rotate", http.MethodPost, "/mcp/projects/7/intake/sources/9/rotate"},
		{"intake_source_delete", http.MethodDelete, "/mcp/projects/7/intake/sources/9"},
		{"intake_source_poll", http.MethodPost, "/mcp/projects/7/intake/sources/9/poll"},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			fake, s, _ := newIntakeFake(t, `{"ok":true}`)
			res := callIntakeTool(t, s, c.tool, map[string]any{"source_id": float64(9)})
			if toolResultIsError(res) {
				t.Fatalf("unexpected error: %s", toolResultText(t, res))
			}
			call := fake.last(t)
			if call.Method != c.method || call.Path != c.path {
				t.Fatalf("%s hit %s %s, want %s %s", c.tool, call.Method, call.Path, c.method, c.path)
			}
		})
	}
}

func TestIntakeSourceTest_ForwardsSample(t *testing.T) {
	fake, s, _ := newIntakeFake(t, `{"allowed":true}`)
	res := callIntakeTool(t, s, "intake_source_test", map[string]any{
		"source_id": float64(9),
		"sample":    map[string]any{"status": "failed"},
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	sample, _ := fake.last(t).Body["sample"].(map[string]any)
	if sample["status"] != "failed" {
		t.Fatalf("sample not forwarded: %v", fake.last(t).Body)
	}
}

func TestIntakeSourceCredentialLink_IssuesImapLinkWithoutPassword(t *testing.T) {
	fake, s, _ := newIntakeFake(t,
		`{"url":"/credential/tok","link_markdown":"[点击打开凭据录入页面](/credential/tok)"}`)
	res := callIntakeTool(t, s, "intake_source_credential_link", map[string]any{
		"name": "support@example.com",
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodPost || call.Path != "/mcp/projects/7/credential-links" {
		t.Fatalf("credential link hit %s %s", call.Method, call.Path)
	}
	if call.Body["kind"] != "provider_credential" {
		t.Fatalf("kind = %v", call.Body["kind"])
	}
	fields, _ := call.Body["fields"].([]any)
	if len(fields) == 0 {
		t.Fatal("fields must name the inputs the page renders")
	}
	var sawPassword bool
	for _, f := range fields {
		if f == "password" {
			sawPassword = true
		}
	}
	if !sawPassword {
		t.Fatalf("IMAP link must collect a password in the browser: %v", fields)
	}
	if _, ok := call.Body["password"]; ok {
		t.Fatal("the credential tool must never carry a password value")
	}
	target, _ := call.Body["target"].(map[string]any)
	if target["provider"] != "imap" {
		t.Fatalf("target provider = %v", target["provider"])
	}
	if !strings.Contains(toolResultText(t, res), "/credential/tok") {
		t.Fatalf("link missing from reply: %s", toolResultText(t, res))
	}
}

func TestIntakeSourceCredentialLink_RejectsNonEmailSource(t *testing.T) {
	fake, s, _ := newIntakeFake(t, `{"sources":[{"id":9,"name":"hooks","source_kind":"webhook"}]}`)
	res := callIntakeTool(t, s, "intake_source_credential_link", map[string]any{
		"source_id": float64(9),
	})
	if !toolResultIsError(res) {
		t.Fatal("a webhook source must not get an IMAP credential link")
	}
	if !strings.Contains(toolResultText(t, res), "not email") {
		t.Fatalf("error should explain the mismatch: %s", toolResultText(t, res))
	}
	if fake.last(t).Path != "/mcp/projects/7/intake/sources" {
		t.Fatal("a rejected link must not reach the credential-links endpoint")
	}
}

func TestIntakeSourceTools_PropagateAPIError(t *testing.T) {
	fake, s, _ := newIntakeFake(t, `{"error":"an intake source with this name already exists"}`)
	fake.mu.Lock()
	fake.status = http.StatusConflict
	fake.mu.Unlock()

	res := callIntakeTool(t, s, "intake_source_create", map[string]any{
		"source_kind": "webhook",
		"name":        "hooks",
	})
	if !toolResultIsError(res) {
		t.Fatal("a 409 must surface as a tool error")
	}
	text := toolResultText(t, res)
	if !strings.Contains(text, "409") || !strings.Contains(text, "already exists") {
		t.Fatalf("status and body must be passed through: %s", text)
	}
}
