package checkers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

// stubAnthropic returns a test server that responds with `text` as the
// model's text content. Optional headerCheck asserts incoming headers.
func stubAnthropic(t *testing.T, text string, headerCheck func(*http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if headerCheck != nil {
			headerCheck(r)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          "msg_test",
			"model":       "claude-haiku-4-5-20251001",
			"stop_reason": "end_turn",
			"content":     []map[string]string{{"type": "text", "text": text}},
			"usage":       map[string]int64{"input_tokens": 100, "output_tokens": 30},
		})
	}))
}

func TestAIJudge_Pass(t *testing.T) {
	srv := stubAnthropic(t, `{"pass": true, "reason": "follows convention"}`, nil)
	defer srv.Close()

	j := NewAIJudge().WithEndpoint(srv.URL).WithAPIKey("test-key")
	spec := harness.Spec{
		Kind:        harness.KindAIJudge,
		Target:      harness.TargetCommitMessage,
		JudgePrompt: "Is this commit message clear?",
	}
	res := j.Run(context.Background(), spec, harness.CheckEnv{CommitMessage: "feat: add ai judge checker"})

	if res.Status != "pass" {
		t.Fatalf("expected pass, got %s: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "follows convention") {
		t.Errorf("message should carry reason, got %q", res.Message)
	}
	if res.CostUSD <= 0 {
		t.Errorf("expected positive cost, got %v", res.CostUSD)
	}
}

func TestAIJudge_Fail(t *testing.T) {
	srv := stubAnthropic(t, `{"pass": false, "reason": "missing scope"}`, nil)
	defer srv.Close()

	j := NewAIJudge().WithEndpoint(srv.URL).WithAPIKey("test-key")
	spec := harness.Spec{Kind: harness.KindAIJudge, Target: harness.TargetCommitMessage, JudgePrompt: "rubric"}
	res := j.Run(context.Background(), spec, harness.CheckEnv{CommitMessage: "wip"})

	if res.Status != "fail" {
		t.Fatalf("expected fail, got %s: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Details, "missing scope") {
		t.Errorf("details should carry reasoning, got %q", res.Details)
	}
}

func TestAIJudge_FencedJSONStillParses(t *testing.T) {
	// Some models still wrap JSON in code fences despite the system prompt.
	srv := stubAnthropic(t, "```json\n{\"pass\": true, \"reason\": \"ok\"}\n```", nil)
	defer srv.Close()
	j := NewAIJudge().WithEndpoint(srv.URL).WithAPIKey("test-key")
	spec := harness.Spec{Kind: harness.KindAIJudge, Target: harness.TargetAgentOutput, JudgePrompt: "rubric"}
	res := j.Run(context.Background(), spec, harness.CheckEnv{AgentOutput: "any text"})
	if res.Status != "pass" {
		t.Fatalf("fenced JSON should still parse; got %s msg=%s", res.Status, res.Message)
	}
}

func TestAIJudge_BadJSONReturnsError(t *testing.T) {
	srv := stubAnthropic(t, "this is not json", nil)
	defer srv.Close()
	j := NewAIJudge().WithEndpoint(srv.URL).WithAPIKey("test-key")
	spec := harness.Spec{Kind: harness.KindAIJudge, JudgePrompt: "rubric"}
	res := j.Run(context.Background(), spec, harness.CheckEnv{AgentOutput: "x"})
	if res.Status != "error" {
		t.Fatalf("expected error, got %s", res.Status)
	}
}

func TestAIJudge_SkipNoPrompt(t *testing.T) {
	j := NewAIJudge().WithAPIKey("test-key")
	res := j.Run(context.Background(), harness.Spec{Kind: harness.KindAIJudge}, harness.CheckEnv{})
	if res.Status != "skip" {
		t.Fatalf("expected skip on empty prompt, got %s", res.Status)
	}
}

func TestAIJudge_ErrorNoAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	j := NewAIJudge()
	res := j.Run(context.Background(), harness.Spec{Kind: harness.KindAIJudge, JudgePrompt: "x"}, harness.CheckEnv{AgentOutput: "x"})
	if res.Status != "error" {
		t.Fatalf("expected error when no API key, got %s", res.Status)
	}
}

func TestAIJudge_SkipEmptyTarget(t *testing.T) {
	srv := stubAnthropic(t, `{"pass":true,"reason":"x"}`, nil)
	defer srv.Close()
	j := NewAIJudge().WithEndpoint(srv.URL).WithAPIKey("test-key")
	spec := harness.Spec{Kind: harness.KindAIJudge, Target: harness.TargetCommitMessage, JudgePrompt: "rubric"}
	res := j.Run(context.Background(), spec, harness.CheckEnv{}) // no CommitMessage
	if res.Status != "skip" {
		t.Fatalf("expected skip on empty input, got %s", res.Status)
	}
}

func TestAIJudge_HTTPErrorReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"type":"invalid_request","message":"boom"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	j := NewAIJudge().WithEndpoint(srv.URL).WithAPIKey("test-key")
	res := j.Run(context.Background(), harness.Spec{Kind: harness.KindAIJudge, JudgePrompt: "rubric"}, harness.CheckEnv{AgentOutput: "x"})
	if res.Status != "error" {
		t.Fatalf("expected error on 4xx, got %s msg=%s", res.Status, res.Message)
	}
}

func TestAIJudge_SendsCorrectHeaders(t *testing.T) {
	gotKey := ""
	gotVer := ""
	srv := stubAnthropic(t, `{"pass":true,"reason":"x"}`, func(r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
	})
	defer srv.Close()
	j := NewAIJudge().WithEndpoint(srv.URL).WithAPIKey("test-key-123")
	_ = j.Run(context.Background(), harness.Spec{Kind: harness.KindAIJudge, JudgePrompt: "rubric"}, harness.CheckEnv{AgentOutput: "x"})
	if gotKey != "test-key-123" {
		t.Errorf("x-api-key=%q, want test-key-123", gotKey)
	}
	if gotVer != "2023-06-01" {
		t.Errorf("anthropic-version=%q, want 2023-06-01", gotVer)
	}
}

func TestAIJudge_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	j := NewAIJudge().WithEndpoint(srv.URL).WithAPIKey("test-key")
	spec := harness.Spec{Kind: harness.KindAIJudge, JudgePrompt: "rubric", TimeoutSec: 0}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res := j.Run(ctx, spec, harness.CheckEnv{AgentOutput: "x"})
	if res.Status != "error" {
		t.Fatalf("expected error from timeout, got %s msg=%s", res.Status, res.Message)
	}
}

func TestComputeJudgeCost_KnownModel(t *testing.T) {
	// haiku: 1 input + 5 output per MTok. 100 in + 50 out = (100*1 + 50*5) / 1M = 0.00035
	got := computeJudgeCost("claude-haiku-4-5-20251001", 100, 50)
	want := 0.00035
	if got < want-1e-9 || got > want+1e-9 {
		t.Errorf("cost=%v, want %v", got, want)
	}
}

func TestComputeJudgeCost_UnknownModelZero(t *testing.T) {
	if got := computeJudgeCost("nonexistent-model", 100, 50); got != 0 {
		t.Errorf("unknown model cost=%v, want 0", got)
	}
}
