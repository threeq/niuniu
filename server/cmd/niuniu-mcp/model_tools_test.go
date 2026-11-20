package main

// MCP tool contract for the shared model library (design §11.2). Each tool is a
// thin tunnel over the project API, so the tests pin the endpoint/method and the
// argument mapping, plus the promise that upstream status codes and error bodies
// reach the conversation unchanged.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type modelCall struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
}

type modelFakeAPI struct {
	mu       sync.Mutex
	calls    []modelCall
	status   int
	response string
}

func newModelFake(t *testing.T, response string) (*modelFakeAPI, *server.MCPServer) {
	t.Helper()
	fake := &modelFakeAPI{status: http.StatusOK, response: response}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		query, _ := url.ParseQuery(r.URL.RawQuery)
		fake.mu.Lock()
		fake.calls = append(fake.calls, modelCall{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  query,
			Body:   body,
		})
		status, response := fake.status, fake.response
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(ts.Close)

	s := server.NewMCPServer("niuniu-test", "test")
	api := &apiClient{base: ts.URL, client: ts.Client()}
	registerModelTools(s, api, "77")
	return fake, s
}

func (f *modelFakeAPI) last(t *testing.T) modelCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no API call was made")
	}
	return f.calls[len(f.calls)-1]
}

func (f *modelFakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func callModelTool(t *testing.T, s *server.MCPServer, name string, args map[string]any) *mcp.CallToolResult {
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

func TestModelTools_AreRegistered(t *testing.T) {
	_, s := newModelFake(t, `{}`)
	for _, name := range []string{
		"model_list",
		"model_create",
		"model_update",
		"model_delete",
		"model_refresh_preview",
		"model_import",
		"model_usage_summary",
		"model_budget_get",
		"model_budget_set",
		"model_budget_set_project",
	} {
		if s.GetTool(name) == nil {
			t.Errorf("tool %q is not registered", name)
		}
	}
}

func TestModelList_ForwardsOwnerAndDisabledFilter(t *testing.T) {
	fake, s := newModelFake(t, `[{"id":1,"name":"alpha"}]`)
	res := callModelTool(t, s, "model_list", map[string]any{
		"include_disabled": true,
		"owner_type":       "org",
		"owner_id":         float64(9),
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodGet || call.Path != "/api/models" {
		t.Fatalf("model_list hit %s %s", call.Method, call.Path)
	}
	if call.Query.Get("all") != "1" || call.Query.Get("owner_type") != "org" || call.Query.Get("owner_id") != "9" {
		t.Fatalf("model_list query lost filters: %v", call.Query)
	}
	if !strings.Contains(toolResultText(t, res), "alpha") {
		t.Fatalf("model_list did not return the API body: %s", toolResultText(t, res))
	}
}

func TestModelCreate_DefaultsEnabledOmitted(t *testing.T) {
	fake, s := newModelFake(t, `{"id":3,"name":"glm-5.1","enabled":true}`)
	res := callModelTool(t, s, "model_create", map[string]any{
		"name":         "glm-5.1",
		"display_name": "GLM 5.1",
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodPost || call.Path != "/api/models" {
		t.Fatalf("model_create hit %s %s", call.Method, call.Path)
	}
	if call.Body["name"] != "glm-5.1" || call.Body["display_name"] != "GLM 5.1" {
		t.Fatalf("model_create body lost fields: %v", call.Body)
	}
	if _, ok := call.Body["enabled"]; ok {
		t.Fatal("enabled must be omitted so the API default (true) applies")
	}
	if _, ok := call.Body["owner"]; ok {
		t.Fatal("no owner override should be sent by default")
	}
}

func TestModelCreate_ForwardsOwnerOverride(t *testing.T) {
	fake, s := newModelFake(t, `{"id":3,"name":"alpha"}`)
	res := callModelTool(t, s, "model_create", map[string]any{
		"name":       "alpha",
		"enabled":    false,
		"owner_type": "org",
		"owner_id":   float64(5),
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	body := fake.last(t).Body
	if body["enabled"] != false {
		t.Fatalf("enabled = %v, want false", body["enabled"])
	}
	owner, ok := body["owner"].(map[string]any)
	if !ok || owner["type"] != "org" || owner["id"] != float64(5) {
		t.Fatalf("owner override lost: %v", body["owner"])
	}
}

func TestModelUpdate_OnlySendsProvidedFields(t *testing.T) {
	fake, s := newModelFake(t, `{"id":4,"name":"alpha","enabled":false}`)
	res := callModelTool(t, s, "model_update", map[string]any{
		"id":      float64(4),
		"enabled": false,
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodPatch || call.Path != "/api/models/4" {
		t.Fatalf("model_update hit %s %s", call.Method, call.Path)
	}
	if _, ok := call.Body["display_name"]; ok {
		t.Fatalf("display_name should be omitted when not provided: %v", call.Body)
	}
	if call.Body["enabled"] != false {
		t.Fatalf("enabled = %v, want false", call.Body["enabled"])
	}
}

func TestModelUpdate_RequiresAField(t *testing.T) {
	fake, s := newModelFake(t, `{}`)
	res := callModelTool(t, s, "model_update", map[string]any{"id": float64(4)})
	if !toolResultIsError(res) {
		t.Fatal("expected an error when neither display_name nor enabled is given")
	}
	if fake.count() != 0 {
		t.Fatal("no API call should be made for an empty patch")
	}
}

func TestModelDelete_HitsRowEndpoint(t *testing.T) {
	fake, s := newModelFake(t, ``)
	fake.status = http.StatusNoContent
	res := callModelTool(t, s, "model_delete", map[string]any{"id": float64(8)})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodDelete || call.Path != "/api/models/8" {
		t.Fatalf("model_delete hit %s %s", call.Method, call.Path)
	}
}

func TestModelRefreshPreview_ForwardsProviderID(t *testing.T) {
	fake, s := newModelFake(t, `[{"name":"glm-5.1","already_in_library":false}]`)
	res := callModelTool(t, s, "model_refresh_preview", map[string]any{"provider_id": float64(12)})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodPost || call.Path != "/api/models/refresh-preview" {
		t.Fatalf("refresh hit %s %s", call.Method, call.Path)
	}
	if call.Body["provider_id"] != float64(12) {
		t.Fatalf("provider_id = %v, want 12", call.Body["provider_id"])
	}
	if !strings.Contains(toolResultText(t, res), "already_in_library") {
		t.Fatalf("candidates not returned: %s", toolResultText(t, res))
	}
}

func TestModelImport_SendsNameArray(t *testing.T) {
	fake, s := newModelFake(t, `{"added":2,"skipped":1}`)
	res := callModelTool(t, s, "model_import", map[string]any{
		"names": []any{"glm-5.1", "glm-5.2"},
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodPost || call.Path != "/api/models/import" {
		t.Fatalf("import hit %s %s", call.Method, call.Path)
	}
	names, ok := call.Body["names"].([]any)
	if !ok || len(names) != 2 || names[0] != "glm-5.1" || names[1] != "glm-5.2" {
		t.Fatalf("names lost: %v", call.Body["names"])
	}
	if !strings.Contains(toolResultText(t, res), `"added":2`) {
		t.Fatalf("import counts not returned: %s", toolResultText(t, res))
	}
}

func TestModelUsageSummary_ForwardsWindowAndGroupBy(t *testing.T) {
	fake, s := newModelFake(t, `{"total_tokens":10}`)
	res := callModelTool(t, s, "model_usage_summary", map[string]any{
		"from":     "2026-11-01",
		"to":       "2026-11-19",
		"group_by": "model",
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodGet || call.Path != "/api/model-usage/summary" {
		t.Fatalf("usage hit %s %s", call.Method, call.Path)
	}
	if call.Query.Get("from") != "2026-11-01" || call.Query.Get("to") != "2026-11-19" || call.Query.Get("group_by") != "model" {
		t.Fatalf("usage query lost filters: %v", call.Query)
	}
}

func TestModelBudgetGet_ForwardsOwner(t *testing.T) {
	fake, s := newModelFake(t, `{"monthly_tokens":0}`)
	res := callModelTool(t, s, "model_budget_get", map[string]any{
		"owner_type": "org",
		"owner_id":   float64(3),
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodGet || call.Path != "/api/model-budget" {
		t.Fatalf("budget get hit %s %s", call.Method, call.Path)
	}
	if call.Query.Get("owner_type") != "org" || call.Query.Get("owner_id") != "3" {
		t.Fatalf("budget get lost owner: %v", call.Query)
	}
}

func TestModelBudgetSet_AllowsZeroAndSendsInteger(t *testing.T) {
	fake, s := newModelFake(t, `{"monthly_tokens":0}`)
	res := callModelTool(t, s, "model_budget_set", map[string]any{"monthly_tokens": float64(0)})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodPut || call.Path != "/api/model-budget" {
		t.Fatalf("budget set hit %s %s", call.Method, call.Path)
	}
	if call.Body["monthly_tokens"] != float64(0) {
		t.Fatalf("monthly_tokens = %v, want 0", call.Body["monthly_tokens"])
	}
}

func TestModelBudgetSet_RejectsNonInteger(t *testing.T) {
	fake, s := newModelFake(t, `{}`)
	res := callModelTool(t, s, "model_budget_set", map[string]any{"monthly_tokens": 12.5})
	if !toolResultIsError(res) {
		t.Fatal("expected an error for a fractional token count")
	}
	if fake.count() != 0 {
		t.Fatal("no API call should be made for an invalid argument")
	}
}

func TestModelBudgetSetProject_DefaultsToWorkspaceProject(t *testing.T) {
	fake, s := newModelFake(t, `{"project_id":77}`)
	res := callModelTool(t, s, "model_budget_set_project", map[string]any{"monthly_tokens": float64(500)})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	call := fake.last(t)
	if call.Method != http.MethodPut || call.Path != "/api/model-budget/projects/77" {
		t.Fatalf("project budget hit %s %s", call.Method, call.Path)
	}
	if call.Body["monthly_tokens"] != float64(500) {
		t.Fatalf("monthly_tokens = %v, want 500", call.Body["monthly_tokens"])
	}
}

func TestModelBudgetSetProject_ExplicitProjectWins(t *testing.T) {
	fake, s := newModelFake(t, `{"project_id":5}`)
	res := callModelTool(t, s, "model_budget_set_project", map[string]any{
		"project_id":     float64(5),
		"monthly_tokens": float64(1),
	})
	if toolResultIsError(res) {
		t.Fatalf("unexpected error: %s", toolResultText(t, res))
	}
	if got := fake.last(t).Path; got != "/api/model-budget/projects/5" {
		t.Fatalf("explicit project id ignored: %s", got)
	}
}

func TestModelRefreshPreview_PropagatesUpstreamErrorCode(t *testing.T) {
	fake, s := newModelFake(t,
		`{"error":{"code":"MODEL_REFRESH_UNSUPPORTED","message":"provider does not support /models"}}`)
	fake.status = http.StatusUnprocessableEntity
	res := callModelTool(t, s, "model_refresh_preview", map[string]any{"provider_id": float64(12)})
	if !toolResultIsError(res) {
		t.Fatal("expected an error result for an upstream 422")
	}
	out := toolResultText(t, res)
	if !strings.Contains(out, "422") || !strings.Contains(out, "MODEL_REFRESH_UNSUPPORTED") {
		t.Fatalf("upstream status/code not propagated: %s", out)
	}
}
