package service

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
)

// PlanRouter performs one-shot intent routing for the conversational
// office assistant: given the user's new message and the list of their current
// plans (open issues in the 办公助手 project), it asks an LLM whether the
// message continues an existing plan or warrants a brand-new one.
//
// It mirrors the lightweight direct-HTTP Anthropic Messages call used by the
// harness ai_judge checker (no SDK dependency). The API key is read from
// ANTHROPIC_API_KEY at call time; when absent the caller applies a
// deterministic fallback instead (continue the active plan, else new).
type PlanRouter struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
	model      string
}

const (
	assistantRouterEndpoint   = "https://api.anthropic.com/v1/messages"
	assistantRouterVersion    = "2023-06-01"
	assistantRouterModel      = "claude-haiku-4-5-20251001"
	assistantRouterMaxTokens  = 256
	assistantRouterTimeoutSec = 20
)

// NewPlanRouter builds a router with the default HTTP client + endpoint.
func NewPlanRouter() *PlanRouter {
	return &PlanRouter{
		httpClient: &http.Client{},
		endpoint:   assistantRouterEndpoint,
		model:      assistantRouterModel,
	}
}

// WithEndpoint overrides the API endpoint (tests point this at a stub).
func (r *PlanRouter) WithEndpoint(url string) *PlanRouter { r.endpoint = url; return r }

// WithAPIKey overrides the API key (tests inject a fixed value).
func (r *PlanRouter) WithAPIKey(key string) *PlanRouter { r.apiKey = key; return r }

// PlanSummary is the minimal view of an existing plan the router needs
// to decide routing.
type PlanSummary struct {
	PlanID int64
	Title  string
}

// DispatchAction is the routing verdict.
type DispatchAction string

const (
	DispatchContinue DispatchAction = "continue"
	DispatchNew      DispatchAction = "new"
)

// DispatchDecision is the parsed routing result. For Continue, PlanID names the
// target plan; for New, Title is an optional short title suggestion.
type DispatchDecision struct {
	Action DispatchAction
	PlanID int64
	Title  string
}

// ErrRouterUnavailable signals the model could not be reached (no key / network
// / parse failure), so the caller must fall back deterministically.
var ErrRouterUnavailable = fmt.Errorf("assistant router unavailable")

type routerVerdict struct {
	Action string `json:"action"`
	PlanID int64  `json:"plan_id"`
	Title  string `json:"title"`
}

// Classify asks the model to route `message` against `plans`. Returns
// ErrRouterUnavailable on any failure so the caller can fall back. When `plans`
// is empty the caller should skip this entirely (always new).
func (r *PlanRouter) Classify(ctx context.Context, plans []PlanSummary, message string) (DispatchDecision, error) {
	apiKey := r.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return DispatchDecision{}, ErrRouterUnavailable
	}
	if len(plans) == 0 {
		return DispatchDecision{Action: DispatchNew}, nil
	}

	var sb strings.Builder
	sb.WriteString("Existing plans (id — title):\n")
	for _, p := range plans {
		fmt.Fprintf(&sb, "- %d — %s\n", p.PlanID, strings.TrimSpace(p.Title))
	}
	sb.WriteString("\nUser's new message:\n")
	sb.WriteString(strings.TrimSpace(message))

	system := `You route an office assistant's incoming message. Decide whether it CONTINUES one of the user's existing plans (a follow-up, refinement, correction, or question about that plan's deliverable) or starts a NEW, unrelated task.

Rules:
- Prefer "continue" only when the message clearly refers to or builds on a specific existing plan.
- If the message is a new, unrelated request, choose "new".
- When unsure, choose "new".

Respond with a single JSON object on one line, no prose, no code fences:
{"action":"continue","plan_id":<id of the matched plan>} or {"action":"new","title":"<short 4-8 word title for the new task>"}`

	reqBody := map[string]any{
		"model":      r.model,
		"max_tokens": assistantRouterMaxTokens,
		"system":     system,
		"messages": []map[string]string{
			{"role": "user", "content": sb.String()},
		},
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return DispatchDecision{}, ErrRouterUnavailable
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, assistantRouterTimeoutSec*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, r.endpoint, bytes.NewReader(raw))
	if err != nil {
		return DispatchDecision{}, ErrRouterUnavailable
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", assistantRouterVersion)

	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return DispatchDecision{}, ErrRouterUnavailable
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return DispatchDecision{}, ErrRouterUnavailable
	}

	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil || len(parsed.Content) == 0 {
		return DispatchDecision{}, ErrRouterUnavailable
	}

	verdict, ok := parseRouterVerdict(parsed.Content[0].Text)
	if !ok {
		return DispatchDecision{}, ErrRouterUnavailable
	}

	// Validate the verdict against the known plan set; an out-of-set plan_id or
	// an unknown action degrades to "new" rather than misrouting.
	if strings.EqualFold(verdict.Action, string(DispatchContinue)) {
		for _, p := range plans {
			if p.PlanID == verdict.PlanID {
				return DispatchDecision{Action: DispatchContinue, PlanID: verdict.PlanID}, nil
			}
		}
		return DispatchDecision{Action: DispatchNew}, nil
	}
	return DispatchDecision{Action: DispatchNew, Title: strings.TrimSpace(verdict.Title)}, nil
}

// parseRouterVerdict tolerates fenced or prose-wrapped JSON, mirroring the
// ai_judge parser.
func parseRouterVerdict(s string) (routerVerdict, bool) {
	candidates := []string{strings.TrimSpace(s)}
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "```"); j >= 0 {
			candidates = append(candidates, strings.TrimSpace(rest[:j]))
		}
	}
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			candidates = append(candidates, s[i:j+1])
		}
	}
	for _, c := range candidates {
		c = strings.TrimPrefix(strings.TrimSpace(c), "json\n")
		var v routerVerdict
		if err := json.Unmarshal([]byte(c), &v); err == nil && v.Action != "" {
			return v, true
		}
	}
	return routerVerdict{}, false
}
