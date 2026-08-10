package checkers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/harness"
)

// Per-model USD pricing per 1M tokens (input / output). Inlined to avoid
// pulling in service package and creating an import cycle. Pricing values
// snapshot 2026-05-02; update with each Anthropic price change.
type judgePricing struct {
	inputPerMTok, outputPerMTok float64
}

var judgePricingTable = map[string]judgePricing{
	"claude-haiku-4-5-20251001":  {inputPerMTok: 1, outputPerMTok: 5},
	"claude-sonnet-4-6":          {inputPerMTok: 3, outputPerMTok: 15},
	"claude-sonnet-4-5-20250929": {inputPerMTok: 3, outputPerMTok: 15},
	"claude-opus-4-7":            {inputPerMTok: 15, outputPerMTok: 75},
	"claude-opus-4-6":            {inputPerMTok: 15, outputPerMTok: 75},
}

const (
	defaultJudgeModel      = "claude-haiku-4-5-20251001"
	defaultJudgeTimeoutSec = 60
	defaultJudgeMaxTokens  = 512
	anthropicEndpoint      = "https://api.anthropic.com/v1/messages"
	anthropicVersion       = "2023-06-01"
)

// AIJudge runs an LLM judge against natural-language inputs (commit message,
// branch name, agent output, or arbitrary text passed in via env). It expects
// the model to return JSON of shape {"pass":bool,"reason":string}.
//
// The judge prompt (`spec.JudgePrompt`) is the operator-authored rubric. At
// runtime it is concatenated with the current target text and a fixed schema
// instruction so the model returns parseable JSON.
type AIJudge struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
}

// NewAIJudge constructs an AIJudge with default HTTP client + endpoint. The
// API key is sourced from ANTHROPIC_API_KEY env var at Run time so server
// process restarts pick up rotated keys without a service-layer rebuild.
//
// The HTTP client has no Timeout field set on purpose — per-call deadlines
// come from the context derived from spec.TimeoutSec. A static client-level
// timeout would silently override longer per-spec configurations.
func NewAIJudge() *AIJudge {
	return &AIJudge{
		httpClient: &http.Client{},
		endpoint:   anthropicEndpoint,
	}
}

// WithEndpoint overrides the API endpoint (used by tests to point at a stub).
func (j *AIJudge) WithEndpoint(url string) *AIJudge { j.endpoint = url; return j }

// WithAPIKey overrides the API key (used by tests to inject a fixed value).
func (j *AIJudge) WithAPIKey(key string) *AIJudge { j.apiKey = key; return j }

// Kind reports the registered Kind for this checker.
func (j *AIJudge) Kind() string { return harness.KindAIJudge }

type anthropicMessagesReq struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	Messages  []anthropicMsg `json:"messages"`
	System    string         `json:"system,omitempty"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicMessagesResp struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type judgeVerdict struct {
	Pass   bool   `json:"pass"`
	Reason string `json:"reason"`
}

// Run posts a single Messages-API request to Claude with the operator's
// judge prompt and the selected target text from env, then parses the model's
// JSON verdict.
func (j *AIJudge) Run(ctx context.Context, spec harness.Spec, env harness.CheckEnv) harness.CheckResult {
	start := time.Now()
	finish := func() int64 { return time.Since(start).Milliseconds() }

	if strings.TrimSpace(spec.JudgePrompt) == "" {
		return harness.CheckResult{Status: "skip", Message: "no judge_prompt configured", DurationMs: finish()}
	}

	apiKey := j.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return harness.CheckResult{
			Status: "error",
			Message: "ANTHROPIC_API_KEY not set; ai_judge cannot reach the model",
			DurationMs: finish(),
		}
	}

	target := pickJudgeTarget(spec.Target, env)
	if strings.TrimSpace(target) == "" {
		return harness.CheckResult{
			Status: "skip",
			Message: fmt.Sprintf("no input for target %q", spec.Target),
			DurationMs: finish(),
		}
	}

	model := spec.JudgeModel
	if model == "" {
		model = defaultJudgeModel
	}

	timeoutSec := spec.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultJudgeTimeoutSec
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	systemPrompt := strings.TrimSpace(spec.JudgePrompt) + `

