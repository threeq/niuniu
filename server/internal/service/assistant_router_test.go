package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubAnthropic returns a test server that replies with one assistant text
// block carrying `verdictJSON`, mimicking the Messages API response envelope.
func stubAnthropic(t *testing.T, verdictJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"content": []map[string]string{{"type": "text", "text": verdictJSON}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestAssistantRouter_NoKey_Unavailable(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	r := NewAssistantRouter() // no WithAPIKey
	_, err := r.Classify(context.Background(),
		[]AssistantPlanSummary{{PlanID: 1, Title: "周报"}}, "改一下")
	if err != ErrRouterUnavailable {
		t.Fatalf("want ErrRouterUnavailable, got %v", err)
	}
}

func TestAssistantRouter_EmptyPlans_AlwaysNew(t *testing.T) {
	r := NewAssistantRouter().WithAPIKey("k")
	d, err := r.Classify(context.Background(), nil, "做个 PPT")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if d.Action != DispatchNew {
		t.Fatalf("want new for empty plans, got %q", d.Action)
	}
}

func TestAssistantRouter_ContinueMatched(t *testing.T) {
	srv := stubAnthropic(t, `{"action":"continue","plan_id":7}`)
	defer srv.Close()
	r := NewAssistantRouter().WithAPIKey("k").WithEndpoint(srv.URL)
	d, err := r.Classify(context.Background(),
		[]AssistantPlanSummary{{PlanID: 7, Title: "深海 PPT"}}, "把第 3 页改一下")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if d.Action != DispatchContinue || d.PlanID != 7 {
		t.Fatalf("want continue plan 7, got %+v", d)
	}
}

func TestAssistantRouter_ContinueUnknownPlan_DegradesToNew(t *testing.T) {
	// Model names a plan that isn't in the set → must not misroute; → new.
	srv := stubAnthropic(t, `{"action":"continue","plan_id":999}`)
	defer srv.Close()
	r := NewAssistantRouter().WithAPIKey("k").WithEndpoint(srv.URL)
	d, err := r.Classify(context.Background(),
		[]AssistantPlanSummary{{PlanID: 7, Title: "深海 PPT"}}, "另做一份海报")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if d.Action != DispatchNew {
		t.Fatalf("want new for unknown plan_id, got %+v", d)
	}
}

func TestAssistantRouter_NewWithTitle_ToleratesFences(t *testing.T) {
	srv := stubAnthropic(t, "```json\n{\"action\":\"new\",\"title\":\"季度销售汇报\"}\n```")
	defer srv.Close()
	r := NewAssistantRouter().WithAPIKey("k").WithEndpoint(srv.URL)
	d, err := r.Classify(context.Background(),
		[]AssistantPlanSummary{{PlanID: 7, Title: "深海 PPT"}}, "帮我做季度销售汇报")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if d.Action != DispatchNew || d.Title != "季度销售汇报" {
		t.Fatalf("want new with title, got %+v", d)
	}
}

func TestAssistantRouter_GarbageResponse_Unavailable(t *testing.T) {
	srv := stubAnthropic(t, "I cannot help with that.")
	defer srv.Close()
	r := NewAssistantRouter().WithAPIKey("k").WithEndpoint(srv.URL)
	_, err := r.Classify(context.Background(),
		[]AssistantPlanSummary{{PlanID: 7, Title: "x"}}, "msg")
	if err != ErrRouterUnavailable {
		t.Fatalf("want ErrRouterUnavailable on unparseable output, got %v", err)
	}
}