Respond with a single JSON object on one line of the form:
{"pass": true|false, "reason": "<one-sentence explanation, <=200 chars>"}
No prose before or after the JSON. No code fences.`

	body := anthropicMessagesReq{
		Model:     model,
		MaxTokens: defaultJudgeMaxTokens,
		System:    systemPrompt,
		Messages: []anthropicMsg{{
			Role:    "user",
			Content: "Evaluate the following content against the rubric:\n\n---\n" + target + "\n---",
		}},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return harness.CheckResult{Status: "error", Message: fmt.Sprintf("marshal request: %v", err), DurationMs: finish()}
	}

	req, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, j.endpoint, bytes.NewReader(raw))
	if err != nil {
		return harness.CheckResult{Status: "error", Message: fmt.Sprintf("build request: %v", err), DurationMs: finish()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := j.httpClient.Do(req)
	if err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			return harness.CheckResult{Status: "error", Message: fmt.Sprintf("ai_judge timed out after %ds", timeoutSec), DurationMs: finish()}
		}
		return harness.CheckResult{Status: "error", Message: fmt.Sprintf("anthropic call failed: %v", err), DurationMs: finish()}
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("anthropic returned %d", resp.StatusCode),
			Details:    truncate(string(respBytes), 1000),
			DurationMs: finish(),
		}
	}

	var parsed anthropicMessagesResp
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return harness.CheckResult{
			Status:     "error",
			Message:    fmt.Sprintf("decode anthropic response: %v", err),
			Details:    truncate(string(respBytes), 1000),
			DurationMs: finish(),
		}
	}
	if parsed.Error != nil {
		return harness.CheckResult{
			Status:     "error",
			Message:    parsed.Error.Message,
			DurationMs: finish(),
		}
	}
	if len(parsed.Content) == 0 || parsed.Content[0].Text == "" {
		return harness.CheckResult{
			Status:     "error",
			Message:    "anthropic returned empty content",
			DurationMs: finish(),
		}
	}

	verdictText := strings.TrimSpace(parsed.Content[0].Text)
	verdict, ok := parseJudgeVerdict(verdictText)
	if !ok {
		return harness.CheckResult{
			Status:     "error",
			Message:    "could not parse JSON verdict from model output",
			Details:    truncate(verdictText, 500),
			DurationMs: finish(),
		}
	}

	cost := computeJudgeCost(parsed.Model, parsed.Usage.InputTokens, parsed.Usage.OutputTokens)
	res := harness.CheckResult{
		Message:    truncate(verdict.Reason, 200),
		DurationMs: finish(),
		CostUSD:    cost,
	}
	if verdict.Pass {
		res.Status = "pass"
	} else {
		res.Status = "fail"
		res.Details = fmt.Sprintf("model verdict: %s\nrubric: %s",
			truncate(verdict.Reason, 500), truncate(spec.JudgePrompt, 500))
	}
	return res
}

// parseJudgeVerdict accepts either bare JSON or JSON wrapped in stray prose,
// and tolerates fenced output that some models still emit despite the system
// prompt. Returns ok=false if no valid object is found.
func parseJudgeVerdict(s string) (judgeVerdict, bool) {
	candidates := []string{s}

	// Strip ```...``` fences and try again.
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "```"); j >= 0 {
			candidates = append(candidates, strings.TrimSpace(rest[:j]))
		}
	}
	// Locate the first {...} substring (greedy through the closing brace).
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			candidates = append(candidates, s[i:j+1])
		}
	}

	for _, c := range candidates {
		c = strings.TrimSpace(c)
		// Strip a leading "json\n" that fence-stripping may leave behind.
		c = strings.TrimPrefix(c, "json\n")
		c = strings.TrimPrefix(c, "JSON\n")
		var v judgeVerdict
		if err := json.Unmarshal([]byte(c), &v); err == nil {
			return v, true
		}
	}
	return judgeVerdict{}, false
}

// pickJudgeTarget selects the input text per spec.Target. Empty target falls
// back to agent_output (matches typical "review the work" use case).
func pickJudgeTarget(target string, env harness.CheckEnv) string {
	switch target {
	case harness.TargetCommitMessage:
		return env.CommitMessage
	case harness.TargetBranchName:
		return env.BranchName
	case harness.TargetAgentOutput, "":
		return env.AgentOutput
	default:
		return ""
	}
}

func computeJudgeCost(model string, in, out int64) float64 {
	p, ok := judgePricingTable[model]
	if !ok {
		return 0
	}
	const million = 1_000_000.0
	return (float64(in)*p.inputPerMTok + float64(out)*p.outputPerMTok) / million
}
